//go:build integration

package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"go.temporal.io/sdk/client"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"

	agentv1 "github.com/bohumix/bohux/gen/bohux/agent/v1"
	bohuxv1 "github.com/bohumix/bohux/gen/bohux/v1"
	"github.com/bohumix/bohux/gen/bohux/v1/bohuxv1connect"
	"github.com/bohumix/bohux/internal/agentapi"
	"github.com/bohumix/bohux/internal/auth"
	"github.com/bohumix/bohux/internal/store"
	"github.com/bohumix/bohux/internal/workflow/deploy"
)

// slowBuilder plays the agent's builder: it takes a moment, then succeeds.
type slowBuilder struct{}

func (slowBuilder) Build(ctx context.Context, _, _ uuid.UUID, req *agentv1.BuildRequest) (*agentv1.BuildResult, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(300 * time.Millisecond):
	}
	return &agentv1.BuildResult{ReleaseId: req.GetReleaseId(), ImageId: "sha256:" + strings.Repeat("c", 64),
		Commit: "c0ffee" + req.GetRef()}, nil
}

// temporalEnv names a Temporal frontend for tests that run real workflows.
const temporalEnv = "BOHUX_TEST_TEMPORAL_ADDRESS"

// deployHarness is an API server whose deploys run on a real Temporal, with a worker
// on a queue of its own, and a node whose agent the test plays by writing reports.
type deployHarness struct {
	*harness
	deployments bohuxv1connect.DeploymentServiceClient
	releases    bohuxv1connect.ReleaseServiceClient
	node        uuid.UUID
	env         string
	app         string
	token       string
}

