package agent

import (
	"context"
	"crypto/rand"
	"fmt"
	"iter"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/nacl/box"
	"google.golang.org/protobuf/proto"

	agentv1 "github.com/bohumix/bohux/gen/bohux/agent/v1"
	bohuxv1 "github.com/bohumix/bohux/gen/bohux/v1"
	"github.com/bohumix/bohux/internal/pki"
	"github.com/bohumix/bohux/internal/provider"
)

// fakeProvider is an in-memory target.
type fakeProvider struct {
	mu sync.Mutex
	// broken Releases start but never pass their health check.
	broken    map[string]bool
	running   map[provider.ResourceRef]provider.Status
	secrets   map[provider.ResourceRef]map[string]string
	applies   int
	destroyed []provider.ResourceRef
	pulls     map[provider.ResourceRef]string
	// rolling makes Apply behave like a provider that updates a workload in place:
	// the previous release keeps running, healthy, under its own hash until converge
	// is called. Swarm's rolling update does exactly this; Docker's recreate does not.
	rolling bool
	pending map[provider.ResourceRef]provider.Status
}

func newFake() *fakeProvider {
	return &fakeProvider{running: map[provider.ResourceRef]provider.Status{},
		secrets: map[provider.ResourceRef]map[string]string{}}
}

func (f *fakeProvider) Describe(context.Context) (provider.Descriptor, error) {
	return provider.Descriptor{Name: "fake", Version: "0", Capabilities: provider.AllVerbs}, nil
}

func (f *fakeProvider) Plan(context.Context, provider.PlanRequest) (provider.Plan, error) {
	return provider.Plan{}, nil
}

func (f *fakeProvider) Apply(_ context.Context, req provider.ApplyRequest) (provider.ApplyResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.applies++
	if req.Spec.Secrets != nil {
		vals, err := req.Spec.Secrets.Open()
		if err != nil {
			return provider.ApplyResult{}, err
		}
		f.secrets[req.Spec.Ref] = vals
	}
	if req.Spec.PullConfig != nil {
		cfg, err := req.Spec.PullConfig.Open()
		if err != nil {
			return provider.ApplyResult{}, err
		}
		if f.pulls == nil {
			f.pulls = map[provider.ResourceRef]string{}
		}
		f.pulls[req.Spec.Ref] = cfg
	}
	st := provider.Status{Exists: true, Hash: req.Spec.Hash, Running: true,
		Healthy: !f.broken[req.Spec.Release], ReadyReplicas: req.Spec.Runtime.GetReplicas()}
	if req.Spec.Ingress {
		st.Endpoints = map[int32]string{}
		for _, p := range req.Spec.Runtime.GetPorts() {
			host := req.Spec.Ref.App
			if req.Spec.Ref.Slot != "" {
				host += "-" + req.Spec.Ref.Slot
			}
			st.Endpoints[p.GetContainerPort()] = fmt.Sprintf("%s:%d", host, p.GetContainerPort())
		}
	}
	if _, serving := f.running[req.Spec.Ref]; f.rolling && serving {
		if f.pending == nil {
			f.pending = map[provider.ResourceRef]provider.Status{}
		}
		f.pending[req.Spec.Ref] = st
		return provider.ApplyResult{Changed: true}, nil
	}
	f.running[req.Spec.Ref] = st
	return provider.ApplyResult{Changed: true}, nil
}

// converge finishes a rolling update: the new release replaces the old one.
func (f *fakeProvider) converge(ref provider.ResourceRef) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if st, ok := f.pending[ref]; ok {
		f.running[ref] = st
		delete(f.pending, ref)
	}
}

func (f *fakeProvider) Status(_ context.Context, ref provider.ResourceRef) (provider.Status, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.running[ref], nil
}

func (f *fakeProvider) Logs(context.Context, provider.LogRequest) (iter.Seq2[provider.LogLine, error], error) {
	return nil, provider.ErrUnsupported
}

func (f *fakeProvider) Exec(context.Context, provider.ExecRequest) (provider.Session, error) {
	return nil, provider.ErrUnsupported
}

func (f *fakeProvider) Scale(context.Context, provider.ScaleRequest) error { return nil }

func (f *fakeProvider) Destroy(_ context.Context, ref provider.ResourceRef) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.running, ref)
	f.destroyed = append(f.destroyed, ref)
	return nil
}

// crash stops a workload's process, as an OOM kill or a dockerd hiccup would.
func (f *fakeProvider) crash(ref provider.ResourceRef) {
	f.mu.Lock()
	defer f.mu.Unlock()
	st := f.running[ref]
	st.Running, st.Healthy, st.ReadyReplicas, st.Endpoints = false, false, 0, nil
	f.running[ref] = st
}

func (f *fakeProvider) counts() (int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.applies, len(f.destroyed)
}

