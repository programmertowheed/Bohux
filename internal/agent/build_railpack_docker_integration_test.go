//go:build integration

package agent

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"

	agentv1 "github.com/bohumix/bohux/gen/bohux/agent/v1"
	bohuxv1 "github.com/bohumix/bohux/gen/bohux/v1"
)

// TestBuilderRailpack plans and builds Git sources without a Dockerfile (ADR-0047).
// BOHUX_TEST_RAILPACK_IMAGE replaces the pinned frontend image and
// BOHUX_TEST_RAILPACK_NETWORK the planning container's network, for hosts that cannot
// pull from ghcr.io or reach the internet from Docker's bridge.
// BOHUX_TEST_RAILPACK_OFFLINE=1 skips the full build, whose base images come from
// ghcr.io.
func TestBuilderRailpack(t *testing.T) {
	if err := exec.Command("docker", "buildx", "version").Run(); err != nil {
		t.Skip("no docker buildx: ", err)
	}
	ctx := context.Background()
	repo, _ := gitRepo(t, map[string]string{
		"shell/start.sh": "#!/bin/sh\necho hello from railpack\n",
		"none/README":    "nothing Railpack can build\n",
	})
	b := &Builder{WorkDir: t.TempDir(), Railpack: os.Getenv("BOHUX_TEST_RAILPACK_IMAGE"),
		RailpackNetwork: os.Getenv("BOHUX_TEST_RAILPACK_NETWORK")}
	req := func(release, sub string, strategy bohuxv1.BuildStrategy) *agentv1.BuildRequest {
		return &agentv1.BuildRequest{ReleaseId: release, AppId: "app-rp", RepoUrl: repo, Ref: "v1", Subpath: sub,
			Strategy: strategy}
	}
	const (
		auto       = bohuxv1.BuildStrategy_BUILD_STRATEGY_UNSPECIFIED
		dockerfile = bohuxv1.BuildStrategy_BUILD_STRATEGY_DOCKERFILE
		railpack   = bohuxv1.BuildStrategy_BUILD_STRATEGY_RAILPACK
	)

	for _, tc := range []struct {
		name         string
		r            *agentv1.BuildRequest
		reason       string
		usedStrategy bohuxv1.BuildStrategy
	}{
		{"nothing to detect", req("rel-rp-1", "none", auto), BuildPlanFailed, railpack},
		{"dockerfile asked, none there", req("rel-rp-2", "none", dockerfile), BuildFailed, dockerfile},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := b.Build(ctx, tc.r)
			if res.GetFailure().GetReason() != tc.reason || res.GetStrategy() != tc.usedStrategy {
				t.Errorf("result = %v, want %s with %v", res, tc.reason, tc.usedStrategy)
			}
		})
	}

	if os.Getenv("BOHUX_TEST_RAILPACK_OFFLINE") != "" {
		t.Log("BOHUX_TEST_RAILPACK_OFFLINE set: skipping the full Railpack build")
		return
	}
	res := b.Build(ctx, req("rel-rp-3", "shell", auto))
	if res.GetFailure() != nil || res.GetStrategy() != railpack {
		t.Fatalf("build = %v, want a Railpack image", res)
	}
	t.Cleanup(func() { _ = exec.Command("docker", "rmi", "-f", res.GetImageId()).Run() })
	if label := dockerCLI(t, "image", "inspect", res.GetImageId(), "--format",
		`{{index .Config.Labels "io.bohux.release"}}`); label != "rel-rp-3" {
		t.Errorf("release label = %q, want rel-rp-3", label)
	}
	out, err := exec.Command("docker", "run", "--rm", res.GetImageId()).CombinedOutput()
	if err != nil || !strings.Contains(string(out), "hello from railpack") {
		t.Errorf("running the image = %q, %v", out, err)
	}
}
