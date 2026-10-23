// Package agentapi is the control plane's side of the agent protocol
// (bohux.agent.v1, ADR-0007): registration and, from S4c, the Connect stream. It is
// served on its own TLS listener with the node CA (ADR-0024).
package agentapi

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"google.golang.org/protobuf/encoding/protojson"

	agentv1 "github.com/bohumix/bohux/gen/bohux/agent/v1"
	"github.com/bohumix/bohux/gen/bohux/agent/v1/agentv1connect"
	"github.com/bohumix/bohux/internal/auth"
	"github.com/bohumix/bohux/internal/pki"
	"github.com/bohumix/bohux/internal/store"
)

const nodeType = "bohux.v1.Node"

// Server implements bohux.agent.v1.AgentService.
type Server struct {
	agentv1connect.UnimplementedAgentServiceHandler

	Store *store.Store
	PKI   *pki.Authority
	// Now is the clock; nil means time.Now.
	Now func() time.Time
	// Version is the control plane version agents are checked against; empty means
	// version.Version (ADR-0020 item 8).
	Version string
	// PingInterval is how often an open stream is pinged and last_seen_at written;
	// zero means 30 seconds.
	PingInterval time.Duration
	// SilenceTimeout closes a stream on which the agent has sent nothing for this
	// long, so half-open links end and the node goes offline; zero means 90 seconds
	// (three agent report intervals).
	SilenceTimeout time.Duration

	// Secrets resolves secret handles when an assignment is sent. Nil means
	// assignments with secrets cannot be sent yet (S7 adds OpenBao).
	Secrets SecretResolver

	// sessions maps a node ID to its newest open stream (*session). One control-plane
	// process owns all streams in v0.1 (ADR-0024 revisits this with HA).
	sessions sync.Map
}

// session is one open Connect stream.
type session struct {
	// wake is signaled when a new assignment is written for the node.
	wake chan struct{}
	// out carries other messages for the stream's sender (build requests).
	out chan *agentv1.ConnectResponse
	// done is closed when the stream ends.
	done chan struct{}

	mu sync.Mutex
	// builds holds who waits for each Release's build result.
	builds map[string][]chan *agentv1.BuildResult
	// logs and execs route log chunks and exec output to their API callers.
	logs  map[string]chan *agentv1.LogChunk
	execs map[string]chan *agentv1.ExecOutput
}

func newSession() *session {
	return &session{wake: make(chan struct{}, 1), out: make(chan *agentv1.ConnectResponse), done: make(chan struct{}),
		logs: map[string]chan *agentv1.LogChunk{}, execs: map[string]chan *agentv1.ExecOutput{}}
}

// wake nudges the node's open stream, if any, to send its latest assignment.
func (s *Server) wake(node uuid.UUID) {
	if v, ok := s.sessions.Load(node); ok {
		select {
		case v.(*session).wake <- struct{}{}:
		default: // already pending; assignments are full state, so one send covers both
		}
	}
}

func (s *Server) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// TLSConfig is the agent listener's TLS: the node CA's server certificate, and client
// certificates from the node CA verified when presented. Register needs none; Connect
// requires one (S4c).
func (s *Server) TLSConfig() (*tls.Config, error) {
	cert, err := s.PKI.ServerCertificate()
	if err != nil {
		return nil, fmt.Errorf("agent listener certificate: %w", err)
	}
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		ClientAuth:   tls.VerifyClientCertIfGiven,
		ClientCAs:    s.PKI.CAPool(),
		MinVersion:   tls.VersionTLS13,
	}, nil
}

// Handler returns the path and handler to mount, with the client certificate made
// available to Connect.
func (s *Server) Handler(opts ...connect.HandlerOption) (string, http.Handler) {
	path, h := agentv1connect.NewAgentServiceHandler(s, opts...)
	return path, WithPeerCert(h)
}

var (
	errUnauthenticated = connect.NewError(connect.CodeUnauthenticated, auth.ErrUnauthenticated)
	providerJSON       = protojson.MarshalOptions{UseProtoNames: true}
)

