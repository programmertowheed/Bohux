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
	"filippo.io/age"
	"github.com/google/uuid"

	bohuxv1 "github.com/bohumix/bohux/gen/bohux/v1"
	"github.com/bohumix/bohux/gen/bohux/v1/bohuxv1connect"
	"github.com/bohumix/bohux/internal/cli"
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
CMD ["sh", "-c", "echo started $(cat /www/version); exec httpd -f -p 8080 -h /www"]
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

// stack is the real binary's modes run in process: bootstrap, control plane, and an
// agent on the local Docker daemon, with deploys on a Temporal dev server.
type stack struct {
	t      *testing.T
	ctx    context.Context
	token  string
	base   string
	env    string
	tenant uuid.UUID
	st     *store.Store
	hc     *http.Client

	dsn, dataDir, apiAddr, agentAddr, temporal string
	// stopCP stops the running control plane and waits for it.
	stopCP func()
}

// controlPlane starts `bohux controlplane` on the stack's addresses with dsn and
// dataDir, and waits until it answers.
func (s *stack) controlPlane(dsn, dataDir string) {
	s.t.Helper()
	ctx, cancel := context.WithCancel(s.ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		err := runWith(ctx, []string{"controlplane", "--listen", s.apiAddr, "--agent-listen", s.agentAddr,
			"--database-url", dsn, "--data-dir", dataDir, "--temporal-address", s.temporal,
			"--deploy-health-timeout", "45s"}, io.Discard, func(string) string { return "" }, defaultDeps)
		if err != nil && !errors.Is(err, context.Canceled) {
			s.t.Errorf("control plane: %v", err)
		}
	}()
	s.dsn, s.dataDir = dsn, dataDir
	s.stopCP = func() { cancel(); <-done }
	for i := 0; ; i++ {
		if _, err := get("http://" + s.apiAddr + "/healthz"); err == nil {
			return
		}
		if i > 100 {
			s.t.Fatal("control plane never became healthy")
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func startStack(t *testing.T) *stack {
	t.Helper()
	temporal := os.Getenv("BOHUX_TEST_TEMPORAL_ADDRESS")
	if temporal == "" {
		t.Skip("BOHUX_TEST_TEMPORAL_ADDRESS is not set")
	}
	ctx, cancel := context.WithCancel(context.Background())
	getenv := func(string) string { return "" }
	dsn := storetest.NewAppDSN(t)

	var out bytes.Buffer
	if err := runWith(ctx, []string{"admin", "bootstrap", "--database-url", dsn, "--slug", "e2e"}, &out, getenv, defaultDeps); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	token := lines[len(lines)-1]

	s := &stack{t: t, ctx: ctx, apiAddr: freeAddr(t), agentAddr: freeAddr(t), temporal: temporal}
	agentAddr := s.agentAddr
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
	s.controlPlane(dsn, t.TempDir())
	stateDir := t.TempDir()
	t.Cleanup(func() {
		cancel()
		s.stopCP()
		wg.Wait()
		removeWorkloads(t, stateDir)
	})
	base := "http://" + s.apiAddr

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
	node := list.Msg.GetNodes()[0]
	projects := bohuxv1connect.NewProjectServiceClient(hc, base)
	pr, err := projects.CreateProject(ctx, call(token, &bohuxv1.CreateProjectRequest{Slug: "shop", IdempotencyKey: "p"}))
	if err != nil {
		t.Fatal(err)
	}
	envs := bohuxv1connect.NewEnvironmentServiceClient(hc, base)
	er, err := envs.CreateEnvironment(ctx, call(token, &bohuxv1.CreateEnvironmentRequest{
		ProjectId: pr.Msg.GetProject().GetMeta().GetId(), Slug: "prod", IdempotencyKey: "e",
		ProviderBinding: &bohuxv1.ProviderBinding{Provider: "docker", Target: &bohuxv1.ProviderBinding_NodeId{NodeId: node.GetMeta().GetId()}}}))
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	s.token, s.base, s.env, s.st, s.hc = token, base, er.Msg.GetEnvironment().GetMeta().GetId(), st, hc
	s.tenant = uuid.MustParse(node.GetMeta().GetTenantId())
	return s
}

// app creates an App serving /healthz on 8080, published on hostPort.
func (s *stack) app(slug string, src *bohuxv1.Source, hostPort int32) string {
	s.t.Helper()
	apps := bohuxv1connect.NewAppServiceClient(s.hc, s.base)
	ar, err := apps.CreateApp(s.ctx, call(s.token, &bohuxv1.CreateAppRequest{EnvironmentId: s.env, Slug: slug,
		IdempotencyKey: "app-" + slug, Source: src,
		Runtime: &bohuxv1.Runtime{Replicas: 1, Ports: []*bohuxv1.Port{{ContainerPort: 8080, HostPort: hostPort}},
			Health: &bohuxv1.HealthCheck{Path: "/healthz", Port: 8080, TimeoutSeconds: 2}}}))
	if err != nil {
		s.t.Fatal(err)
	}
	return ar.Msg.GetApp().GetMeta().GetId()
}

// deploy deploys rel and waits for want.
func (s *stack) deploy(rel, key string, want bohuxv1.DeploymentState) *bohuxv1.Deployment {
	s.t.Helper()
	deployments := bohuxv1connect.NewDeploymentServiceClient(s.hc, s.base)
	r, err := deployments.CreateDeployment(s.ctx, call(s.token, &bohuxv1.CreateDeploymentRequest{
		ReleaseId: rel, EnvironmentId: s.env, IdempotencyKey: key}))
	if err != nil {
		s.t.Fatalf("deploy %s: %v", key, err)
	}
	id := r.Msg.GetDeployment().GetMeta().GetId()
	deadline := time.Now().Add(4 * time.Minute)
	for {
		g, err := deployments.GetDeployment(s.ctx, call(s.token, &bohuxv1.GetDeploymentRequest{Id: id}))
		if err != nil {
			s.t.Fatal(err)
		}
		d := g.Msg.GetDeployment()
		if d.GetState() == want {
			return d
		}
		if terminal(d) || time.Now().After(deadline) {
			s.t.Fatalf("deploy %s ended %s (failure %v), want %s", key, d.GetState(), d.GetFailure(), want)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// TestDeployEndToEnd: a healthy Release deploys, a second replaces it (recreate
// downtime measured, ADR-0020 item 5), and a Release whose health check never passes
// rolls back to it (rule 5).
func TestDeployEndToEnd(t *testing.T) {
	s := startStack(t)
	hostPort := freePort(t)
	// The source is not used: these Releases are built images (ADR-0020 item 2).
	app := s.app("web", &bohuxv1.Source{Kind: &bohuxv1.Source_Image{Image: &bohuxv1.ImageSource{
		Repository: "example.invalid/web", Digest: "sha256:" + strings.Repeat("0", 64)}}}, hostPort)
	release := func(version string, broken bool) string {
		return fixture.BuiltRelease(t, s.st, s.tenant, uuid.MustParse(app), buildImage(t, version, broken), "commit-"+version).String()
	}
	v1, v2, bad := release("v1", false), release("v2", false), release("bad", true)
	deploy := s.deploy
	ctx := s.ctx
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

// TestBuildEndToEnd drives the CLI: Releases built from a Git repository by the real
// agent with buildx (ADR-0028) are deployed, a broken one rolls back on its own, a
// rollback goes back to the last healthy Release, and scale reaches the node.
func TestBuildEndToEnd(t *testing.T) {
	s := startStack(t)
	repo := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", repo, "-c", "user.name=t", "-c", "user.email=t@example.com"}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	commit := func(version string, broken bool, tag string) {
		t.Helper()
		df := strings.Replace(e2eDockerfile, "ARG VERSION\nARG BROKEN\n", "", 1)
		df = strings.ReplaceAll(df, "$VERSION", version)
		if broken {
			df = strings.Replace(df, `if [ -z "$BROKEN" ]`, `if false`, 1)
		} else {
			df = strings.Replace(df, `if [ -z "$BROKEN" ]`, `if true`, 1)
		}
		if err := os.WriteFile(filepath.Join(repo, "Dockerfile"), []byte(df), 0o644); err != nil {
			t.Fatal(err)
		}
		git("add", ".")
		git("commit", "-q", "-m", version)
		git("tag", tag)
	}
	git("init", "-q")
	commit("g1", false, "v1")
	commit("g2", true, "v2")

	commit("g3", false, "v3")

	hostPort := freePort(t)
	app := s.app("built", &bohuxv1.Source{Kind: &bohuxv1.Source_Git{Git: &bohuxv1.GitSource{RepoUrl: repo, Ref: "v1"}}}, hostPort)
	t.Cleanup(func() {
		_ = exec.Command("sh", "-c", "docker images -q --filter label=io.bohux.app="+app+" | xargs -r docker rmi -f").Run()
	})
	served := fmt.Sprintf("http://127.0.0.1:%d/version", hostPort)
	// The CLI, as a user runs it (rule 8: nothing here is more than public RPCs).
	bohux := func(args ...string) (string, error) {
		var out bytes.Buffer
		env := map[string]string{"BOHUX_API_URL": s.base, "BOHUX_TOKEN": s.token, "BOHUX_PROJECT": "shop"}
		err := runWith(s.ctx, args, &out, func(k string) string { return env[k] }, defaultDeps)
		t.Logf("bohux %s\n%s", strings.Join(args, " "), out.String())
		return out.String(), err
	}
	serving := func(want string) {
		t.Helper()
		if got, err := get(served); got != want {
			t.Fatalf("serving %q (%v), want %q", got, err, want)
		}
	}

	start := time.Now()
	out, err := bohux("deploy", "built", "-e", "prod", "--ref", "v1")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("built and deployed from git in %v", time.Since(start).Round(time.Millisecond))
	serving("g1")
	if !strings.Contains(out, "publishes host port") || !strings.Contains(out, "succeeded") {
		t.Errorf("deploy output lacks the downtime note or the result:\n%s", out)
	}

	if _, err := bohux("deploy", "built", "-e", "prod", "--ref", "v2"); err == nil ||
		!strings.Contains(err.Error(), "rolled_back") {
		t.Fatalf("deploying the broken build = %v, want it rolled back", err)
	}
	serving("g1")

	if _, err := bohux("deploy", "built", "-e", "prod", "--ref", "v3"); err != nil {
		t.Fatal(err)
	}
	serving("g3")
	if _, err := bohux("rollback", "built", "-e", "prod", "--reason", "e2e"); err != nil {
		t.Fatal(err)
	}
	serving("g1")

	if _, err := bohux("scale", "built", "-e", "prod", "--replicas", "0"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { _, err := get(served); return err != nil }, "scaled to 0, the port to close")
	if _, err := bohux("scale", "built", "-e", "prod", "--replicas", "1"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { got, _ := get(served); return got == "g1" }, "scaled to 1, g1 to serve again")

	out, err = bohux("logs", "built", "-e", "prod", "--tail", "20")
	if err != nil || !strings.Contains(out, "started g1") {
		t.Errorf("logs = %q, %v; want the workload's start line", out, err)
	}
	out, err = bohux("exec", "built", "-e", "prod", "--", "cat", "/www/version")
	if err != nil || strings.TrimSpace(out) != "g1" {
		t.Errorf("exec = %q, %v; want g1", out, err)
	}
	_, err = bohux("exec", "built", "-e", "prod", "--", "sh", "-c", "exit 3")
	var exit *cli.ExitError
	if !errors.As(err, &exit) || exit.Code != 3 {
		t.Errorf("exec of a failing command = %v, want exit status 3", err)
	}
	// Every exec session is audited (rule 10); its input and output are not.
	var sessions int
	if err := s.st.InTenant(s.ctx, s.tenant, func(tx *store.Tx) error {
		return tx.QueryRow(s.ctx, `SELECT count(*) FROM audit_events
			WHERE action = '/bohux.v1.WorkloadService/Exec' AND resource_id = $1`, uuid.MustParse(app)).Scan(&sessions)
	}); err != nil || sessions != 2 {
		t.Errorf("audited exec sessions = %d, %v; want 2", sessions, err)
	}
}

func waitFor(t *testing.T, ok func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(time.Minute)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(200 * time.Millisecond)
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

// TestRestoreDrill is ADR-0020 item 7's drill: deploy two Releases, back up, wipe the
// control plane (new database, new data directory), restore, and roll back with the
// CLI against the restored state. The agent never re-registers: the restored node CA
// still vouches for it.
func TestRestoreDrill(t *testing.T) {
	s := startStack(t)
	hostPort := freePort(t)
	app := s.app("web", &bohuxv1.Source{Kind: &bohuxv1.Source_Image{Image: &bohuxv1.ImageSource{
		Repository: "example.invalid/web", Digest: "sha256:" + strings.Repeat("0", 64)}}}, hostPort)
	release := func(version string) string {
		return fixture.BuiltRelease(t, s.st, s.tenant, uuid.MustParse(app), buildImage(t, version, false), "commit-"+version).String()
	}
	v1, v2 := release("v1"), release("v2")
	s.deploy(v1, "d1", bohuxv1.DeploymentState_DEPLOYMENT_STATE_SUCCEEDED)
	s.deploy(v2, "d2", bohuxv1.DeploymentState_DEPLOYMENT_STATE_SUCCEEDED)
	served := fmt.Sprintf("http://127.0.0.1:%d/version", hostPort)
	if got, err := get(served); got != "v2" {
		t.Fatalf("serving %q (%v), want v2", got, err)
	}

	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	idFile := filepath.Join(t.TempDir(), "backup.key")
	if err := os.WriteFile(idFile, []byte(identity.String()+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(t.TempDir(), "bohux.backup")
	admin := func(args ...string) {
		t.Helper()
		var out bytes.Buffer
		if err := runWith(s.ctx, append([]string{"admin"}, args...), &out, func(string) string { return "" }, defaultDeps); err != nil {
			t.Fatalf("bohux admin %s: %v", args[0], err)
		}
		t.Logf("bohux admin %s: %s", args[0], strings.TrimSpace(out.String()))
	}
	admin("backup", "--admin-database-url", storetest.AdminDSNFor(t, s.dsn), "--data-dir", s.dataDir,
		"--recipient", identity.Recipient().String(), "--out", archive)

	// Wipe: the control plane goes away with its database and data directory; the
	// node keeps serving v2 meanwhile (rule 1).
	s.stopCP()
	if got, err := get(served); got != "v2" {
		t.Fatalf("with the control plane gone, serving %q (%v), want v2", got, err)
	}
	newDSN, newDir := storetest.NewAppDSN(t), t.TempDir()
	restored := time.Now()
	admin("restore", "--admin-database-url", storetest.AdminDSNFor(t, newDSN), "--database-url", newDSN,
		"--data-dir", newDir, "--identity", idFile, "--in", archive)
	s.controlPlane(newDSN, newDir)

	nodes := bohuxv1connect.NewNodeServiceClient(s.hc, s.base)
	// The restored rows still say "online"; a report newer than the restore is the
	// agent actually back.
	waitFor(t, func() bool {
		r, err := nodes.ListNodes(s.ctx, call(s.token, &bohuxv1.ListNodesRequest{}))
		return err == nil && len(r.Msg.GetNodes()) == 1 &&
			r.Msg.GetNodes()[0].GetLastSeenAt().AsTime().After(restored)
	}, "the agent to reconnect to the restored control plane")

	var out bytes.Buffer
	env := map[string]string{"BOHUX_API_URL": s.base, "BOHUX_TOKEN": s.token, "BOHUX_PROJECT": "shop"}
	if err := runWith(s.ctx, []string{"rollback", "web", "-e", "prod"}, &out, func(k string) string { return env[k] }, defaultDeps); err != nil {
		t.Fatalf("rollback after restore: %v\n%s", err, out.String())
	}
	t.Logf("bohux rollback after restore:\n%s", out.String())
	if got, err := get(served); got != "v1" {
		t.Fatalf("after rollback on the restored control plane, serving %q (%v), want v1", got, err)
	}
}
