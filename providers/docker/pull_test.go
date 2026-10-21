package docker

import (
	"encoding/json"
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
	operator := t.TempDir()
	for _, sub := range []string{"cli-plugins", "contexts"} {
		if err := os.Mkdir(filepath.Join(operator, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// The operator's own config: a context, a proxy, a helper for the App's registry
	// and one for another, and a global store.
	if err := os.WriteFile(filepath.Join(operator, "config.json"), []byte(`{
		"currentContext": "rootless",
		"proxies": {"default": {"httpsProxy": "http://proxy:3128"}},
		"auths": {"other.example": {"auth": "b3RoZXI="}},
		"credHelpers": {"r.example": "ecr-login", "gcr.io": "gcloud"},
		"credsStore": "desktop"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DOCKER_CONFIG", operator)

	tests := []struct {
		name    string
		src     *pullSource
		wantEnv bool
		wantErr bool
	}{
		{name: "public image", src: nil},
		{name: "credential", src: &pullSource{cfg: `{"auths":{"r.example":{"auth":"eDp5"}}}`}, wantEnv: true},
		{name: "unreadable credential", src: &pullSource{err: errors.New("sealed for another key")}, wantErr: true},
		{name: "not a config.json", src: &pullSource{cfg: `hunter2`}, wantErr: true},
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
				if strings.Contains(err.Error(), "hunter2") {
					t.Errorf("error quotes the secret: %v", err)
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
			if st, err := os.Stat(dir); err != nil || st.Mode().Perm() != 0o700 {
				t.Fatalf("config dir: %v", err)
			}
			if fi, err := os.Stat(filepath.Join(dir, "config.json")); err != nil || fi.Mode().Perm() != 0o600 {
				t.Fatalf("config.json mode: %v", err)
			}
			raw, err := os.ReadFile(filepath.Join(dir, "config.json"))
			if err != nil {
				t.Fatal(err)
			}
			var got struct {
				CurrentContext string                       `json:"currentContext"`
				Proxies        map[string]any               `json:"proxies"`
				Auths          map[string]map[string]string `json:"auths"`
				CredHelpers    map[string]string            `json:"credHelpers"`
				CredsStore     string                       `json:"credsStore"`
			}
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatal(err)
			}
			if got.CurrentContext != "rootless" || got.Proxies == nil {
				t.Errorf("the operator's context or proxies were dropped: %s", raw)
			}
			if got.Auths["r.example"]["auth"] != "eDp5" || got.Auths["other.example"]["auth"] != "b3RoZXI=" {
				t.Errorf("auths = %v", got.Auths)
			}
			if _, ok := got.CredHelpers["r.example"]; ok || got.CredHelpers["gcr.io"] != "gcloud" || got.CredsStore != "" {
				t.Errorf("helpers = %v, store %q: the App's registry must use the secret", got.CredHelpers, got.CredsStore)
			}
			for _, sub := range []string{"cli-plugins", "contexts"} {
				if target, err := os.Readlink(filepath.Join(dir, sub)); err != nil || target != filepath.Join(operator, sub) {
					t.Errorf("%s link = %q, %v", sub, target, err)
				}
			}
			done()
			if _, err := os.Stat(dir); !os.IsNotExist(err) {
				t.Fatalf("config dir still exists after done: %v", err)
			}
		})
	}
}
