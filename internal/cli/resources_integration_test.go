//go:build integration

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	"github.com/bohumix/bohux/internal/api"
	"github.com/bohumix/bohux/internal/auth"
	"github.com/bohumix/bohux/internal/pki"
	"github.com/bohumix/bohux/internal/secrets/openbao"
	"github.com/bohumix/bohux/internal/store"
	"github.com/bohumix/bohux/internal/store/fixture"
	"github.com/bohumix/bohux/internal/store/storetest"
)

// TestResourceCommands drives every resource command against a real API server, the
// way the installer and the self-deploy test used to with curl (ADR-0036).
func TestResourceCommands(t *testing.T) {
	ctx := context.Background()
	dsn := storetest.NewAppDSN(t)
	if err := store.MigrateDSN(ctx, dsn); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	tenant, root, err := api.Bootstrap(ctx, st, "acme", "Acme")
	if err != nil {
		t.Fatal(err)
	}
	fixture.Node(t, st, tenant, "vps-1")
	authority, err := pki.LoadOrCreate(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	srv := &api.Server{Store: st, Authn: &auth.Authenticator{Store: st}, PKI: authority}
	addr, token := os.Getenv("BOHUX_TEST_OPENBAO_ADDR"), os.Getenv("BOHUX_TEST_OPENBAO_TOKEN")
	if addr != "" {
		srv.Secrets = &openbao.Client{Addr: addr, Token: token, Mount: "secret"}
	}
	mux := http.NewServeMux()
	srv.Mount(mux)
	hs := httptest.NewServer(h2c.NewHandler(mux, &http2.Server{}))
	t.Cleanup(hs.Close)

	env := map[string]string{EnvAPIURL: hs.URL, EnvToken: root, EnvProject: "shop"}
	bohux := func(stdin string, args ...string) (string, error) {
		t.Helper()
		var out bytes.Buffer
		err := Run(ctx, args[0], args[1:], strings.NewReader(stdin), &out, func(k string) string { return env[k] })
		return out.String(), err
	}
	must := func(args ...string) string {
		t.Helper()
		out, err := bohux("", args...)
		if err != nil {
			t.Fatalf("bohux %s: %v", strings.Join(args, " "), err)
		}
		return out
	}

	must("project", "create", "shop", "--name", "Shop")
	if out := must("project", "list"); !strings.Contains(out, "shop") || !strings.Contains(out, "Shop") {
		t.Errorf("project list:\n%s", out)
	}
	must("env", "create", "prod", "--node", "vps-1")
	if _, err := bohux("", "env", "create", "staging", "--node", "nope"); err == nil {
		t.Error("env create on an unknown node succeeded")
	}
	digest := "sha256:" + strings.Repeat("ab", 32)
	must("app", "create", "api", "-e", "prod", "--image", "ghcr.io/acme/api@"+digest,
		"--port", "8080", "--health", "/healthz", "--route", "api.example.com")
	out := must("app", "list", "-e", "prod", "-o", "json")
	var listed struct {
		Apps []struct {
			Slug   string `json:"slug"`
			Routes []struct {
				Host string `json:"host"`
				Port int    `json:"port"`
			} `json:"routes"`
		} `json:"apps"`
	}
	if err := json.Unmarshal([]byte(out), &listed); err != nil {
		t.Fatalf("app list -o json is not JSON: %v\n%s", err, out)
	}
	if len(listed.Apps) != 1 || listed.Apps[0].Slug != "api" || listed.Apps[0].Routes[0].Port != 8080 {
		t.Errorf("app list -o json = %+v", listed)
	}

	must("route", "add", "api", "www.example.com", "-e", "prod")
	if out := must("route", "list", "api", "-e", "prod"); !strings.Contains(out, "api.example.com") ||
		!strings.Contains(out, "www.example.com") {
		t.Errorf("route list after add:\n%s", out)
	}
	must("route", "remove", "api", "api.example.com", "-e", "prod")
	if out := must("app", "show", "api", "-e", "prod"); strings.Contains(out, "api.example.com") ||
		!strings.Contains(out, "www.example.com") {
		t.Errorf("app show after remove:\n%s", out)
	}
	if _, err := bohux("", "route", "remove", "api", "nope.example.com", "-e", "prod"); err == nil {
		t.Error("removing a route the app does not have succeeded")
	}

	if out := must("node", "list"); !strings.Contains(out, "vps-1") {
		t.Errorf("node list:\n%s", out)
	}
	if out := must("node", "token", "vps-2"); !strings.Contains(out, "bxb_") {
		t.Errorf("node token:\n%s", out)
	}

	if addr == "" {
		t.Log("BOHUX_TEST_OPENBAO_ADDR is not set; skipping secret commands")
		return
	}
	const value = "postgres://u:hunter2@db/app"
	out, err = bohux(value+"\n", "secret", "set", "DATABASE_URL", "-e", "prod", "--app", "api")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "hunter2") || !strings.Contains(out, "openbao:") {
		t.Errorf("secret set printed:\n%s", out)
	}
	if out := must("secret", "list", "-e", "prod"); !strings.Contains(out, "DATABASE_URL") || strings.Contains(out, "hunter2") {
		t.Errorf("secret list:\n%s", out)
	}
	if out := must("app", "show", "api", "-e", "prod", "-o", "json"); !strings.Contains(out, "DATABASE_URL") {
		t.Errorf("the app did not get the secret:\n%s", out)
	}
	if _, err := bohux("", "secret", "set", "EMPTY", "-e", "prod"); err == nil {
		t.Error("an empty value on stdin was accepted")
	}

	// A private registry credential is an environment secret the app names (ADR-0041).
	if _, err := bohux("", "app", "pull-secret", "api", "--secret", "NO_SUCH", "-e", "prod"); err == nil ||
		!strings.Contains(err.Error(), "NO_SUCH") {
		t.Errorf("pull-secret naming a missing secret: %v", err)
	}
	if _, err := bohux(`{"auths":{"r.example":{"auth":"eDpzZWtyaXQ="}}}`+"\n", "secret", "set", "REGISTRY", "-e", "prod"); err != nil {
		t.Fatal(err)
	}
	must("app", "pull-secret", "api", "--secret", "REGISTRY", "-e", "prod")
	if out := must("app", "show", "api", "-e", "prod"); !strings.Contains(out, "REGISTRY") || strings.Contains(out, "eDpzZWtyaXQ") {
		t.Errorf("app show after pull-secret:\n%s", out)
	}
	must("app", "pull-secret", "api", "--clear", "-e", "prod")
	if out := must("app", "show", "api", "-e", "prod", "-o", "json"); strings.Contains(out, "REGISTRY") {
		t.Errorf("app show after pull-secret --clear:\n%s", out)
	}

	must("app", "create", "site", "-e", "prod", "--git", "https://example.com/acme/site.git", "--port", "8080")
	out = must("webhook", "create", "site", "-e", "prod", "--branch", "main", "-o", "json")
	var created struct {
		Webhook struct {
			Meta struct {
				ID string `json:"id"`
			} `json:"meta"`
			Path string `json:"path"`
		} `json:"webhook"`
		Secret string `json:"secret"`
	}
	if err := json.Unmarshal([]byte(out), &created); err != nil || len(created.Secret) != 64 {
		t.Fatalf("webhook create -o json: %v\n%s", err, out)
	}
	if out := must("webhook", "list", "site", "-e", "prod"); !strings.Contains(out, hs.URL+created.Webhook.Path) ||
		strings.Contains(out, created.Secret) {
		t.Errorf("webhook list:\n%s", out)
	}
	must("webhook", "remove", "site", created.Webhook.Meta.ID, "-e", "prod")

	out, err = bohux("https://hooks.slack.com/services/T/B/sekrit\n", "channel", "create", "--kind", "slack", "--state", "failed")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "sekrit") || !strings.Contains(out, "hooks.slack.com") {
		t.Errorf("channel create printed:\n%s", out)
	}
	if out := must("channel", "list"); !strings.Contains(out, "slack") || !strings.Contains(out, "failed") {
		t.Errorf("channel list:\n%s", out)
	}
	if out := must("audit", "list", "--limit", "5"); !strings.Contains(out, "NotificationService/CreateNotificationChannel") {
		t.Errorf("audit list:\n%s", out)
	}
	if out := must("webhook", "list", "site", "-e", "prod"); strings.Contains(out, created.Webhook.Meta.ID) {
		t.Errorf("webhook list after remove:\n%s", out)
	}
}
