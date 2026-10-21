package docker

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type pullSource struct {
	cfg string
	err error
}

func (s pullSource) Open() (string, error) { return s.cfg, s.err }

func TestPullEnv(t *testing.T) {
	plugins := t.TempDir()
	if err := os.Mkdir(filepath.Join(plugins, "cli-plugins"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DOCKER_CONFIG", plugins)

	tests := []struct {
		name    string
		src     *pullSource
		wantEnv bool
		wantErr bool
	}{
		{name: "public image", src: nil},
		{name: "credential", src: &pullSource{cfg: `{"auths":{"r.example":{"auth":"eDp5"}}}`}, wantEnv: true},
		{name: "unreadable credential", src: &pullSource{err: errors.New("sealed for another key")}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var src interface{ Open() (string, error) }
			if tt.src != nil {
				src = *tt.src
			}
			env, done, err := pullEnv(src)
			if tt.wantErr {
				if err == nil {
					t.Fatal("pullEnv succeeded, want an error")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !tt.wantEnv {
				if len(env) != 0 {
					t.Fatalf("env = %v, want none", env)
				}
				done()
				return
			}
			if len(env) != 1 || !strings.HasPrefix(env[0], "DOCKER_CONFIG=") {
				t.Fatalf("env = %v", env)
			}
			dir := strings.TrimPrefix(env[0], "DOCKER_CONFIG=")
			st, err := os.Stat(dir)
			if err != nil || st.Mode().Perm() != 0o700 {
				t.Fatalf("config dir mode = %v, %v; want 0700", st.Mode().Perm(), err)
			}
			b, err := os.ReadFile(filepath.Join(dir, "config.json"))
			if err != nil || string(b) != tt.src.cfg {
				t.Fatalf("config.json = %q, %v", b, err)
			}
			if fi, err := os.Stat(filepath.Join(dir, "config.json")); err != nil || fi.Mode().Perm() != 0o600 {
				t.Fatalf("config.json mode = %v, %v; want 0600", fi.Mode().Perm(), err)
			}
			if target, err := os.Readlink(filepath.Join(dir, "cli-plugins")); err != nil || target != filepath.Join(plugins, "cli-plugins") {
				t.Fatalf("cli-plugins link = %q, %v", target, err)
			}
			done()
			if _, err := os.Stat(dir); !os.IsNotExist(err) {
				t.Fatalf("config dir still exists after done: %v", err)
			}
		})
	}
}
