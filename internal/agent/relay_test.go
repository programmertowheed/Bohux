package agent

import (
	"bytes"
	"context"
	"io"
	"iter"
	"strings"
	"testing"
	"time"

	agentv1 "github.com/bohumix/bohux/gen/bohux/agent/v1"
	bohuxv1 "github.com/bohumix/bohux/gen/bohux/v1"
	"github.com/bohumix/bohux/internal/provider"
)

// fakeWorkloads has one App, "app-1", whose logs and exec are canned.
type fakeWorkloads struct {
	lines []string
	block bool // after the lines, wait for cancellation (follow)
}

func (f *fakeWorkloads) Ref(app string) (provider.ResourceRef, bool) {
	return provider.ResourceRef{App: app}, app == "app-1"
}

func (f *fakeWorkloads) Logs(ctx context.Context, _ provider.LogRequest) (iter.Seq2[provider.LogLine, error], error) {
	return func(yield func(provider.LogLine, error) bool) {
		for _, l := range f.lines {
			if !yield(provider.LogLine{Time: time.Now(), Instance: "i1", Message: l}, nil) {
				return
			}
		}
		if f.block {
			<-ctx.Done()
			yield(provider.LogLine{}, ctx.Err())
		}
	}, nil
}

type fakeSession struct {
	stdout io.Reader
	stdin  bytes.Buffer
	code   int
}

func (s *fakeSession) Stdin() io.WriteCloser       { return nopCloser{&s.stdin} }
func (s *fakeSession) Stdout() io.Reader           { return s.stdout }
func (s *fakeSession) Stderr() io.Reader           { return strings.NewReader("warn\n") }
func (s *fakeSession) Resize(uint16, uint16) error { return nil }
func (s *fakeSession) Wait() (int, error)          { return s.code, nil }
func (s *fakeSession) Close() error                { return nil }

type nopCloser struct{ io.Writer }

func (nopCloser) Close() error { return nil }

func (f *fakeWorkloads) Exec(context.Context, provider.ExecRequest) (provider.Session, error) {
	return &fakeSession{stdout: strings.NewReader("hello\n"), code: 3}, nil
}

// collect reads relay output until done says stop or a second passes.
func collect(t *testing.T, out <-chan *agentv1.ConnectRequest, done func(*agentv1.ConnectRequest) bool) []*agentv1.ConnectRequest {
	t.Helper()
	var got []*agentv1.ConnectRequest
	for {
		select {
		case m := <-out:
			got = append(got, m)
			if done(m) {
				return got
			}
		case <-time.After(time.Second):
			t.Fatalf("relay stalled after %d messages", len(got))
		}
	}
}

func TestRelayLogs(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := make(chan *agentv1.ConnectRequest, 16)
	r := newRelays(ctx, &fakeWorkloads{lines: []string{"a", "b"}}, out)
	eof := func(m *agentv1.ConnectRequest) bool { return m.GetLogChunk().GetEof() }

	r.handle(&agentv1.ConnectResponse{Msg: &agentv1.ConnectResponse_LogRequest{LogRequest: &agentv1.LogRequest{RequestId: "r1", AppId: "app-1"}}})
	got := collect(t, out, eof)
	if len(got) != 3 || got[0].GetLogChunk().GetLines()[0].GetMessage() != "a" || got[0].GetLogChunk().GetRequestId() != "r1" {
		t.Errorf("logs = %v, want a, b, eof", got)
	}

	r.handle(&agentv1.ConnectResponse{Msg: &agentv1.ConnectResponse_LogRequest{LogRequest: &agentv1.LogRequest{RequestId: "r2", AppId: "nope"}}})
	if got := collect(t, out, eof); len(got) != 1 {
		t.Errorf("unknown app = %v, want only eof", got)
	}

	follow := newRelays(ctx, &fakeWorkloads{lines: []string{"x"}, block: true}, out)
	follow.handle(&agentv1.ConnectResponse{Msg: &agentv1.ConnectResponse_LogRequest{LogRequest: &agentv1.LogRequest{RequestId: "r3", AppId: "app-1", Follow: true}}})
	collect(t, out, func(m *agentv1.ConnectRequest) bool { return len(m.GetLogChunk().GetLines()) == 1 })
	follow.handle(&agentv1.ConnectResponse{Msg: &agentv1.ConnectResponse_LogCancel{LogCancel: &agentv1.LogCancel{RequestId: "r3"}}})
	collect(t, out, eof)
}

