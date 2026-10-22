package swarm

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"

	"github.com/bohumix/bohux/internal/provider"
	"github.com/bohumix/bohux/providers/docker"
)

// Scale rewrites the replica count in the artifact and the state, then deploys the
// stack again, so the running system never drifts from stack.yaml.
func (p *Provider) Scale(ctx context.Context, req provider.ScaleRequest) error {
	if req.Replicas < 0 {
		return fmt.Errorf("replicas must not be negative")
	}
	dir := p.dir(req.Ref)
	raw, err := os.ReadFile(filepath.Join(dir, stackFile))
	if err != nil {
		return fmt.Errorf("read artifact: %w", err)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return fmt.Errorf("parse artifact: %w", err)
	}
	if !docker.SetReplicas(&doc, req.Replicas) {
		return fmt.Errorf("artifact has no services.%s.deploy.replicas", service)
	}
	body, err := yaml.Marshal(&doc)
	if err != nil {
		return fmt.Errorf("render artifact: %w", err)
	}
	st, err := p.readState(req.Ref)
	if err != nil || st == nil {
		return fmt.Errorf("read state: %w", err)
	}
	st.Replicas = req.Replicas
	stJSON, err := json.Marshal(st)
	if err != nil {
		return fmt.Errorf("encode state: %w", err)
	}
	if err := docker.WriteAtomic(filepath.Join(dir, stateFile), stJSON, 0o644); err != nil {
		return err
	}
	if err := docker.WriteAtomic(filepath.Join(dir, stackFile), body, 0o644); err != nil {
		return err
	}
	_, err = p.run(ctx, dir, "stack", "deploy", "-c", stackFile, "--resolve-image", "never",
		"--prune", "--detach=true", "--quiet", stack(req.Ref))
	return err
}
