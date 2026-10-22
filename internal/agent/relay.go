package agent

import (
	"context"
	"errors"
	"io"
	"iter"
	"log/slog"
	"sync"

	"google.golang.org/protobuf/types/known/timestamppb"

	agentv1 "github.com/bohumix/bohux/gen/bohux/agent/v1"
	bohuxv1 "github.com/bohumix/bohux/gen/bohux/v1"
	"github.com/bohumix/bohux/internal/provider"
)

// WorkloadAccess reads logs from and runs commands in assigned workloads; the
// Reconciler implements it through its provider.
type WorkloadAccess interface {
	Ref(appID string) (provider.ResourceRef, bool)
	Logs(ctx context.Context, req provider.LogRequest) (iter.Seq2[provider.LogLine, error], error)
	Exec(ctx context.Context, req provider.ExecRequest) (provider.Session, error)
}

// Ref returns the provider resource of an App in the cached assignment.
func (r *Reconciler) Ref(appID string) (provider.ResourceRef, bool) {
	a := r.Cache.Current()
	for _, w := range a.GetWorkloads() {
		if w.GetAppId() == appID {
			ref := provider.ResourceRef{Environment: w.GetEnvironmentId(), App: w.GetAppId(),
				EnvironmentSlug: w.GetEnvironmentSlug(), AppSlug: w.GetAppSlug()}
			// The slot that serves (ADR-0035); none for an App that never switched.
			slots, err := r.loadSlots()
			if err != nil {
				slog.Warn("read slot record for logs or exec; using the unslotted workload", "app", appID, "err", err)
			}
			ref.Slot = slots[slotKey(ref)].Slot
			return ref, true
		}
	}
	return provider.ResourceRef{}, false
}

// Logs reads log lines through the App's provider.
func (r *Reconciler) Logs(ctx context.Context, req provider.LogRequest) (iter.Seq2[provider.LogLine, error], error) {
	return r.providerOf(req.Ref.App).Logs(ctx, req)
}

// Exec starts a process through the App's provider.
func (r *Reconciler) Exec(ctx context.Context, req provider.ExecRequest) (provider.Session, error) {
	return r.providerOf(req.Ref.App).Exec(ctx, req)
}

// providerOf is the provider the App's workload names in the cached assignment.
func (r *Reconciler) providerOf(appID string) provider.Provider {
	for _, w := range r.Cache.Current().GetWorkloads() {
		if w.GetAppId() == appID {
			return r.providerNamed(w.GetProvider())
		}
	}
	return r.Provider
}

// execNotStarted is the exit code reported when a process could not be started, as a
// shell reports a command it cannot execute.
const execNotStarted = 126

// relays serves the log requests and exec sessions of one stream. Everything it sends
// goes through out, which the stream's loop drains (the stream's only sender).
type relays struct {
	ctx context.Context
	w   WorkloadAccess
	out chan<- *agentv1.ConnectRequest

	mu    sync.Mutex
	logs  map[string]context.CancelFunc
	execs map[string]provider.Session
}

func newRelays(ctx context.Context, w WorkloadAccess, out chan<- *agentv1.ConnectRequest) *relays {
	return &relays{ctx: ctx, w: w, out: out, logs: map[string]context.CancelFunc{}, execs: map[string]provider.Session{}}
}

// handle serves a log or exec message; it reports false for anything else.
func (r *relays) handle(msg *agentv1.ConnectResponse) bool {
	switch m := msg.GetMsg().(type) {
	case *agentv1.ConnectResponse_LogRequest:
		r.startLogs(m.LogRequest)
	case *agentv1.ConnectResponse_LogCancel:
		r.mu.Lock()
		if cancel, ok := r.logs[m.LogCancel.GetRequestId()]; ok {
			cancel()
		}
		r.mu.Unlock()
	case *agentv1.ConnectResponse_ExecOpen:
		r.startExec(m.ExecOpen)
	case *agentv1.ConnectResponse_ExecData:
		r.execData(m.ExecData)
	case *agentv1.ConnectResponse_ExecClose:
		r.mu.Lock()
		s, ok := r.execs[m.ExecClose.GetSessionId()]
		r.mu.Unlock()
		if ok {
			_ = s.Stdin().Close()
		}
	default:
		return false
	}
	return true
}

func (r *relays) emit(msg *agentv1.ConnectRequest) bool {
	select {
	case r.out <- msg:
		return true
	case <-r.ctx.Done():
		return false
	}
}

