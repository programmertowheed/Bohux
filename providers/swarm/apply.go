package swarm

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/bohumix/bohux/internal/provider"
	"github.com/bohumix/bohux/providers/docker"
)

// Apply writes the artifact first (rule 2), then runs `docker stack deploy` on it. It
// is resumable: the deploy is declarative, so an interrupted Apply is finished by the
// next one without duplicates.
func (p *Provider) Apply(ctx context.Context, req provider.ApplyRequest) (provider.ApplyResult, error) {
	spec := req.Spec
	plan, err := p.Plan(ctx, provider.PlanRequest{Spec: spec})
	if err != nil {
		return provider.ApplyResult{}, err
	}
	if len(plan.Changes) == 0 {
		return provider.ApplyResult{}, nil
	}
	stackYAML, secretsEnv, stateJSON, err := render(spec)
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
	// secret values exist (ADR-0008). stack.yaml is meant to be read.
	for _, f := range []struct {
		name string
		data []byte
		mode os.FileMode
	}{
		{secretsFile, secretsEnv, 0o600},
		{stateFile, stateJSON, 0o644},
		{stackFile, stackYAML, 0o644},
	} {
		if err := docker.WriteAtomic(filepath.Join(dir, f.name), f.data, f.mode); err != nil {
			return provider.ApplyResult{}, err
		}
	}
	if spec.PullConfig != nil {
		// Pulled before the deploy with a throwaway credential, so it never enters the
		// swarm's state as --with-registry-auth would put it (ADR-0045 item 9).
		env, done, err := docker.PullEnv(spec.PullConfig)
		if err != nil {
			return provider.ApplyResult{}, err
		}
		_, err = p.runEnv(ctx, "", env, "pull", "-q", spec.Image)
		done()
		if err != nil {
			return provider.ApplyResult{}, err
		}
	}
	if _, err := p.run(ctx, dir, "stack", "deploy", "-c", stackFile, "--resolve-image", "never",
		"--prune", "--detach=true", "--quiet", stack(spec.Ref)); err != nil {
		return provider.ApplyResult{}, err
	}
	if plan.Changes[0].Action == "restart" {
		// The same spec deploys as a no-op; new tasks need a forced update.
		if _, err := p.run(ctx, "", "service", "update", "--force", "--detach", "--quiet",
			serviceName(spec.Ref)); err != nil {
			return provider.ApplyResult{}, err
		}
	}
	return provider.ApplyResult{Changed: true}, nil
}

// ensureIngressNetwork creates bohux-ingress as an attachable overlay if it is missing,
// and refuses a bridge of that name, which Swarm services cannot join (ADR-0045 item 6).
func (p *Provider) ensureIngressNetwork(ctx context.Context) error {
	driver := func() (string, error) {
		out, err := p.run(ctx, "", "network", "inspect", "--format", "{{.Driver}} {{.Attachable}}", IngressNetwork)
		return strings.TrimSpace(string(out)), err
	}
	got, err := driver()
	if err != nil {
		if _, cerr := p.run(ctx, "", "network", "create", "--driver", "overlay", "--attachable",
			IngressNetwork); cerr != nil {
			// Another apply may have created it meanwhile.
			if got, err = driver(); err != nil {
				return fmt.Errorf("create network %s: %w", IngressNetwork, cerr)
			}
		} else {
			return nil
		}
	}
	if got != "overlay true" {
		return fmt.Errorf("network %s is %q, not an attachable overlay; Swarm services cannot join it. "+
			"Recreate it with `docker network create --driver overlay --attachable %s` (ADR-0045)",
			IngressNetwork, got, IngressNetwork)
	}
	return nil
}
