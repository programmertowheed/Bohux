//go:build integration

package docker

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	bohuxv1 "github.com/bohumix/bohux/gen/bohux/v1"
	"github.com/bohumix/bohux/internal/provider"
)

func dockerOut(t *testing.T, args ...string) string {
	t.Helper()
	out, err := exec.Command("docker", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("docker %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// TestOptionsDocker attaches an App to an external network and volume on a real
// daemon, and leaves both in place on Destroy (ADR-0032).
func TestOptionsDocker(t *testing.T) {
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skip("no Docker daemon: ", err)
	}
	ctx := context.Background()
	suffix := fmt.Sprint(time.Now().UnixNano())
	network, volume := "bohux-opt-net-"+suffix, "bohux-opt-vol-"+suffix
	dockerOut(t, "network", "create", network)
	dockerOut(t, "volume", "create", volume)
	t.Cleanup(func() {
		_ = exec.Command("docker", "network", "rm", network).Run()
		_ = exec.Command("docker", "volume", "rm", volume).Run()
	})
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM busybox:1.36\nCMD [\"sleep\", \"3600\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	image := "bohux-opt-test:" + suffix
	dockerOut(t, "build", "-q", "-t", image, dir)
	t.Cleanup(func() { _ = exec.Command("docker", "rmi", "-f", image).Run() })

	p, err := New(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	ref := provider.ResourceRef{Environment: "env-" + suffix, App: "app-" + suffix, AppSlug: "opt", EnvironmentSlug: "test"}
	spec := provider.Spec{Ref: ref, Release: "r1", Image: image, Hash: "h1", Runtime: &bohuxv1.Runtime{Replicas: 1},
		Raw: []byte(fmt.Sprintf(`{"networks": [%q], "volumes": [{"source": %q, "target": "/data"}]}`, network, volume))}
	if _, err := p.Apply(ctx, provider.ApplyRequest{Spec: spec}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Destroy(ctx, ref) })
	id := dockerOut(t, "ps", "-q", "--filter", "label=io.bohux.app="+ref.App)
	if nets := dockerOut(t, "inspect", "--format", "{{range $k, $v := .NetworkSettings.Networks}}{{$k}} {{end}}", id); !strings.Contains(nets, network) {
		t.Errorf("networks = %q, want %s among them", nets, network)
	}
	if mounts := dockerOut(t, "inspect", "--format", "{{range .Mounts}}{{.Name}}:{{.Destination}} {{end}}", id); !strings.Contains(mounts, volume+":/data") {
		t.Errorf("mounts = %q, want %s at /data", mounts, volume)
	}
	if err := p.Destroy(ctx, ref); err != nil {
		t.Fatal(err)
	}
	dockerOut(t, "network", "inspect", network)
	dockerOut(t, "volume", "inspect", volume)
}
