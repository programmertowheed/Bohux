//go:build integration

package agent

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	agentv1 "github.com/bohumix/bohux/gen/bohux/agent/v1"
	"github.com/bohumix/bohux/internal/pki"
	"github.com/bohumix/bohux/providers/docker"
)

func dockerCLI(t *testing.T, args ...string) string {
	t.Helper()
	out, err := exec.Command("docker", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("docker %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// Retention against a real daemon: eight labeled releases of one App, the newest
// current, the oldest used by a stopped container. Exactly one image may go.
func TestRetentionDocker(t *testing.T) {
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skip("no Docker daemon: ", err)
	}
	ctx := context.Background()
	app := fmt.Sprintf("retention-test-%d", time.Now().UnixNano())
	repo := "bohux-" + app

	dir := t.TempDir()
	images := map[string]string{} // release -> image ID
	for i := 1; i <= 8; i++ {
		rel := fmt.Sprintf("r-%02d", i)
		df := fmt.Sprintf("FROM busybox:1.36\nRUN echo %s > /release\n", rel)
		if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte(df), 0o600); err != nil {
			t.Fatal(err)
		}
		dockerCLI(t, "build", "--quiet", "--label", "io.bohux.app="+app, "--label", "io.bohux.release="+rel,
			"--tag", repo+":"+rel, dir)
		images[rel] = dockerCLI(t, "image", "inspect", "--format", "{{.Id}}", repo+":"+rel)
	}
	ctr := dockerCLI(t, "container", "create", repo+":r-01")
	t.Cleanup(func() {
		_ = exec.Command("docker", "container", "rm", "--force", ctr).Run()
		for rel := range images {
			_ = exec.Command("docker", "image", "rm", "--force", repo+":"+rel).Run()
		}
	})

	p, err := docker.New(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	signer, err := pki.LoadOrCreate(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	st := testState(t, signer)
	c, err := OpenCache(st)
	if err != nil {
		t.Fatal(err)
	}
	r := &Retention{Images: p, Cache: c}

	// No assignment: nothing is known to be current, so nothing goes.
	if removed, err := r.Once(ctx); err != nil || len(removed) != 0 {
		t.Fatalf("Once without assignment = %v, %v; want nothing removed", removed, err)
	}

	payload, err := proto.Marshal(&agentv1.AssignmentPayload{NodeId: st.NodeID(), Generation: 1,
		Workloads: []*agentv1.Workload{{AppId: app, ReleaseId: "r-08", Provider: docker.Name}}})
	if err != nil {
		t.Fatal(err)
	}
	sig, keyID := signer.SignAssignment(payload)
	if ok, err := c.Accept(&agentv1.Assignment{Payload: payload, Signature: sig, KeyId: keyID}); err != nil || !ok {
		t.Fatalf("Accept: %v %v", ok, err)
	}

	removed, err := r.Once(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{images["r-02"]}; !slices.Equal(removed, want) {
		t.Fatalf("removed %v, want only r-02 (%v)", removed, want)
	}
	for rel, id := range images {
		gone := exec.Command("docker", "image", "inspect", id).Run() != nil
		if gone != (rel == "r-02") {
			t.Errorf("release %s: image gone = %v", rel, gone)
		}
	}
	// A second pass finds nothing more to do.
	if removed, err := r.Once(ctx); err != nil || len(removed) != 0 {
		t.Fatalf("second pass removed %v, %v; want nothing", removed, err)
	}
}
