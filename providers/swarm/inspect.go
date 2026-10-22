package swarm

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strings"

	"github.com/bohumix/bohux/internal/provider"
)

// stackObjects lists the swarm's services, networks, and containers of the stack.
// While the service exists, exited task containers are Swarm's task history, which
// it trims itself, and only live ones are listed; once it is gone, every container
// is a leftover.
func (p *Provider) stackObjects(ctx context.Context, ref provider.ResourceRef, allContainers bool) ([]string, error) {
	filter := "label=" + labelStack + "=" + stack(ref)
	var out []string
	for _, q := range []struct {
		kind string
		args []string
	}{
		{"service", []string{"service", "ls", "--filter", filter, "--format", "{{.Name}}"}},
		{"network", []string{"network", "ls", "--filter", filter, "--format", "{{.Name}}"}},
		{"volume", []string{"volume", "ls", "--filter", filter, "--format", "{{.Name}}"}},
	} {
		b, err := p.run(ctx, "", q.args...)
		if err != nil {
			return nil, err
		}
		for _, name := range strings.Fields(string(b)) {
			out = append(out, q.kind+"/"+name)
		}
	}
	hasService := len(out) > 0 && strings.HasPrefix(out[0], "service/")
	b, err := p.run(ctx, "", "ps", "--all", "--filter", filter, "--format", "{{.Names}} {{.State}}")
	if err != nil {
		return nil, err
	}
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		name, state, ok := strings.Cut(line, " ")
		if !ok {
			continue
		}
		if allContainers || !hasService || state != "exited" {
			out = append(out, "container/"+name)
		}
	}
	return out, nil
}

// The methods below look at Docker and the disk directly. They implement the
// conformance suite's Inspector and are not used by the agent.

// Leftovers lists the stack's swarm objects and the files in its directory.
func (p *Provider) Leftovers(ctx context.Context, ref provider.ResourceRef) ([]string, error) {
	out, err := p.stackObjects(ctx, ref, false)
	if err != nil {
		return nil, err
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

// Reach connects to endpoint from a throwaway container on the ingress network, where
// the node's ingress runs.
func (p *Provider) Reach(ctx context.Context, endpoint string) error {
	host, port, err := net.SplitHostPort(endpoint)
	if err != nil {
		return fmt.Errorf("endpoint %q: %w", endpoint, err)
	}
	if _, err := p.run(ctx, "", "run", "--rm", "--network", IngressNetwork, "busybox:1.36",
		"nc", "-z", "-w", "5", host, port); err != nil {
		return fmt.Errorf("reach %s: %w", endpoint, err)
	}
	return nil
}

// Instances counts the service's running tasks.
func (p *Provider) Instances(ctx context.Context, ref provider.ResourceRef) (int, error) {
	cs, err := p.containers(ctx, ref)
	if err != nil {
		return 0, err
	}
	return len(running(cs)), nil
}

// Artifacts returns stack.yaml.
func (p *Provider) Artifacts(_ context.Context, ref provider.ResourceRef) (map[string][]byte, error) {
	return p.readFiles(ref, stackFile)
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
