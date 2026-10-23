// Package docker is the Docker provider (ADR-0025): it renders a readable Compose file
// and a 0600 secrets.env per workload (rule 2, ADR-0008) and drives the docker compose
// CLI against exactly that file. It runs inside the agent (ADR-0007).
package docker

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
const Name = "docker"

// Minimum versions (ADR-0025).
const (
	minEngineMajor  = 25
	minComposeMajor = 2
	minComposeMinor = 24
)

// Provider drives one Docker daemon.
type Provider struct {
	// Root holds one directory per workload: compose.yaml, secrets.env, state.
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
		return nil, errors.New("docker provider: artifact root is required")
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("docker provider: create %s: %w", root, err)
	}
	return &Provider{Root: root, Host: host, http: &http.Client{
		// Health checks must not follow redirects off the container.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}, nil
}

// Describe checks the CLI and Compose versions and declares every verb.
func (p *Provider) Describe(ctx context.Context) (provider.Descriptor, error) {
	engine, err := p.run(ctx, "", "version", "--format", "{{.Server.Version}}")
	if err != nil {
		return provider.Descriptor{}, fmt.Errorf("docker daemon: %w", err)
	}
	if major, _, ok := majorMinor(string(engine)); !ok || major < minEngineMajor {
		return provider.Descriptor{}, fmt.Errorf("docker engine %s is older than %d", strings.TrimSpace(string(engine)), minEngineMajor)
	}
	compose, err := p.run(ctx, "", "compose", "version", "--short")
	if err != nil {
		return provider.Descriptor{}, fmt.Errorf("docker compose plugin: %w", err)
	}
	cv := strings.TrimSpace(string(compose))
	major, minor, ok := majorMinor(cv)
	if !ok || major < minComposeMajor || (major == minComposeMajor && minor < minComposeMinor) {
		return provider.Descriptor{}, fmt.Errorf("docker compose %s is older than %d.%d", cv, minComposeMajor, minComposeMinor)
	}
	return provider.Descriptor{Name: Name, Version: "compose " + cv, Capabilities: provider.AllVerbs}, nil
}

func majorMinor(v string) (int, int, bool) {
	parts := strings.SplitN(strings.TrimPrefix(strings.TrimSpace(v), "v"), ".", 3)
	if len(parts) < 2 {
		return 0, 0, false
	}
	major, err1 := strconv.Atoi(parts[0])
	minor, err2 := strconv.Atoi(parts[1])
	return major, minor, err1 == nil && err2 == nil
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

// runEnv is run with extra environment variables.
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
		return out, fmt.Errorf("docker %s: %w: %s", firstArgs(args), err, msg)
	}
	return out, nil
}

func firstArgs(args []string) string {
	if len(args) > 3 {
		args = args[:3]
	}
	return strings.Join(args, " ")
}
