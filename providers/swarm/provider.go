// Package swarm is the Swarm provider (ADR-0045): one single-manager swarm on the
// agent's own node. It renders a readable stack file and a 0600 secrets.env per
// workload (rule 2, ADR-0008) and drives `docker stack deploy` against exactly that
// file. It runs inside the agent (ADR-0007).
package swarm

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/bohumix/bohux/internal/provider"
)

// Name is the provider name workloads use.
const Name = "swarm"

// minEngineMajor is the oldest Docker Engine the provider accepts (as ADR-0025).
const minEngineMajor = 25

// Provider drives the swarm whose only manager is the node's own dockerd.
type Provider struct {
	// Root holds one directory per workload: stack.yaml, secrets.env, state.
	Root string
	// Docker is the docker CLI binary; empty means "docker" on PATH.
	Docker string
	// Host is DOCKER_HOST for the CLI; empty means the local default.
	Host string

	http *http.Client
}

var _ provider.Provider = (*Provider)(nil)

// New returns a provider that keeps its artifacts under root.
func New(root, host string) (*Provider, error) {
	if root == "" {
		return nil, errors.New("swarm provider: artifact root is required")
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("swarm provider: create %s: %w", root, err)
	}
	return &Provider{Root: root, Host: host, http: &http.Client{
		// Health checks must not follow redirects off the container.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}, nil
}

// Describe checks the engine and that it is a swarm of exactly one manager, this node
// (ADR-0045 item 1), and declares every verb.
func (p *Provider) Describe(ctx context.Context) (provider.Descriptor, error) {
	out, err := p.run(ctx, "", "info", "--format",
		"{{.ServerVersion}} {{.Swarm.LocalNodeState}} {{.Swarm.ControlAvailable}} {{.Swarm.Nodes}}")
	if err != nil {
		return provider.Descriptor{}, fmt.Errorf("docker daemon: %w", err)
	}
	f := strings.Fields(string(out))
	if len(f) != 4 {
		return provider.Descriptor{}, fmt.Errorf("docker info: unexpected output %q", strings.TrimSpace(string(out)))
	}
	if major, err := strconv.Atoi(strings.SplitN(f[0], ".", 2)[0]); err != nil || major < minEngineMajor {
		return provider.Descriptor{}, fmt.Errorf("docker engine %s is older than %d", f[0], minEngineMajor)
	}
	if f[1] != "active" || f[2] != "true" {
		return provider.Descriptor{}, errors.New("this dockerd is not a swarm manager: run `docker swarm init` (ADR-0045)")
	}
	if f[3] != "1" {
		return provider.Descriptor{}, fmt.Errorf("the swarm has %s nodes; the Swarm provider runs on a single-manager swarm only (ADR-0045)", f[3])
	}
	return provider.Descriptor{Name: Name, Version: "swarm " + f[0], Capabilities: provider.AllVerbs}, nil
}

func (p *Provider) cmd(ctx context.Context, dir string, args ...string) *exec.Cmd {
	bin := p.Docker
	if bin == "" {
		bin = "docker"
	}
	c := exec.CommandContext(ctx, bin, args...)
	c.Dir = dir
	c.Env = os.Environ()
	if p.Host != "" {
		c.Env = append(c.Env, "DOCKER_HOST="+p.Host)
	}
	c.WaitDelay = 5 * time.Second
	return c
}

// run executes the docker CLI and returns stdout. Errors carry the CLI's stderr, which
// never holds secret values: they live only in secrets.env, which the CLI does not
// echo (rule 3).
func (p *Provider) run(ctx context.Context, dir string, args ...string) ([]byte, error) {
	return p.runEnv(ctx, dir, nil, args...)
}

func (p *Provider) runEnv(ctx context.Context, dir string, env []string, args ...string) ([]byte, error) {
	c := p.cmd(ctx, dir, args...)
	c.Env = append(c.Env, env...)
	var stderr bytes.Buffer
	c.Stderr = &stderr
	out, err := c.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if len(msg) > 2000 {
			msg = msg[:2000] + "..."
		}
		args = args[:min(len(args), 3)]
		return out, fmt.Errorf("docker %s: %w: %s", strings.Join(args, " "), err, msg)
	}
	return out, nil
}
