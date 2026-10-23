//go:build integration

package agentapi

import (
	"context"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	agentv1 "github.com/bohumix/bohux/gen/bohux/agent/v1"
	"github.com/bohumix/bohux/gen/bohux/agent/v1/agentv1connect"
	"github.com/bohumix/bohux/internal/pki"
	"github.com/bohumix/bohux/internal/store"
	"github.com/bohumix/bohux/internal/version"
)

// registered is an agent identity: its node ID, sealing key, and an mTLS client.
type registered struct {
	node    string
	sealing *ecdh.PrivateKey
	client  agentv1connect.AgentServiceClient
}

func (h *harness) register(slug string) registered {
	h.t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		h.t.Fatal(err)
	}
	sealing, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		h.t.Fatal(err)
	}
	r := registerReq(h.bootstrapToken(slug, time.Now().Add(time.Hour)), csrFor(h.t, key))
	r.SealingPublicKey = sealing.PublicKey().Bytes()
	resp, err := h.client.Register(context.Background(), connect.NewRequest(r))
	if err != nil {
		h.t.Fatal(err)
	}
	blk, _ := pem.Decode(resp.Msg.GetClientCertificate())
	return registered{node: resp.Msg.GetNodeId(), sealing: sealing, client: h.clientWith(&tls.Certificate{
		Certificate: [][]byte{blk.Bytes}, PrivateKey: key})}
}

func (h *harness) clientWith(cert *tls.Certificate) agentv1connect.AgentServiceClient {
	cfg := &tls.Config{RootCAs: h.pki.CAPool(), ServerName: pki.ServerName, MinVersion: tls.VersionTLS13}
	if cert != nil {
		cfg.Certificates = []tls.Certificate{*cert}
	}
	return agentv1connect.NewAgentServiceClient(&http.Client{Transport: &http.Transport{
		TLSClientConfig: cfg, ForceAttemptHTTP2: true}}, "https://"+h.addr)
}

func hello(node, agentVersion string) *agentv1.ConnectRequest {
	return &agentv1.ConnectRequest{Msg: &agentv1.ConnectRequest_Hello{Hello: &agentv1.Hello{
		NodeId: node, AgentVersion: agentVersion, ProtocolVersion: version.Protocol}}}
}

func (h *harness) nodeState(node string) string {
	h.t.Helper()
	var state string
	err := h.st.InTenant(context.Background(), h.tenant, func(tx *store.Tx) error {
		return tx.QueryRow(context.Background(), `SELECT r.state FROM nodes_heads h
			JOIN nodes_revisions r ON r.id = h.revision_id WHERE h.id = $1`, node).Scan(&state)
	})
	if err != nil {
		h.t.Fatal(err)
	}
	return state
}

