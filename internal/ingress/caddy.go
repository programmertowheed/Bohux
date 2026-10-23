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
	// Weights, when set, splits requests between the upstreams in these proportions
	// (Caddy's weighted round robin), one per upstream: a canary (ADR-0049).
	Weights []int
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
	// AccessLog, if set, is where Caddy writes one line per request with its upstream,
	// status, and duration, and nothing else, for the SLO gate (ADR-0050). The same
	// path inside Caddy's container and on the host, like AdminSocket.
	AccessLog string

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
		proxy := map[string]any{"handler": "reverse_proxy", "upstreams": ups}
		handle := []map[string]any{proxy}
		if c.AccessLog != "" {
			// Names the upstream (so the slot) that served the request in its log line.
			handle = []map[string]any{{"handler": "log_append", "key": "upstream",
				"value": "{http.reverse_proxy.upstream.hostport}"}, proxy}
		}
		if len(s.Weights) > 0 {
			if len(s.Weights) != len(s.Upstreams) {
				return nil, fmt.Errorf("site %s has %d weights for %d upstreams", s.Host, len(s.Weights), len(s.Upstreams))
			}
			proxy["load_balancing"] = map[string]any{"selection_policy": map[string]any{
				"policy": "weighted_round_robin", "weights": s.Weights}}
		}
		routes = append(routes, map[string]any{
			"match":    []map[string]any{{"host": []string{s.Host}}},
			"handle":   handle,
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
	server := map[string]any{"listen": []string{":443"}, "routes": routes}
	cfg := map[string]any{
		// Loading replaces the whole config, admin listener included: keep it on the socket.
		// On a socket Caddy accepts only the listed Host headers; Go always sends one.
		"admin": map[string]any{"listen": "unix/" + c.AdminSocket, "origins": []string{"localhost"}},
		"apps": map[string]any{
			"http": map[string]any{"servers": map[string]any{"bohux": server}},
			"tls":  map[string]any{"automation": map[string]any{"policies": policies}},
		},
	}
	if c.AccessLog != "" {
		server["logs"] = map[string]any{"default_logger_name": accessLogger}
		cfg["logging"] = accessLogging(c.AccessLog)
	}
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode caddy config: %w", err)
	}
	return append(b, '\n'), nil
}

// accessLogger names the server's access logger: Caddy logs as http.log.access.<it>.
const accessLogger = "bohux"

// accessLogging keeps access lines out of Caddy's own output and writes them to path,
// rolled at 10 MB with one old file kept. The filter deletes the request (its URI,
// headers, and addresses) and the response headers, so a line holds only its time,
// duration, status, and upstream: no URL, token, or client address (ADR-0050).
func accessLogging(path string) map[string]any {
	drop := map[string]any{"filter": "delete"}
	return map[string]any{"logs": map[string]any{
		"default": map[string]any{"exclude": []string{"http.log.access." + accessLogger}},
		accessLogger: map[string]any{
			"include": []string{"http.log.access." + accessLogger},
			"writer":  map[string]any{"output": "file", "filename": path, "roll_size_mb": 10, "roll_keep": 1},
			"encoder": map[string]any{"format": "filter", "wrap": map[string]any{"format": "json"},
				"fields": map[string]any{"request": drop, "resp_headers": drop, "user_id": drop,
					"bytes_read": drop, "size": drop}},
		},
	}}
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
