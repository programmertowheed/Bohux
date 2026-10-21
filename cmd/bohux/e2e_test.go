//go:build integration && e2e

package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	bohuxv1 "github.com/bohumix/bohux/gen/bohux/v1"
	"github.com/bohumix/bohux/gen/bohux/v1/bohuxv1connect"
	"github.com/bohumix/bohux/internal/store"
	"github.com/bohumix/bohux/internal/store/fixture"
	"github.com/bohumix/bohux/internal/store/storetest"
)

// e2eDockerfile serves /healthz on 8080 unless BROKEN is set, and says which version
// answers.
const e2eDockerfile = `FROM busybox:1.36
ARG VERSION
ARG BROKEN
RUN mkdir -p /www && echo "$VERSION" > /www/version && if [ -z "$BROKEN" ]; then echo ok > /www/healthz; fi
CMD ["httpd", "-f", "-p", "8080", "-h", "/www"]
`

func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	return ln.Addr().String()
}

func freePort(t *testing.T) int32 {
	t.Helper()
	_, port, _ := strings.Cut(freeAddr(t), ":")
	var p int32
	if _, err := fmt.Sscan(port, &p); err != nil {
		t.Fatal(err)
	}
	return p
}

// buildImage builds the fixture and returns its local image ID.
func buildImage(t *testing.T, version string, broken bool) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte(e2eDockerfile), 0o644); err != nil {
		t.Fatal(err)
	}
	args := []string{"build", "-q", "--build-arg", "VERSION=" + version, "--label", "io.bohux.e2e=1"}
	if broken {
		args = append(args, "--build-arg", "BROKEN=1")
	}
	out, err := exec.Command("docker", append(args, dir)...).CombinedOutput()
	if err != nil {
		t.Fatalf("docker build: %v: %s", err, out)
	}
	id := strings.TrimSpace(string(out))
	t.Cleanup(func() { _ = exec.Command("docker", "rmi", "-f", id).Run() })
	return id
}

func get(url string) (string, error) {
	c := http.Client{Timeout: time.Second}
	resp, err := c.Get(url)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("status %d", resp.StatusCode)
	}
	return strings.TrimSpace(string(b)), err
}

func call[T any](token string, msg *T) *connect.Request[T] {
	r := connect.NewRequest(msg)
	r.Header().Set("Authorization", "Bearer "+token)
	return r
}

