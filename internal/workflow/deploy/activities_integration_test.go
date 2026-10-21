//go:build integration

package deploy

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"go.temporal.io/sdk/testsuite"
	"google.golang.org/protobuf/proto"

	agentv1 "github.com/bohumix/bohux/gen/bohux/agent/v1"
	bohuxv1 "github.com/bohumix/bohux/gen/bohux/v1"
	"github.com/bohumix/bohux/internal/agentapi"
	"github.com/bohumix/bohux/internal/store"
	"github.com/bohumix/bohux/internal/store/fixture"
	"github.com/bohumix/bohux/internal/store/storetest"
)

type harness struct {
	t      *testing.T
	st     *store.Store
	tenant uuid.UUID
	node   uuid.UUID
	acts   *Activities
	env    *testsuite.TestActivityEnvironment
}

var testActor = Actor{ID: store.SystemActorID.String(), Kind: string(store.ActorSystem)}

func newHarness(t *testing.T) *harness {
	t.Helper()
	ctx := context.Background()
	dsn := storetest.NewAppDSN(t)
	if err := store.MigrateDSN(ctx, dsn); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	tenant := fixture.Tenant(t, st, "acme")
	acts := &Activities{Store: st, Agents: &agentapi.Server{Store: st}, PollInterval: 10 * time.Millisecond}
	var ts testsuite.WorkflowTestSuite
	env := ts.NewTestActivityEnvironment()
	env.RegisterActivity(acts)
	return &harness{t: t, st: st, tenant: tenant, node: fixture.Node(t, st, tenant, "vps-1"), acts: acts, env: env}
}

func (h *harness) input(app, env, release uuid.UUID) Input {
	return Input{
		TenantID: h.tenant.String(), DeploymentID: uuid.Must(uuid.NewV7()).String(),
		AppID: app.String(), EnvironmentID: env.String(), ReleaseID: release.String(), Actor: testActor,
	}
}

// deployment reads a Deployment's current state, failure reason, and revision count.
func (h *harness) deployment(id string) (state, reason string, revisions int, finished bool) {
	h.t.Helper()
	ctx := context.Background()
	err := h.st.InTenant(ctx, h.tenant, func(tx *store.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT r.state, COALESCE(r.failure->>'reason', ''), r.finished_at IS NOT NULL
			FROM deployments_heads h JOIN deployments_revisions r ON r.id = h.revision_id
			WHERE h.id = $1`, id).Scan(&state, &reason, &finished); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT count(*) FROM deployments_revisions WHERE resource_id = $1`, id).Scan(&revisions)
	})
	if err != nil {
		h.t.Fatalf("read deployment: %v", err)
	}
	return state, reason, revisions, finished
}

