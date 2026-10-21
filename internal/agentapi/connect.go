package agentapi

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"google.golang.org/protobuf/types/known/timestamppb"

	agentv1 "github.com/bohumix/bohux/gen/bohux/agent/v1"
	"github.com/bohumix/bohux/gen/bohux/agent/v1/agentv1connect"
	bohuxv1 "github.com/bohumix/bohux/gen/bohux/v1"
	"github.com/bohumix/bohux/internal/pki"
	"github.com/bohumix/bohux/internal/store"
	"github.com/bohumix/bohux/internal/version"
)

const (
	defaultPingInterval   = 30 * time.Second
	defaultSilenceTimeout = 90 * time.Second
)

type peerCertKey struct{}

// WithPeerCert wraps the agent handler so Connect can see the TLS client
// certificate, which the TLS stack has already verified against the node CA.
func WithPeerCert(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS != nil && len(r.TLS.VerifiedChains) > 0 && len(r.TLS.VerifiedChains[0]) > 0 {
			r = r.WithContext(context.WithValue(r.Context(), peerCertKey{}, r.TLS.VerifiedChains[0][0]))
		}
		next.ServeHTTP(w, r)
	})
}

// Connect is the agent's long-lived stream (ADR-0003). The agent's identity is its
// client certificate; its first message must be Hello. An unsupported agent version
// gets VersionRejected and the stream closes; the agent keeps reconciling its cache
// (rule 1). While the stream is open the node is online.
func (s *Server) Connect(ctx context.Context, stream *connect.BidiStream[agentv1.ConnectRequest, agentv1.ConnectResponse]) error {
	cert, _ := ctx.Value(peerCertKey{}).(*x509.Certificate)
	if cert == nil {
		return errUnauthenticated
	}
	tenant, node, err := pki.NodeIdentity(cert)
	if err != nil {
		return errUnauthenticated
	}
	first, err := stream.Receive()
	if err != nil {
		return fmt.Errorf("receive hello: %w", err)
	}
	hello := first.GetHello()
	if hello == nil || hello.GetNodeId() != node.String() {
		return invalid("the first message must be Hello for the certificate's node")
	}
	actor := store.Actor{ID: node, Kind: store.ActorNode}

	skew := version.CheckAgent(s.version(), hello.GetAgentVersion(), hello.GetProtocolVersion())
	state, detail := "online", (*bohuxv1.ErrorDetail)(nil)
	if !skew.OK {
		state, detail = "rejected", &bohuxv1.ErrorDetail{Reason: "VERSION_REJECTED"}
	}
	if err := s.setNodeState(ctx, tenant, node, actor, hello, state, detail); err != nil {
		var ce *connect.Error
		switch {
		case errors.Is(err, errRevoked) || errors.Is(err, store.ErrNotFound):
			return connect.NewError(connect.CodePermissionDenied, errors.New("node is revoked or unknown"))
		case errors.As(err, &ce):
			return ce
		}
		return internal(ctx, err)
	}
	if !skew.OK {
		slog.WarnContext(ctx, "agent version rejected", "node", node.String(),
			"agent_version", hello.GetAgentVersion(), "reason", skew.Reason)
		return stream.Send(&agentv1.ConnectResponse{Msg: &agentv1.ConnectResponse_VersionRejected{
			VersionRejected: &agentv1.VersionRejected{
				MinAgentVersion: skew.MinAgentVersion, MinProtocolVersion: skew.MinProtocol, Message: skew.Reason,
			}}})
	}
	slog.InfoContext(ctx, "agent connected", "node", node.String(), "agent_version", hello.GetAgentVersion())

	// The newest stream for a node owns its online state. When this one ends, it marks
	// the node offline only if no newer stream has replaced it, so a quick reconnect is
	// not overwritten by the old stream's cleanup. ctx is done by then; use a fresh one.
	sess := &session{wake: make(chan struct{}, 1)}
	s.sessions.Store(node, sess)
	defer func() {
		if !s.sessions.CompareAndDelete(node, sess) {
			return
		}
		offCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		if err := s.setNodeState(offCtx, tenant, node, actor, nil, "offline", nil); err != nil &&
			!errors.Is(err, errRevoked) {
			slog.ErrorContext(offCtx, "mark node offline", "node", node.String(), "err", err)
		}
		slog.InfoContext(offCtx, "agent disconnected", "node", node.String())
	}()

	recvErr := make(chan error, 1)
	var lastHeard atomic.Int64
	lastHeard.Store(time.Now().UnixNano())
	go func() {
		for {
			msg, err := stream.Receive()
			if err != nil {
				recvErr <- err
				return
			}
			lastHeard.Store(time.Now().UnixNano())
			switch m := msg.GetMsg().(type) {
			case *agentv1.ConnectRequest_Ack:
				if err := s.recordAck(ctx, tenant, node, m.Ack.GetGeneration()); err != nil {
					slog.WarnContext(ctx, "record ack", "node", node.String(), "err", err)
				}
			case *agentv1.ConnectRequest_StatusReport:
				if err := s.recordReport(ctx, tenant, node, m.StatusReport); err != nil {
					slog.WarnContext(ctx, "record status report", "node", node.String(), "err", err)
				}
			default:
				// LogChunk and ExecOutput are handled with WorkloadService (S8).
				slog.DebugContext(ctx, "agent message", "node", node.String(), "type", fmt.Sprintf("%T", m))
			}
		}
	}()

	// Send the latest assignment now if the agent is behind, then whenever a new one
	// is written. Assignments are full state, so only the newest ever matters.
	sent := hello.GetAppliedGeneration()
	push := func() error {
		a, gen, err := s.signedLatest(ctx, tenant, node, sent)
		if err != nil {
			// The agent keeps its cached assignment; nothing it runs is affected.
			slog.ErrorContext(ctx, "cannot send assignment", "node", node.String(), "err", err)
			return nil
		}
		if a == nil {
			return nil
		}
		if err := stream.Send(&agentv1.ConnectResponse{Msg: &agentv1.ConnectResponse_Assignment{Assignment: a}}); err != nil {
			return err
		}
		sent = gen
		return nil
	}
	if err := push(); err != nil {
		return nil
	}

	ticker := time.NewTicker(s.pingInterval())
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-recvErr:
			if isClosed(err) {
				return nil
			}
			return fmt.Errorf("receive: %w", err)
		case <-sess.wake:
			if err := push(); err != nil {
				return nil
			}
		case <-ticker.C:
			if silent := time.Since(time.Unix(0, lastHeard.Load())); silent > s.silenceTimeout() {
				slog.WarnContext(ctx, "agent silent; closing its stream", "node", node.String(), "silent", silent)
				return nil
			}
			if err := stream.Send(&agentv1.ConnectResponse{Msg: &agentv1.ConnectResponse_Ping{
				Ping: &agentv1.Ping{SentAt: timestamppb.New(s.now())}}}); err != nil {
				return nil
			}
			if err := s.touch(ctx, tenant, node); err != nil {
				slog.WarnContext(ctx, "record node last seen", "node", node.String(), "err", err)
			}
		}
	}
}