// TestDeployEndToEnd runs the real binary's modes in process: control plane, agent on
// the local Docker daemon, deploy workflow on a Temporal dev server. A healthy Release
// deploys, a second replaces it (recreate downtime measured, ADR-0020 item 5), and a
// Release whose health check never passes rolls back to it (rule 5).
func TestDeployEndToEnd(t *testing.T) {
	temporal := os.Getenv("BOHUX_TEST_TEMPORAL_ADDRESS")
	if temporal == "" {
		t.Skip("BOHUX_TEST_TEMPORAL_ADDRESS is not set")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	getenv := func(string) string { return "" }
	dsn := storetest.NewAppDSN(t)

	var out bytes.Buffer
	if err := runWith(ctx, []string{"admin", "bootstrap", "--database-url", dsn, "--slug", "e2e"}, &out, getenv, defaultDeps); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	token := lines[len(lines)-1]

	apiAddr, agentAddr := freeAddr(t), freeAddr(t)
	var wg sync.WaitGroup
	run := func(name string, args ...string) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := runWith(ctx, args, io.Discard, getenv, defaultDeps); err != nil && !errors.Is(err, context.Canceled) {
				t.Errorf("%s: %v", name, err)
			}
		}()
	}
	run("control plane", "controlplane", "--listen", apiAddr, "--agent-listen", agentAddr, "--database-url", dsn,
		"--data-dir", t.TempDir(), "--temporal-address", temporal, "--deploy-health-timeout", "45s")
	stateDir := t.TempDir()
	t.Cleanup(func() {
		cancel()
		wg.Wait()
		removeWorkloads(t, stateDir)
	})
	base := "http://" + apiAddr
	for i := 0; ; i++ {
		if _, err := get(base + "/healthz"); err == nil {
			break
		}
		if i > 100 {
			t.Fatal("control plane never became healthy")
		}
		time.Sleep(100 * time.Millisecond)
	}

	hc := http.DefaultClient
	nodes := bohuxv1connect.NewNodeServiceClient(hc, base)
	bt, err := nodes.CreateBootstrapToken(ctx, call(token, &bohuxv1.CreateBootstrapTokenRequest{NodeSlug: "e2e-node", IdempotencyKey: "bt"}))
	if err != nil {
		t.Fatal(err)
	}
	if err := runWith(ctx, []string{"agent", "register", "--state-dir", stateDir, "--control-plane", agentAddr,
		"--token", bt.Msg.GetToken(), "--ca-cert-sha256", bt.Msg.GetCaCertSha256()}, io.Discard, getenv, defaultDeps); err != nil {
		t.Fatal(err)
	}
	run("agent", "agent", "run", "--state-dir", stateDir)

	list, err := nodes.ListNodes(ctx, call(token, &bohuxv1.ListNodesRequest{}))
	if err != nil || len(list.Msg.GetNodes()) != 1 {
		t.Fatalf("ListNodes = %v, %v", list, err)
	}
	node := list.Msg.GetNodes()[0].GetMeta().GetId()
	projects := bohuxv1connect.NewProjectServiceClient(hc, base)
	pr, err := projects.CreateProject(ctx, call(token, &bohuxv1.CreateProjectRequest{Slug: "shop", IdempotencyKey: "p"}))
	if err != nil {
		t.Fatal(err)
	}
	envs := bohuxv1connect.NewEnvironmentServiceClient(hc, base)
	er, err := envs.CreateEnvironment(ctx, call(token, &bohuxv1.CreateEnvironmentRequest{
		ProjectId: pr.Msg.GetProject().GetMeta().GetId(), Slug: "prod", IdempotencyKey: "e",
		ProviderBinding: &bohuxv1.ProviderBinding{Provider: "docker", Target: &bohuxv1.ProviderBinding_NodeId{NodeId: node}}}))
	if err != nil {
		t.Fatal(err)
	}
	env := er.Msg.GetEnvironment().GetMeta().GetId()
	hostPort := freePort(t)
	apps := bohuxv1connect.NewAppServiceClient(hc, base)
	ar, err := apps.CreateApp(ctx, call(token, &bohuxv1.CreateAppRequest{EnvironmentId: env, Slug: "web", IdempotencyKey: "a",
		// The source is not used: Releases below are built images (ADR-0020 item 2).
		Source: &bohuxv1.Source{Kind: &bohuxv1.Source_Image{Image: &bohuxv1.ImageSource{
			Repository: "example.invalid/web", Digest: "sha256:" + strings.Repeat("0", 64)}}},
		Runtime: &bohuxv1.Runtime{Replicas: 1, Ports: []*bohuxv1.Port{{ContainerPort: 8080, HostPort: hostPort}},
			Health: &bohuxv1.HealthCheck{Path: "/healthz", Port: 8080, TimeoutSeconds: 2}}}))
	if err != nil {
		t.Fatal(err)
	}
	app := ar.Msg.GetApp().GetMeta().GetId()

	st, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	tenant := uuid.MustParse(ar.Msg.GetApp().GetMeta().GetTenantId())
	release := func(version string, broken bool) string {
		return fixture.BuiltRelease(t, st, tenant, uuid.MustParse(app), buildImage(t, version, broken), "commit-"+version).String()
	}
	v1, v2, bad := release("v1", false), release("v2", false), release("bad", true)

	deployments := bohuxv1connect.NewDeploymentServiceClient(hc, base)
	deploy := func(rel, key string, want bohuxv1.DeploymentState) *bohuxv1.Deployment {
		t.Helper()
		r, err := deployments.CreateDeployment(ctx, call(token, &bohuxv1.CreateDeploymentRequest{
			ReleaseId: rel, EnvironmentId: env, IdempotencyKey: key}))
		if err != nil {
			t.Fatalf("deploy %s: %v", key, err)
		}
		id := r.Msg.GetDeployment().GetMeta().GetId()
		deadline := time.Now().Add(3 * time.Minute)
		for {
			g, err := deployments.GetDeployment(ctx, call(token, &bohuxv1.GetDeploymentRequest{Id: id}))
			if err != nil {
				t.Fatal(err)
			}
			d := g.Msg.GetDeployment()
			if d.GetState() == want {
				return d
			}
			if terminal(d) || time.Now().After(deadline) {
				t.Fatalf("deploy %s ended %s (failure %v), want %s", key, d.GetState(), d.GetFailure(), want)
			}
			time.Sleep(200 * time.Millisecond)
		}
	}
	served := fmt.Sprintf("http://127.0.0.1:%d/version", hostPort)

	start := time.Now()
	deploy(v1, "d1", bohuxv1.DeploymentState_DEPLOYMENT_STATE_SUCCEEDED)
	t.Logf("first deploy healthy after %v", time.Since(start).Round(time.Millisecond))
	if got, err := get(served); got != "v1" {
		t.Fatalf("serving %q (%v), want v1", got, err)
	}

	// Probe the host port through the recreate to measure its downtime (ADR-0020 item 5).
	probeCtx, stopProbe := context.WithCancel(ctx)
	downtime := make(chan time.Duration, 1)
	go func() {
		var worst, cur time.Duration
		var downSince time.Time
		for probeCtx.Err() == nil {
			_, err := get(served)
			switch {
			case err != nil && downSince.IsZero():
				downSince = time.Now()
			case err == nil && !downSince.IsZero():
				cur, downSince = time.Since(downSince), time.Time{}
				worst = max(worst, cur)
			}
			time.Sleep(20 * time.Millisecond)
		}
		downtime <- worst
	}()
	start = time.Now()
	deploy(v2, "d2", bohuxv1.DeploymentState_DEPLOYMENT_STATE_SUCCEEDED)
	stopProbe()
	t.Logf("second deploy healthy after %v; host port down for %v during the recreate",
		time.Since(start).Round(time.Millisecond), (<-downtime).Round(time.Millisecond))
	if got, err := get(served); got != "v2" {
		t.Fatalf("serving %q (%v), want v2", got, err)
	}

	start = time.Now()
	d := deploy(bad, "d3", bohuxv1.DeploymentState_DEPLOYMENT_STATE_ROLLED_BACK)
	t.Logf("broken release rolled back after %v (%s)", time.Since(start).Round(time.Millisecond), d.GetFailure().GetReason())
	if got, err := get(served); got != "v2" {
		t.Fatalf("after rollback serving %q (%v), want v2", got, err)
	}
}

func terminal(d *bohuxv1.Deployment) bool {
	switch d.GetState() {
	case bohuxv1.DeploymentState_DEPLOYMENT_STATE_SUCCEEDED, bohuxv1.DeploymentState_DEPLOYMENT_STATE_FAILED,
		bohuxv1.DeploymentState_DEPLOYMENT_STATE_ROLLED_BACK, bohuxv1.DeploymentState_DEPLOYMENT_STATE_CANCELLED:
		return true
	}
	return false
}

// removeWorkloads stops what the agent left running: every Compose artifact it wrote
// is brought down (rule 2 makes that possible without Bohux).
func removeWorkloads(t *testing.T, stateDir string) {
	_ = filepath.WalkDir(filepath.Join(stateDir, "workloads"), func(path string, e os.DirEntry, err error) error {
		if err == nil && e.Name() == "compose.yaml" {
			if out, err := exec.Command("docker", "compose", "-f", path, "down", "-v", "--remove-orphans").CombinedOutput(); err != nil {
				t.Logf("compose down %s: %v: %s", path, err, out)
			}
		}
		return nil
	})
}