func TestRelayExec(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := make(chan *agentv1.ConnectRequest, 16)
	r := newRelays(ctx, &fakeWorkloads{}, out)
	exit := func(m *agentv1.ConnectRequest) bool { return m.GetExecOutput().GetExit() != nil }

	r.handle(&agentv1.ConnectResponse{Msg: &agentv1.ConnectResponse_ExecOpen{ExecOpen: &agentv1.ExecOpen{SessionId: "s1", AppId: "app-1", Command: []string{"x"}}}})
	got := collect(t, out, exit)
	var stdout, stderr string
	for _, m := range got {
		stdout += string(m.GetExecOutput().GetStdout())
		stderr += string(m.GetExecOutput().GetStderr())
	}
	if stdout != "hello\n" || stderr != "warn\n" || got[len(got)-1].GetExecOutput().GetExit().GetExitCode() != 3 {
		t.Errorf("exec output %q / %q, exit %v", stdout, stderr, got[len(got)-1])
	}

	r.handle(&agentv1.ConnectResponse{Msg: &agentv1.ConnectResponse_ExecOpen{ExecOpen: &agentv1.ExecOpen{SessionId: "s2", AppId: "nope"}}})
	got = collect(t, out, exit)
	if code := got[len(got)-1].GetExecOutput().GetExit().GetExitCode(); code != execNotStarted {
		t.Errorf("unknown app exit = %d, want %d", code, execNotStarted)
	}

	none := newRelays(ctx, nil, out)
	none.handle(&agentv1.ConnectResponse{Msg: &agentv1.ConnectResponse_ExecOpen{ExecOpen: &agentv1.ExecOpen{SessionId: "s3", AppId: "app-1"}}})
	collect(t, out, exit)
	if none.handle(&agentv1.ConnectResponse{Msg: &agentv1.ConnectResponse_Ping{}}) {
		t.Error("relays claimed a ping")
	}
}

// pipeWorkloads' Exec is a cat on pipes that records its request and resizes.
type pipeWorkloads struct {
	fakeWorkloads
	req     provider.ExecRequest
	resized chan [2]uint16
}

type pipeSession struct {
	in      *io.PipeWriter
	out     *io.PipeReader
	resized chan [2]uint16
	done    chan struct{}
}

func (p *pipeWorkloads) Exec(_ context.Context, req provider.ExecRequest) (provider.Session, error) {
	p.req = req
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	s := &pipeSession{in: inW, out: outR, resized: p.resized, done: make(chan struct{})}
	go func() {
		_, _ = io.Copy(outW, inR)
		_ = outW.Close()
		close(s.done)
	}()
	return s, nil
}

func (s *pipeSession) Stdin() io.WriteCloser { return s.in }
func (s *pipeSession) Stdout() io.Reader     { return s.out }
func (s *pipeSession) Stderr() io.Reader     { return nil }
func (s *pipeSession) Resize(rows, cols uint16) error {
	s.resized <- [2]uint16{rows, cols}
	return nil
}
func (s *pipeSession) Wait() (int, error) { <-s.done; return 0, nil }
func (s *pipeSession) Close() error       { return nil }

// Input, resizes, and the end of input reach the provider's session; a TTY session's
// output is all stdout.
func TestRelayExecInput(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := make(chan *agentv1.ConnectRequest, 16)
	w := &pipeWorkloads{resized: make(chan [2]uint16, 1)}
	r := newRelays(ctx, w, out)

	r.handle(&agentv1.ConnectResponse{Msg: &agentv1.ConnectResponse_ExecOpen{ExecOpen: &agentv1.ExecOpen{
		SessionId: "s1", AppId: "app-1", Command: []string{"sh"}, Tty: true, Size: &bohuxv1.TerminalSize{Rows: 24, Cols: 80}}}})
	r.handle(&agentv1.ConnectResponse{Msg: &agentv1.ConnectResponse_ExecData{ExecData: &agentv1.ExecData{
		SessionId: "s1", Msg: &agentv1.ExecData_Stdin{Stdin: []byte("typed\n")}}}})
	r.handle(&agentv1.ConnectResponse{Msg: &agentv1.ConnectResponse_ExecData{ExecData: &agentv1.ExecData{
		SessionId: "s1", Msg: &agentv1.ExecData_Resize{Resize: &bohuxv1.TerminalSize{Rows: 40, Cols: 100}}}}})
	select {
	case got := <-w.resized:
		if got != [2]uint16{40, 100} {
			t.Errorf("resized to %v, want 40x100", got)
		}
	case <-time.After(time.Second):
		t.Fatal("resize did not reach the session")
	}
	r.handle(&agentv1.ConnectResponse{Msg: &agentv1.ConnectResponse_ExecClose{ExecClose: &agentv1.ExecClose{SessionId: "s1"}}})

	got := collect(t, out, func(m *agentv1.ConnectRequest) bool { return m.GetExecOutput().GetExit() != nil })
	var stdout string
	for _, m := range got {
		stdout += string(m.GetExecOutput().GetStdout())
		if m.GetExecOutput().GetStderr() != nil {
			t.Error("a TTY session sent stderr")
		}
	}
	if stdout != "typed\n" {
		t.Errorf("stdout %q, want the typed input", stdout)
	}
	if !w.req.TTY || w.req.Rows != 24 || w.req.Cols != 80 {
		t.Errorf("provider request %+v, want a 24x80 TTY", w.req)
	}
}