// Register trades a single-use bootstrap token and a CSR for a node identity
// (ADR-0003, ADR-0024). Every token problem is the same Unauthenticated error.
func (s *Server) Register(ctx context.Context, req *connect.Request[agentv1.RegisterRequest]) (*connect.Response[agentv1.RegisterResponse], error) {
	msg := req.Msg
	tok, err := auth.Parse(msg.GetBootstrapToken())
	if err != nil || tok.Kind != auth.KindBootstrap {
		return nil, errUnauthenticated
	}
	switch {
	case len(msg.GetCsr()) == 0:
		return nil, invalid("csr is required")
	case len(msg.GetSealingPublicKey()) != 32:
		return nil, invalid("sealing_public_key must be a 32-byte X25519 public key")
	case msg.GetAgentVersion() == "":
		return nil, invalid("agent_version is required")
	case msg.GetProtocolVersion() == 0:
		return nil, invalid("protocol_version is required")
	}
	providers, err := providersJSON(msg)
	if err != nil {
		return nil, invalid("providers are not valid")
	}

	nodeID, err := store.NewID()
	if err != nil {
		return nil, internal(ctx, err)
	}
	var certPEM []byte
	err = s.Store.InTenant(ctx, tok.Tenant, func(tx *store.Tx) error {
		var (
			hash      []byte
			slug      string
			expiresAt time.Time
			usedAt    *time.Time
		)
		err := tx.QueryRow(ctx, `SELECT token_hash, node_slug, expires_at, used_at
			FROM node_bootstrap_tokens WHERE id = $1 FOR UPDATE`, tok.ID).
			Scan(&hash, &slug, &expiresAt, &usedAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return auth.ErrUnauthenticated
		}
		if err != nil {
			return fmt.Errorf("look up bootstrap token: %w", err)
		}
		if !tok.Matches(hash) || usedAt != nil || !s.now().Before(expiresAt) {
			return auth.ErrUnauthenticated
		}
		if certPEM, err = s.PKI.SignNodeCSR(msg.GetCsr(), tok.Tenant, nodeID); err != nil {
			return err
		}
		actor := store.Actor{ID: nodeID, Kind: store.ActorNode}
		_, err = tx.CreateWithID(ctx, "nodes", nodeID,
			[]store.Col{{Name: "slug", Value: slug}, {Name: "sealing_public_key", Value: msg.GetSealingPublicKey()}},
			[]store.Col{
				{Name: "agent_version", Value: msg.GetAgentVersion()},
				{Name: "protocol_version", Value: int32(msg.GetProtocolVersion())},
				{Name: "providers", Value: providers}, {Name: "state", Value: "online"},
			},
			store.Audit{Actor: actor, Action: agentv1connect.AgentServiceRegisterProcedure, ResourceType: nodeType})
		if err != nil {
			return err
		}
		if err := tx.Exec(ctx, `UPDATE node_bootstrap_tokens SET used_at = $1, node_id = $2 WHERE id = $3`,
			s.now(), nodeID, tok.ID); err != nil {
			return fmt.Errorf("mark bootstrap token used: %w", err)
		}
		return tx.AuditOnly(ctx, store.Audit{Actor: actor, Action: "bohux.agent.v1/BootstrapTokenUsed",
			ResourceType: "bohux.v1.BootstrapToken"}, tok.ID)
	})
	switch {
	case errors.Is(err, auth.ErrUnauthenticated):
		return nil, errUnauthenticated
	case errors.Is(err, pki.ErrBadCSR):
		return nil, invalid("csr is not a valid P-256 certificate request")
	case errors.Is(err, store.ErrAlreadyExists):
		return nil, connect.NewError(connect.CodeAlreadyExists,
			errors.New("a node with this token's slug is already registered"))
	case err != nil:
		return nil, internal(ctx, err)
	}
	slog.InfoContext(ctx, "node registered", "node", nodeID.String(), "tenant", tok.Tenant.String())
	return connect.NewResponse(&agentv1.RegisterResponse{
		NodeId:                     nodeID.String(),
		ClientCertificate:          certPEM,
		CaCertificate:              s.PKI.CACertPEM(),
		AssignmentSigningPublicKey: s.PKI.SigningPublicKey(),
	}), nil
}

func providersJSON(msg *agentv1.RegisterRequest) ([]byte, error) {
	items := make([]json.RawMessage, 0, len(msg.GetProviders()))
	for _, p := range msg.GetProviders() {
		if p.GetName() == "" {
			return nil, errors.New("provider without a name")
		}
		b, err := providerJSON.Marshal(p)
		if err != nil {
			return nil, fmt.Errorf("encode provider: %w", err)
		}
		items = append(items, b)
	}
	b, err := json.Marshal(items)
	if err != nil {
		return nil, fmt.Errorf("encode providers: %w", err)
	}
	return b, nil
}

func invalid(msg string) error {
	return connect.NewError(connect.CodeInvalidArgument, errors.New(msg))
}

// internal logs the cause and returns a detail-free error (rule 3).
func internal(ctx context.Context, err error) error {
	slog.ErrorContext(ctx, "agent request failed", "err", err)
	return connect.NewError(connect.CodeInternal, errors.New("internal error"))
}
