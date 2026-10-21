//go:build integration

package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect"

	bohuxv1 "github.com/bohumix/bohux/gen/bohux/v1"
	"github.com/bohumix/bohux/gen/bohux/v1/bohuxv1connect"
	"github.com/bohumix/bohux/internal/auth"
	"github.com/bohumix/bohux/internal/notify"
	"github.com/bohumix/bohux/internal/store"
)

// TestNotificationChannels: only owners wire a project to a channel, the URL is a
// credential that goes to OpenBao and never comes back, and deleting a channel
// destroys it (ADR-0038).
func TestNotificationChannels(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	bao := &memBackend{vals: map[string][]string{}}
	mux := http.NewServeMux()
	(&Server{Store: h.st, Authn: &auth.Authenticator{Store: h.st}, PKI: h.pki, Secrets: bao}).Mount(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	ch := bohuxv1connect.NewNotificationServiceClient(srv.Client(), srv.URL)
	project := h.project("web")
	deployer, err := h.createToken(h.root, project, bohuxv1.Role_ROLE_DEPLOYER, "d")
	if err != nil {
		t.Fatal(err)
	}
	const slackURL = "https://hooks.slack.com/services/T000/B000/sekrit-path-token"
	create := func(token, key string, kind bohuxv1.NotificationKind, url string, states ...bohuxv1.DeploymentState) (*bohuxv1.CreateNotificationChannelResponse, error) {
		r, err := ch.CreateNotificationChannel(ctx, req(token, &bohuxv1.CreateNotificationChannelRequest{
			ProjectId: project.String(), Kind: kind, Url: url, States: states, IdempotencyKey: key}))
		if err != nil {
			return nil, err
		}
		return r.Msg, nil
	}
	_, err = create(deployer.GetSecret(), "d", bohuxv1.NotificationKind_NOTIFICATION_KIND_SLACK, slackURL)
	wantCode(t, err, connect.CodePermissionDenied)
	for i, bad := range []string{"http://hooks.slack.com/x", "https://user:pw@hooks.slack.com/x", "hooks.slack.com/x"} {
		_, err := create(h.root, "bad"+string(rune('a'+i)), bohuxv1.NotificationKind_NOTIFICATION_KIND_SLACK, bad)
		wantCode(t, err, connect.CodeInvalidArgument)
		if err != nil && strings.Contains(err.Error(), "pw") {
			t.Errorf("the error repeats the URL: %v", err)
		}
	}
	_, err = create(h.root, "state", bohuxv1.NotificationKind_NOTIFICATION_KIND_SLACK, slackURL,
		bohuxv1.DeploymentState_DEPLOYMENT_STATE_HEALTH_GATE)
	wantCode(t, err, connect.CodeInvalidArgument)

	slack, err := create(h.root, "slack", bohuxv1.NotificationKind_NOTIFICATION_KIND_SLACK, slackURL)
	if err != nil {
		t.Fatal(err)
	}
	if c := slack.GetChannel(); c.GetUrlHost() != "hooks.slack.com" || len(c.GetStates()) != 3 || slack.GetSigningSecret() != "" {
		t.Errorf("slack channel = %v (signing %q)", c, slack.GetSigningSecret())
	}
	hook, err := create(h.root, "hook", bohuxv1.NotificationKind_NOTIFICATION_KIND_WEBHOOK, "https://ops.example.com/bohux",
		bohuxv1.DeploymentState_DEPLOYMENT_STATE_SUCCEEDED)
	if err != nil {
		t.Fatal(err)
	}
	if len(hook.GetSigningSecret()) != 64 {
		t.Errorf("webhook signing secret = %q", hook.GetSigningSecret())
	}

	list, err := ch.ListNotificationChannels(ctx, req(deployer.GetSecret(), &bohuxv1.ListNotificationChannelsRequest{ProjectId: project.String()}))
	if err != nil || len(list.Msg.GetChannels()) != 2 {
		t.Fatalf("list = %v, %v", list, err)
	}
	if strings.Contains(list.Msg.String(), "sekrit") {
		t.Error("the list carries the URL")
	}
	var leaked int
	if err := h.st.InTenant(ctx, h.tenant, func(tx *store.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM notification_channels_heads WHERE url_handle LIKE '%sekrit%' OR url_host LIKE '%sekrit%'`).Scan(&leaked)
	}); err != nil || leaked != 0 {
		t.Errorf("the URL reached Postgres (%d, %v)", leaked, err)
	}

	// EMAIL needs the install's relay (ADR-0043).
	_, err = create(h.root, "mail-norelay", bohuxv1.NotificationKind_NOTIFICATION_KIND_EMAIL, "mailto:ops@example.com")
	wantCode(t, err, connect.CodeFailedPrecondition)
	relayed := httptest.NewServer(func() http.Handler {
		m := http.NewServeMux()
		(&Server{Store: h.st, Authn: &auth.Authenticator{Store: h.st}, PKI: h.pki, Secrets: bao, MailRelay: true}).Mount(m)
		return m
	}())
	t.Cleanup(relayed.Close)
	mailCh := bohuxv1connect.NewNotificationServiceClient(relayed.Client(), relayed.URL)
	mailCreate := func(key, url string) (*bohuxv1.CreateNotificationChannelResponse, error) {
		r, err := mailCh.CreateNotificationChannel(ctx, req(h.root, &bohuxv1.CreateNotificationChannelRequest{
			ProjectId: project.String(), Kind: bohuxv1.NotificationKind_NOTIFICATION_KIND_EMAIL, Url: url, IdempotencyKey: key}))
		if err != nil {
			return nil, err
		}
		return r.Msg, nil
	}
	for i, bad := range []string{"https://example.com", "mailto:ops@example.com?bcc=spy@evil.example", "mailto:Ops <ops@example.com>"} {
		_, err := mailCreate("mail-bad"+string(rune('a'+i)), bad)
		wantCode(t, err, connect.CodeInvalidArgument)
		if err != nil && strings.Contains(err.Error(), "evil") {
			t.Errorf("the error repeats the URL: %v", err)
		}
	}
	mailed, err := mailCreate("mail", " ops@example.com, dev@example.org")
	if err != nil {
		t.Fatal(err)
	}
	if c := mailed.GetChannel(); c.GetUrlHost() != "example.com,example.org" || mailed.GetSigningSecret() != "" {
		t.Errorf("email channel = %v", c)
	}
	bao.mu.Lock()
	stored := bao.vals[notify.URLPath(h.tenant, mustUUID(t, mailed.GetChannel().GetMeta().GetId()))]
	bao.mu.Unlock()
	if len(stored) != 1 || stored[0] != "mailto:ops@example.com,dev@example.org" {
		t.Errorf("stored recipients %q, want the canonical mailto: form", stored)
	}

	c := slack.GetChannel()
	if _, err := ch.DeleteNotificationChannel(ctx, req(h.root, &bohuxv1.DeleteNotificationChannelRequest{Id: c.GetMeta().GetId(),
		ExpectedRevisionId: c.GetMeta().GetRevisionId(), IdempotencyKey: "del"})); err != nil {
		t.Fatal(err)
	}
	bao.mu.Lock()
	defer bao.mu.Unlock()
	for path, vs := range bao.vals {
		for _, v := range vs {
			if v == slackURL {
				t.Errorf("the deleted channel's URL survives at %s", path)
			}
		}
	}
}
