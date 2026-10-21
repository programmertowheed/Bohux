//go:build integration

package agent

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	agentv1 "github.com/bohumix/bohux/gen/bohux/agent/v1"
	bohuxv1 "github.com/bohumix/bohux/gen/bohux/v1"
)

// gitRepo makes a local repository with the given files committed and tagged v1, and
// returns its path and commit.
func gitRepo(t *testing.T, files map[string]string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.com"}, args...)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q")
	for name, body := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git("add", ".")
	git("commit", "-q", "-m", "init")
	git("tag", "v1")
	return dir, git("rev-parse", "HEAD")
}

type pressure bool

func (p pressure) Degraded() (bool, *bohuxv1.ErrorDetail) {
	if p {
		return true, &bohuxv1.ErrorDetail{Reason: "DISK_PRESSURE"}
	}
	return false, nil
}

// TestBuilderDocker builds from Git with buildx into the local daemon (ADR-0028).
func TestBuilderDocker(t *testing.T) {
	if err := exec.Command("docker", "buildx", "version").Run(); err != nil {
		t.Skip("no docker buildx: ", err)
	}
	ctx := context.Background()
	repo, commit := gitRepo(t, map[string]string{
		"svc/Dockerfile":    "FROM busybox:1.36\nRUN echo built > /built\nCMD [\"cat\", \"/built\"]\n",
		"broken/Dockerfile": "FROM busybox:1.36\nRUN exit 3\n",
	})
	b := &Builder{WorkDir: t.TempDir()}
	req := func(release, ref, sub string) *agentv1.BuildRequest {
		return &agentv1.BuildRequest{ReleaseId: release, AppId: "app-1", RepoUrl: repo, Ref: ref, Subpath: sub}
	}

	res := b.Build(ctx, req("rel-build-test-1", "v1", "svc"))
	if res.GetFailure() != nil {
		t.Fatalf("build failed: %v", res.GetFailure())
	}
	t.Cleanup(func() { _ = exec.Command("docker", "rmi", "-f", res.GetImageId()).Run() })
	if res.GetCommit() != commit || !strings.HasPrefix(res.GetImageId(), "sha256:") {
		t.Errorf("result = %v, want commit %s", res, commit)
	}
	labels := dockerCLI(t, "image", "inspect", res.GetImageId(), "--format",
		`{{index .Config.Labels "io.bohux.app"}} {{index .Config.Labels "io.bohux.release"}}`)
	if labels != "app-1 rel-build-test-1" {
		t.Errorf("labels = %q, want the app and release", labels)
	}
	if again := b.Build(ctx, req("rel-build-test-1", "v1", "svc")); again != res {
		t.Error("a repeated request built again instead of answering from recent results")
	}
	byCommit := b.Build(ctx, req("rel-build-test-2", commit, "svc"))
	t.Cleanup(func() { _ = exec.Command("docker", "rmi", "-f", byCommit.GetImageId()).Run() })
	// Another Release is another label, so another image ID; the source is the same.
	if byCommit.GetFailure() != nil || byCommit.GetCommit() != commit {
		t.Errorf("building by commit SHA gave %v, want commit %s", byCommit, commit)
	}

	tests := []struct {
		name string
		b    *Builder
		r    *agentv1.BuildRequest
		want string
	}{
		{"unknown ref", b, req("rel-x1", "no-such-branch", "svc"), BuildFetchFailed},
		{"failing Dockerfile", b, req("rel-x2", "v1", "broken"), BuildFailed},
		{"escaping subpath", b, req("rel-x3", "v1", "../etc"), BuildInvalid},
		{"option as ref", b, req("rel-x4", "--upload-pack=touch /tmp/x", "svc"), BuildInvalid},
		{"disk pressure", &Builder{WorkDir: t.TempDir(), Disk: pressure(true)}, req("rel-x5", "v1", "svc"), BuildDiskPressure},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.b.Build(ctx, tt.r).GetFailure().GetReason(); got != tt.want {
				t.Errorf("failure = %q, want %q", got, tt.want)
			}
		})
	}
}
