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
	if _, err := p.run(ctx, dir, "compose", "-p", project(spec.Ref), "-f", composeFile,
		"up", "--detach", "--remove-orphans", "--quiet-pull"); err != nil {
		return provider.ApplyResult{}, err
	}
	return provider.ApplyResult{Changed: true}, nil
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
