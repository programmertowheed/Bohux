package docker

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"

	"github.com/bohumix/bohux/internal/provider"
)

// Scale rewrites the replica count in the artifact and the state, then runs `up`, so
// the running system never drifts from compose.yaml (ADR-0025).
func (p *Provider) Scale(ctx context.Context, req provider.ScaleRequest) error {
	if req.Replicas < 0 {
		return fmt.Errorf("replicas must not be negative")
	}
	dir := p.dir(req.Ref)
	raw, err := os.ReadFile(filepath.Join(dir, composeFile))
	if err != nil {
		return fmt.Errorf("read artifact: %w", err)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return fmt.Errorf("parse artifact: %w", err)
	}
	if !setReplicas(&doc, req.Replicas) {
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
	stJSON, err := jsonMarshal(st)
	if err != nil {
		return err
	}
	if err := writeAtomic(filepath.Join(dir, stateFile), stJSON, 0o644); err != nil {
		return err
	}
	if err := writeAtomic(filepath.Join(dir, composeFile), body, 0o644); err != nil {
		return err
	}
	_, err = p.run(ctx, dir, "compose", "-p", project(req.Ref), "-f", composeFile, "up", "--detach", "--remove-orphans")
	return err
}

// setReplicas finds services.app.deploy.replicas in the document and sets it,
// keeping the header comment and everything else as written.
func setReplicas(n *yaml.Node, replicas int32) bool {
	path := []string{"services", service, "deploy", "replicas"}
	cur := n
	if cur.Kind == yaml.DocumentNode && len(cur.Content) == 1 {
		cur = cur.Content[0]
	}
	for i, key := range path {
		if cur.Kind != yaml.MappingNode {
			return false
		}
		found := false
		for j := 0; j+1 < len(cur.Content); j += 2 {
			if cur.Content[j].Value == key {
				if i == len(path)-1 {
					cur.Content[j+1].Value = fmt.Sprint(replicas)
					return true
				}
				cur = cur.Content[j+1]
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return false
}
