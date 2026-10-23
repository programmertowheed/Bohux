//go:build integration

package api

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"connectrpc.com/connect"

	bohuxv1 "github.com/bohumix/bohux/gen/bohux/v1"
	"github.com/bohumix/bohux/gen/bohux/v1/bohuxv1connect"
	"github.com/bohumix/bohux/internal/auth"
	"github.com/bohumix/bohux/internal/store"
	"github.com/bohumix/bohux/internal/store/storetest"
)

// syncBuffer is a goroutine-safe log sink: handlers log from server goroutines.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// Rule 3: secret values never appear in logs or error strings, on success or failure.
func TestSecretsNeverLogged(t *testing.T) {
	ctx := context.Background()
	var logs syncBuffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	dsn := storetest.NewAppDSN(t)
	if err := store.MigrateDSN(ctx, dsn); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	_, root, err := Bootstrap(ctx, st, "acme", "")
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	(&Server{Store: st, Authn: &auth.Authenticator{Store: st}}).Mount(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	identity := bohuxv1connect.NewIdentityServiceClient(srv.Client(), srv.URL)
	secretsSvc := bohuxv1connect.NewSecretServiceClient(srv.Client(), srv.URL)

	const value = "s3cr3t-value-must-not-leak"
	secretsSeen := []string{root, value}
	var errs []string

	created, err := identity.CreateApiToken(ctx, req(root, &bohuxv1.CreateApiTokenRequest{
		Name: "ci", Role: bohuxv1.Role_ROLE_VIEWER, IdempotencyKey: "k1"}))
	if err != nil {
		t.Fatal(err)
	}
	secretsSeen = append(secretsSeen, created.Msg.GetSecret())

	// SetSecret is not implemented yet, so it fails after auth; its value must still
	// not reach the logs. Also with a bad token, and with a valid one.
	for _, token := range []string{root, "bxa_garbage", created.Msg.GetSecret()} {
		_, err := secretsSvc.SetSecret(ctx, req(token, &bohuxv1.SetSecretRequest{
			EnvironmentId: "e", Key: "DB_URL", Value: value, IdempotencyKey: "k2"}))
		if err == nil {
			t.Fatal("SetSecret unexpectedly succeeded")
		}
		errs = append(errs, err.Error())
	}
	// A failing call that carries a secret in a request field.
	_, err = identity.CreateApiToken(ctx, req(created.Msg.GetSecret(), &bohuxv1.CreateApiTokenRequest{
		Name: value, Role: bohuxv1.Role_ROLE_OWNER, IdempotencyKey: "k3"}))
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("escalation: got %v", err)
	}
	errs = append(errs, err.Error())

	out := logs.String()
	if !strings.Contains(out, bohuxv1connect.SecretServiceSetSecretProcedure) {
		t.Fatalf("access log is missing SetSecret calls; the test would prove nothing:\n%s", out)
	}
	for _, s := range secretsSeen {
		if strings.Contains(out, s) {
			t.Errorf("logs contain a secret value")
		}
		for _, e := range errs {
			if strings.Contains(e, s) {
				t.Errorf("error string contains a secret value: %s", e)
			}
		}
	}
	// The token secrets' random part alone must not appear either.
	for _, s := range secretsSeen[:1] {
		part := s[strings.LastIndex(s, "_")+1:]
		if len(part) > 10 && strings.Contains(out, part) {
			t.Error("logs contain a token's secret part")
		}
	}
}
