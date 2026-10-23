package agentapi

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"golang.org/x/crypto/nacl/box"
	"google.golang.org/protobuf/proto"

	agentv1 "github.com/bohumix/bohux/gen/bohux/agent/v1"
	bohuxv1 "github.com/bohumix/bohux/gen/bohux/v1"
	"github.com/bohumix/bohux/internal/store"
)

// ErrNodeOffline means the node has no open stream, or it closed while waiting.
var ErrNodeOffline = errors.New("node is not connected")

// Build asks the node to build a Release over its open stream and waits for the
// result (ADR-0028). It fails with ErrNodeOffline if the node is not connected or
// disconnects first; the caller (a Temporal activity) retries. A deploy key named by
// handle is resolved and sealed to the node here, and sent only sealed (ADR-0041).
func (s *Server) Build(ctx context.Context, tenant, node uuid.UUID, req *agentv1.BuildRequest) (*agentv1.BuildResult, error) {
	v, ok := s.sessions.Load(node)
	if !ok {
		return nil, ErrNodeOffline
	}
	req = proto.Clone(req).(*agentv1.BuildRequest)
	req.SealedSecrets = nil
	if ref := req.GetSshKey(); ref != nil {
		if err := s.sealBuild(ctx, tenant, node, req, ref); err != nil {
			return nil, err
		}
	}
	sess := v.(*session)
	ch := sess.await(req.GetReleaseId())
	defer sess.forget(req.GetReleaseId(), ch)
	select {
	case sess.out <- &agentv1.ConnectResponse{Msg: &agentv1.ConnectResponse_BuildRequest{BuildRequest: req}}:
	case <-sess.done:
		return nil, ErrNodeOffline
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	select {
	case r := <-ch:
		return r, nil
	case <-sess.done:
		return nil, ErrNodeOffline
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (s *Server) sealBuild(ctx context.Context, tenant, node uuid.UUID, req *agentv1.BuildRequest, ref *bohuxv1.SecretRef) error {
	if s.Secrets == nil {
		return errNoResolver
	}
	var key []byte
	if err := s.Store.InTenant(ctx, tenant, func(tx *store.Tx) error {
		return tx.QueryRow(ctx, `SELECT sealing_public_key FROM nodes_heads WHERE id = $1 AND NOT deleted`,
			node).Scan(&key)
	}); err != nil {
		return fmt.Errorf("read node %s sealing key: %w", node, err)
	}
	if len(key) != 32 {
		return fmt.Errorf("node %s has no sealing key", node)
	}
	byHandle, err := s.Secrets.Resolve(ctx, tenant, []string{ref.GetHandle()})
	if err != nil {
		return fmt.Errorf("resolve deploy key for release %s: %w", req.GetReleaseId(), err)
	}
	v, ok := byHandle[ref.GetHandle()]
	if !ok {
		return fmt.Errorf("deploy key %s for release %s did not resolve", ref.GetKey(), req.GetReleaseId())
	}
	plain, err := proto.Marshal(&agentv1.SecretValues{SshKey: v})
	if err != nil {
		return fmt.Errorf("encode deploy key: %w", err)
	}
	var recipient [32]byte
	copy(recipient[:], key)
	if req.SealedSecrets, err = box.SealAnonymous(nil, plain, &recipient, rand.Reader); err != nil {
		return fmt.Errorf("seal deploy key: %w", err)
	}
	return nil
}

// await registers a waiter for a Release's build result.
func (s *session) await(release string) chan *agentv1.BuildResult {
	ch := make(chan *agentv1.BuildResult, 1)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.builds == nil {
		s.builds = map[string][]chan *agentv1.BuildResult{}
	}
	s.builds[release] = append(s.builds[release], ch)
	return ch
}

func (s *session) forget(release string, ch chan *agentv1.BuildResult) {
	s.mu.Lock()
	defer s.mu.Unlock()
	list := s.builds[release]
	for i, c := range list {
		if c == ch {
			s.builds[release] = append(list[:i], list[i+1:]...)
			break
		}
	}
	if len(s.builds[release]) == 0 {
		delete(s.builds, release)
	}
}

// deliver hands a result to everyone waiting on its Release; nobody waiting means a
// late answer to a request that gave up, which is dropped.
func (s *session) deliver(r *agentv1.BuildResult) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ch := range s.builds[r.GetReleaseId()] {
		select {
		case ch <- r:
		default:
		}
	}
}