func (h *harness) audits(id string) int {
	h.t.Helper()
	var n int
	ctx := context.Background()
	if err := h.st.InTenant(ctx, h.tenant, func(tx *store.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE resource_id = $1`, id).Scan(&n)
	}); err != nil {
		h.t.Fatal(err)
	}
	return n
}

func (h *harness) set(id string, s State, f *Failure) {
	h.t.Helper()
	if _, err := h.env.ExecuteActivity(h.acts.SetState, StateChange{
		TenantID: h.tenant.String(), DeploymentID: id, State: s, Failure: f, Actor: testActor,
	}); err != nil {
		h.t.Fatalf("SetState(%s): %v", s, err)
	}
}

func (h *harness) report(r *agentv1.StatusReport) {
	h.t.Helper()
	b, err := storeJSON.Marshal(r)
	if err != nil {
		h.t.Fatal(err)
	}
	ctx := context.Background()
	if err := h.st.InTenant(ctx, h.tenant, func(tx *store.Tx) error {
		return tx.Exec(ctx, `UPDATE nodes_heads SET report = $1, report_generation = $2 WHERE id = $3`,
			b, int64(r.GetGeneration()), h.node)
	}); err != nil {
		h.t.Fatal(err)
	}
}

func TestRecordStart(t *testing.T) {
	h := newHarness(t)
	app, env := fixture.DeployableApp(t, h.st, h.tenant, h.node, "api", 1)
	ready := fixture.Release(t, h.st, h.tenant, app, "ready", true)

	in := h.input(app, env, ready)
	for i := 0; i < 2; i++ {
		if _, err := h.env.ExecuteActivity(h.acts.RecordStart, in); err != nil {
			t.Fatalf("RecordStart #%d: %v", i+1, err)
		}
	}
	if state, _, revs, _ := h.deployment(in.DeploymentID); state != "pending" || revs != 1 {
		t.Errorf("after two RecordStarts: state %s, %d revisions; want pending, 1", state, revs)
	}
	if n := h.audits(in.DeploymentID); n != 1 {
		t.Errorf("audit events = %d, want 1", n)
	}

	other, otherEnv := fixture.DeployableApp(t, h.st, h.tenant, h.node, "worker", 1)
	t.Run("a building release is reported, not refused", func(t *testing.T) {
		v, err := h.env.ExecuteActivity(h.acts.RecordStart, h.input(app, env, fixture.Release(t, h.st, h.tenant, app, "building", false)))
		if err != nil {
			t.Fatal(err)
		}
		var building bool
		if err := v.Get(&building); err != nil || !building {
			t.Errorf("building = %v, %v; want true", building, err)
		}
	})
	tests := []struct {
		name string
		in   Input
	}{
		{"failed", h.input(app, env, fixture.Release(t, h.st, h.tenant, app, "failed", false))},
		{"artifact gone", h.input(app, env, fixture.Release(t, h.st, h.tenant, app, "ready", false))},
		{"another app's release", h.input(app, env, fixture.Release(t, h.st, h.tenant, other, "ready", true))},
		{"wrong environment", h.input(app, otherEnv, ready)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := h.env.ExecuteActivity(h.acts.RecordStart, tt.in)
			if !isType(err, ErrTypeInvalid) {
				t.Fatalf("RecordStart = %v, want a non-retryable %s error", err, ErrTypeInvalid)
			}
			if state, _, _, _ := h.deployment(tt.in.DeploymentID); state != "pending" {
				t.Errorf("row state = %q, want the row written before refusing", state)
			}
		})
	}
	t.Run("unknown release", func(t *testing.T) {
		_, err := h.env.ExecuteActivity(h.acts.RecordStart, h.input(app, env, uuid.Must(uuid.NewV7())))
		if !isType(err, ErrTypeInvalid) {
			t.Fatalf("RecordStart = %v, want %s", err, ErrTypeInvalid)
		}
	})
}

func TestSetState(t *testing.T) {
	h := newHarness(t)
	app, env := fixture.DeployableApp(t, h.st, h.tenant, h.node, "api", 1)
	in := h.input(app, env, fixture.Release(t, h.st, h.tenant, app, "ready", true))
	if _, err := h.env.ExecuteActivity(h.acts.RecordStart, in); err != nil {
		t.Fatal(err)
	}
	h.set(in.DeploymentID, StateApplying, nil)
	h.set(in.DeploymentID, StateApplying, nil)
	if state, _, revs, finished := h.deployment(in.DeploymentID); state != "applying" || revs != 2 || finished {
		t.Errorf("applying twice: %s, %d revisions, finished %v; want applying, 2, false", state, revs, finished)
	}
	h.set(in.DeploymentID, StateCompensating, &Failure{Reason: ReasonHealthTimeout})
	h.set(in.DeploymentID, StateRolledBack, &Failure{Reason: ReasonHealthTimeout})
	state, reason, revs, finished := h.deployment(in.DeploymentID)
	if state != "rolled_back" || reason != ReasonHealthTimeout || revs != 4 || !finished {
		t.Errorf("rolled back: %s %q, %d revisions, finished %v", state, reason, revs, finished)
	}
	h.set(in.DeploymentID, StateSucceeded, nil)
	if state, _, revs, _ := h.deployment(in.DeploymentID); state != "rolled_back" || revs != 4 {
		t.Errorf("a terminal deployment changed: %s, %d revisions", state, revs)
	}
	if n := h.audits(in.DeploymentID); n != 4 {
		t.Errorf("audit events = %d, want 4 (one per revision)", n)
	}
	if _, err := h.env.ExecuteActivity(h.acts.SetState, StateChange{TenantID: h.tenant.String(),
		DeploymentID: uuid.Must(uuid.NewV7()).String(), State: StateFailed, Actor: testActor}); !isType(err, ErrTypeInvalid) {
		t.Errorf("unknown deployment = %v, want %s", err, ErrTypeInvalid)
	}
}

func TestLastHealthyRelease(t *testing.T) {
	h := newHarness(t)
	app, env := fixture.DeployableApp(t, h.st, h.tenant, h.node, "api", 1)
	last := func(exclude string) string {
		t.Helper()
		v, err := h.env.ExecuteActivity(h.acts.LastHealthyRelease, LastHealthyInput{
			TenantID: h.tenant.String(), AppID: app.String(), DeploymentID: exclude})
		if err != nil {
			t.Fatal(err)
		}
		var out string
		if err := v.Get(&out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	deploy := func(release uuid.UUID, final State) string {
		in := h.input(app, env, release)
		if _, err := h.env.ExecuteActivity(h.acts.RecordStart, in); err != nil {
			t.Fatal(err)
		}
		h.set(in.DeploymentID, final, nil)
		return in.DeploymentID
	}
	if got := last(uuid.Must(uuid.NewV7()).String()); got != "" {
		t.Errorf("no deployments: %q, want empty", got)
	}
	r1 := fixture.Release(t, h.st, h.tenant, app, "ready", true)
	r2 := fixture.Release(t, h.st, h.tenant, app, "ready", true)
	r3 := fixture.Release(t, h.st, h.tenant, app, "ready", true)
	deploy(r1, StateSucceeded)
	d2 := deploy(r2, StateSucceeded)
	deploy(r3, StateRolledBack)
	if got := last(uuid.Must(uuid.NewV7()).String()); got != r2.String() {
		t.Errorf("last healthy = %s, want r2 %s (r3 rolled back)", got, r2)
	}
	if got := last(d2); got != r1.String() {
		t.Errorf("excluding r2's deployment = %s, want r1 %s", got, r1)
	}
	// A rollback that succeeded makes its (older) Release the newest healthy one.
	deploy(r1, StateSucceeded)
	if got := last(uuid.Must(uuid.NewV7()).String()); got != r1.String() {
		t.Errorf("after a rollback to r1 = %s, want r1", got)
	}
}

func TestAssignRelease(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	app, _ := fixture.DeployableApp(t, h.st, h.tenant, h.node, "api", 2)
	release := fixture.Release(t, h.st, h.tenant, app, "ready", true)
	assign := func(release string) Assigned {
		t.Helper()
		v, err := h.env.ExecuteActivity(h.acts.AssignRelease, AssignInput{TenantID: h.tenant.String(),
			AppID: app.String(), ReleaseID: release, DeploymentID: "d-1", Actor: testActor})
		if err != nil {
			t.Fatalf("AssignRelease(%q): %v", release, err)
		}
		var out Assigned
		if err := v.Get(&out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	got := assign(release.String())
	if got.NodeID != h.node.String() || got.Generation != 1 || got.Replicas != 2 {
		t.Errorf("assigned = %+v, want node %s, generation 1, 2 replicas", got, h.node)
	}
	if again := assign(release.String()); again.Generation != 1 {
		t.Errorf("retry wrote generation %d, want 1", again.Generation)
	}

	var raw []byte
	if err := h.st.InTenant(ctx, h.tenant, func(tx *store.Tx) error {
		return tx.QueryRow(ctx, `SELECT payload FROM assignments WHERE node_id = $1 AND generation = 1`, h.node).Scan(&raw)
	}); err != nil {
		t.Fatal(err)
	}
	p := &agentv1.AssignmentPayload{}
	if err := proto.Unmarshal(raw, p); err != nil {
		t.Fatal(err)
	}
	if len(p.GetWorkloads()) != 1 {
		t.Fatalf("workloads = %d, want 1", len(p.GetWorkloads()))
	}
	w := p.GetWorkloads()[0]
	if !strings.HasPrefix(w.GetImage(), "ghcr.io/acme/app@sha256:") || w.GetProvider() != "docker" ||
		w.GetReleaseId() != release.String() || w.GetDeploymentId() != "d-1" ||
		w.GetRuntime().GetHealth().GetPath() != "/healthz" || w.GetAppSlug() != "api" {
		t.Errorf("workload = %v", w)
	}
	if len(w.GetSecrets()) != 1 || w.GetSecrets()[0].GetHandle() != "openbao:db" || len(w.GetSealedSecrets()) != 0 {
		t.Errorf("secrets = %v, want the handle only", w.GetSecrets())
	}

	if removed := assign(""); removed.Generation != 2 || removed.Replicas != 0 {
		t.Errorf("removal = %+v, want generation 2", removed)
	}
	if again := assign(""); again.Generation != 2 {
		t.Errorf("repeat removal wrote generation %d", again.Generation)
	}

	t.Run("replicas come from the app's current revision", func(t *testing.T) {
		// Scale the App after the Release captured replicas 2 (ADR-0027).
		if err := h.st.InTenant(ctx, h.tenant, func(tx *store.Tx) error {
			var rev uuid.UUID
			var rt []byte
			if err := tx.QueryRow(ctx, `SELECT h.revision_id, r.runtime FROM apps_heads h
				JOIN apps_revisions r ON r.id = h.revision_id WHERE h.id = $1`, app).Scan(&rev, &rt); err != nil {
				return err
			}
			var src, secs []byte
			if err := tx.QueryRow(ctx, `SELECT source, secrets FROM apps_revisions WHERE id = $1`, rev).Scan(&src, &secs); err != nil {
				return err
			}
			scaled := strings.Replace(string(rt), `"replicas": 2`, `"replicas": 5`, 1)
			_, err := tx.Update(ctx, "apps", app, rev, []store.Col{{Name: "source", Value: src},
				{Name: "runtime", Value: scaled}, {Name: "secrets", Value: secs}},
				store.Audit{Actor: store.Actor{ID: store.SystemActorID, Kind: store.ActorSystem}, Action: "test", ResourceType: "bohux.v1.App"})
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if got := assign(release.String()); got.Replicas != 5 {
			t.Errorf("replicas = %d, want the current 5, not the release's 2", got.Replicas)
		}
	})

	t.Run("refusals are non-retryable", func(t *testing.T) {
		for name, rel := range map[string]string{
			"building":      fixture.Release(t, h.st, h.tenant, app, "building", false).String(),
			"unknown":       uuid.Must(uuid.NewV7()).String(),
			"not an ID":     "nope",
			"artifact gone": fixture.Release(t, h.st, h.tenant, app, "ready", false).String(),
		} {
			_, err := h.env.ExecuteActivity(h.acts.AssignRelease, AssignInput{TenantID: h.tenant.String(),
				AppID: app.String(), ReleaseID: rel, DeploymentID: "d-2", Actor: testActor})
			if !isType(err, ErrTypeInvalid) {
				t.Errorf("%s: %v, want %s", name, err, ErrTypeInvalid)
			}
		}
	})
}

func TestWaitHealthy(t *testing.T) {
	h := newHarness(t)
	app := uuid.Must(uuid.NewV7()).String()
	routed := false
	wait := func(gen uint64, replicas int32, within time.Duration) error {
		_, err := h.env.ExecuteActivity(h.acts.WaitHealthy, WaitInput{
			TenantID: h.tenant.String(), NodeID: h.node.String(), Generation: gen, AppID: app,
			ReleaseID: "r2", Replicas: replicas, Routed: routed, Deadline: time.Now().Add(within)})
		return err
	}
	status := func(gen uint64, release string, state agentv1.WorkloadState, ready int32) *agentv1.StatusReport {
		return &agentv1.StatusReport{Generation: gen, Workloads: []*agentv1.WorkloadStatus{
			{AppId: app, ReleaseId: release, State: state, ReadyReplicas: ready}}}
	}
	healthy := agentv1.WorkloadState_WORKLOAD_STATE_HEALTHY
	failed := func(gen uint64, release, reason string) *agentv1.StatusReport {
		r := status(gen, release, agentv1.WorkloadState_WORKLOAD_STATE_FAILED, 0)
		r.Workloads[0].Failure = &bohuxv1.ErrorDetail{Reason: reason}
		return r
	}

	if err := wait(3, 1, 50*time.Millisecond); !isType(err, ErrTypeHealthTimeout) {
		t.Errorf("no report: %v, want %s", err, ErrTypeHealthTimeout)
	}
	routedHealthy := func(loaded bool) *agentv1.StatusReport {
		r := status(3, "r2", healthy, 2)
		r.Workloads[0].IngressLoaded = loaded
		return r
	}
	tests := []struct {
		name   string
		report *agentv1.StatusReport
		routed bool
		want   string // "" = healthy
	}{
		{"healthy", status(3, "r2", healthy, 2), false, ""},
		{"routed, served by the ingress", routedHealthy(true), true, ""},
		{"routed, not yet in the ingress", routedHealthy(false), true, ErrTypeHealthTimeout},
		{"routed, ingress refused", failed(3, "r2", "INGRESS_FAILED"), true, ErrTypeHealthTimeout},
		{"newer generation, healthy", status(4, "r2", healthy, 2), false, ""},
		{"older generation", status(2, "r2", healthy, 2), false, ErrTypeHealthTimeout},
		{"previous release still running", status(3, "r1", healthy, 2), false, ErrTypeHealthTimeout},
		{"not all replicas ready", status(3, "r2", healthy, 1), false, ErrTypeHealthTimeout},
		{"unhealthy", status(3, "r2", agentv1.WorkloadState_WORKLOAD_STATE_UNHEALTHY, 2), false, ErrTypeHealthTimeout},
		{"spec invalid", failed(3, "r2", "SPEC_INVALID"), false, ErrTypeWorkloadFailed},
		{"provider missing", failed(3, "r2", "PROVIDER_MISSING"), false, ErrTypeWorkloadFailed},
		{"apply failed is retried by the agent", failed(3, "r2", "APPLY_FAILED"), false, ErrTypeHealthTimeout},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h.report(tt.report)
			routed = tt.routed
			defer func() { routed = false }()
			err := wait(3, 2, 80*time.Millisecond)
			if tt.want == "" && err != nil {
				t.Errorf("WaitHealthy = %v, want healthy", err)
			}
			if tt.want != "" && !isType(err, tt.want) {
				t.Errorf("WaitHealthy = %v, want %s", err, tt.want)
			}
		})
	}
	t.Run("a report arriving while waiting ends the wait", func(t *testing.T) {
		h.report(status(3, "r1", healthy, 2))
		go func() {
			time.Sleep(100 * time.Millisecond)
			h.report(status(3, "r2", healthy, 2))
		}()
		if err := wait(3, 2, 5*time.Second); err != nil {
			t.Errorf("WaitHealthy = %v, want healthy once the report arrives", err)
		}
	})
}

// fakeDispatcher answers builds without an agent.
type fakeDispatcher struct {
	got  []*agentv1.BuildRequest
	fail string
}

func (f *fakeDispatcher) Build(_ context.Context, _ uuid.UUID, req *agentv1.BuildRequest) (*agentv1.BuildResult, error) {
	f.got = append(f.got, req)
	if f.fail != "" {
		return &agentv1.BuildResult{ReleaseId: req.GetReleaseId(), Failure: &bohuxv1.ErrorDetail{Reason: f.fail}}, nil
	}
	return &agentv1.BuildResult{ReleaseId: req.GetReleaseId(), ImageId: "sha256:" + strings.Repeat("b", 64), Commit: "c0ffee"}, nil
}

// gitApp turns a deployable App's source into Git.
func (h *harness) gitApp(app uuid.UUID) {
	h.t.Helper()
	ctx := context.Background()
	if err := h.st.InTenant(ctx, h.tenant, func(tx *store.Tx) error {
		var rev uuid.UUID
		var rt, secs []byte
		if err := tx.QueryRow(ctx, `SELECT h.revision_id, r.runtime, r.secrets FROM apps_heads h
			JOIN apps_revisions r ON r.id = h.revision_id WHERE h.id = $1`, app).Scan(&rev, &rt, &secs); err != nil {
			return err
		}
		_, err := tx.Update(ctx, "apps", app, rev, []store.Col{
			{Name: "source", Value: `{"git": {"repo_url": "https://example.com/web.git", "ref": "main", "subpath": "svc"}}`},
			{Name: "runtime", Value: rt}, {Name: "secrets", Value: secs}},
			store.Audit{Actor: store.Actor{ID: store.SystemActorID, Kind: store.ActorSystem}, Action: "test", ResourceType: "bohux.v1.App"})
		return err
	}); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) release(id uuid.UUID) (state, digest, sourceRef, reason string, available bool) {
	h.t.Helper()
	ctx := context.Background()
	if err := h.st.InTenant(ctx, h.tenant, func(tx *store.Tx) error {
		return tx.QueryRow(ctx, `SELECT r.state, r.artifact_digest, r.source_ref, COALESCE(r.failure->>'reason', ''),
			r.artifact_available FROM releases_heads h JOIN releases_revisions r ON r.id = h.revision_id
			WHERE h.id = $1`, id).Scan(&state, &digest, &sourceRef, &reason, &available)
	}); err != nil {
		h.t.Fatal(err)
	}
	return
}

func TestBuildActivities(t *testing.T) {
	h := newHarness(t)
	app, _ := fixture.DeployableApp(t, h.st, h.tenant, h.node, "web", 1)
	h.gitApp(app)
	disp := &fakeDispatcher{}
	h.acts.Builds = disp
	run := func(release uuid.UUID, ref string) (BuildOutcome, error) {
		v, err := h.env.ExecuteActivity(h.acts.RunBuild, BuildInput{TenantID: h.tenant.String(),
			ReleaseID: release.String(), Ref: ref, Actor: testActor})
		if err != nil {
			return BuildOutcome{}, err
		}
		var out BuildOutcome
		return out, v.Get(&out)
	}
	finish := func(release uuid.UUID, out BuildOutcome) {
		t.Helper()
		if _, err := h.env.ExecuteActivity(h.acts.FinishBuild, FinishInput{TenantID: h.tenant.String(),
			ReleaseID: release.String(), Actor: testActor, Outcome: out}); err != nil {
			t.Fatal(err)
		}
	}
	waitReady := func(release uuid.UUID) error {
		_, err := h.env.ExecuteActivity(h.acts.WaitReleaseReady, ReleaseRef{TenantID: h.tenant.String(), ReleaseID: release.String()})
		return err
	}

	ok := fixture.Release(t, h.st, h.tenant, app, "building", false)
	out, err := run(ok, "")
	if err != nil || out.ImageID == "" || out.Commit != "c0ffee" {
		t.Fatalf("RunBuild = %+v, %v", out, err)
	}
	if r := disp.got[0]; r.GetRepoUrl() != "https://example.com/web.git" || r.GetRef() != "main" ||
		r.GetSubpath() != "svc" || r.GetAppId() != app.String() {
		t.Errorf("dispatched %v", r)
	}
	if _, err := run(ok, "v2"); err != nil || disp.got[1].GetRef() != "v2" {
		t.Errorf("an explicit ref was not used: %v", disp.got[1])
	}
	finish(ok, out)
	finish(ok, BuildOutcome{Failure: "BUILD_FAILED"})
	if state, digest, src, _, avail := h.release(ok); state != "ready" || digest != out.ImageID || src != "c0ffee" || !avail {
		t.Errorf("after a good build: %s %s %s %v", state, digest, src, avail)
	}
	if err := waitReady(ok); err != nil {
		t.Errorf("WaitReleaseReady on a ready release = %v", err)
	}

	bad := fixture.Release(t, h.st, h.tenant, app, "building", false)
	disp.fail = "BUILD_FAILED"
	out, err = run(bad, "")
	if err != nil || out.Failure != "BUILD_FAILED" {
		t.Fatalf("RunBuild = %+v, %v; want a failure outcome", out, err)
	}
	finish(bad, out)
	if state, _, _, reason, avail := h.release(bad); state != "failed" || reason != "BUILD_FAILED" || avail {
		t.Errorf("after a failed build: %s %s %v", state, reason, avail)
	}
	if err := waitReady(bad); !isType(err, ErrTypeBuildFailed) {
		t.Errorf("WaitReleaseReady on a failed release = %v, want %s", err, ErrTypeBuildFailed)
	}

	_, err = run(uuid.Must(uuid.NewV7()), "")
	if err == nil || isType(err, ErrTypeInvalid) {
		t.Errorf("RunBuild before its row exists = %v, want a retryable error", err)
	}
}

// TestProviderOptionsTravel: a Release's App revision's provider options reach the
// workload (ADR-0032).
func TestProviderOptionsTravel(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	app, _ := fixture.DeployableApp(t, h.st, h.tenant, h.node, "cp", 1)
	if err := h.st.InTenant(ctx, h.tenant, func(tx *store.Tx) error {
		var rev uuid.UUID
		var src, rt, secs []byte
		if err := tx.QueryRow(ctx, `SELECT h.revision_id, r.source, r.runtime, r.secrets FROM apps_heads h
			JOIN apps_revisions r ON r.id = h.revision_id WHERE h.id = $1`, app).Scan(&rev, &src, &rt, &secs); err != nil {
			return err
		}
		_, err := tx.Update(ctx, "apps", app, rev, []store.Col{{Name: "source", Value: src}, {Name: "runtime", Value: rt},
			{Name: "secrets", Value: secs}, {Name: "provider_options", Value: `{"networks": ["bohux-infra"]}`}},
			store.Audit{Actor: store.Actor{ID: store.SystemActorID, Kind: store.ActorSystem}, Action: "test", ResourceType: "bohux.v1.App"})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	release := fixture.Release(t, h.st, h.tenant, app, "ready", true)
	if _, err := h.env.ExecuteActivity(h.acts.AssignRelease, AssignInput{TenantID: h.tenant.String(),
		AppID: app.String(), ReleaseID: release.String(), DeploymentID: "d-1", Actor: testActor}); err != nil {
		t.Fatal(err)
	}
	var raw []byte
	if err := h.st.InTenant(ctx, h.tenant, func(tx *store.Tx) error {
		return tx.QueryRow(ctx, `SELECT payload FROM assignments WHERE node_id = $1 ORDER BY generation DESC LIMIT 1`, h.node).Scan(&raw)
	}); err != nil {
		t.Fatal(err)
	}
	p := &agentv1.AssignmentPayload{}
	if err := proto.Unmarshal(raw, p); err != nil {
		t.Fatal(err)
	}
	if got := string(p.GetWorkloads()[0].GetProviderOptions()); !strings.Contains(got, `"bohux-infra"`) {
		t.Errorf("workload provider options = %q", got)
	}
}

// TestRoutesFollowTheApp: a Release captured before the App had routes still runs on
// the App's current routes, so a rollback never moves a host (ADR-0034).
func TestRoutesFollowTheApp(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	app, _ := fixture.DeployableApp(t, h.st, h.tenant, h.node, "web", 1)
	old := fixture.Release(t, h.st, h.tenant, app, "ready", true)
	if err := h.st.InTenant(ctx, h.tenant, func(tx *store.Tx) error {
		var rev uuid.UUID
		var src, rt, secs []byte
		if err := tx.QueryRow(ctx, `SELECT h.revision_id, r.source, r.runtime, r.secrets FROM apps_heads h
			JOIN apps_revisions r ON r.id = h.revision_id WHERE h.id = $1`, app).Scan(&rev, &src, &rt, &secs); err != nil {
			return err
		}
		_, err := tx.Update(ctx, "apps", app, rev, []store.Col{{Name: "source", Value: src}, {Name: "runtime", Value: rt},
			{Name: "secrets", Value: secs}, {Name: "routes", Value: `[{"host": "web.example.com", "port": 8080}]`}},
			store.Audit{Actor: store.Actor{ID: store.SystemActorID, Kind: store.ActorSystem}, Action: "test", ResourceType: "bohux.v1.App"})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.env.ExecuteActivity(h.acts.AssignRelease, AssignInput{TenantID: h.tenant.String(),
		AppID: app.String(), ReleaseID: old.String(), DeploymentID: "d-rollback", Actor: testActor}); err != nil {
		t.Fatal(err)
	}
	var raw []byte
	if err := h.st.InTenant(ctx, h.tenant, func(tx *store.Tx) error {
		return tx.QueryRow(ctx, `SELECT payload FROM assignments WHERE node_id = $1 ORDER BY generation DESC LIMIT 1`, h.node).Scan(&raw)
	}); err != nil {
		t.Fatal(err)
	}
	p := &agentv1.AssignmentPayload{}
	if err := proto.Unmarshal(raw, p); err != nil {
		t.Fatal(err)
	}
	rs := p.GetWorkloads()[0].GetRoutes()
	if len(rs) != 1 || rs[0].GetHost() != "web.example.com" || rs[0].GetPort() != 8080 {
		t.Errorf("routes = %v, want the App's current one", rs)
	}
}
