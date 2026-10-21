package docker

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/bohumix/bohux/internal/provider"
)

// Apply writes the artifact first (rule 2), then runs `docker compose up` on it. It is
// resumable: an interrupted Apply leaves files and containers that the next Apply's
// `up` brings to the same end state, without duplicates.
func (p *Provider) Apply(ctx context.Context, req provider.ApplyRequest) (provider.ApplyResult, error) {
	spec := req.Spec
	plan, err := p.Plan(ctx, provider.PlanRequest{Spec: spec})
	if err != nil {
		return provider.ApplyResult{}, err
	}
	if len(plan.Changes) == 0 {
		return provider.ApplyResult{}, nil
	}
	composeYAML, secretsEnv, stateJSON, err := render(spec)
	if err != nil {
		return provider.ApplyResult{}, err
	}
	if spec.Ingress {
		if err := p.ensureIngressNetwork(ctx); err != nil {
			return provider.ApplyResult{}, err
		}
	}
	dir := p.dir(spec.Ref)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return provider.ApplyResult{}, fmt.Errorf("create %s: %w", dir, err)
	}
	// secrets.env is 0600: the only place on disk outside the agent's sealed cache where
	// secret values exist (ADR-0008). compose.yaml is meant to be read.
	for _, f := range []struct {
		name string
		data []byte
		mode os.FileMode
	}{
		{secretsFile, secretsEnv, 0o600},
		{stateFile, stateJSON, 0o644},
		{composeFile, composeYAML, 0o644},
	} {
		if err := writeAtomic(filepath.Join(dir, f.name), f.data, f.mode); err != nil {
			return provider.ApplyResult{}, err
		}
	}
	if plan.Changes[0].Action == "restart" {
		// Same spec, not running: often a start that failed (a host port still held).
		// Starting the container Docker left behind attaches only its first network,
		// so remove the stopped containers (never running ones) and let `up` create
		// them again from the artifact.
		if _, err := p.run(ctx, dir, "compose", "-p", project(spec.Ref), "-f", composeFile,
			"rm", "--force"); err != nil {
			return provider.ApplyResult{}, err
		}
	}
	env, done, err := pullEnv(spec.PullConfig)
	if err != nil {
		return provider.ApplyResult{}, err
	}
	defer done()
	if _, err := p.runEnv(ctx, dir, env, "compose", "-p", project(spec.Ref), "-f", composeFile,
		"up", "--detach", "--remove-orphans", "--quiet-pull"); err != nil {
		return provider.ApplyResult{}, err
	}
	return provider.ApplyResult{Changed: true}, nil
}

// ensureIngressNetwork creates the shared ingress network if it is missing. The
// install bundle creates it with the ingress; this covers a workload applied first.
func (p *Provider) ensureIngressNetwork(ctx context.Context) error {
	if _, err := p.run(ctx, "", "network", "inspect", IngressNetwork); err == nil {
		return nil
	}
	if _, err := p.run(ctx, "", "network", "create", IngressNetwork); err != nil {
		// Another apply may have created it meanwhile.
		if _, ierr := p.run(ctx, "", "network", "inspect", IngressNetwork); ierr != nil {
			return fmt.Errorf("create network %s: %w", IngressNetwork, err)
		}
	}
	return nil
}

// pullEnv puts a registry credential where `docker compose` reads it for this one
// command: a fresh 0700 DOCKER_CONFIG directory, removed by done (ADR-0041). The
// daemon's and root's own Docker config are never touched. Per-user CLI plugins
// (compose itself, possibly) are linked in so the command still finds them.
func pullEnv(src provider.PullConfigSource) (env []string, done func(), err error) {
	if src == nil {
		return nil, func() {}, nil
	}
	cfg, err := src.Open()
	if err != nil {
		return nil, nil, fmt.Errorf("open image pull secret: %w", err)
	}
	dir, err := os.MkdirTemp("", "bohux-pull-")
	if err != nil {
		return nil, nil, fmt.Errorf("create pull config directory: %w", err)
	}
	done = func() { _ = os.RemoveAll(dir) }
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(cfg), 0o600); err != nil {
		done()
		return nil, nil, fmt.Errorf("write pull config: %w", err)
	}
	plugins := os.Getenv("DOCKER_CONFIG")
	if plugins == "" {
		if home, herr := os.UserHomeDir(); herr == nil {
			plugins = filepath.Join(home, ".docker")
		}
	}
	if st, serr := os.Stat(filepath.Join(plugins, "cli-plugins")); serr == nil && st.IsDir() {
		_ = os.Symlink(filepath.Join(plugins, "cli-plugins"), filepath.Join(dir, "cli-plugins"))
	}
	return []string{"DOCKER_CONFIG=" + dir}, done, nil
}

func writeAtomic(path string, data []byte, mode os.FileMode) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return fmt.Errorf("write %s: %w", filepath.Base(path), err)
	}
	// WriteFile keeps an existing file's mode; set it explicitly.
	if err := os.Chmod(tmp, mode); err != nil {
		return fmt.Errorf("chmod %s: %w", filepath.Base(path), err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("install %s: %w", filepath.Base(path), err)
	}
	return nil
}
