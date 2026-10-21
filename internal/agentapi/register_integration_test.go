//go:build integration

package agentapi

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"net"
	"net/http"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	agentv1 "github.com/bohumix/bohux/gen/bohux/agent/v1"
	"github.com/bohumix/bohux/gen/bohux/agent/v1/agentv1connect"
	bohuxv1 "github.com/bohumix/bohux/gen/bohux/v1"
	"github.com/bohumix/bohux/internal/api"
	"github.com/bohumix/bohux/internal/auth"
	"github.com/bohumix/bohux/internal/pki"
	"github.com/bohumix/bohux/internal/store"
	"github.com/bohumix/bohux/internal/store/storetest"
)

type harness struct {
	t      *testing.T
	st     *store.Store
	pki    *pki.Authority
	tenant uuid.UUID
	client agentv1connect.AgentServiceClient
}

func newHarness(t *testing.T) *harness {
	t.Helper()
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
	tenant, _, err := api.Bootstrap(ctx, st, "acme", "")
	if err != nil {
		t.Fatal(err)
	}
	authority, err := pki.LoadOrCreate(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Store: st, PKI: authority}
	tlsCfg, err := s.TLSConfig()
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle(s.Handler())
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: mux, TLSConfig: tlsCfg, ReadHeaderTimeout: time.Second}
	go func() { _ = srv.ServeTLS(ln, "", "") }()
	t.Cleanup(func() { _ = srv.Close() })

	// An agent that pinned the CA: trusts only it, and checks the fixed server name.
	httpClient := &http.Client{Transport: &http.Transport{
		TLSClientConfig:   &tls.Config{RootCAs: authority.CAPool(), ServerName: pki.ServerName, MinVersion: tls.VersionTLS13},
		ForceAttemptHTTP2: true,
	}}
	return &harness{t: t, st: st, pki: authority, tenant: tenant,
		client: agentv1connect.NewAgentServiceClient(httpClient, "https://"+ln.Addr().String())}
}

// bootstrapToken stores a bootstrap token as NodeService does and returns it.
func (h *harness) bootstrapToken(slug string, expires time.Time) string {
	h.t.Helper()
	id := uuid.Must(uuid.NewV7())
	raw, hash, err := auth.Issue(auth.KindBootstrap, h.tenant, id)
	if err != nil {
		h.t.Fatal(err)
	}
	err = h.st.InTenant(context.Background(), h.tenant, func(tx *store.Tx) error {
		return tx.Exec(context.Background(), `INSERT INTO node_bootstrap_tokens
			(id, tenant_id, node_slug, token_hash, expires_at, created_by)
			VALUES ($1, $2, $3, $4, $5, $6)`, id, h.tenant, slug, hash, expires, store.SystemActorID)
	})
	if err != nil {
		h.t.Fatal(err)
	}
	return raw
}

func newCSR(t *testing.T) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der})
}

func registerReq(token string, csr []byte) *agentv1.RegisterRequest {
	return &agentv1.RegisterRequest{
		BootstrapToken: token, Csr: csr, SealingPublicKey: bytes.Repeat([]byte{7}, 32),
		AgentVersion: "0.1.0", ProtocolVersion: 1,
		Providers: []*bohuxv1.ProviderDescriptor{{Name: "docker", Version: "0.1.0",
			Capabilities: []string{"plan", "apply", "status", "logs", "exec", "scale", "destroy"}}},
	}
}

func wantCode(t *testing.T, err error, want connect.Code) {
	t.Helper()
	if err == nil || connect.CodeOf(err) != want {
		t.Fatalf("got %v, want %v", err, want)
	}
}

func TestRegister(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	hour := time.Now().Add(time.Hour)

	token := h.bootstrapToken("vps-1", hour)

	t.Run("a bad CSR fails without spending the token", func(t *testing.T) {
		csr := newCSR(t)
		blk, _ := pem.Decode(csr)
		blk.Bytes[len(blk.Bytes)-3] ^= 0xff
		_, err := h.client.Register(ctx, connect.NewRequest(registerReq(token, pem.EncodeToMemory(blk))))
		wantCode(t, err, connect.CodeInvalidArgument)
	})

	resp, err := h.client.Register(ctx, connect.NewRequest(registerReq(token, newCSR(t))))
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	msg := resp.Msg

	t.Run("the client certificate carries the node identity", func(t *testing.T) {
		roots := x509.NewCertPool()
		if !roots.AppendCertsFromPEM(msg.GetCaCertificate()) {
			t.Fatal("CA certificate does not parse")
		}
		blk, _ := pem.Decode(msg.GetClientCertificate())
		cert, err := x509.ParseCertificate(blk.Bytes)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := cert.Verify(x509.VerifyOptions{Roots: roots,
			KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
			t.Errorf("client cert does not verify: %v", err)
		}
		tenant, node, err := pki.NodeIdentity(cert)
		if err != nil || tenant != h.tenant || node.String() != msg.GetNodeId() {
			t.Errorf("identity %s/%s (%v), want %s/%s", tenant, node, err, h.tenant, msg.GetNodeId())
		}
		if !bytes.Equal(msg.GetAssignmentSigningPublicKey(), h.pki.SigningPublicKey()) {
			t.Error("wrong assignment signing key")
		}
	})

	t.Run("the node is stored online with the token's slug", func(t *testing.T) {
		var slug, state string
		var used *time.Time
		err := h.st.InTenant(ctx, h.tenant, func(tx *store.Tx) error {
			if err := tx.QueryRow(ctx, `SELECT h.slug, r.state FROM nodes_heads h
				JOIN nodes_revisions r ON r.id = h.revision_id WHERE h.id = $1`,
				msg.GetNodeId()).Scan(&slug, &state); err != nil {
				return err
			}
			return tx.QueryRow(ctx, `SELECT used_at FROM node_bootstrap_tokens WHERE node_id = $1`,
				msg.GetNodeId()).Scan(&used)
		})
		if err != nil {
			t.Fatal(err)
		}
		if slug != "vps-1" || state != "online" || used == nil {
			t.Errorf("slug %q state %q used %v", slug, state, used)
		}
	})

	apiTok, _, err := auth.Issue(auth.KindAPI, h.tenant, uuid.Must(uuid.NewV7()))
	if err != nil {
		t.Fatal(err)
	}
	sameSlug := h.bootstrapToken("vps-1", hour)
	bad := []struct {
		name string
		req  *agentv1.RegisterRequest
		want connect.Code
	}{
		{"reused token", registerReq(token, newCSR(t)), connect.CodeUnauthenticated},
		{"expired token", registerReq(h.bootstrapToken("vps-2", time.Now().Add(-time.Minute)), newCSR(t)), connect.CodeUnauthenticated},
		{"api token", registerReq(apiTok, newCSR(t)), connect.CodeUnauthenticated},
		{"garbage token", registerReq("bxb_nope", newCSR(t)), connect.CodeUnauthenticated},
		{"slug already registered", registerReq(sameSlug, newCSR(t)), connect.CodeAlreadyExists},
		{"short sealing key", func() *agentv1.RegisterRequest {
			r := registerReq(h.bootstrapToken("vps-3", hour), newCSR(t))
			r.SealingPublicKey = []byte{1}
			return r
		}(), connect.CodeInvalidArgument},
	}
	for _, tt := range bad {
		t.Run(tt.name, func(t *testing.T) {
			_, err := h.client.Register(ctx, connect.NewRequest(tt.req))
			wantCode(t, err, tt.want)
		})
	}
}
