// Package openbao stores and resolves secret values in OpenBao's KV v2 engine
// (ADR-0008). Values pass through this package and nowhere else on the control plane:
// they are never logged, never put in an error, and never returned by anything but
// Resolve (rule 3).
package openbao

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ErrNotFound means the path, or the requested version of it, holds no value.
var ErrNotFound = errors.New("openbao: secret not found")

// Client talks to one KV v2 mount. It needs only these calls, so it is plain HTTP
// rather than OpenBao's Go module and its dependencies.
type Client struct {
	// Addr is the server URL, e.g. http://127.0.0.1:8200.
	Addr string
	// Token authenticates every call. It is a credential: never log it.
	Token string
	// Mount is the KV v2 mount path, e.g. "secret".
	Mount string
	// HTTP is the client to use; nil means one with a 10 s timeout.
	HTTP *http.Client
}

// Put writes value as the next version of path and returns that version.
func (c *Client) Put(ctx context.Context, path, value string) (int, error) {
	body, err := json.Marshal(map[string]any{"data": map[string]string{"value": value}})
	if err != nil {
		return 0, errors.New("openbao: encode value")
	}
	var out struct {
		Data struct {
			Version int `json:"version"`
		} `json:"data"`
	}
	if err := c.do(ctx, http.MethodPost, "data/"+path, nil, body, &out); err != nil {
		return 0, err
	}
	if out.Data.Version < 1 {
		return 0, fmt.Errorf("openbao: write %s returned no version", path)
	}
	return out.Data.Version, nil
}

// Get reads one version of path.
func (c *Client) Get(ctx context.Context, path string, version int) (string, error) {
	var out struct {
		Data struct {
			Data map[string]string `json:"data"`
		} `json:"data"`
	}
	q := url.Values{"version": {fmt.Sprint(version)}}
	if err := c.do(ctx, http.MethodGet, "data/"+path, q, nil, &out); err != nil {
		return "", err
	}
	v, ok := out.Data.Data["value"]
	if !ok {
		// A deleted or destroyed version reads back with no data.
		return "", fmt.Errorf("%w: %s version %d", ErrNotFound, path, version)
	}
	return v, nil
}

// Destroy removes every version of path and its metadata.
func (c *Client) Destroy(ctx context.Context, path string) error {
	err := c.do(ctx, http.MethodDelete, "metadata/"+path, nil, nil, nil)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	return err
}

// Ping checks that OpenBao answers and is unsealed.
func (c *Client) Ping(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(c.Addr, "/")+"/v1/sys/health", nil)
	if err != nil {
		return fmt.Errorf("openbao: health request: %w", err)
	}
	resp, err := c.client().Do(req)
	if err != nil {
		return fmt.Errorf("openbao: health: %w", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("openbao: health: status %d (sealed or not initialized)", resp.StatusCode)
	}
	return nil
}

// do sends one request. Response bodies are decoded into out only on success and are
// never copied into errors: a body could echo a value.
func (c *Client) do(ctx context.Context, method, sub string, q url.Values, body []byte, out any) error {
	u := strings.TrimRight(c.Addr, "/") + "/v1/" + strings.Trim(c.Mount, "/") + "/" + sub
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("openbao: build %s request: %w", method, err)
	}
	req.Header.Set("X-Vault-Token", c.Token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.client().Do(req)
	if err != nil {
		return fmt.Errorf("openbao: %s %s: %w", method, sub, err)
	}
	defer func() { _ = resp.Body.Close() }()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return fmt.Errorf("%w: %s", ErrNotFound, sub)
	case resp.StatusCode == http.StatusNoContent:
		return nil
	case resp.StatusCode/100 != 2:
		return fmt.Errorf("openbao: %s %s: status %d", method, sub, resp.StatusCode)
	}
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("openbao: decode %s response", sub)
	}
	return nil
}

func (c *Client) client() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 10 * time.Second}
}

// List returns the keys under prefix (one level; folders end with "/").
func (c *Client) List(ctx context.Context, prefix string) ([]string, error) {
	var out struct {
		Data struct {
			Keys []string `json:"keys"`
		} `json:"data"`
	}
	err := c.do(ctx, http.MethodGet, "metadata/"+strings.TrimSuffix(prefix, "/"), url.Values{"list": {"true"}}, nil, &out)
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	return out.Data.Keys, err
}

// Version is one version's metadata.
type Version struct {
	// Gone is true when the version was deleted or destroyed: it has no value.
	Gone bool
}

// Versions returns the metadata of every version of path, by version number, and
// the current version.
func (c *Client) Versions(ctx context.Context, path string) (map[int]Version, int, error) {
	var out struct {
		Data struct {
			CurrentVersion int `json:"current_version"`
			Versions       map[string]struct {
				DeletionTime string `json:"deletion_time"`
				Destroyed    bool   `json:"destroyed"`
			} `json:"versions"`
		} `json:"data"`
	}
	if err := c.do(ctx, http.MethodGet, "metadata/"+path, nil, nil, &out); err != nil {
		return nil, 0, err
	}
	vs := make(map[int]Version, len(out.Data.Versions))
	for k, v := range out.Data.Versions {
		var n int
		if _, err := fmt.Sscan(k, &n); err != nil {
			return nil, 0, fmt.Errorf("openbao: version %q of %s", k, path)
		}
		vs[n] = Version{Gone: v.Destroyed || v.DeletionTime != ""}
	}
	return vs, out.Data.CurrentVersion, nil
}

// DestroyVersions permanently removes the given versions of path.
func (c *Client) DestroyVersions(ctx context.Context, path string, versions []int) error {
	body, err := json.Marshal(map[string][]int{"versions": versions})
	if err != nil {
		return errors.New("openbao: encode versions")
	}
	return c.do(ctx, http.MethodPost, "destroy/"+path, nil, body, nil)
}
