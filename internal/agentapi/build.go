package agentapi

import (
	"context"
	"errors"

	"github.com/google/uuid"

	agentv1 "github.com/bohumix/bohux/gen/bohux/agent/v1"
)

// ErrNodeOffline means the node has no open stream, or it closed while waiting.
var ErrNodeOffline = errors.New("node is not connected")

// Build asks the node to build a Release over its open stream and waits for the
// result (ADR-0028). It fails with ErrNodeOffline if the node is not connected or
// disconnects first; the caller (a Temporal activity) retries.
func (s *Server) Build(ctx context.Context, node uuid.UUID, req *agentv1.BuildRequest) (*agentv1.BuildResult, error) {
	v, ok := s.sessions.Load(node)
	if !ok {
		return nil, ErrNodeOffline
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
