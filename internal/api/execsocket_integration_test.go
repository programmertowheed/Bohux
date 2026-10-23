//go:build integration

package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/coder/websocket"
	"google.golang.org/protobuf/proto"

	bohuxv1 "github.com/bohumix/bohux/gen/bohux/v1"
	"github.com/bohumix/bohux/gen/bohux/v1/bohuxv1connect"
	"github.com/bohumix/bohux/internal/agentapi"
	"github.com/bohumix/bohux/internal/auth"
	"github.com/bohumix/bohux/internal/store"
)

// TestExecSocket checks the web terminal's bridge authenticates, authorizes, and
// audits as the Connect Exec does, and closes with 4000 + the Connect code (ADR-0046).
// A whole session against a real agent runs in the e2e test.
func TestExecSocket(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	mux := http.NewServeMux()
	// No agent is connected: a session that passes every check ends "node offline".
	(&Server{Store: h.st, Authn: &auth.Authenticator{Store: h.st}, PKI: h.pki,
		Agents: &agentapi.Server{Store: h.st}}).Mount(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	envs := bohuxv1connect.NewEnvironmentServiceClient(srv.Client(), srv.URL)
	project := h.project("one")
	e, err := envs.CreateEnvironment(ctx, req(h.root, &bohuxv1.CreateEnvironmentRequest{
		ProjectId: project.String(), Slug: "prod", ProviderBinding: dockerOn(h.node("vps-1")), IdempotencyKey: "e"}))
	if err != nil {
		t.Fatal(err)
	}
	app := fixtureApp(t, h, e.Msg.GetEnvironment().GetMeta().GetId())
	viewer, err := h.createToken(h.root, project, bohuxv1.Role_ROLE_VIEWER, "viewer")
	if err != nil {
		t.Fatal(err)
	}
	url := "ws" + strings.TrimPrefix(srv.URL, "http") + ExecSocketPath
	start := func(command ...string) []byte {
		b, err := proto.Marshal(&bohuxv1.ExecRequest{Msg: &bohuxv1.ExecRequest_Start{Start: &bohuxv1.ExecStart{
			AppId: app.String(), Command: command, Tty: true, Size: &bohuxv1.TerminalSize{Rows: 24, Cols: 80}}}})
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	stdin, _ := proto.Marshal(&bohuxv1.ExecRequest{Msg: &bohuxv1.ExecRequest_Stdin{Stdin: []byte("ls\n")}})

	type frame struct {
		typ  websocket.MessageType
		data []byte
	}
	text := func(s string) frame { return frame{websocket.MessageText, []byte(s)} }
	binary := func(b []byte) frame { return frame{websocket.MessageBinary, b} }
	for _, tc := range []struct {
		name   string
		frames []frame
		want   connect.Code
		reason string
	}{
		{"token must come first", []frame{binary(start("sh"))}, connect.CodeUnauthenticated, "the first frame must be the API token"},
		{"unknown token", []frame{text("bxa_nope"), binary(start("sh"))}, connect.CodeUnauthenticated, "unauthenticated"},
		{"viewers cannot exec", []frame{text(viewer.GetSecret()), binary(start("sh"))}, connect.CodePermissionDenied, "permission denied"},
		{"start must come first", []frame{text(h.root), binary(stdin)}, connect.CodeInvalidArgument, "the first message must be start"},
		{"frames are protobuf", []frame{text(h.root), binary([]byte{0xff, 0xff})}, connect.CodeInvalidArgument, "an exec frame is not an ExecRequest"},
		{"frames are binary", []frame{text(h.root), text("ls")}, connect.CodeInvalidArgument, "must be binary protobuf"},
		{"command is required", []frame{text(h.root), binary(start())}, connect.CodeInvalidArgument, "command is required"},
		{"node offline", []frame{text(h.root), binary(start("sh"))}, connect.CodeUnavailable, "not connected"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
			defer cancel()
			c, _, err := websocket.Dial(ctx, url, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = c.CloseNow() }()
			for _, f := range tc.frames {
				if err := c.Write(ctx, f.typ, f.data); err != nil {
					t.Fatal(err)
				}
			}
			_, _, err = c.Read(ctx)
			var ce websocket.CloseError
			if !errors.As(err, &ce) {
				t.Fatalf("read = %v, want a close frame", err)
			}
			if want := websocket.StatusCode(4000 + int(tc.want)); ce.Code != want || !strings.Contains(ce.Reason, tc.reason) {
				t.Errorf("closed with %d %q, want %d containing %q", ce.Code, ce.Reason, want, tc.reason)
			}
		})
	}

	// A session that got past the checks is audited like a Connect one (rule 10).
	var sessions int
	if err := h.st.InTenant(ctx, h.tenant, func(tx *store.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE action = $1`,
			bohuxv1connect.WorkloadServiceExecProcedure).Scan(&sessions)
	}); err != nil || sessions != 1 {
		t.Errorf("audited exec sessions = %d, %v; want 1", sessions, err)
	}

	// Another site's page cannot open a session with the user's browser.
	t.Run("cross-origin refused", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		_, resp, err := websocket.Dial(ctx, url, &websocket.DialOptions{
			HTTPHeader: http.Header{"Origin": []string{"https://evil.example"}}})
		if err == nil || resp == nil || resp.StatusCode != http.StatusForbidden {
			t.Errorf("cross-origin dial = %v, %v; want 403", resp, err)
		}
	})
}
