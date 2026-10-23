package cli

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	bohuxv1 "github.com/bohumix/bohux/gen/bohux/v1"
	"github.com/bohumix/bohux/gen/bohux/v1/bohuxv1connect"
)

// fakeAPI answers the lookups `exec` makes and runs Exec as an upper-casing cat that
// exits 7, recording what the CLI sent.
type fakeAPI struct {
	bohuxv1connect.UnimplementedProjectServiceHandler
	bohuxv1connect.UnimplementedEnvironmentServiceHandler
	bohuxv1connect.UnimplementedAppServiceHandler
	bohuxv1connect.UnimplementedWorkloadServiceHandler
	start *bohuxv1.ExecStart
}

func meta(id string) *bohuxv1.ResourceMeta { return &bohuxv1.ResourceMeta{Id: id} }

func (f *fakeAPI) GetProject(context.Context, *connect.Request[bohuxv1.GetProjectRequest]) (*connect.Response[bohuxv1.GetProjectResponse], error) {
	return connect.NewResponse(&bohuxv1.GetProjectResponse{Project: &bohuxv1.Project{Meta: meta("p")}}), nil
}

func (f *fakeAPI) GetEnvironment(context.Context, *connect.Request[bohuxv1.GetEnvironmentRequest]) (*connect.Response[bohuxv1.GetEnvironmentResponse], error) {
	return connect.NewResponse(&bohuxv1.GetEnvironmentResponse{Environment: &bohuxv1.Environment{Meta: meta("e")}}), nil
}

func (f *fakeAPI) GetApp(context.Context, *connect.Request[bohuxv1.GetAppRequest]) (*connect.Response[bohuxv1.GetAppResponse], error) {
	return connect.NewResponse(&bohuxv1.GetAppResponse{App: &bohuxv1.App{Meta: meta("a")}}), nil
}

func (f *fakeAPI) Exec(_ context.Context, s *connect.BidiStream[bohuxv1.ExecRequest, bohuxv1.ExecResponse]) error {
	first, err := s.Receive()
	if err != nil {
		return err
	}
	f.start = first.GetStart()
	for {
		in, err := s.Receive()
		if err != nil {
			break // the client closed its input
		}
		if b := in.GetStdin(); b != nil {
			if err := s.Send(&bohuxv1.ExecResponse{Msg: &bohuxv1.ExecResponse_Stdout{Stdout: bytes.ToUpper(b)}}); err != nil {
				return err
			}
		}
	}
	return s.Send(&bohuxv1.ExecResponse{Msg: &bohuxv1.ExecResponse_Exit{Exit: &bohuxv1.ExecExit{ExitCode: 7}}})
}

func TestExecInput(t *testing.T) {
	f := &fakeAPI{}
	mux := http.NewServeMux()
	mux.Handle(bohuxv1connect.NewProjectServiceHandler(f))
	mux.Handle(bohuxv1connect.NewEnvironmentServiceHandler(f))
	mux.Handle(bohuxv1connect.NewAppServiceHandler(f))
	mux.Handle(bohuxv1connect.NewWorkloadServiceHandler(f))
	srv := httptest.NewServer(h2c.NewHandler(mux, &http2.Server{}))
	defer srv.Close()
	env := map[string]string{EnvAPIURL: srv.URL, EnvToken: "t", EnvProject: "shop"}
	getenv := func(k string) string { return env[k] }

	tests := []struct {
		name    string
		args    []string
		stdin   string
		want    string
		wantErr error
	}{
		{name: "-i forwards input until its end", args: []string{"api", "-e", "prod", "-i", "--", "cat"},
			stdin: "hello\nworld\n", want: "HELLO\nWORLD\n"},
		{name: "without -i the process gets no input", args: []string{"api", "-e", "prod", "--", "cat"},
			stdin: "ignored\n", want: ""},
		{name: "-t needs a terminal", args: []string{"api", "-e", "prod", "-t", "--", "sh"},
			stdin: "x", wantErr: ErrUsage},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			err := Run(context.Background(), "exec", tt.args, strings.NewReader(tt.stdin), &out, getenv)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("err = %v, want %v", err, tt.wantErr)
				}
				return
			}
			var exit *ExitError
			if !errors.As(err, &exit) || exit.Code != 7 {
				t.Fatalf("err = %v, want exit status 7", err)
			}
			if out.String() != tt.want {
				t.Errorf("output %q, want %q", out.String(), tt.want)
			}
			if f.start.GetTty() || f.start.GetAppId() != "a" {
				t.Errorf("start = %v", f.start)
			}
		})
	}
}
