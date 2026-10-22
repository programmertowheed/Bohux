package swarm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/bohumix/bohux/internal/provider"
)

const defaultHealthTimeout = 2 * time.Second

// container is one line of `docker ps --format json`: one task's container.
type container struct {
	ID     string `json:"ID"`
	Names  string `json:"Names"`
	State  string `json:"State"`
	Labels string `json:"Labels"`
}

func (c container) label(key string) string {
	for _, kv := range strings.Split(c.Labels, ",") {
		if k, v, ok := strings.Cut(kv, "="); ok && k == key {
			return v
		}
	}
	return ""
}

// instance is the task's name without its ID, "<stack>_app.<n>", as `docker service
// logs --no-task-ids` names it.
func (c container) instance() string {
	name := c.label(labelTask)
	if i := strings.LastIndex(name, "."); i > 0 {
		return name[:i]
	}
	return name
}

// containers lists the service's task containers, any state. Swarm keeps a few
// exited ones as task history; callers that count look at running ones.
func (p *Provider) containers(ctx context.Context, ref provider.ResourceRef) ([]container, error) {
	out, err := p.run(ctx, "", "ps", "--all", "--no-trunc", "--filter", "label="+labelService+"="+serviceName(ref),
		"--format", "json")
	if err != nil {
		return nil, err
	}
	var cs []container
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var c container
		if err := json.Unmarshal(line, &c); err != nil {
			return nil, fmt.Errorf("parse docker ps: %w", err)
		}
		cs = append(cs, c)
	}
	return cs, sc.Err()
}

func running(cs []container) []container {
	var out []container
	for _, c := range cs {
		if c.State == "running" {
			out = append(out, c)
		}
	}
	return out
}

// serviceExists reports whether the stack's service is in the swarm.
func (p *Provider) serviceExists(ctx context.Context, ref provider.ResourceRef) (bool, error) {
	out, err := p.run(ctx, "", "service", "ls", "--quiet", "--filter", "name="+serviceName(ref))
	if err != nil {
		return false, err
	}
	return len(bytes.TrimSpace(out)) > 0, nil
}

func (p *Provider) readState(ref provider.ResourceRef) (*state, error) {
	b, err := os.ReadFile(filepath.Join(p.dir(ref), stateFile))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read state: %w", err)
	}
	var st state
	if err := json.Unmarshal(b, &st); err != nil {
		return nil, fmt.Errorf("decode state: %w", err)
	}
	return &st, nil
}

// Status reports what is running. Hash is reported only when exactly the desired
// number of tasks run and all carry the same spec hash; anything less reads as drift,
// so the agent applies again.
func (p *Provider) Status(ctx context.Context, ref provider.ResourceRef) (provider.Status, error) {
	st, err := p.readState(ref)
	if err != nil {
		return provider.Status{}, err
	}
	exists, err := p.serviceExists(ctx, ref)
	if err != nil {
		return provider.Status{}, err
	}
	if st == nil && !exists {
		return provider.Status{}, nil
	}
	out := provider.Status{Exists: true}
	cs, err := p.containers(ctx, ref)
	if err != nil {
		return provider.Status{}, err
	}
	up := running(cs)
	hashes := map[string]bool{}
	for _, c := range up {
		hashes[c.label(labelSpecHash)] = true
	}
	out.ReadyReplicas = int32(len(up))
	want := int32(0)
	if st != nil {
		want = st.Replicas
	}
	out.Running = exists && len(up) == int(want)
	if out.Running && len(hashes) == 1 {
		for h := range hashes {
			out.Hash = h
		}
	}
	if !out.Running {
		out.Message = fmt.Sprintf("%d of %d replicas running", len(up), want)
		return out, nil
	}
	if len(st.IngressPorts) > 0 {
		out.Endpoints = map[int32]string{}
		for _, port := range st.IngressPorts {
			out.Endpoints[port] = net.JoinHostPort(stack(ref), strconv.Itoa(int(port)))
		}
	}
	out.Healthy = true
	if st.Health == nil {
		return out, nil
	}
	for _, c := range up {
		if err := p.checkHealth(ctx, c, st.Health); err != nil {
			out.Healthy = false
			out.Message = fmt.Sprintf("%s: %v", c.instance(), err)
			break
		}
	}
	return out, nil
}

// checkHealth makes the HTTP health check from inside the task's network namespace
// (ADR-0045 item 4): the host has no route to the overlay.
func (p *Provider) checkHealth(ctx context.Context, c container, h *healthJSON) error {
	out, err := p.run(ctx, "", "inspect", "--format", "{{.State.Pid}}", c.ID)
	if err != nil {
		return err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil || pid <= 0 {
		return errors.New("the task's process is not running")
	}
	timeout := defaultHealthTimeout
	if h.TimeoutSeconds > 0 {
		timeout = time.Duration(h.TimeoutSeconds) * time.Second
	}
	return probeInNamespace(ctx, p.http, pid, h.Port, h.Path, timeout)
}
