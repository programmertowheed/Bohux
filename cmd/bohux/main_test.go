package main

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/bohumix/bohux/internal/agent"
	"github.com/bohumix/bohux/internal/controlplane"
)

var errCalled = errors.New("called")

func TestRun(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		env     map[string]string
		want    string
		wantErr error
		wantCfg controlplane.Config
		wantArg string
	}{
		{name: "no args", args: nil, wantErr: errUsage},
		{name: "version", args: []string{"version"}, want: "dev\n"},
		{name: "unknown command", args: []string{"nope"}, wantErr: errUsage},
		{name: "controlplane needs a database", args: []string{"controlplane"},
			wantErr: errMissing},
		{name: "controlplane from flags", wantErr: errCalled,
			args: []string{"controlplane", "--listen", ":1", "--database-url", "postgres://f",
				"--temporal-address", "temporal:7233", "--temporal-namespace", "bohux"},
			wantCfg: controlplane.Config{ListenAddr: ":1", AgentListenAddr: defaultAgentListenAddr, DatabaseURL: "postgres://f",
				DataDir: defaultDataDir, TemporalAddress: "temporal:7233", TemporalNamespace: "bohux"}},
		{name: "controlplane from env with default listen", wantErr: errCalled,
			args: []string{"controlplane"},
			env:  map[string]string{envDatabaseURL: "postgres://e"},
			wantCfg: controlplane.Config{ListenAddr: defaultListenAddr, AgentListenAddr: defaultAgentListenAddr, DatabaseURL: "postgres://e",
				DataDir: defaultDataDir, TemporalAddress: defaultTemporalAddress, TemporalNamespace: defaultTemporalNS}},
		{name: "flag beats env", wantErr: errCalled,
			args: []string{"controlplane", "--database-url", "postgres://f"},
			env: map[string]string{envDatabaseURL: "postgres://e", envListenAddr: ":2", envDataDir: "/d",
				envAgentListenAddr: ":3", envTemporalAddress: "t:1", envTemporalNS: "ns"},
			wantCfg: controlplane.Config{ListenAddr: ":2", AgentListenAddr: ":3", DatabaseURL: "postgres://f", DataDir: "/d",
				TemporalAddress: "t:1", TemporalNamespace: "ns"}},
		{name: "admin needs a subcommand", args: []string{"admin"}, wantErr: errUsage},
		{name: "admin migrate needs a database", args: []string{"admin", "migrate"},
			wantErr: errMissing},
		{name: "admin migrate", wantErr: errCalled,
			args:    []string{"admin", "migrate", "--database-url", "postgres://m"},
			wantArg: "postgres://m"},
		{name: "admin unknown subcommand", args: []string{"admin", "nope"}, wantErr: errUsage},
		{name: "admin bootstrap needs a slug", wantErr: errMissing,
			args: []string{"admin", "bootstrap", "--database-url", "postgres://b"}},
		{name: "agent register needs its flags", wantErr: errMissing,
			args: []string{"agent", "register", "--control-plane", "cp:7443"}},
		{name: "agent register takes the token from env", wantErr: errCalled,
			args: []string{"agent", "register", "--control-plane", "cp:7443", "--ca-cert-sha256", "ab"},
			env:  map[string]string{envBootstrapToken: "bxb_x"}, wantArg: "cp:7443|bxb_x|ab|" + defaultAgentStateDir},
		{name: "agent run uses the state dir", wantErr: errCalled,
			args: []string{"agent", "run", "--state-dir", "/s"}, wantArg: "/s"},
		{name: "agent unknown subcommand", args: []string{"agent", "nope"}, wantErr: errUsage},
		{name: "admin bootstrap prints the token once", want: bootstrapOut,
			args:    []string{"admin", "bootstrap", "--database-url", "postgres://b", "--slug", "acme"},
			wantArg: "postgres://b"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotCfg controlplane.Config
			var gotArg string
			d := deps{
				runControlPlane: func(_ context.Context, c controlplane.Config) error {
					gotCfg = c
					return errCalled
				},
				migrate: func(_ context.Context, dsn string) error {
					gotArg = dsn
					return errCalled
				},
				bootstrap: func(_ context.Context, dsn, slug, _ string) (string, error) {
					gotArg = dsn
					return "bxa_token-for-" + slug, nil
				},
				agentRegister: func(_ context.Context, o agent.RegisterOptions) error {
					gotArg = o.ControlPlane + "|" + o.Token + "|" + o.CACertSHA256 + "|" + o.StateDir
					return errCalled
				},
				agentRun: func(_ context.Context, dir string) error {
					gotArg = dir
					return errCalled
				},
			}
			getenv := func(k string) string { return tt.env[k] }
			var out bytes.Buffer
			err := runWith(context.Background(), tt.args, &out, getenv, d)
			if tt.wantErr == errMissing {
				if err == nil || errors.Is(err, errCalled) {
					t.Fatalf("got %v, want a missing-database error before running", err)
				}
			} else if !errors.Is(err, tt.wantErr) {
				t.Fatalf("error = %v, want %v", err, tt.wantErr)
			}
			if got := out.String(); got != tt.want {
				t.Errorf("output = %q, want %q", got, tt.want)
			}
			if gotCfg != tt.wantCfg {
				t.Errorf("controlplane config = %+v, want %+v", gotCfg, tt.wantCfg)
			}
			if gotArg != tt.wantArg {
				t.Errorf("dependency argument = %q, want %q", gotArg, tt.wantArg)
			}
		})
	}
}

const bootstrapOut = "Tenant \"acme\" created.\n" +
	"Owner token for the whole tenant (shown once, store it now):\n\nbxa_token-for-acme\n"

// errMissing marks cases that must fail validation without calling a dependency.
var errMissing = errors.New("missing")