func (f *fakeProvider) isRunning(ref provider.ResourceRef) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.running[ref].Running
}

// assign caches a signed assignment, as if the control plane had sent it.
func assign(t *testing.T, signer *pki.Authority, st *State, c *Cache, gen uint64, apps map[string]string) {
	t.Helper()
	var workloads []*agentv1.Workload
	for app, secret := range apps {
		w := &agentv1.Workload{AppId: app, EnvironmentId: "env", Provider: "fake", ReleaseId: "r1",
			Image: "img@sha256:1", Runtime: &bohuxv1.Runtime{Replicas: 1}}
		if secret != "" {
			plain, err := proto.Marshal(&agentv1.SecretValues{Values: map[string]string{"DB": secret}})
			if err != nil {
				t.Fatal(err)
			}
			var pub [32]byte
			copy(pub[:], st.sealingKey.PublicKey().Bytes())
			if w.SealedSecrets, err = box.SealAnonymous(nil, plain, &pub, rand.Reader); err != nil {
				t.Fatal(err)
			}
		}
		workloads = append(workloads, w)
	}
	payload, err := proto.Marshal(&agentv1.AssignmentPayload{NodeId: st.NodeID(), Generation: gen, Workloads: workloads})
	if err != nil {
		t.Fatal(err)
	}
	sig, keyID := signer.SignAssignment(payload)
	if ok, err := c.Accept(&agentv1.Assignment{Payload: payload, Signature: sig, KeyId: keyID}); err != nil || !ok {
		t.Fatalf("Accept gen %d: %v %v", gen, ok, err)
	}
}

func ref(app string) provider.ResourceRef {
	return provider.ResourceRef{Environment: "env", App: app}
}