func newDeployHarness(t *testing.T) *deployHarness {
	t.Helper()
	addr := os.Getenv(temporalEnv)
	if addr == "" {
		t.Skipf("%s is not set; deploy workflow tests need a Temporal dev server", temporalEnv)
	}
	ctx, cancel := context.WithCancel(context.Background())
	h := newHarness(t)
	c, err := client.Dial(client.Options{HostPort: addr, Namespace: "default"})
	if err != nil {
		t.Fatalf("dial Temporal: %v", err)
	}
	queue := "bohux-deploy-test-" + uuid.NewString()
	done := make(chan struct{})
	go func() {
		defer close(done)
		deploy.RunWorker(ctx, c, queue, &deploy.Activities{Store: h.st,
			Agents: &agentapi.Server{Store: h.st}, Builds: slowBuilder{}, PollInterval: 20 * time.Millisecond})
	}()
	t.Cleanup(func() { cancel(); <-done; c.Close() })

	mux := http.NewServeMux()
	(&Server{Store: h.st, Authn: &auth.Authenticator{Store: h.st}, PKI: h.pki,
		Deploys: &deploy.Starter{Client: c, TaskQueue: queue, HealthTimeout: 20 * time.Second}}).Mount(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	d := &deployHarness{harness: h,
		deployments: bohuxv1connect.NewDeploymentServiceClient(srv.Client(), srv.URL),
		releases:    bohuxv1connect.NewReleaseServiceClient(srv.Client(), srv.URL),
		node:        h.node("vps-1"), token: h.root}
	envs := bohuxv1connect.NewEnvironmentServiceClient(srv.Client(), srv.URL)
	apps := bohuxv1connect.NewAppServiceClient(srv.Client(), srv.URL)
	project := h.project("one")
	e, err := envs.CreateEnvironment(context.Background(), req(h.root, &bohuxv1.CreateEnvironmentRequest{
		ProjectId: project.String(), Slug: "prod", ProviderBinding: dockerOn(d.node), IdempotencyKey: "e1"}))
	if err != nil {
		t.Fatal(err)
	}
	d.env = e.Msg.GetEnvironment().GetMeta().GetId()
	a, err := apps.CreateApp(context.Background(), req(h.root, &bohuxv1.CreateAppRequest{
		EnvironmentId: d.env, Slug: "api", IdempotencyKey: "a1",
		Source: &bohuxv1.Source{Kind: &bohuxv1.Source_Image{Image: &bohuxv1.ImageSource{
			Repository: "ghcr.io/acme/api", Digest: "sha256:" + strings.Repeat("ab", 32)}}},
		Runtime: &bohuxv1.Runtime{Replicas: 1}}))
	if err != nil {
		t.Fatal(err)
	}
	d.app = a.Msg.GetApp().GetMeta().GetId()
	return d
}

func (d *deployHarness) release(key string) string {
	d.t.Helper()
	r, err := d.releases.CreateRelease(context.Background(), req(d.token, &bohuxv1.CreateReleaseRequest{
		AppId: d.app, IdempotencyKey: key}))
	if err != nil {
		d.t.Fatal(err)
	}
	return r.Msg.GetRelease().GetMeta().GetId()
}

func (d *deployHarness) deploy(release, key string) (*bohuxv1.Deployment, error) {
	r, err := d.deployments.CreateDeployment(context.Background(), req(d.token, &bohuxv1.CreateDeploymentRequest{
		ReleaseId: release, EnvironmentId: d.env, IdempotencyKey: key}))
	if err != nil {
		return nil, err
	}
	return r.Msg.GetDeployment(), nil
}

// waitState polls the Deployment until it reaches want.
func (d *deployHarness) waitState(id string, want bohuxv1.DeploymentState) *bohuxv1.Deployment {
	d.t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		r, err := d.deployments.GetDeployment(context.Background(), req(d.token, &bohuxv1.GetDeploymentRequest{Id: id}))
		if err != nil {
			d.t.Fatal(err)
		}
		got := r.Msg.GetDeployment()
		if got.GetState() == want {
			return got
		}
		if time.Now().After(deadline) {
			d.t.Fatalf("deployment %s is %s, want %s", id, got.GetState(), want)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// latest returns the node's newest generation and the Release it assigns the App.
func (d *deployHarness) latest() (uint64, string) {
	d.t.Helper()
	ctx := context.Background()
	var (
		gen int64
		raw []byte
	)
	if err := d.st.InTenant(ctx, d.tenant, func(tx *store.Tx) error {
		return tx.QueryRow(ctx, `SELECT generation, payload FROM assignments WHERE node_id = $1
			ORDER BY generation DESC LIMIT 1`, d.node).Scan(&gen, &raw)
	}); err != nil {
		d.t.Fatal(err)
	}
	p := &agentv1.AssignmentPayload{}
	if err := proto.Unmarshal(raw, p); err != nil {
		d.t.Fatal(err)
	}
	for _, w := range p.GetWorkloads() {
		if w.GetAppId() == d.app {
			return uint64(gen), w.GetReleaseId()
		}
	}
	return uint64(gen), ""
}

// report plays the agent: once the latest generation assigns release, it reports it
// running in state.
func (d *deployHarness) report(release string, state agentv1.WorkloadState) {
	d.t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	gen, rel := d.latest()
	for rel != release {
		if time.Now().After(deadline) {
			d.t.Fatalf("release %s was never assigned (latest assigns %q)", release, rel)
		}
		time.Sleep(20 * time.Millisecond)
		gen, rel = d.latest()
	}
	ws := &agentv1.WorkloadStatus{AppId: d.app, ReleaseId: release, State: state, ReadyReplicas: 1}
	if state == failed {
		// A failure the agent cannot retry away ends the health gate at once.
		ws.Failure = &bohuxv1.ErrorDetail{Reason: "SPEC_INVALID"}
	}
	b, err := protojson.Marshal(&agentv1.StatusReport{Generation: gen, Workloads: []*agentv1.WorkloadStatus{ws}})
	if err != nil {
		d.t.Fatal(err)
	}
	ctx := context.Background()
	if err := d.st.InTenant(ctx, d.tenant, func(tx *store.Tx) error {
		return tx.Exec(ctx, `UPDATE nodes_heads SET report = $1, report_generation = $2 WHERE id = $3`,
			b, int64(gen), d.node)
	}); err != nil {
		d.t.Fatal(err)
	}
}

const (
	healthy = agentv1.WorkloadState_WORKLOAD_STATE_HEALTHY
	failed  = agentv1.WorkloadState_WORKLOAD_STATE_FAILED
)

// TestDeployWorkflow runs deploys end to end on a real Temporal: the rollback paths
// first (rule 5), then the forward path they protect.
func TestDeployWorkflow(t *testing.T) {
	ctx := context.Background()
	d := newDeployHarness(t)
	r1, r2 := d.release("r1"), d.release("r2")

	first, err := d.deploy(r1, "d1")
	if err != nil {
		t.Fatal(err)
	}
	if first.GetWorkflowId() != deploy.WorkflowID(d.app) {
		t.Errorf("workflow_id = %q, want %q", first.GetWorkflowId(), deploy.WorkflowID(d.app))
	}
	d.waitState(first.GetMeta().GetId(), bohuxv1.DeploymentState_DEPLOYMENT_STATE_HEALTH_GATE)
	d.report(r1, healthy)
	d.waitState(first.GetMeta().GetId(), bohuxv1.DeploymentState_DEPLOYMENT_STATE_SUCCEEDED)

	t.Run("a failed release rolls back to the last healthy one", func(t *testing.T) {
		second, err := d.deploy(r2, "d2")
		if err != nil {
			t.Fatal(err)
		}
		_, err = d.deploy(r2, "d2-again")
		wantCode(t, err, connect.CodeFailedPrecondition)

		d.waitState(second.GetMeta().GetId(), bohuxv1.DeploymentState_DEPLOYMENT_STATE_HEALTH_GATE)
		if _, rel := d.latest(); rel != r2 {
			t.Fatalf("assigned %s, want r2 during its health gate", rel)
		}
		d.report(r2, failed)
		d.waitState(second.GetMeta().GetId(), bohuxv1.DeploymentState_DEPLOYMENT_STATE_COMPENSATING)
		// report waits for compensation to assign r1 back.
		d.report(r1, healthy)
		got := d.waitState(second.GetMeta().GetId(), bohuxv1.DeploymentState_DEPLOYMENT_STATE_ROLLED_BACK)
		if got.GetFailure().GetReason() != deploy.ReasonWorkloadFailed || got.GetFinishedAt() == nil {
			t.Errorf("rolled back deployment = %v", got)
		}
	})

	t.Run("rollback with nothing earlier is refused", func(t *testing.T) {
		_, err := d.deployments.Rollback(ctx, req(d.token, &bohuxv1.RollbackRequest{AppId: d.app, IdempotencyKey: "rb0"}))
		wantCode(t, err, connect.CodeFailedPrecondition)
	})

	t.Run("cancel during the health gate restores the last healthy release", func(t *testing.T) {
		dep, err := d.deploy(r2, "d3")
		if err != nil {
			t.Fatal(err)
		}
		d.waitState(dep.GetMeta().GetId(), bohuxv1.DeploymentState_DEPLOYMENT_STATE_HEALTH_GATE)
		if _, err := d.deployments.CancelDeployment(ctx, req(d.token, &bohuxv1.CancelDeploymentRequest{
			Id: dep.GetMeta().GetId(), IdempotencyKey: "c3"})); err != nil {
			t.Fatal(err)
		}
		d.waitState(dep.GetMeta().GetId(), bohuxv1.DeploymentState_DEPLOYMENT_STATE_COMPENSATING)
		d.report(r1, healthy)
		d.waitState(dep.GetMeta().GetId(), bohuxv1.DeploymentState_DEPLOYMENT_STATE_CANCELLED)
		if _, rel := d.latest(); rel != r1 {
			t.Errorf("after cancel the node runs %s, want r1", rel)
		}
	})

	t.Run("deploy then roll back to the previous healthy release", func(t *testing.T) {
		dep, err := d.deploy(r2, "d4")
		if err != nil {
			t.Fatal(err)
		}
		d.waitState(dep.GetMeta().GetId(), bohuxv1.DeploymentState_DEPLOYMENT_STATE_HEALTH_GATE)
		d.report(r2, healthy)
		d.waitState(dep.GetMeta().GetId(), bohuxv1.DeploymentState_DEPLOYMENT_STATE_SUCCEEDED)

		rb, err := d.deployments.Rollback(ctx, req(d.token, &bohuxv1.RollbackRequest{AppId: d.app, IdempotencyKey: "rb1"}))
		if err != nil {
			t.Fatal(err)
		}
		back := rb.Msg.GetDeployment()
		if back.GetReleaseId() != r1 || back.GetRollbackOf() != dep.GetMeta().GetId() {
			t.Errorf("rollback = release %s rollback_of %s, want r1 and %s", back.GetReleaseId(), back.GetRollbackOf(), dep.GetMeta().GetId())
		}
		d.waitState(back.GetMeta().GetId(), bohuxv1.DeploymentState_DEPLOYMENT_STATE_HEALTH_GATE)
		d.report(r1, healthy)
		d.waitState(back.GetMeta().GetId(), bohuxv1.DeploymentState_DEPLOYMENT_STATE_SUCCEEDED)
	})

	t.Run("list, replay, watch", func(t *testing.T) {
		list, err := d.deployments.ListDeployments(ctx, req(d.token, &bohuxv1.ListDeploymentsRequest{
			EnvironmentId: d.env, AppId: d.app}))
		if err != nil {
			t.Fatal(err)
		}
		if n := len(list.Msg.GetDeployments()); n != 5 {
			t.Errorf("listed %d deployments, want 5", n)
		}
		if got := list.Msg.GetDeployments()[4].GetMeta().GetId(); got != first.GetMeta().GetId() {
			t.Errorf("oldest listed last = %s, want %s", got, first.GetMeta().GetId())
		}
		again, err := d.deploy(r1, "d1")
		if err != nil || again.GetMeta().GetId() != first.GetMeta().GetId() {
			t.Errorf("replay = %v, %v; want the first deployment", again, err)
		}
		stream, err := d.deployments.WatchDeployment(ctx, req(d.token, &bohuxv1.WatchDeploymentRequest{Id: first.GetMeta().GetId()}))
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for stream.Receive() {
			n++
			if s := stream.Msg().GetDeployment().GetState(); s != bohuxv1.DeploymentState_DEPLOYMENT_STATE_SUCCEEDED {
				t.Errorf("watched state %s", s)
			}
		}
		if err := stream.Err(); err != nil || n != 1 {
			t.Errorf("watch of a finished deployment: %d messages, %v; want 1 and a clean end", n, err)
		}
	})
}

// TestDeployRefusals needs no Temporal: everything here is refused before a run starts.
func TestDeployRefusals(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	srv := h.server()
	deployments := bohuxv1connect.NewDeploymentServiceClient(srv.Client(), srv.URL)
	_, err := deployments.CreateDeployment(ctx, req(h.root, &bohuxv1.CreateDeploymentRequest{
		ReleaseId: uuid.NewString(), EnvironmentId: uuid.NewString(), IdempotencyKey: "k"}))
	wantCode(t, err, connect.CodeNotFound)
	_, err = deployments.CreateDeployment(ctx, req(h.root, &bohuxv1.CreateDeploymentRequest{
		ReleaseId: "nope", EnvironmentId: uuid.NewString(), IdempotencyKey: "k"}))
	wantCode(t, err, connect.CodeInvalidArgument)
	_, err = deployments.GetDeployment(ctx, req(h.root, &bohuxv1.GetDeploymentRequest{Id: uuid.NewString()}))
	wantCode(t, err, connect.CodeNotFound)
}

// TestScaleAppAssigns checks ScaleApp writes the node's next generation with the
// running Release at the new scale, in the same transaction (ADR-0027).
func TestScaleAppAssigns(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	agents := &agentapi.Server{Store: h.st}
	mux := http.NewServeMux()
	(&Server{Store: h.st, Authn: &auth.Authenticator{Store: h.st}, PKI: h.pki, Agents: agents}).Mount(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	envs := bohuxv1connect.NewEnvironmentServiceClient(srv.Client(), srv.URL)
	apps := bohuxv1connect.NewAppServiceClient(srv.Client(), srv.URL)
	node := h.node("vps-1")
	e, err := envs.CreateEnvironment(ctx, req(h.root, &bohuxv1.CreateEnvironmentRequest{
		ProjectId: h.project("one").String(), Slug: "prod", ProviderBinding: dockerOn(node), IdempotencyKey: "e"}))
	if err != nil {
		t.Fatal(err)
	}
	a, err := apps.CreateApp(ctx, req(h.root, &bohuxv1.CreateAppRequest{
		EnvironmentId: e.Msg.GetEnvironment().GetMeta().GetId(), Slug: "api", IdempotencyKey: "a",
		Source: &bohuxv1.Source{Kind: &bohuxv1.Source_Image{Image: &bohuxv1.ImageSource{
			Repository: "ghcr.io/acme/api", Digest: "sha256:" + strings.Repeat("ab", 32)}}},
		Runtime: &bohuxv1.Runtime{Replicas: 1}}))
	if err != nil {
		t.Fatal(err)
	}
	app := a.Msg.GetApp()
	scale := func(replicas int32, expected, key string) *bohuxv1.App {
		t.Helper()
		r, err := apps.ScaleApp(ctx, req(h.root, &bohuxv1.ScaleAppRequest{Id: app.GetMeta().GetId(),
			Replicas: replicas, ExpectedRevisionId: expected, IdempotencyKey: key}))
		if err != nil {
			t.Fatal(err)
		}
		return r.Msg.GetApp()
	}
	latest := func() (int64, int32) {
		t.Helper()
		var (
			gen int64
			raw []byte
		)
		err := h.st.InTenant(ctx, h.tenant, func(tx *store.Tx) error {
			return tx.QueryRow(ctx, `SELECT generation, payload FROM assignments WHERE node_id = $1
				ORDER BY generation DESC LIMIT 1`, node).Scan(&gen, &raw)
		})
		if err != nil {
			return 0, 0
		}
		p := &agentv1.AssignmentPayload{}
		if err := proto.Unmarshal(raw, p); err != nil {
			t.Fatal(err)
		}
		return gen, p.GetWorkloads()[0].GetRuntime().GetReplicas()
	}

	// Not deployed yet: only the App changes.
	app = scale(2, app.GetMeta().GetRevisionId(), "s1")
	if gen, _ := latest(); gen != 0 {
		t.Errorf("scaling an undeployed app wrote generation %d", gen)
	}
	appID := uuid.MustParse(app.GetMeta().GetId())
	if _, err := agents.AssignApp(ctx, h.tenant, node, appID, &agentv1.Workload{AppId: appID.String(),
		ReleaseId: "r1", DeploymentId: "d1", Image: "img", Runtime: &bohuxv1.Runtime{Replicas: 2}}, time.Time{},
		store.Actor{ID: store.SystemActorID, Kind: store.ActorSystem}); err != nil {
		t.Fatal(err)
	}
	before := app.GetMeta().GetRevisionId()
	scale(4, before, "s2")
	if gen, replicas := latest(); gen != 2 || replicas != 4 {
		t.Errorf("after scale: generation %d with %d replicas, want 2 and 4", gen, replicas)
	}
	// A replay changes nothing more.
	scale(4, before, "s2")
	if gen, _ := latest(); gen != 2 {
		t.Errorf("replayed scale wrote generation %d", gen)
	}
}

// TestBuildThenDeploy creates a Release from Git, deploys it while it builds, and
// sees the deploy wait for the build (ADR-0028).
func TestBuildThenDeploy(t *testing.T) {
	ctx := context.Background()
	d := newDeployHarness(t)
	var app *bohuxv1.App
	if err := d.st.InTenant(ctx, d.tenant, func(tx *store.Tx) error {
		var err error
		app, err = getApp(ctx, tx, mustUUID(t, d.app), uuid.Nil, "")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	// Point the App at a Git source; the fake builder never fetches it.
	if err := d.st.InTenant(ctx, d.tenant, func(tx *store.Tx) error {
		_, err := tx.Update(ctx, "apps", mustUUID(t, d.app), mustUUID(t, app.GetMeta().GetRevisionId()), []store.Col{
			{Name: "source", Value: `{"git": {"repo_url": "https://example.com/api.git", "ref": "main"}}`},
			{Name: "runtime", Value: `{"replicas": 1}`}, {Name: "secrets", Value: `[]`}},
			store.Audit{Actor: store.Actor{ID: store.SystemActorID, Kind: store.ActorSystem}, Action: "test", ResourceType: appType})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	r, err := d.releases.CreateRelease(ctx, req(d.token, &bohuxv1.CreateReleaseRequest{AppId: d.app,
		From: &bohuxv1.CreateReleaseRequest_GitRef{GitRef: "v7"}, IdempotencyKey: "git1"}))
	if err != nil {
		t.Fatal(err)
	}
	rel := r.Msg.GetRelease()
	if rel.GetState() != bohuxv1.ReleaseState_RELEASE_STATE_BUILDING || rel.GetBuildWorkflowId() != deploy.BuildWorkflowID(rel.GetMeta().GetId()) {
		t.Fatalf("release = %v, want building with its build workflow", rel)
	}
	dep, err := d.deploy(rel.GetMeta().GetId(), "gd1")
	if err != nil {
		t.Fatal(err)
	}
	d.waitState(dep.GetMeta().GetId(), bohuxv1.DeploymentState_DEPLOYMENT_STATE_HEALTH_GATE)
	got, err := d.releases.GetRelease(ctx, req(d.token, &bohuxv1.GetReleaseRequest{Id: rel.GetMeta().GetId()}))
	if err != nil {
		t.Fatal(err)
	}
	if g := got.Msg.GetRelease(); g.GetState() != bohuxv1.ReleaseState_RELEASE_STATE_READY || g.GetSourceRef() != "c0ffeev7" ||
		!g.GetArtifactAvailable() {
		t.Errorf("after the build the release is %v", g)
	}
	d.report(rel.GetMeta().GetId(), healthy)
	d.waitState(dep.GetMeta().GetId(), bohuxv1.DeploymentState_DEPLOYMENT_STATE_SUCCEEDED)
}

// reportServed plays the agent for a routed App: release healthy and served by the
// ingress, on the latest generation, which must assign it with weight.
func (d *deployHarness) reportServed(release string, weight uint32) {
	d.t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		gen, w := d.latestWorkload()
		if w.GetReleaseId() == release && w.GetCanaryWeight() == weight {
			ws := &agentv1.WorkloadStatus{AppId: d.app, ReleaseId: release, State: healthy, ReadyReplicas: 1,
				IngressLoaded: true}
			b, err := protojson.Marshal(&agentv1.StatusReport{Generation: gen, Workloads: []*agentv1.WorkloadStatus{ws}})
			if err != nil {
				d.t.Fatal(err)
			}
			ctx := context.Background()
			if err := d.st.InTenant(ctx, d.tenant, func(tx *store.Tx) error {
				return tx.Exec(ctx, `UPDATE nodes_heads SET report = $1, report_generation = $2 WHERE id = $3`,
					b, int64(gen), d.node)
			}); err != nil {
				d.t.Fatal(err)
			}
			return
		}
		if time.Now().After(deadline) {
			d.t.Fatalf("release %s at weight %d was never assigned (latest %s at %d)", release, weight,
				w.GetReleaseId(), w.GetCanaryWeight())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// latestWorkload returns the node's newest generation and the App's workload in it;
// nil before anything is assigned.
func (d *deployHarness) latestWorkload() (uint64, *agentv1.Workload) {
	d.t.Helper()
	ctx := context.Background()
	var (
		gen int64
		raw []byte
	)
	err := d.st.InTenant(ctx, d.tenant, func(tx *store.Tx) error {
		return tx.QueryRow(ctx, `SELECT generation, payload FROM assignments WHERE node_id = $1
			ORDER BY generation DESC LIMIT 1`, d.node).Scan(&gen, &raw)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		d.t.Fatal(err)
	}
	p := &agentv1.AssignmentPayload{}
	if err := proto.Unmarshal(raw, p); err != nil {
		d.t.Fatal(err)
	}
	for _, w := range p.GetWorkloads() {
		if w.GetAppId() == d.app {
			return uint64(gen), w
		}
	}
	return uint64(gen), nil
}

// TestCanaryWorkflow runs canary deploys on a real Temporal (ADR-0049): refusals, a
// failing canary rolling back and a cancelled one aborting (rule 5), then promotion.
func TestCanaryWorkflow(t *testing.T) {
	ctx := context.Background()
	d := newDeployHarness(t)
	unrouted := d.app
	canary := func(release, key string, weight uint32, bake time.Duration) (*bohuxv1.Deployment, error) {
		r, err := d.deployments.CreateDeployment(ctx, req(d.token, &bohuxv1.CreateDeploymentRequest{
			ReleaseId: release, EnvironmentId: d.env, IdempotencyKey: key,
			Strategy: bohuxv1.DeploymentStrategy_DEPLOYMENT_STRATEGY_CANARY,
			Canary:   &bohuxv1.Canary{WeightPercent: weight, Bake: durationpb.New(bake)}}))
		if err != nil {
			return nil, err
		}
		return r.Msg.GetDeployment(), nil
	}
	promote := func(id, key string) error {
		_, err := d.deployments.PromoteDeployment(ctx, req(d.token, &bohuxv1.PromoteDeploymentRequest{
			Id: id, IdempotencyKey: key}))
		return err
	}

	t.Run("refusals", func(t *testing.T) {
		r := d.release("plain")
		_, err := canary(r, "c-unrouted", 10, time.Hour)
		wantCode(t, err, connect.CodeFailedPrecondition)
		for _, bad := range []struct {
			weight uint32
			bake   time.Duration
		}{{0, time.Hour}, {100, time.Hour}, {10, 30 * time.Second}, {10, 25 * time.Hour}, {10, 90500 * time.Millisecond}} {
			_, err := canary(r, fmt.Sprintf("c-bad-%d-%v", bad.weight, bad.bake), bad.weight, bad.bake)
			wantCode(t, err, connect.CodeInvalidArgument)
		}
		_, err = d.deployments.CreateDeployment(ctx, req(d.token, &bohuxv1.CreateDeploymentRequest{
			ReleaseId: r, EnvironmentId: d.env, IdempotencyKey: "c-nospec",
			Strategy: bohuxv1.DeploymentStrategy_DEPLOYMENT_STRATEGY_CANARY}))
		wantCode(t, err, connect.CodeInvalidArgument)
	})

	// A routed App without host ports switches slots, so it can have a canary.
	srv := d.server()
	apps := bohuxv1connect.NewAppServiceClient(srv.Client(), srv.URL)
	a, err := apps.CreateApp(ctx, req(d.token, &bohuxv1.CreateAppRequest{
		EnvironmentId: d.env, Slug: "web", IdempotencyKey: "a-web",
		Source: &bohuxv1.Source{Kind: &bohuxv1.Source_Image{Image: &bohuxv1.ImageSource{
			Repository: "ghcr.io/acme/web", Digest: "sha256:" + strings.Repeat("cd", 32)}}},
		Runtime: &bohuxv1.Runtime{Replicas: 1, Ports: []*bohuxv1.Port{{ContainerPort: 8080}}},
		Routes:  []*bohuxv1.Route{{Host: "web.example.com", Port: 8080}}}))
	if err != nil {
		t.Fatal(err)
	}
	d.app = a.Msg.GetApp().GetMeta().GetId()
	defer func() { d.app = unrouted }()
	r1, r2, r3, r4 := d.release("w1"), d.release("w2"), d.release("w3"), d.release("w4")
	first, err := d.deploy(r1, "w-d1")
	if err != nil {
		t.Fatal(err)
	}
	d.reportServed(r1, 0)
	d.waitState(first.GetMeta().GetId(), bohuxv1.DeploymentState_DEPLOYMENT_STATE_SUCCEEDED)
	if err := promote(first.GetMeta().GetId(), "p-done"); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Errorf("promoting a finished deployment = %v, want failed_precondition", err)
	}

	t.Run("a failing canary rolls back", func(t *testing.T) {
		dep, err := canary(r2, "w-c2", 10, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		if dep.GetStrategy() != bohuxv1.DeploymentStrategy_DEPLOYMENT_STRATEGY_CANARY ||
			dep.GetCanary().GetWeightPercent() != 10 || dep.GetCanary().GetBake().AsDuration() != time.Hour {
			t.Errorf("recorded %v / %v", dep.GetStrategy(), dep.GetCanary())
		}
		d.reportServed(r2, 10)
		d.waitState(dep.GetMeta().GetId(), bohuxv1.DeploymentState_DEPLOYMENT_STATE_CANARY)
		d.report(r2, failed)
		d.waitState(dep.GetMeta().GetId(), bohuxv1.DeploymentState_DEPLOYMENT_STATE_COMPENSATING)
		d.reportServed(r1, 0)
		got := d.waitState(dep.GetMeta().GetId(), bohuxv1.DeploymentState_DEPLOYMENT_STATE_ROLLED_BACK)
		if got.GetFailure().GetReason() != deploy.ReasonWorkloadFailed {
			t.Errorf("rolled back with %v", got.GetFailure())
		}
	})
	t.Run("cancelling a canary aborts it", func(t *testing.T) {
		dep, err := canary(r3, "w-c3", 25, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		d.reportServed(r3, 25)
		d.waitState(dep.GetMeta().GetId(), bohuxv1.DeploymentState_DEPLOYMENT_STATE_CANARY)
		if _, err := d.deployments.CancelDeployment(ctx, req(d.token, &bohuxv1.CancelDeploymentRequest{
			Id: dep.GetMeta().GetId(), IdempotencyKey: "w-x3"})); err != nil {
			t.Fatal(err)
		}
		d.reportServed(r1, 0)
		d.waitState(dep.GetMeta().GetId(), bohuxv1.DeploymentState_DEPLOYMENT_STATE_CANCELLED)
	})
	t.Run("promote ends the bake and the release takes all traffic", func(t *testing.T) {
		dep, err := canary(r4, "w-c4", 10, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		d.reportServed(r4, 10)
		d.waitState(dep.GetMeta().GetId(), bohuxv1.DeploymentState_DEPLOYMENT_STATE_CANARY)
		viewer, err := d.createToken(d.root, uuid.Nil, bohuxv1.Role_ROLE_VIEWER, "canary-viewer")
		if err != nil {
			t.Fatal(err)
		}
		_, err = d.deployments.PromoteDeployment(ctx, req(viewer.GetSecret(), &bohuxv1.PromoteDeploymentRequest{
			Id: dep.GetMeta().GetId(), IdempotencyKey: "p-viewer"}))
		wantCode(t, err, connect.CodePermissionDenied)
		if err := promote(dep.GetMeta().GetId(), "p4"); err != nil {
			t.Fatal(err)
		}
		d.reportServed(r4, 0)
		d.waitState(dep.GetMeta().GetId(), bohuxv1.DeploymentState_DEPLOYMENT_STATE_SUCCEEDED)
		var promoted int
		if err := d.st.InTenant(ctx, d.tenant, func(tx *store.Tx) error {
			return tx.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE action = $1`,
				bohuxv1connect.DeploymentServicePromoteDeploymentProcedure).Scan(&promoted)
		}); err != nil || promoted != 1 {
			t.Errorf("audited promotions = %d, %v; want 1", promoted, err)
		}
	})
}
