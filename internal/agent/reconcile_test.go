package agent

import (
	"context"
	"crypto/rand"
	"iter"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/nacl/box"
	"google.golang.org/protobuf/proto"

	agentv1 "github.com/bohumix/bohux/gen/bohux/agent/v1"
	bohuxv1 "github.com/bohumix/bohux/gen/bohux/v1"
	"github.com/bohumix/bohux/internal/pki"
	"github.com/bohumix/bohux/providers"
)

// fakeProvider is an in-memory target.
type fakeProvider struct {
	mu        sync.Mutex
	running   map[providers.ResourceRef]providers.Status
	secrets   map[providers.ResourceRef]map[string]string
	applies   int
	destroyed []providers.ResourceRef
}

func newFake() *fakeProvider {
	return &fakeProvider{running: map[providers.ResourceRef]providers.Status{},
		secrets: map[providers.ResourceRef]map[string]string{}}
}

func (f *fakeProvider) Describe(context.Context) (providers.Descriptor, error) {
	return providers.Descriptor{Name: "fake", Version: "0", Capabilities: providers.AllVerbs}, nil
}

func (f *fakeProvider) Plan(context.Context, providers.PlanRequest) (providers.Plan, error) {
	return providers.Plan{}, nil
}

func (f *fakeProvider) Apply(_ context.Context, req providers.ApplyRequest) (providers.ApplyResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.applies++
	if req.Spec.Secrets != nil {
		vals, err := req.Spec.Secrets.Open()
		if err != nil {
			return providers.ApplyResult{}, err
		}
		f.secrets[req.Spec.Ref] = vals
	}
	f.running[req.Spec.Ref] = providers.Status{Exists: true, Hash: req.Spec.Hash, Running: true,
		Healthy: true, ReadyReplicas: req.Spec.Runtime.GetReplicas()}
	return providers.ApplyResult{Changed: true}, nil
}

func (f *fakeProvider) Status(_ context.Context, ref providers.ResourceRef) (providers.Status, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.running[ref], nil
}

func (f *fakeProvider) Logs(context.Context, providers.LogRequest) (iter.Seq2[providers.LogLine, error], error) {
	return nil, providers.ErrUnsupported
}

func (f *fakeProvider) Exec(context.Context, providers.ExecRequest) (providers.Session, error) {
	return nil, providers.ErrUnsupported
}

func (f *fakeProvider) Scale(context.Context, providers.ScaleRequest) error { return nil }

func (f *fakeProvider) Destroy(_ context.Context, ref providers.ResourceRef) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.running, ref)
	f.destroyed = append(f.destroyed, ref)
	return nil
}

// crash stops a workload's process, as an OOM kill or a dockerd hiccup would.
func (f *fakeProvider) crash(ref providers.ResourceRef) {
	f.mu.Lock()
	defer f.mu.Unlock()
	st := f.running[ref]
	st.Running, st.Healthy, st.ReadyReplicas = false, false, 0
	f.running[ref] = st
}

func (f *fakeProvider) counts() (int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.applies, len(f.destroyed)
}

func (f *fakeProvider) isRunning(ref providers.ResourceRef) bool {
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

func ref(app string) providers.ResourceRef {
	return providers.ResourceRef{Environment: "env", App: app}
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
		if err := r.saveApplied(map[providers.ResourceRef]bool{ref("old"): true}); err != nil {
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
		if applied[ref("web")] || !applied[ref("api")] {
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