func isClosed(err error) bool {
	return err == nil || errors.Is(err, context.Canceled) || errors.Is(err, io.EOF) ||
		connect.CodeOf(err) == connect.CodeCanceled
}

func (s *Server) version() string {
	if s.Version != "" {
		return s.Version
	}
	return version.Version
}

func (s *Server) silenceTimeout() time.Duration {
	if s.SilenceTimeout > 0 {
		return s.SilenceTimeout
	}
	return defaultSilenceTimeout
}

// recordReport stores the node's latest StatusReport (an observation).
func (s *Server) recordReport(ctx context.Context, tenant, node uuid.UUID, r *agentv1.StatusReport) error {
	b, err := providerJSON.Marshal(r)
	if err != nil {
		return fmt.Errorf("encode status report: %w", err)
	}
	return s.Store.InTenant(ctx, tenant, func(tx *store.Tx) error {
		return tx.Exec(ctx, `UPDATE nodes_heads SET report_generation = $1, report = $2,
			report_received_at = $3, last_seen_at = $3 WHERE id = $4`,
			int64(r.GetGeneration()), b, s.now(), node)
	})
}

func (s *Server) pingInterval() time.Duration {
	if s.PingInterval > 0 {
		return s.PingInterval
	}
	return defaultPingInterval
}

var errRevoked = errors.New("node is revoked")

// setNodeState writes a node revision with this state, taking agent details from
// hello, or from the current revision if hello is nil. A revoked node stays revoked.
// Unchanged state writes nothing but last_seen_at.
func (s *Server) setNodeState(ctx context.Context, tenant, node uuid.UUID, actor store.Actor, hello *agentv1.Hello, state string, detail *bohuxv1.ErrorDetail) error {
	return s.Store.InTenant(ctx, tenant, func(tx *store.Tx) error {
		var (
			rev                  uuid.UUID
			curVersion, curState string
			curProtocol          int32
			curProviders         []byte
		)
		err := tx.QueryRow(ctx, `SELECT h.revision_id, r.agent_version, r.protocol_version, r.providers, r.state
			FROM nodes_heads h JOIN nodes_revisions r ON r.id = h.revision_id
			WHERE h.id = $1 AND NOT h.deleted FOR UPDATE OF h`, node).
			Scan(&rev, &curVersion, &curProtocol, &curProviders, &curState)
		if errors.Is(err, pgx.ErrNoRows) {
			return store.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("read node: %w", err)
		}
		if curState == "revoked" {
			return errRevoked
		}
		agentVersion, protocol, providers := curVersion, curProtocol, curProviders
		if hello != nil {
			agentVersion, protocol = hello.GetAgentVersion(), int32(hello.GetProtocolVersion())
			if providers, err = providersJSON(&agentv1.RegisterRequest{Providers: hello.GetProviders()}); err != nil {
				return invalid("providers are not valid")
			}
		}
		var detailJSON any
		if detail != nil {
			b, err := providerJSON.Marshal(detail)
			if err != nil {
				return fmt.Errorf("encode state detail: %w", err)
			}
			detailJSON = b
		}
		if err := tx.Exec(ctx, `UPDATE nodes_heads SET last_seen_at = $1 WHERE id = $2`, s.now(), node); err != nil {
			return fmt.Errorf("record node last seen: %w", err)
		}
		if hello == nil && state == curState {
			return nil
		}
		_, err = tx.Update(ctx, "nodes", node, rev, []store.Col{
			{Name: "agent_version", Value: agentVersion}, {Name: "protocol_version", Value: protocol},
			{Name: "providers", Value: providers}, {Name: "state", Value: state},
			{Name: "state_detail", Value: detailJSON},
		}, store.Audit{Actor: actor, Action: agentv1connect.AgentServiceConnectProcedure, ResourceType: nodeType})
		return err
	})
}

// touch records that the node is still connected.
func (s *Server) touch(ctx context.Context, tenant, node uuid.UUID) error {
	return s.Store.InTenant(ctx, tenant, func(tx *store.Tx) error {
		return tx.Exec(ctx, `UPDATE nodes_heads SET last_seen_at = $1 WHERE id = $2`, s.now(), node)
	})
}
