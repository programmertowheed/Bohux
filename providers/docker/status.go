package docker

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/bohumix/bohux/internal/provider"
)

const defaultHealthTimeout = 2 * time.Second

// container is one line of `docker compose ps --format json`.
type container struct {
	ID      string `json:"ID"`
	Name    string `json:"Name"`
	Service string `json:"Service"`
	State   string `json:"State"`
	Labels  string `json:"Labels"`
}

func (c container) label(key string) string {
	for _, kv := range strings.Split(c.Labels, ",") {
		if k, v, ok := strings.Cut(kv, "="); ok && k == key {
			return v
		}
	}
	return ""
}

// containers lists the project's containers, running or not. Compose prints one JSON
// object per line.
func (p *Provider) containers(ctx context.Context, ref provider.ResourceRef) ([]container, error) {
	out, err := p.run(ctx, "", "compose", "-p", project(ref), "ps", "--all", "--format", "json")
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
			return nil, fmt.Errorf("parse compose ps: %w", err)
		}
		cs = append(cs, c)
	}
	return cs, sc.Err()
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

// Status reports what is running. Hash is reported only when every desired replica
// runs and carries the same spec hash; anything less reads as drift, so the agent
// applies again.
func (p *Provider) Status(ctx context.Context, ref provider.ResourceRef) (provider.Status, error) {
	st, err := p.readState(ref)
	if err != nil {
		return provider.Status{}, err
	}
	cs, err := p.containers(ctx, ref)
	if err != nil {
		return provider.Status{}, err
	}
	if st == nil && len(cs) == 0 {
		return provider.Status{}, nil
	}
	out := provider.Status{Exists: true}
	var running []container
	hashes := map[string]bool{}
	for _, c := range cs {
		if c.Service != service {
			continue
		}
		if c.State == "running" {
			running = append(running, c)
			hashes[c.label(labelSpecHash)] = true
		}
	}
	out.ReadyReplicas = int32(len(running))
	want := int32(0)
	if st != nil {
		want = st.Replicas
	}
	out.Running = len(running) == int(want) && len(cs) == int(want)
	if out.Running && len(hashes) == 1 {
		for h := range hashes {
			out.Hash = h
		}
	}
	if !out.Running {
		out.Message = fmt.Sprintf("%d of %d replicas running", len(running), want)
		return out, nil
	}
	if st.Health == nil {
		out.Healthy = true
		return out, nil
	}
	out.Healthy = true
	for _, c := range running {
		if err := p.checkHealth(ctx, c, st.Health); err != nil {
			out.Healthy = false
			out.Message = fmt.Sprintf("%s: %v", c.Name, err)
			break
		}
	}
	return out, nil
}

// checkHealth sends the HTTP health check to the container from the host (ADR-0025).
func (p *Provider) checkHealth(ctx context.Context, c container, h *healthJSON) error {
	ip, err := p.containerIP(ctx, c.ID)
	if err != nil {
		return err
	}
	timeout := defaultHealthTimeout
	if h.TimeoutSeconds > 0 {
		timeout = time.Duration(h.TimeoutSeconds) * time.Second
	}
	hctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	url := "http://" + net.JoinHostPort(ip, strconv.Itoa(int(h.Port))) + h.Path
	req, err := http.NewRequestWithContext(hctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("health request: %w", err)
	}
	resp, err := p.http.Do(req)
	if err != nil {
		return fmt.Errorf("health check: %w", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("health check returned %d", resp.StatusCode)
	}
	return nil
}

func (p *Provider) containerIP(ctx context.Context, id string) (string, error) {
	out, err := p.run(ctx, "", "inspect", "--format",
		"{{range .NetworkSettings.Networks}}{{.IPAddress}} {{end}}", id)
	if err != nil {
		return "", err
	}
	for _, ip := range strings.Fields(string(out)) {
		if ip != "" {
			return ip, nil
		}
	}
	return "", errors.New("container has no IP address")
}
