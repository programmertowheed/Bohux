package agent

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	bohuxv1 "github.com/bohumix/bohux/gen/bohux/v1"
)

func TestChooseStrategy(t *testing.T) {
	const (
		auto       = bohuxv1.BuildStrategy_BUILD_STRATEGY_UNSPECIFIED
		dockerfile = bohuxv1.BuildStrategy_BUILD_STRATEGY_DOCKERFILE
		railpack   = bohuxv1.BuildStrategy_BUILD_STRATEGY_RAILPACK
	)
	withFile, withDir, empty := t.TempDir(), t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(withFile, "Dockerfile"), []byte("FROM scratch\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(withDir, "Dockerfile"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		asked bohuxv1.BuildStrategy
		dir   string
		want  bohuxv1.BuildStrategy
	}{
		{"auto with a Dockerfile", auto, withFile, dockerfile},
		{"auto without one", auto, empty, railpack},
		{"auto with a directory named Dockerfile", auto, withDir, railpack},
		{"railpack despite a Dockerfile", railpack, withFile, railpack},
		{"dockerfile without one", dockerfile, empty, dockerfile},
	} {
		if got := chooseStrategy(tc.asked, tc.dir); got != tc.want {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestRailpackErrors(t *testing.T) {
	p := filepath.Join(t.TempDir(), "info.json")
	info := `{"logs":[{"Level":"info","Msg":"Detected Node"},{"Level":"error","Msg":"No start command was found"}],"success":false}`
	if err := os.WriteFile(p, []byte(info), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := railpackErrors(p); !reflect.DeepEqual(got, []string{"No start command was found"}) {
		t.Errorf("errors = %q", got)
	}
	if got := railpackErrors(filepath.Join(t.TempDir(), "missing.json")); got != nil {
		t.Errorf("a missing info file gave %q", got)
	}
}

func TestHasEnv(t *testing.T) {
	env := []string{"HTTPS_PROXY=http://proxy:3128", "NO_PROXY="}
	if !hasEnv(env, "HTTPS_PROXY") || hasEnv(env, "NO_PROXY") || hasEnv(env, "HTTP_PROXY") {
		t.Error("hasEnv wants set, non-empty variables only")
	}
}