// No control plane exists anywhere in these tests: the reconciler works from the
// cache alone, which is rule 1.
func TestReconcile(t *testing.T) {
	ctx := context.Background()
	signer, err := pki.LoadOrCreate(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	st := testState(t, signer)
	c, err := OpenCache(st)
	if err != nil {
		t.Fatal(err)
	}
	fake := newFake()
	r := &Reconciler{State: st, Cache: c, Provider: fake}

	t.Run("no assignment changes nothing", func(t *testing.T) {
		// Even with a stale record of applied workloads from an earlier run.
		if err := r.saveApplied(appliedSet{ref("old"): ""}); err != nil {
			t.Fatal(err)
		}
		if rep := r.Once(ctx); rep != nil {
			t.Errorf("report %v without an assignment", rep)
		}
		if a, d := fake.counts(); a != 0 || d != 0 {
			t.Errorf("applies %d destroys %d, want none", a, d)
		}
	})

	assign(t, signer, st, c, 1, map[string]string{"api": "s1", "web": ""})
	t.Run("converges, then is idempotent", func(t *testing.T) {
		rep := r.Once(ctx)
		if rep.GetGeneration() != 1 || len(rep.GetWorkloads()) != 2 {
			t.Fatalf("report %v", rep)
		}
		for _, ws := range rep.GetWorkloads() {
			if ws.GetState() != agentv1.WorkloadState_WORKLOAD_STATE_HEALTHY {
				t.Errorf("%s state %v", ws.GetAppId(), ws.GetState())
			}
		}
		if fake.secrets[ref("api")]["DB"] != "s1" {
			t.Error("provider did not get the secret value")
		}
		before, _ := fake.counts()
		r.Once(ctx)
		if after, _ := fake.counts(); after != before {
			t.Errorf("unchanged state re-applied %d times", after-before)
		}
	})

	t.Run("a crashed workload is restarted from the cache alone", func(t *testing.T) {
		fake.crash(ref("api"))
		r.Once(ctx)
		if !fake.isRunning(ref("api")) {
			t.Error("crashed workload not restarted")
		}
	})

	t.Run("a rotated secret re-applies", func(t *testing.T) {
		assign(t, signer, st, c, 2, map[string]string{"api": "s2", "web": ""})
		before, _ := fake.counts()
		r.Once(ctx)
		after, _ := fake.counts()
		if after != before+1 || fake.secrets[ref("api")]["DB"] != "s2" {
			t.Errorf("applies %d→%d, secret %q", before, after, fake.secrets[ref("api")]["DB"])
		}
	})

	t.Run("a removed workload is destroyed, also by a restarted agent", func(t *testing.T) {
		assign(t, signer, st, c, 3, map[string]string{"api": "s2"})
		fresh := &Reconciler{State: st, Cache: c, Provider: fake}
		fresh.Once(ctx)
		if fake.isRunning(ref("web")) {
			t.Error("removed workload still running")
		}
		applied, err := fresh.loadApplied()
		if err != nil {
			t.Fatal(err)
		}
		_, web := applied[ref("web")]
		_, api := applied[ref("api")]
		if web || !api {
			t.Errorf("applied record %v", applied)
		}
	})

	t.Run("an empty assignment removes everything", func(t *testing.T) {
		assign(t, signer, st, c, 4, nil)
		r.Once(ctx)
		if fake.isRunning(ref("api")) {
			t.Error("workload survived an empty assignment")
		}
	})

	t.Run("a workload for another provider fails without applying", func(t *testing.T) {
		payload, err := proto.Marshal(&agentv1.AssignmentPayload{NodeId: st.NodeID(), Generation: 5,
			Workloads: []*agentv1.Workload{{AppId: "k8s-app", EnvironmentId: "env", Provider: "kubernetes"}}})
		if err != nil {
			t.Fatal(err)
		}
		sig, keyID := signer.SignAssignment(payload)
		if _, err := c.Accept(&agentv1.Assignment{Payload: payload, Signature: sig, KeyId: keyID}); err != nil {
			t.Fatal(err)
		}
		before, _ := fake.counts()
		rep := r.Once(ctx)
		if after, _ := fake.counts(); after != before {
			t.Error("applied a workload for another provider")
		}
		if rep.GetWorkloads()[0].GetFailure().GetReason() != "PROVIDER_MISSING" {
			t.Errorf("report %v", rep)
		}
	})
}

func TestReconcileLoopSelfHeals(t *testing.T) {
	signer, err := pki.LoadOrCreate(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	st := testState(t, signer)
	c, err := OpenCache(st)
	if err != nil {
		t.Fatal(err)
	}
	fake := newFake()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- (&Reconciler{State: st, Cache: c, Provider: fake, Interval: 20 * time.Millisecond}).Run(ctx)
	}()
	assign(t, signer, st, c, 1, map[string]string{"api": ""})
	waitUntil(t, func() bool { return fake.isRunning(ref("api")) })
	fake.crash(ref("api"))
	waitUntil(t, func() bool { return fake.isRunning(ref("api")) })
	cancel()
	if err := <-done; err != nil {
		t.Errorf("Run returned %v", err)
	}
}

func waitUntil(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met in time")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestReconcilePullConfig(t *testing.T) {
	ctx := context.Background()
	signer, err := pki.LoadOrCreate(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	st := testState(t, signer)
	c, err := OpenCache(st)
	if err != nil {
		t.Fatal(err)
	}
	fake := newFake()
	r := &Reconciler{State: st, Cache: c, Provider: fake}
	send := func(gen uint64, cfg string) {
		t.Helper()
		plain, err := proto.Marshal(&agentv1.SecretValues{ImagePullConfig: cfg})
		if err != nil {
			t.Fatal(err)
		}
		var pub [32]byte
		copy(pub[:], st.sealingKey.PublicKey().Bytes())
		w := &agentv1.Workload{AppId: "api", EnvironmentId: "env", Provider: "fake", ReleaseId: "r1",
			Image: "r.example/api@sha256:1", Runtime: &bohuxv1.Runtime{Replicas: 1},
			ImagePullSecret: &bohuxv1.SecretRef{Key: "REGISTRY", Handle: "openbao:reg"}}
		if w.SealedSecrets, err = box.SealAnonymous(nil, plain, &pub, rand.Reader); err != nil {
			t.Fatal(err)
		}
		payload, err := proto.Marshal(&agentv1.AssignmentPayload{NodeId: st.NodeID(), Generation: gen,
			Workloads: []*agentv1.Workload{w}})
		if err != nil {
			t.Fatal(err)
		}
		sig, keyID := signer.SignAssignment(payload)
		if ok, err := c.Accept(&agentv1.Assignment{Payload: payload, Signature: sig, KeyId: keyID}); err != nil || !ok {
			t.Fatalf("Accept gen %d: %v %v", gen, ok, err)
		}
	}

	send(1, `{"auths":{"r.example":{"auth":"Zmlyc3Q="}}}`)
	r.Once(ctx)
	if got := fake.pulls[ref("api")]; got != `{"auths":{"r.example":{"auth":"Zmlyc3Q="}}}` {
		t.Fatalf("provider pulled with %q", got)
	}
	if len(fake.secrets[ref("api")]) != 0 {
		t.Errorf("credential reached the environment: %v", fake.secrets[ref("api")])
	}

	send(2, `{"auths":{"r.example":{"auth":"c2Vjb25k"}}}`)
	before, _ := fake.counts()
	r.Once(ctx)
	if after, _ := fake.counts(); after != before {
		t.Errorf("a rotated credential restarted the workload (%d applies)", after-before)
	}
}

// TestReconcileExtraProvider runs two providers on one node (ADR-0045): each workload
// goes to the one it names, records remember the owner, and records written before
// owners were named belong to the primary provider.
func TestReconcileExtraProvider(t *testing.T) {
	ctx := context.Background()
	signer, err := pki.LoadOrCreate(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	st := testState(t, signer)
	c, err := OpenCache(st)
	if err != nil {
		t.Fatal(err)
	}
	primary, other := newFake(), newFake()
	r := &Reconciler{State: st, Cache: c, Provider: primary, Extra: map[string]provider.Provider{"other": other}}

	// An applied.json from an agent that did not name providers.
	legacy := `[{"Environment":"env","App":"legacy"}]`
	if err := writeFile(st.dir, appliedFile, []byte(legacy)); err != nil {
		t.Fatal(err)
	}
	payload, err := proto.Marshal(&agentv1.AssignmentPayload{NodeId: st.NodeID(), Generation: 1,
		Workloads: []*agentv1.Workload{{AppId: "swarm-app", EnvironmentId: "env", Provider: "other", ReleaseId: "r1",
			Image: "img@sha256:1", Runtime: &bohuxv1.Runtime{Replicas: 1}}}})
	if err != nil {
		t.Fatal(err)
	}
	sig, keyID := signer.SignAssignment(payload)
	if _, err := c.Accept(&agentv1.Assignment{Payload: payload, Signature: sig, KeyId: keyID}); err != nil {
		t.Fatal(err)
	}
	rep := r.Once(ctx)
	if f := rep.GetWorkloads()[0].GetFailure(); f != nil {
		t.Fatalf("workload failed: %v", f)
	}
	if !other.isRunning(ref("swarm-app")) || primary.isRunning(ref("swarm-app")) {
		t.Error("the workload did not run on the provider it names")
	}
	if len(primary.destroyed) != 1 || primary.destroyed[0] != ref("legacy") || len(other.destroyed) != 0 {
		t.Errorf("legacy record destroyed by primary %v, other %v; want the primary", primary.destroyed, other.destroyed)
	}
	applied, err := r.loadApplied()
	if err != nil || len(applied) != 1 || applied[ref("swarm-app")] != "other" {
		t.Errorf("applied = %v, %v; want swarm-app owned by other", applied, err)
	}

	assign(t, signer, st, c, 2, nil)
	r.Once(ctx)
	if len(other.destroyed) != 1 || other.destroyed[0] != ref("swarm-app") {
		t.Errorf("removed through %v; want the owning provider", other.destroyed)
	}
}

// A provider that updates a workload in place keeps the previous release running and
// healthy, under its own hash, while the new one rolls out. Reporting that as healthy
// would let the deploy's health gate accept the old release's health as the new
// release's, so a broken Release would be declared a success and never roll back —
// exactly what rule 5 exists to prevent. Found by the Swarm end-to-end run: Docker
// recreates its container, so the stale state is never both running and healthy there.
func TestReconcileDoesNotReportAStaleReleaseHealthy(t *testing.T) {
	ctx := context.Background()
	signer, err := pki.LoadOrCreate(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	st := testState(t, signer)
	c, err := OpenCache(st)
	if err != nil {
		t.Fatal(err)
	}
	fake := newFake()
	r := &Reconciler{State: st, Cache: c, Provider: fake}

	assign(t, signer, st, c, 1, map[string]string{"api": "s1"})
	if got := state(t, r.Once(ctx), "api"); got != agentv1.WorkloadState_WORKLOAD_STATE_HEALTHY {
		t.Fatalf("first convergence reported %v, want HEALTHY", got)
	}

	// From here the target updates in place.
	fake.rolling = true
	assign(t, signer, st, c, 2, map[string]string{"api": "s2"})

	if got := state(t, r.Once(ctx), "api"); got == agentv1.WorkloadState_WORKLOAD_STATE_HEALTHY {
		t.Error("the new release was reported HEALTHY while the previous one was still serving: " +
			"the deploy's health gate would accept the old release's health")
	} else if got != agentv1.WorkloadState_WORKLOAD_STATE_APPLYING {
		t.Errorf("mid-rollout state %v, want APPLYING", got)
	}

	// Once the rollout finishes, the new release is healthy on its own hash.
	fake.converge(ref("api"))
	if got := state(t, r.Once(ctx), "api"); got != agentv1.WorkloadState_WORKLOAD_STATE_HEALTHY {
		t.Errorf("after the rollout finished, state %v, want HEALTHY", got)
	}
}

// state returns the reported state of one app in a report.
func state(t *testing.T, rep *agentv1.StatusReport, app string) agentv1.WorkloadState {
	t.Helper()
	for _, ws := range rep.GetWorkloads() {
		if ws.GetAppId() == app {
			return ws.GetState()
		}
	}
	t.Fatalf("no workload %q in report %v", app, rep)
	return agentv1.WorkloadState_WORKLOAD_STATE_UNSPECIFIED
}
