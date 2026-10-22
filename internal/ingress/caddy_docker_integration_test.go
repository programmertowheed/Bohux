//go:build integration

package ingress

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// caddyImage is what the install bundle runs (ADR-0034).
const caddyImage = "caddy:2.8.4-alpine"

func docker(t *testing.T, args ...string) string {
	t.Helper()
	out, err := exec.Command("docker", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("docker %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// get fetches https://host/ through the published port, trusting any certificate.
func get(port, host string) (string, error) {
	c := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{
		TLSClientConfig: &tls.Config{ServerName: host, InsecureSkipVerify: true}, //nolint:gosec // Caddy's internal CA, test only
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, network, "127.0.0.1:"+port)
		},
	}}
	resp, err := c.Get("https://" + host + "/")
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	return strings.TrimSpace(string(b)), err
}

func eventually(t *testing.T, what string, f func() error) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		err := f()
		if err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: %v", what, err)
		}
		time.Sleep(300 * time.Millisecond)
	}
}

// TestCaddy runs the node's ingress for real: Load serves a host from an endpoint, a
// load that fails leaves the file as it was and is retried, and a restarted Caddy
// serves from the file alone, with no agent and no control plane (rules 1 and 2).
func TestCaddy(t *testing.T) {
	if err := exec.Command("docker", "image", "inspect", caddyImage).Run(); err != nil {
		t.Skip("no Docker daemon or no ", caddyImage, " image: ", err)
	}
	ctx := context.Background()
	suffix := fmt.Sprint(time.Now().UnixNano())
	network := "bohux-ingress-test-" + suffix
	docker(t, "network", "create", network)
	t.Cleanup(func() { _ = exec.Command("docker", "network", "rm", network).Run() })

	// Unix socket paths are short (108 bytes): not under t.TempDir().
	sockDir, err := os.MkdirTemp("/tmp", "bxc")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(sockDir) })
	// The access log sits beside the socket, at the same path inside and out (ADR-0050).
	c := &Caddy{AdminSocket: filepath.Join(sockDir, "admin.sock"), Dir: t.TempDir(),
		AccessLog: filepath.Join(sockDir, "access.log")}
	first, err := c.Render(nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeAtomic(filepath.Join(c.Dir, ConfigFile), first); err != nil {
		t.Fatal(err)
	}

	backend := "backend-" + suffix
	docker(t, "run", "-d", "--name", backend, "--network", network, "--network-alias", "web",
		"busybox:1.36", "sh", "-c", "mkdir -p /www && echo hello > /www/index.html && exec httpd -f -p 8080 -h /www")
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", backend).Run() })
	name := "caddy-" + suffix
	docker(t, "run", "-d", "--name", name, "--network", network, "-p", "127.0.0.1::443",
		"-v", sockDir+":"+sockDir, "-v", c.Dir+":/etc/bohux-ingress:ro",
		caddyImage, "caddy", "run", "--config", "/etc/bohux-ingress/"+ConfigFile)
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", name).Run() })
	t.Cleanup(func() {
		if t.Failed() {
			out, _ := exec.Command("docker", "logs", "--tail", "30", name).CombinedOutput()
			t.Logf("caddy logs:\n%s", out)
		}
	})
	_, port, err := net.SplitHostPort(docker(t, "port", name, "443/tcp"))
	if err != nil {
		t.Fatal(err)
	}

	sites := []Site{{Host: "web.localhost", Upstreams: []string{"web:8080"}}}
	eventually(t, "load", func() error { return c.Load(ctx, sites) })
	eventually(t, "serve web.localhost", func() error {
		body, err := get(port, "web.localhost")
		if err == nil && body != "hello" {
			err = fmt.Errorf("body %q", body)
		}
		return err
	})

	t.Run("a failed load writes nothing and is retried", func(t *testing.T) {
		docker(t, "stop", name)
		more := append(sites, Site{Host: "www.localhost", Upstreams: []string{"web:8080"}})
		if err := c.Load(ctx, more); err == nil {
			t.Fatal("Load succeeded with Caddy stopped")
		}
		onDisk, err := os.ReadFile(filepath.Join(c.Dir, ConfigFile))
		if err != nil || strings.Contains(string(onDisk), "www.localhost") {
			t.Errorf("a config Caddy never accepted reached the file: %v", err)
		}
		docker(t, "start", name)
		eventually(t, "retry the load", func() error { return c.Load(ctx, more) })
		_, port, err := net.SplitHostPort(docker(t, "port", name, "443/tcp"))
		if err != nil {
			t.Fatal(err)
		}
		eventually(t, "serve www.localhost", func() error {
			body, err := get(port, "www.localhost")
			if err == nil && body != "hello" {
				err = fmt.Errorf("body %q", body)
			}
			return err
		})
	})
	t.Run("weights split requests between upstreams (a canary)", func(t *testing.T) {
		canary := "canary-" + suffix
		docker(t, "run", "-d", "--name", canary, "--network", network, "--network-alias", "canary",
			"busybox:1.36", "sh", "-c", "mkdir -p /www && echo canary > /www/index.html && exec httpd -f -p 8080 -h /www")
		t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", canary).Run() })
		// The hosts loaded so far stay, so the restart below still serves them.
		split := append(append([]Site{}, sites...), Site{Host: "www.localhost", Upstreams: []string{"web:8080"}},
			Site{Host: "split.localhost", Upstreams: []string{"web:8080", "canary:8080"}, Weights: []int{3, 1}})
		eventually(t, "load the weighted site", func() error { return c.Load(ctx, split) })
		_, port, err := net.SplitHostPort(docker(t, "port", name, "443/tcp"))
		if err != nil {
			t.Fatal(err)
		}
		eventually(t, "serve split.localhost", func() error { _, err := get(port, "split.localhost"); return err })
		counts := map[string]int{}
		for range 40 {
			body, err := get(port, "split.localhost")
			if err != nil {
				t.Fatal(err)
			}
			counts[body]++
		}
		if counts["canary"] < 8 || counts["canary"] > 12 || counts["hello"]+counts["canary"] != 40 {
			t.Errorf("40 requests at weights 3:1 went %v; want about 30 and 10", counts)
		}
	})
	t.Run("the access log names each request's upstream and nothing about the request", func(t *testing.T) {
		eventually(t, "log the split's requests", func() error {
			b, err := os.ReadFile(c.AccessLog)
			if err != nil {
				return err
			}
			if strings.Contains(string(b), "split.localhost") || strings.Contains(string(b), "172.") ||
				strings.Contains(string(b), "127.0.0.1") {
				t.Fatalf("the access log holds a host or an address:\n%s", b)
			}
			byUpstream := map[string]int{}
			for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
				var l map[string]any
				if err := json.Unmarshal([]byte(line), &l); err != nil {
					return fmt.Errorf("line %q: %w", line, err)
				}
				for _, f := range []string{"request", "resp_headers", "user_id"} {
					if _, ok := l[f]; ok {
						t.Fatalf("field %s logged: %s", f, line)
					}
				}
				if _, ok := l["duration"].(float64); !ok || l["status"] != float64(200) {
					return fmt.Errorf("line without a status or duration: %s", line)
				}
				up, _ := l["upstream"].(string)
				byUpstream[up]++
			}
			if byUpstream["canary:8080"] < 10 || byUpstream["web:8080"] < 30 {
				return fmt.Errorf("requests by upstream %v; want at least 30 to web:8080 and 10 to canary:8080", byUpstream)
			}
			return nil
		})
	})
	t.Run("a restarted Caddy serves from the file alone", func(t *testing.T) {
		docker(t, "restart", name)
		_, port, err := net.SplitHostPort(docker(t, "port", name, "443/tcp"))
		if err != nil {
			t.Fatal(err)
		}
		eventually(t, "serve after restart", func() error {
			body, err := get(port, "www.localhost")
			if err == nil && body != "hello" {
				err = fmt.Errorf("body %q", body)
			}
			return err
		})
	})
}
