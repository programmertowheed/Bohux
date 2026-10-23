//go:build integration && e2e

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	bohuxv1 "github.com/bohumix/bohux/gen/bohux/v1"
	"github.com/bohumix/bohux/internal/store/fixture"
	"github.com/bohumix/bohux/providers/swarm"

	"github.com/google/uuid"
)

// swarmHostEnv names the DOCKER_HOST of a single-manager swarm. S21 left the Swarm
// provider conformance-tested and the routing unit-tested, but never ran the control
// plane and the agent against it together; this is that run.
//
// The daemon cannot be the one the Docker end-to-end tests use: a swarm manager cannot
// have live-restore (ADR-0045), and the agent requires it of a plain Docker node
// (ADR-0020). So the suites need separate daemons, and separate CI jobs.
const swarmHostEnv = "BOHUX_TEST_SWARM_HOST"

// swarmStack starts the stack with its environment bound to the Swarm provider, with
// every `docker` the test and the agent run pointed at the swarm manager.
func swarmStack(t *testing.T) *stack {
	t.Helper()
	host := os.Getenv(swarmHostEnv)
	if host == "" {
		t.Skip(swarmHostEnv + " is not set; it needs a single-manager swarm's DOCKER_HOST")
	}
	// The agent runs in this process and resolves its daemon from the environment,
	// so this selects the daemon for the agent, its providers and buildImage alike.
	t.Setenv("DOCKER_HOST", host)
	return startStackOn(t, swarm.Name)
}

// TestSwarmDeployRollsBackABrokenRelease is the failure path, and it comes first
// (CLAUDE.md rule 5): a Release that never turns healthy must leave the previous one
// serving, through the Swarm provider as through Docker.
func TestSwarmDeployRollsBackABrokenRelease(t *testing.T) {
	s := swarmStack(t)
	hostPort := freePort(t)
	app := s.app("web", &bohuxv1.Source{Kind: &bohuxv1.Source_Image{Image: &bohuxv1.ImageSource{
		Repository: "example.invalid/web", Digest: "sha256:" + strings.Repeat("0", 64)}}}, hostPort)
	release := func(version string, broken bool) string {
		return fixture.BuiltRelease(t, s.st, s.tenant, uuid.MustParse(app),
			buildImage(t, version, broken), "commit-"+version).String()
	}
	good, broken := release("v1", false), release("broken", true)

	s.deploy(good, "swarm-d1", bohuxv1.DeploymentState_DEPLOYMENT_STATE_SUCCEEDED)
	served := fmt.Sprintf("http://127.0.0.1:%d/version", hostPort)
	waitFor(t, func() bool { v, _ := get(served); return v == "v1" }, "the swarm service to serve v1")

	// The broken Release has no /healthz, so the health gate never opens and the
	// deploy compensates back to v1.
	s.deploy(broken, "swarm-d2", bohuxv1.DeploymentState_DEPLOYMENT_STATE_ROLLED_BACK)
	waitFor(t, func() bool { v, _ := get(served); return v == "v1" }, "v1 to serve again after the rollback")
}

// The forward path: deploy, serve, deploy again, and roll back with the CLI, all
// through one swarm manager.
func TestSwarmDeployAndRollback(t *testing.T) {
	s := swarmStack(t)
	hostPort := freePort(t)
	app := s.app("shop", &bohuxv1.Source{Kind: &bohuxv1.Source_Image{Image: &bohuxv1.ImageSource{
		Repository: "example.invalid/shop", Digest: "sha256:" + strings.Repeat("1", 64)}}}, hostPort)
	release := func(version string) string {
		return fixture.BuiltRelease(t, s.st, s.tenant, uuid.MustParse(app),
			buildImage(t, version, false), "commit-"+version).String()
	}
	v1, v2 := release("sv1"), release("sv2")

	s.deploy(v1, "swarm-f1", bohuxv1.DeploymentState_DEPLOYMENT_STATE_SUCCEEDED)
	served := fmt.Sprintf("http://127.0.0.1:%d/version", hostPort)
	waitFor(t, func() bool { v, _ := get(served); return v == "sv1" }, "the swarm service to serve sv1")

	s.deploy(v2, "swarm-f2", bohuxv1.DeploymentState_DEPLOYMENT_STATE_SUCCEEDED)
	waitFor(t, func() bool { v, _ := get(served); return v == "sv2" }, "the swarm service to serve sv2")

	// The provider wrote a stack file for the service, so a user who deletes Bohux is
	// left with something they can run themselves (rule 2).
	assertSwarmArtifacts(t, s)

	var out strings.Builder
	env := map[string]string{"BOHUX_API_URL": s.base, "BOHUX_TOKEN": s.token, "BOHUX_PROJECT": "shop"}
	if err := runWith(s.ctx, []string{"rollback", "shop", "-e", "prod"}, &out,
		func(k string) string { return env[k] }, defaultDeps); err != nil {
		t.Fatalf("rollback on swarm: %v\n%s", err, out.String())
	}
	t.Logf("bohux rollback on swarm:\n%s", out.String())
	waitFor(t, func() bool { v, _ := get(served); return v == "sv1" }, "sv1 to serve again after the rollback")
}

// assertSwarmArtifacts checks the no-lock-in rule for the Swarm provider (rule 2):
// a user who deletes Bohux is left with a readable stack file they can deploy
// themselves, and the secrets beside it are not world-readable (rule 3).
func assertSwarmArtifacts(t *testing.T, s *stack) {
	t.Helper()
	// The agent keeps each provider's artifacts under <state dir>/workloads/<provider>.
	root := filepath.Join(s.stateDir, "workloads", swarm.Name)
	var stacks []string
	err := filepath.WalkDir(root, func(path string, e os.DirEntry, err error) error {
		if err == nil && !e.IsDir() && e.Name() == "stack.yaml" {
			stacks = append(stacks, path)
		}
		return nil
	})
	if err != nil || len(stacks) == 0 {
		t.Errorf("the Swarm provider left no stack.yaml under %s (rule 2): %v", root, err)
		return
	}
	b, err := os.ReadFile(stacks[0])
	if err != nil || !strings.Contains(string(b), "services:") {
		t.Errorf("%s is not a readable Compose stack: %v", stacks[0], err)
	}
	t.Logf("swarm artifact %s:\n%s", stacks[0], b)

	// secrets.env is written beside it only when the App has secrets; when it is
	// there it must be 0600.
	if st, err := os.Stat(filepath.Join(filepath.Dir(stacks[0]), "secrets.env")); err == nil {
		if perm := st.Mode().Perm(); perm != 0o600 {
			t.Errorf("secrets.env is mode %v, want 0600 (rule 3)", perm)
		}
	}
}
