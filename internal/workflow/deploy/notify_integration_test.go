//go:build integration

package deploy

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/bohumix/bohux/internal/notify"
	"github.com/bohumix/bohux/internal/secrets/openbao"
	"github.com/bohumix/bohux/internal/store"
	"github.com/bohumix/bohux/internal/store/fixture"
)

type memSecrets map[string]string

func (m memSecrets) Get(_ context.Context, path string, _ int) (string, error) {
	v, ok := m[path]
	if !ok {
		return "", fmt.Errorf("no %s", path)
	}
	return v, nil
}

// TestNotifyActivities posts a rolled-back Deployment to a project's Slack and webhook
// channels for real (over TLS), skipping channels that do not want the state and
// channels of other projects (ADR-0038).
func TestNotifyActivities(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	var (
		mu    sync.Mutex
		posts = map[string][]byte{}
		sigs  = map[string]string{}
	)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		posts[r.URL.Path], sigs[r.URL.Path] = b, r.Header.Get("X-Bohux-Signature")
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)
	secrets := memSecrets{}
	h.acts.Secrets, h.acts.HTTP = secrets, srv.Client()

	app, env := fixture.DeployableApp(t, h.st, h.tenant, h.node, "api", 1)
	in := h.input(app, env, fixture.Release(t, h.st, h.tenant, app, "ready", true))
	if _, err := h.env.ExecuteActivity(h.acts.RecordStart, in); err != nil {
		t.Fatal(err)
	}
	var project uuid.UUID
	if err := h.st.InTenant(ctx, h.tenant, func(tx *store.Tx) error {
		return tx.QueryRow(ctx, `SELECT project_id FROM environments_heads WHERE id = $1`, env).Scan(&project)
	}); err != nil {
		t.Fatal(err)
	}
	other, _ := fixture.App(t, h.st, h.tenant, "elsewhere", "other-env")
	var otherProject uuid.UUID
	if err := h.st.InTenant(ctx, h.tenant, func(tx *store.Tx) error {
		return tx.QueryRow(ctx, `SELECT e.project_id FROM apps_heads a JOIN environments_heads e ON e.id = a.environment_id
			WHERE a.id = $1`, other).Scan(&otherProject)
	}); err != nil {
		t.Fatal(err)
	}
	channel := func(project uuid.UUID, kind, path string, signing string, states ...string) uuid.UUID {
		t.Helper()
		id := uuid.Must(uuid.NewV7())
		secrets[notify.URLPath(h.tenant, id)] = srv.URL + path
		var sh *string
		if signing != "" {
			secrets[notify.SigningPath(h.tenant, id)] = signing
			s := openbao.HandlePrefix + notify.SigningPath(h.tenant, id)
			sh = &s
		}
		if err := h.st.InTenant(ctx, h.tenant, func(tx *store.Tx) error {
			_, err := tx.CreateWithID(ctx, "notification_channels", id, []store.Col{
				{Name: "project_id", Value: project}, {Name: "kind", Value: kind}, {Name: "states", Value: states},
				{Name: "url_handle", Value: openbao.HandlePrefix + notify.URLPath(h.tenant, id)},
				{Name: "url_host", Value: "127.0.0.1"}, {Name: "signing_handle", Value: sh},
			}, nil, store.Audit{Actor: store.Actor{ID: store.SystemActorID, Kind: store.ActorSystem},
				Action: "test", ResourceType: "bohux.v1.NotificationChannel", ProjectID: project})
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return id
	}
	channel(project, "slack", "/slack", "", "failed", "rolled_back")
	channel(project, "webhook", "/hook", "sign-me", "rolled_back")
	channel(project, "discord", "/discord", "", "succeeded")
	channel(otherProject, "slack", "/other", "", "rolled_back")

	n := NotifyInput{TenantID: h.tenant.String(), DeploymentID: in.DeploymentID, State: StateRolledBack,
		Failure: &Failure{Reason: ReasonHealthTimeout}, StartedBy: "webhook"}
	v, err := h.env.ExecuteActivity(h.acts.NotificationChannels, n)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	if err := v.Get(&ids); err != nil || len(ids) != 2 {
		t.Fatalf("channels = %v, %v; want slack and webhook of this project", ids, err)
	}
	for _, id := range ids {
		c := n
		c.ChannelID = id
		if _, err := h.env.ExecuteActivity(h.acts.NotifyChannel, c); err != nil {
			t.Fatalf("NotifyChannel: %v", err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	var slack struct{ Text string }
	if err := json.Unmarshal(posts["/slack"], &slack); err != nil || slack.Text == "" {
		t.Fatalf("slack got %s", posts["/slack"])
	}
	var o notify.Outcome
	if err := json.Unmarshal(posts["/hook"], &o); err != nil || o.State != "rolled_back" || o.Reason != ReasonHealthTimeout ||
		o.App != "api" || o.StartedBy != "webhook" {
		t.Errorf("webhook got %s", posts["/hook"])
	}
	mac := hmac.New(sha256.New, []byte("sign-me"))
	mac.Write(posts["/hook"])
	if sigs["/hook"] != "sha256="+hex.EncodeToString(mac.Sum(nil)) {
		t.Errorf("webhook signature %q does not match its body", sigs["/hook"])
	}
	if len(posts) != 2 {
		t.Errorf("posted to %d channels, want 2", len(posts))
	}
}
