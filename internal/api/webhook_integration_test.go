//go:build integration

package api

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	bohuxv1 "github.com/bohumix/bohux/gen/bohux/v1"
	"github.com/bohumix/bohux/gen/bohux/v1/bohuxv1connect"
	"github.com/bohumix/bohux/internal/auth"
	"github.com/bohumix/bohux/internal/store"
	"github.com/bohumix/bohux/internal/workflow/deploy"
)

// fakeDeployer counts what the API starts and can refuse as a running deploy would.
type fakeDeployer struct {
	mu      sync.Mutex
	builds  []string
	deploys []deploy.Input
	running bool
}

func (f *fakeDeployer) Start(_ context.Context, in deploy.Input) (string, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.running {
		return "", "", deploy.ErrRunning
	}
	f.deploys = append(f.deploys, in)
	return deploy.WorkflowID(in.AppID), uuid.NewString(), nil
}

func (f *fakeDeployer) StartBuild(_ context.Context, in deploy.BuildInput) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.builds = append(f.builds, in.ReleaseID)
	return deploy.BuildWorkflowID(in.ReleaseID), nil
}

func (f *fakeDeployer) Cancel(context.Context, string, string) error { return nil }

func (f *fakeDeployer) count() (int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.builds), len(f.deploys)
}

func sign(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// TestWebhooks: a signed push to the branch deploys its commit as the webhook, once;
// anything unsigned, misdirected, or revoked starts nothing and reveals nothing
// (ADR-0037).
func TestWebhooks(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	deployer := &fakeDeployer{}
	mux := http.NewServeMux()
	(&Server{Store: h.st, Authn: &auth.Authenticator{Store: h.st}, PKI: h.pki,
		Secrets: &memBackend{vals: map[string][]string{}}, Deploys: deployer}).Mount(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	apps := bohuxv1connect.NewAppServiceClient(srv.Client(), srv.URL)
	envs := bohuxv1connect.NewEnvironmentServiceClient(srv.Client(), srv.URL)
	hooks := bohuxv1connect.NewWebhookServiceClient(srv.Client(), srv.URL)

	project := h.project("web")
	viewer, err := h.createToken(h.root, project, bohuxv1.Role_ROLE_VIEWER, "v")
	if err != nil {
		t.Fatal(err)
	}
	e, err := envs.CreateEnvironment(ctx, req(h.root, &bohuxv1.CreateEnvironmentRequest{ProjectId: project.String(),
		Slug: "prod", ProviderBinding: dockerOn(h.node("vps-1")), IdempotencyKey: "e"}))
	if err != nil {
		t.Fatal(err)
	}
	create := func(slug string, src *bohuxv1.Source) string {
		a, err := apps.CreateApp(ctx, req(h.root, &bohuxv1.CreateAppRequest{EnvironmentId: e.Msg.GetEnvironment().GetMeta().GetId(),
			Slug: slug, Source: src, Runtime: &bohuxv1.Runtime{Replicas: 1}, IdempotencyKey: slug}))
		if err != nil {
			t.Fatal(err)
		}
		return a.Msg.GetApp().GetMeta().GetId()
	}
	gitApp := create("site", &bohuxv1.Source{Kind: &bohuxv1.Source_Git{Git: &bohuxv1.GitSource{RepoUrl: "https://example.com/acme/site.git"}}})
	imageApp := create("api", &bohuxv1.Source{Kind: &bohuxv1.Source_Image{Image: &bohuxv1.ImageSource{
		Repository: "ghcr.io/acme/api", Digest: "sha256:" + strings.Repeat("ab", 32)}}})

	_, err = hooks.CreateWebhook(ctx, req(h.root, &bohuxv1.CreateWebhookRequest{AppId: imageApp, Branch: "main", IdempotencyKey: "i"}))
	wantCode(t, err, connect.CodeFailedPrecondition)
	_, err = hooks.CreateWebhook(ctx, req(viewer.GetSecret(), &bohuxv1.CreateWebhookRequest{AppId: gitApp, Branch: "main", IdempotencyKey: "v"}))
	wantCode(t, err, connect.CodePermissionDenied)
	_, err = hooks.CreateWebhook(ctx, req(h.root, &bohuxv1.CreateWebhookRequest{AppId: gitApp, Branch: "refs/heads/main", IdempotencyKey: "b"}))
	wantCode(t, err, connect.CodeInvalidArgument)

	created, err := hooks.CreateWebhook(ctx, req(h.root, &bohuxv1.CreateWebhookRequest{AppId: gitApp, Branch: "main", IdempotencyKey: "w"}))
	if err != nil {
		t.Fatal(err)
	}
	hook, secret := created.Msg.GetWebhook(), created.Msg.GetSecret()
	if len(secret) != 64 || !strings.HasPrefix(hook.GetPath(), "/hooks/v1/") {
		t.Fatalf("created %v with a %d-char secret", hook, len(secret))
	}
	replay, err := hooks.CreateWebhook(ctx, req(h.root, &bohuxv1.CreateWebhookRequest{AppId: gitApp, Branch: "main", IdempotencyKey: "w"}))
	if err != nil || replay.Msg.GetSecret() != "" || replay.Msg.GetWebhook().GetMeta().GetId() != hook.GetMeta().GetId() {
		t.Errorf("replay = %v, %v; want the same webhook and no secret", replay, err)
	}
	var stored int
	if err := h.st.InTenant(ctx, h.tenant, func(tx *store.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM webhooks_heads WHERE secret_handle LIKE '%' || $1 || '%'`, secret).Scan(&stored)
	}); err != nil || stored != 0 {
		t.Errorf("the secret reached Postgres (%d rows, %v)", stored, err)
	}

	post := func(path, event, signature string, body string) (int, string) {
		t.Helper()
		r, err := http.NewRequest(http.MethodPost, srv.URL+path, bytes.NewBufferString(body))
		if err != nil {
			t.Fatal(err)
		}
		r.Header.Set("X-GitHub-Event", event)
		r.Header.Set("X-Hub-Signature-256", signature)
		resp, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		var b bytes.Buffer
		_, _ = b.ReadFrom(resp.Body)
		return resp.StatusCode, b.String()
	}
	commit := strings.Repeat("c", 40)
	push := `{"ref": "refs/heads/main", "after": "` + commit + `", "repository": {"clone_url": "https://evil.example/x.git"}}`

	for _, tt := range []struct {
		name, path, event, sig, body string
		want                         int
	}{
		{"unsigned", hook.GetPath(), "push", "", push, http.StatusNotFound},
		{"wrong secret", hook.GetPath(), "push", sign("nope", []byte(push)), push, http.StatusNotFound},
		{"signature of another body", hook.GetPath(), "push", sign(secret, []byte("{}")), push, http.StatusNotFound},
		{"unknown webhook", "/hooks/v1/" + strings.Repeat("ab", 16) + "/" + strings.Repeat("cd", 16), "push", sign(secret, []byte(push)), push, http.StatusNotFound},
		{"ping", hook.GetPath(), "ping", sign(secret, []byte(`{}`)), `{}`, http.StatusNoContent},
		{"another branch", hook.GetPath(), "push", sign(secret, []byte(`{"ref": "refs/heads/dev", "after": "`+commit+`"}`)),
			`{"ref": "refs/heads/dev", "after": "` + commit + `"}`, http.StatusNoContent},
	} {
		if got, body := post(tt.path, tt.event, tt.sig, tt.body); got != tt.want {
			t.Errorf("%s: %d %q, want %d", tt.name, got, body, tt.want)
		}
	}
	if b, d := deployer.count(); b != 0 || d != 0 {
		t.Fatalf("refused deliveries started %d builds and %d deploys", b, d)
	}

	got, body := post(hook.GetPath(), "push", sign(secret, []byte(push)), push)
	if got != http.StatusAccepted || !strings.HasPrefix(body, "deployment ") {
		t.Fatalf("signed push: %d %q", got, body)
	}
	if got, _ := post(hook.GetPath(), "push", sign(secret, []byte(push)), push); got != http.StatusAccepted {
		t.Errorf("redelivery: %d", got)
	}
	if b, d := deployer.count(); b != 1 || d != 1 {
		t.Errorf("one push, delivered twice: %d builds and %d deploys, want 1 and 1", b, d)
	}
	var actorKind, ref string
	if err := h.st.InTenant(ctx, h.tenant, func(tx *store.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT actor_kind FROM audit_events WHERE action = $1 ORDER BY occurred_at DESC LIMIT 1`,
			bohuxv1connect.ReleaseServiceCreateReleaseProcedure).Scan(&actorKind); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT r.source_ref FROM releases_heads h JOIN releases_revisions r ON r.id = h.revision_id
			WHERE h.app_id = $1`, gitApp).Scan(&ref)
	}); err != nil {
		t.Fatal(err)
	}
	if actorKind != "webhook" || !strings.Contains(ref, commit) || strings.Contains(ref, "evil") {
		t.Errorf("deploy audited as %q, release source %q", actorKind, ref)
	}

	deployer.mu.Lock()
	deployer.running = true
	deployer.mu.Unlock()
	next := strings.Replace(push, commit, strings.Repeat("d", 40), 1)
	if got, _ := post(hook.GetPath(), "push", sign(secret, []byte(next)), next); got != http.StatusConflict {
		t.Errorf("a push while deploying: %d, want 409", got)
	}

	list, err := hooks.ListWebhooks(ctx, req(viewer.GetSecret(), &bohuxv1.ListWebhooksRequest{AppId: gitApp}))
	if err != nil || len(list.Msg.GetWebhooks()) != 1 {
		t.Fatalf("list = %v, %v", list, err)
	}
	if _, err := hooks.DeleteWebhook(ctx, req(h.root, &bohuxv1.DeleteWebhookRequest{Id: hook.GetMeta().GetId(),
		ExpectedRevisionId: hook.GetMeta().GetRevisionId(), IdempotencyKey: "d"})); err != nil {
		t.Fatal(err)
	}
	third := strings.Replace(push, commit, strings.Repeat("e", 40), 1)
	if got, _ := post(hook.GetPath(), "push", sign(secret, []byte(third)), third); got != http.StatusNotFound {
		t.Errorf("a revoked webhook: %d, want 404", got)
	}
}
