package docker

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/bohumix/bohux/internal/provider"
)

// The methods below look at Docker and the disk directly. They implement the
// conformance suite's Inspector and are not used by the agent.

// Leftovers lists every container, network, and volume labeled with the workload's
// Compose project, and the files in its directory.
func (p *Provider) Leftovers(ctx context.Context, ref provider.ResourceRef) ([]string, error) {
	filter := "label=" + labelProject + "=" + project(ref)
	var out []string
	for _, q := range []struct{ kind, format string }{
		{"container", "{{.Names}}"}, {"network", "{{.Name}}"}, {"volume", "{{.Name}}"},
	} {
		args := []string{q.kind, "ls", "--filter", filter, "--format", q.format}
		if q.kind == "container" {
			args = []string{"ps", "--all", "--filter", filter, "--format", q.format}
		}
		b, err := p.run(ctx, "", args...)
		if err != nil {
			return nil, err
		}
		for _, name := range strings.Fields(string(b)) {
			out = append(out, q.kind+"/"+name)
		}
	}
	entries, err := os.ReadDir(p.dir(ref))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("list %s: %w", p.dir(ref), err)
	}
	for _, e := range entries {
		out = append(out, "file/"+e.Name())
	}
	return out, nil
}

// Instances counts the workload's running containers.
func (p *Provider) Instances(ctx context.Context, ref provider.ResourceRef) (int, error) {
	cs, err := p.containers(ctx, ref)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, c := range cs {
		if c.State == "running" {
			n++
		}
	}
	return n, nil
}

// Artifacts returns compose.yaml.
func (p *Provider) Artifacts(_ context.Context, ref provider.ResourceRef) (map[string][]byte, error) {
	return p.readFiles(ref, composeFile)
}

// SecretFiles returns secrets.env, after checking it is 0600.
func (p *Provider) SecretFiles(_ context.Context, ref provider.ResourceRef) (map[string][]byte, error) {
	path := filepath.Join(p.dir(ref), secretsFile)
	if info, err := os.Stat(path); err == nil && info.Mode().Perm() != 0o600 {
		return nil, fmt.Errorf("%s has mode %v, want 0600", path, info.Mode().Perm())
	}
	return p.readFiles(ref, secretsFile)
}

func (p *Provider) readFiles(ref provider.ResourceRef, names ...string) (map[string][]byte, error) {
	out := map[string][]byte{}
	for _, n := range names {
		b, err := os.ReadFile(filepath.Join(p.dir(ref), n))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", n, err)
		}
		out[n] = b
	}
	return out, nil
}
