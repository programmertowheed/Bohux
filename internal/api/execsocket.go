package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"connectrpc.com/connect"
	"github.com/coder/websocket"
	"google.golang.org/protobuf/proto"

	bohuxv1 "github.com/bohumix/bohux/gen/bohux/v1"
	"github.com/bohumix/bohux/gen/bohux/v1/bohuxv1connect"
)

// ExecSocketPath is the web terminal's WebSocket bridge to WorkloadService.Exec
// (ADR-0046). The first frame is a text frame holding the API token; every later frame
// is one binary protobuf ExecRequest (client) or ExecResponse (server).
const ExecSocketPath = "/sockets/v1/exec"

const (
	// execSocketAuthWait bounds how long the bridge waits for the token frame.
	execSocketAuthWait = 10 * time.Second
	// execSocketPing keeps idle sessions alive through proxies.
	execSocketPing = 30 * time.Second
	// execSocketReadLimit refuses larger frames; the CLI sends stdin in 32 KiB chunks.
	execSocketReadLimit = 64 << 10
	// execSocketCodeBase plus a Connect code is the close code for a failed session.
	execSocketCodeBase = 4000
)

// ServeExecSocket upgrades to a WebSocket and runs one Exec session over it, with the
// same authentication, permission check, and audit event as the Connect handler.
func (s *Server) ServeExecSocket(w http.ResponseWriter, r *http.Request) {
	// Accept refuses an Origin whose host differs from Host (cross-site hijacking).
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		// Accept has answered the request already.
		return
	}
	conn.SetReadLimit(execSocketReadLimit)
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	rec := &callRecord{}
	start := time.Now()
	err = s.execSocket(context.WithValue(ctx, callRecordKey{}, rec), cancel, conn)
	logCall(ctx, ExecSocketPath, rec, start, err)
	if err == nil {
		_ = conn.Close(websocket.StatusNormalClosure, "")
		return
	}
	code := connect.CodeOf(err)
	msg := err.Error()
	var ce *connect.Error
	if errors.As(err, &ce) {
		msg = ce.Message()
	}
	// A close reason is at most 123 bytes.
	if len(msg) > 120 {
		msg = msg[:120]
	}
	_ = conn.Close(websocket.StatusCode(execSocketCodeBase+int(code)), msg)
}

func (s *Server) execSocket(ctx context.Context, cancel context.CancelFunc, conn *websocket.Conn) error {
	authCtx, stop := context.WithTimeout(ctx, execSocketAuthWait)
	typ, token, err := conn.Read(authCtx)
	stop()
	if err != nil || typ != websocket.MessageText {
		return connect.NewError(connect.CodeUnauthenticated, errors.New("the first frame must be the API token"))
	}
	authz := &authInterceptor{authn: s.Authn, perms: permissions}
	ctx, err = authz.check(ctx, bohuxv1connect.WorkloadServiceExecProcedure, "Bearer "+string(token))
	if err != nil {
		return err
	}
	stream := &socketStream{ctx: ctx, cancel: cancel, conn: conn}
	go func() {
		t := time.NewTicker(execSocketPing)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if err := conn.Ping(ctx); err != nil {
					_ = stream.fail(fmt.Errorf("%w: %w", errClientGone, err))
					return
				}
			}
		}
	}()
	err = (&workloadService{s}).exec(ctx, stream)
	if f := stream.failure(); f != nil {
		if errors.Is(f, errClientGone) {
			// There is nobody left to tell.
			return nil
		}
		return f
	}
	return err
}

// errClientGone marks a session whose client closed the socket or stopped answering.
var errClientGone = errors.New("exec socket closed by the client")

// socketStream adapts the WebSocket to execStream.
type socketStream struct {
	ctx    context.Context
	cancel context.CancelFunc
	conn   *websocket.Conn

	mu     sync.Mutex
	failed error
}

// fail ends the session: a WebSocket has no half-close, so a read that fails means
// the client is gone, and a frame that is not an ExecRequest is answered by closing.
func (s *socketStream) fail(err error) error {
	s.mu.Lock()
	if s.failed == nil {
		s.failed = err
	}
	s.mu.Unlock()
	s.cancel()
	return err
}

func (s *socketStream) failure() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.failed
}

// Receive reads the next ExecRequest.
func (s *socketStream) Receive() (*bohuxv1.ExecRequest, error) {
	typ, b, err := s.conn.Read(s.ctx)
	if err != nil {
		return nil, s.fail(fmt.Errorf("%w: %w", errClientGone, err))
	}
	if typ != websocket.MessageBinary {
		return nil, s.fail(invalidArgument("frame", "BINARY", "exec frames after the token must be binary protobuf"))
	}
	m := &bohuxv1.ExecRequest{}
	if err := proto.Unmarshal(b, m); err != nil {
		return nil, s.fail(invalidArgument("frame", "PROTOBUF", "an exec frame is not an ExecRequest"))
	}
	return m, nil
}

func (s *socketStream) Send(m *bohuxv1.ExecResponse) error {
	b, err := proto.Marshal(m)
	if err != nil {
		return fmt.Errorf("encode exec frame: %w", err)
	}
	if err := s.conn.Write(s.ctx, websocket.MessageBinary, b); err != nil {
		slog.DebugContext(s.ctx, "exec socket write failed", "err", err)
		return fmt.Errorf("write exec frame: %w", err)
	}
	return nil
}