func (r *relays) startLogs(req *agentv1.LogRequest) {
	id := req.GetRequestId()
	eof := func() {
		r.emit(&agentv1.ConnectRequest{Msg: &agentv1.ConnectRequest_LogChunk{LogChunk: &agentv1.LogChunk{RequestId: id, Eof: true}}})
	}
	if r.w == nil {
		eof()
		return
	}
	ref, ok := r.w.Ref(req.GetAppId())
	if !ok {
		eof()
		return
	}
	ctx, cancel := context.WithCancel(r.ctx)
	r.mu.Lock()
	r.logs[id] = cancel
	r.mu.Unlock()
	go func() {
		defer func() {
			cancel()
			r.mu.Lock()
			delete(r.logs, id)
			r.mu.Unlock()
		}()
		lr := provider.LogRequest{Ref: ref, Instance: req.GetInstance(), Follow: req.GetFollow(), TailLines: int(req.GetTailLines())}
		if req.GetSince() != nil {
			lr.Since = req.GetSince().AsTime()
		}
		lines, err := r.w.Logs(ctx, lr)
		if err != nil {
			slog.WarnContext(ctx, "read logs", "app", req.GetAppId(), "err", err)
			eof()
			return
		}
		for l, err := range lines {
			if err != nil {
				if !errors.Is(err, context.Canceled) {
					slog.WarnContext(ctx, "read logs", "app", req.GetAppId(), "err", err)
				}
				break
			}
			stream := bohuxv1.LogStream_LOG_STREAM_STDOUT
			if l.Stderr {
				stream = bohuxv1.LogStream_LOG_STREAM_STDERR
			}
			if !r.emit(&agentv1.ConnectRequest{Msg: &agentv1.ConnectRequest_LogChunk{LogChunk: &agentv1.LogChunk{
				RequestId: id, Lines: []*bohuxv1.LogLine{{Time: timestamppb.New(l.Time), Instance: l.Instance,
					Stream: stream, Message: l.Message}}}}}) {
				return
			}
		}
		eof()
	}()
}

func (r *relays) startExec(open *agentv1.ExecOpen) {
	id := open.GetSessionId()
	exit := func(code int) {
		r.emit(&agentv1.ConnectRequest{Msg: &agentv1.ConnectRequest_ExecOutput{ExecOutput: &agentv1.ExecOutput{
			SessionId: id, Msg: &agentv1.ExecOutput_Exit{Exit: &bohuxv1.ExecExit{ExitCode: int32(code)}}}}})
	}
	if r.w == nil {
		r.emit(execText(id, "bohux: this agent runs no workloads\n"))
		exit(execNotStarted)
		return
	}
	ref, ok := r.w.Ref(open.GetAppId())
	if !ok {
		r.emit(execText(id, "bohux: the app has no workload on this node\n"))
		exit(execNotStarted)
		return
	}
	sess, err := r.w.Exec(r.ctx, provider.ExecRequest{Ref: ref, Instance: open.GetInstance(), Command: open.GetCommand(),
		TTY: open.GetTty(), Rows: uint16(open.GetSize().GetRows()), Cols: uint16(open.GetSize().GetCols())})
	if err != nil {
		slog.WarnContext(r.ctx, "exec", "app", open.GetAppId(), "err", err)
		r.emit(execText(id, "bohux: cannot start the command\n"))
		exit(execNotStarted)
		return
	}
	r.mu.Lock()
	r.execs[id] = sess
	r.mu.Unlock()
	go func() {
		var wg sync.WaitGroup
		pump := func(src io.Reader, stderr bool) {
			defer wg.Done()
			buf := make([]byte, 32<<10)
			for {
				n, err := src.Read(buf)
				if n > 0 {
					chunk := append([]byte(nil), buf[:n]...)
					o := &agentv1.ExecOutput{SessionId: id, Msg: &agentv1.ExecOutput_Stdout{Stdout: chunk}}
					if stderr {
						o.Msg = &agentv1.ExecOutput_Stderr{Stderr: chunk}
					}
					if !r.emit(&agentv1.ConnectRequest{Msg: &agentv1.ConnectRequest_ExecOutput{ExecOutput: o}}) {
						return
					}
				}
				if err != nil {
					return
				}
			}
		}
		wg.Add(1)
		go pump(sess.Stdout(), false)
		if e := sess.Stderr(); e != nil {
			wg.Add(1)
			go pump(e, true)
		}
		wg.Wait()
		code, err := sess.Wait()
		if err != nil {
			slog.WarnContext(r.ctx, "exec wait", "app", open.GetAppId(), "err", err)
		}
		_ = sess.Close()
		r.mu.Lock()
		delete(r.execs, id)
		r.mu.Unlock()
		exit(code)
	}()
}

func execText(id, s string) *agentv1.ConnectRequest {
	return &agentv1.ConnectRequest{Msg: &agentv1.ConnectRequest_ExecOutput{ExecOutput: &agentv1.ExecOutput{
		SessionId: id, Msg: &agentv1.ExecOutput_Stderr{Stderr: []byte(s)}}}}
}

func (r *relays) execData(d *agentv1.ExecData) {
	r.mu.Lock()
	s, ok := r.execs[d.GetSessionId()]
	r.mu.Unlock()
	if !ok {
		return
	}
	switch m := d.GetMsg().(type) {
	case *agentv1.ExecData_Stdin:
		if _, err := s.Stdin().Write(m.Stdin); err != nil {
			slog.DebugContext(r.ctx, "exec stdin", "err", err)
		}
	case *agentv1.ExecData_Resize:
		_ = s.Resize(uint16(m.Resize.GetRows()), uint16(m.Resize.GetCols()))
	}
}

// closeAll ends every log request and exec session when the stream ends.
func (r *relays) closeAll() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, cancel := range r.logs {
		cancel()
	}
	for _, s := range r.execs {
		_ = s.Close()
	}
}
