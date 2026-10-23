package agentapi

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"

	agentv1 "github.com/bohumix/bohux/gen/bohux/agent/v1"
)

const (
	// logBuffer bounds the log lines waiting for a slow API client. Beyond it lines
	// are dropped rather than stalling the node's stream, which also carries
	// assignments and reports.
	logBuffer = 1024
	// execStall bounds how long one exec chunk may wait for its API client.
	execStall = 2 * time.Second
)

// Logs asks the node for an App's log lines (ADR-0007). Chunks arrive on the returned
// channel, which closes after the last chunk, when ctx ends (the agent is told to
// stop), or when the node disconnects.
func (s *Server) Logs(ctx context.Context, node uuid.UUID, req *agentv1.LogRequest) (<-chan *agentv1.LogChunk, error) {
	sess, err := s.session(node)
	if err != nil {
		return nil, err
	}
	id := uuid.NewString()
	req.RequestId = id
	in := make(chan *agentv1.LogChunk, logBuffer)
	sess.mu.Lock()
	sess.logs[id] = in
	sess.mu.Unlock()
	if err := sess.send(ctx, &agentv1.ConnectResponse{Msg: &agentv1.ConnectResponse_LogRequest{LogRequest: req}}); err != nil {
		sess.dropLogs(id)
		return nil, err
	}
	out := make(chan *agentv1.LogChunk)
	go func() {
		defer close(out)
		defer sess.dropLogs(id)
		for {
			select {
			case c := <-in:
				select {
				case out <- c:
				case <-ctx.Done():
					sess.cancelLogs(id)
					return
				}
				if c.GetEof() {
					return
				}
			case <-ctx.Done():
				sess.cancelLogs(id)
				return
			case <-sess.done:
				return
			}
		}
	}()
	return out, nil
}

// ExecRelay is one exec session on a node.
type ExecRelay struct {
	// Output carries stdout, stderr, and finally the exit; it closes after the exit
	// or when the node disconnects.
	Output <-chan *agentv1.ExecOutput
	sess   *session
	id     string
}

// Send forwards stdin or a resize to the process.
func (r *ExecRelay) Send(ctx context.Context, d *agentv1.ExecData) error {
	d.SessionId = r.id
	return r.sess.send(ctx, &agentv1.ConnectResponse{Msg: &agentv1.ConnectResponse_ExecData{ExecData: d}})
}

// CloseStdin tells the agent to close the process's stdin; output keeps flowing until
// the process exits.
func (r *ExecRelay) CloseStdin(ctx context.Context) {
	_ = r.sess.send(ctx, &agentv1.ConnectResponse{Msg: &agentv1.ConnectResponse_ExecClose{
		ExecClose: &agentv1.ExecClose{SessionId: r.id}}})
}

// Close detaches from the session; output that still arrives is dropped.
func (r *ExecRelay) Close(ctx context.Context) {
	r.CloseStdin(ctx)
	r.sess.dropExec(r.id)
}

// Exec starts a process in one of the App's instances on the node.
func (s *Server) Exec(ctx context.Context, node uuid.UUID, open *agentv1.ExecOpen) (*ExecRelay, error) {
	sess, err := s.session(node)
	if err != nil {
		return nil, err
	}
	id := uuid.NewString()
	open.SessionId = id
	in := make(chan *agentv1.ExecOutput, 64)
	sess.mu.Lock()
	sess.execs[id] = in
	sess.mu.Unlock()
	if err := sess.send(ctx, &agentv1.ConnectResponse{Msg: &agentv1.ConnectResponse_ExecOpen{ExecOpen: open}}); err != nil {
		sess.dropExec(id)
		return nil, err
	}
	out := make(chan *agentv1.ExecOutput)
	go func() {
		defer close(out)
		for {
			select {
			case o := <-in:
				select {
				case out <- o:
				case <-ctx.Done():
					return
				}
				if o.GetExit() != nil {
					return
				}
			case <-sess.done:
				return
			case <-ctx.Done():
				return
			}
		}
	}()
	return &ExecRelay{Output: out, sess: sess, id: id}, nil
}

func (s *Server) session(node uuid.UUID) (*session, error) {
	v, ok := s.sessions.Load(node)
	if !ok {
		return nil, ErrNodeOffline
	}
	return v.(*session), nil
}

// send queues a message for the stream's sender.
func (s *session) send(ctx context.Context, msg *agentv1.ConnectResponse) error {
	select {
	case s.out <- msg:
		return nil
	case <-s.done:
		return ErrNodeOffline
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *session) dropLogs(id string) {
	s.mu.Lock()
	delete(s.logs, id)
	s.mu.Unlock()
}

// cancelLogs tells the agent to stop a log request; a closed stream needs nothing.
func (s *session) cancelLogs(id string) {
	select {
	case s.out <- &agentv1.ConnectResponse{Msg: &agentv1.ConnectResponse_LogCancel{LogCancel: &agentv1.LogCancel{RequestId: id}}}:
	case <-s.done:
	case <-time.After(time.Second):
	}
}

func (s *session) dropExec(id string) {
	s.mu.Lock()
	delete(s.execs, id)
	s.mu.Unlock()
}

// deliverLog hands a chunk to its request, dropping it if the client is too slow.
func (s *session) deliverLog(c *agentv1.LogChunk) {
	s.mu.Lock()
	ch, ok := s.logs[c.GetRequestId()]
	s.mu.Unlock()
	if !ok {
		return
	}
	select {
	case ch <- c:
	default:
		slog.Warn("log client too slow; dropping lines", "request", c.GetRequestId(), "lines", len(c.GetLines()))
	}
}

// deliverExec hands output to its session, waiting a little for a slow client.
func (s *session) deliverExec(o *agentv1.ExecOutput) {
	s.mu.Lock()
	ch, ok := s.execs[o.GetSessionId()]
	s.mu.Unlock()
	if !ok {
		return
	}
	select {
	case ch <- o:
	case <-time.After(execStall):
		slog.Warn("exec client too slow; dropping output", "session", o.GetSessionId())
	}
}
