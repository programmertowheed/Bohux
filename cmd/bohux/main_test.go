package main

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/bohumix/bohux/internal/controlplane"
)

var errCalled = errors.New("called")

func TestRun(t *testing.T) {
	tests := []struct {
		name      string
		args      []string
		env       map[string]string
		want      string
		wantErr   error
		wantCfg   controlplane.Config
		wantDBURL string
	}{
		{name: "no args", args: nil, wantErr: errUsage},
		{name: "version", args: []string{"version"}, want: "dev\n"},
		{name: "unknown command", args: []string{"nope"}, wantErr: errUsage},
		{name: "controlplane needs a database", args: []string{"controlplane"},
			wantErr: errMissing},
		{name: "controlplane from flags", wantErr: errCalled,
			args:    []string{"controlplane", "--listen", ":1", "--database-url", "postgres://f"},
			wantCfg: controlplane.Config{ListenAddr: ":1", DatabaseURL: "postgres://f"}},
		{name: "controlplane from env with default listen", wantErr: errCalled,
			args:    []string{"controlplane"},
			env:     map[string]string{envDatabaseURL: "postgres://e"},
			wantCfg: controlplane.Config{ListenAddr: defaultListenAddr, DatabaseURL: "postgres://e"}},
		{name: "flag beats env", wantErr: errCalled,
			args:    []string{"controlplane", "--database-url", "postgres://f"},
			env:     map[string]string{envDatabaseURL: "postgres://e", envListenAddr: ":2"},
			wantCfg: controlplane.Config{ListenAddr: ":2", DatabaseURL: "postgres://f"}},
		{name: "admin needs a subcommand", args: []string{"admin"}, wantErr: errUsage},
		{name: "admin migrate needs a database", args: []string{"admin", "migrate"},
			wantErr: errMissing},
		{name: "admin migrate", wantErr: errCalled,
			args:      []string{"admin", "migrate", "--database-url", "postgres://m"},
			wantDBURL: "postgres://m"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotCfg controlplane.Config
			var gotDBURL string
			d := deps{
				runControlPlane: func(_ context.Context, c controlplane.Config) error {
					gotCfg = c
					return errCalled
				},
				migrate: func(_ context.Context, dsn string) error {
					gotDBURL = dsn
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
			if gotDBURL != tt.wantDBURL {
				t.Errorf("migrate DSN = %q, want %q", gotDBURL, tt.wantDBURL)
			}
		})
	}
}

// errMissing marks cases that must fail validation without calling a dependency.
var errMissing = errors.New("missing")