func (h *harness) waitState(node, want string) {
	h.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for h.nodeState(node) != want {
		if time.Now().After(deadline) {
			h.t.Fatalf("node state %q, want %q", h.nodeState(node), want)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// open sends Hello and returns the stream and its first response (or error).
func open(ctx context.Context, c agentv1connect.AgentServiceClient, first *agentv1.ConnectRequest) (*connect.BidiStreamForClient[agentv1.ConnectRequest, agentv1.ConnectResponse], *agentv1.ConnectResponse, error) {
	stream := c.Connect(ctx)
	if err := stream.Send(first); err != nil && !errors.Is(err, io.EOF) {
		return stream, nil, err
	}
	// io.EOF from Send means the server ended the stream; Receive returns why.
	msg, err := stream.Receive()
	return stream, msg, err
}

// closeStream ends a stream as the agent does on shutdown. Canceling the context alone
// does not reset an HTTP/2 stream whose request body is still open.
func closeStream(s *connect.BidiStreamForClient[agentv1.ConnectRequest, agentv1.ConnectResponse], cancel context.CancelFunc) {
	_ = s.CloseRequest()
	cancel()
}

func TestConnect(t *testing.T) {
	h := newHarness(t)

	t.Run("accepted: pinged, online while open, offline after", func(t *testing.T) {
		a := h.register("vps-ok")
		ctx, cancel := context.WithCancel(context.Background())
		stream, msg, err := open(ctx, a.client, hello(a.node, "0.3.0"))
		if err != nil {
			t.Fatal(err)
		}
		if msg.GetPing() == nil {
			t.Fatalf("first message %v, want Ping", msg)
		}
		h.waitState(a.node, "online")
		closeStream(stream, cancel)
		h.waitState(a.node, "offline")
	})

	t.Run("a quick reconnect stays online when the old stream ends", func(t *testing.T) {
		a := h.register("vps-flap")
		ctxOld, cancelOld := context.WithCancel(context.Background())
		oldStream, _, err := open(ctxOld, a.client, hello(a.node, "0.3.0"))
		if err != nil {
			t.Fatal(err)
		}
		ctxNew, cancelNew := context.WithCancel(context.Background())
		defer cancelNew()
		newStream, _, err := open(ctxNew, a.client, hello(a.node, "0.3.0"))
		if err != nil {
			t.Fatal(err)
		}
		closeStream(oldStream, cancelOld)
		time.Sleep(300 * time.Millisecond)
		if got := h.nodeState(a.node); got != "online" {
			t.Fatalf("state after the old stream closed = %q, want online", got)
		}
		closeStream(newStream, cancelNew)
		h.waitState(a.node, "offline")
	})

	t.Run("previous minor accepted, older rejected with a reason", func(t *testing.T) {
		a := h.register("vps-old")
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		_, msg, err := open(ctx, a.client, hello(a.node, "0.2.5"))
		if err != nil || msg.GetPing() == nil {
			t.Fatalf("0.2.5 against 0.3.0: %v %v", msg, err)
		}
		cancel()

		ctx2, cancel2 := context.WithCancel(context.Background())
		defer cancel2()
		_, msg, err = open(ctx2, a.client, hello(a.node, "0.1.0"))
		if err != nil {
			t.Fatal(err)
		}
		rej := msg.GetVersionRejected()
		if rej == nil || rej.GetMinAgentVersion() != "0.2.0" || rej.GetMessage() == "" {
			t.Fatalf("got %v, want VersionRejected with min 0.2.0", msg)
		}
		h.waitState(a.node, "rejected")
	})

	t.Run("revoked node is refused", func(t *testing.T) {
		a := h.register("vps-revoked")
		err := h.st.InTenant(context.Background(), h.tenant, func(tx *store.Tx) error {
			var rev uuid.UUID
			if err := tx.QueryRow(context.Background(), `SELECT revision_id FROM nodes_heads WHERE id = $1`,
				a.node).Scan(&rev); err != nil {
				return err
			}
			_, err := tx.Update(context.Background(), "nodes", uuid.MustParse(a.node), rev, []store.Col{
				{Name: "agent_version", Value: "0.1.0"}, {Name: "protocol_version", Value: 1},
				{Name: "state", Value: "revoked"}, {Name: "revoked_at", Value: time.Now()},
			}, store.Audit{Actor: store.Actor{ID: store.SystemActorID, Kind: store.ActorSystem},
				Action: "test", ResourceType: nodeType})
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		_, _, err = open(context.Background(), a.client, hello(a.node, "0.3.0"))
		wantCode(t, err, connect.CodePermissionDenied)
	})

	t.Run("no client certificate", func(t *testing.T) {
		a := h.register("vps-nocert")
		_, _, err := open(context.Background(), h.clientWith(nil), hello(a.node, "0.3.0"))
		wantCode(t, err, connect.CodeUnauthenticated)
	})

	t.Run("hello must name the certificate's node", func(t *testing.T) {
		a := h.register("vps-liar")
		b := h.register("vps-victim")
		_, _, err := open(context.Background(), a.client, hello(b.node, "0.3.0"))
		wantCode(t, err, connect.CodeInvalidArgument)
	})

	t.Run("a certificate from another CA is refused by TLS", func(t *testing.T) {
		other, err := pki.LoadOrCreate(t.TempDir(), nil)
		if err != nil {
			t.Fatal(err)
		}
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		certPEM, err := other.SignNodeCSR(csrFor(t, key), h.tenant, uuid.Must(uuid.NewV7()))
		if err != nil {
			t.Fatal(err)
		}
		blk, _ := pem.Decode(certPEM)
		if _, err := x509.ParseCertificate(blk.Bytes); err != nil {
			t.Fatal(err)
		}
		c := h.clientWith(&tls.Certificate{Certificate: [][]byte{blk.Bytes}, PrivateKey: key})
		_, _, err = open(context.Background(), c, hello(uuid.Nil.String(), "0.3.0"))
		if err == nil {
			t.Fatal("a foreign CA's certificate was accepted")
		}
	})
}
