// Package ingress programs the node's Caddy (ADR-0019, ADR-0034). The agent owns the
// whole config: it renders one JSON document from every workload's routes, writes it
// to disk, where Caddy also reads it at start (rule 2: it serves without Bohux), and
// loads it through Caddy's admin API on a Unix socket in one atomic call.
package ingress

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// ConfigFile is the rendered config's name in Caddy.Dir.
const ConfigFile = "caddy.json"

// Site is one host and the endpoints that serve it.
type Site struct {
	Host      string
	Upstreams []string
}

// Caddy is the node's Caddy, reached through its admin socket.
type Caddy struct {
	// AdminSocket is the admin API's Unix socket, the same path inside Caddy's
	// container and on the host.
	AdminSocket string
	// Dir holds ConfigFile; Caddy mounts it and starts from it.
	Dir string
	// ACMEEmail is the ACME account's e-mail for automatic HTTPS; may be empty.
	ACMEEmail string

	mu     sync.Mutex
	loaded []byte // last config Caddy accepted from this process
}

// Render returns Caddy's JSON config for sites. Hosts under .localhost get Caddy's
// internal CA; all others get certificates over ACME.
func (c *Caddy) Render(sites []Site) ([]byte, error) {
	sorted := append([]Site(nil), sites...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Host < sorted[j].Host })
	routes := make([]map[string]any, 0, len(sorted))
	var local []string
	for _, s := range sorted {
		if len(s.Upstreams) == 0 {
			return nil, fmt.Errorf("site %s has no upstream", s.Host)
		}
		ups := make([]map[string]string, 0, len(s.Upstreams))
		for _, u := range s.Upstreams {
			ups = append(ups, map[string]string{"dial": u})
		}
		routes = append(routes, map[string]any{
			"match":    []map[string]any{{"host": []string{s.Host}}},
			"handle":   []map[string]any{{"handler": "reverse_proxy", "upstreams": ups}},
			"terminal": true,
		})
		if s.Host == "localhost" || strings.HasSuffix(s.Host, ".localhost") {
			local = append(local, s.Host)
		}
	}
	acme := map[string]any{"module": "acme"}
	if c.ACMEEmail != "" {
		acme["email"] = c.ACMEEmail
	}
	var policies []map[string]any
	if len(local) > 0 {
		policies = append(policies, map[string]any{"subjects": local,
			"issuers": []map[string]any{{"module": "internal"}}})
	}
	policies = append(policies, map[string]any{"issuers": []map[string]any{acme}})
	cfg := map[string]any{
		// Loading replaces the whole config, admin listener included: keep it on the socket.
		// On a socket Caddy accepts only the listed Host headers; Go always sends one.
		"admin": map[string]any{"listen": "unix/" + c.AdminSocket, "origins": []string{"localhost"}},
		"apps": map[string]any{
			"http": map[string]any{"servers": map[string]any{"bohux": map[string]any{
				"listen": []string{":443"},
				"routes": routes,
			}}},
			"tls": map[string]any{"automation": map[string]any{"policies": policies}},
		},
	}
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode caddy config: %w", err)
	}
	return append(b, '\n'), nil
}

// Load makes Caddy serve sites. It does nothing when this process already loaded the
// same config. Otherwise it loads it, then writes ConfigFile, so the file Caddy starts
// from only ever holds a config Caddy accepted. A refused config leaves Caddy serving
// the previous one, and the next Load tries again.
func (c *Caddy) Load(ctx context.Context, sites []Site) error {
	cfg, err := c.Render(sites)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if bytes.Equal(cfg, c.loaded) {
		return nil
	}
	if err := c.post(ctx, "/load", cfg); err != nil {
		return err
	}
	if err := writeAtomic(filepath.Join(c.Dir, ConfigFile), cfg); err != nil {
		return err
	}
	c.loaded = cfg
	return nil
}

func (c *Caddy) post(ctx context.Context, path string, body []byte) error {
	client := &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", c.AdminSocket)
		}},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://localhost"+path, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("caddy admin request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("caddy admin %s: %w", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("caddy admin %s: %s: %s", path, resp.Status, strings.TrimSpace(string(msg)))
	}
	return nil
}

func writeAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(path), err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", filepath.Base(path), err)
	}
	if err := os.Chmod(tmp, 0o644); err != nil {
		return fmt.Errorf("chmod %s: %w", filepath.Base(path), err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return errors.Join(fmt.Errorf("install %s: %w", filepath.Base(path), err), os.Remove(tmp))
	}
	return nil
}
