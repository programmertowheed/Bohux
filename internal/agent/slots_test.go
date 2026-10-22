package agent

import (
	"context"
	"errors"
	"slices"
	"testing"

	agentv1 "github.com/bohumix/bohux/gen/bohux/agent/v1"
	"github.com/bohumix/bohux/internal/ingress"
	"github.com/bohumix/bohux/internal/pki"
	"github.com/bohumix/bohux/internal/provider"
	"github.com/bohumix/bohux/internal/slo"
)

// TestSlotSwitch is ADR-0035, broken Release first (CLAUDE.md rule 5): a routed App
// deploys into the other slot and the ingress cuts over only once it is healthy; a
// Release that never turns healthy never serves, and going back drops it.
func TestSlotSwitch(t *testing.T) {
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
	fake, ing := newFake(), &fakeIngress{}
	fake.broken = map[string]bool{"r2": true}
	r := &Reconciler{State: st, Cache: c, Provider: fake, Ingress: ing}
	base := provider.ResourceRef{Environment: "env", App: "api"}
	slot := func(s string) provider.ResourceRef { ref := base; ref.Slot = s; return ref }
	destroyed := func(ref provider.ResourceRef) bool {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		return slices.Contains(fake.destroyed, ref)
	}
	serving := func() string { return ing.hosts()["api.example.com"] }
	pass := func(gen uint64, release string) *agentv1.WorkloadStatus {
		t.Helper()
		cacheWorkloads(t, signer, st, c, gen, withRoutes(release, "api.example.com"))
		return only(t, r.Once(ctx))
	}

	if ws := pass(1, "r1"); !ws.GetIngressLoaded() || serving() != "api:8080" {
		t.Fatalf("first deploy, in place: %v, serving %q", ws, serving())
	}

	t.Run("a broken release never serves", func(t *testing.T) {
		ws := pass(2, "r2")
		if ws.GetReleaseId() != "r2" || ws.GetIngressLoaded() ||
			ws.GetState() == agentv1.WorkloadState_WORKLOAD_STATE_HEALTHY {
			t.Errorf("broken release reads %v", ws)
		}
		if serving() != "api:8080" || !fake.isRunning(slot("")) || !fake.isRunning(slot("blue")) {
			t.Errorf("serving %q; live running %v, pending running %v", serving(),
				fake.isRunning(slot("")), fake.isRunning(slot("blue")))
		}
	})
	t.Run("going back drops the pending slot", func(t *testing.T) {
		ws := pass(3, "r1")
		if !ws.GetIngressLoaded() || ws.GetReleaseId() != "r1" || !destroyed(slot("blue")) || destroyed(slot("")) {
			t.Errorf("after compensation: %v; blue destroyed %v", ws, destroyed(slot("blue")))
		}
	})
	t.Run("a healthy release cuts over and the old slot goes", func(t *testing.T) {
		ws := pass(4, "r3")
		if !ws.GetIngressLoaded() || ws.GetReleaseId() != "r3" || serving() != "api-blue:8080" {
			t.Errorf("after the switch: %v, serving %q", ws, serving())
		}
		if !destroyed(slot("")) || fake.isRunning(slot("")) {
			t.Error("the old slot survived the switch")
		}
	})
	t.Run("a failed cut-over keeps the old slot serving", func(t *testing.T) {
		ing.fail = errors.New("caddy said no")
		ws := pass(5, "r4")
		if ws.GetFailure().GetReason() != "INGRESS_FAILED" || !fake.isRunning(slot("blue")) || serving() != "api-blue:8080" {
			t.Errorf("refused cut-over: %v, serving %q", ws, serving())
		}
		ing.fail = nil
		ws = only(t, r.Once(ctx))
		if !ws.GetIngressLoaded() || serving() != "api-green:8080" || fake.isRunning(slot("blue")) {
			t.Errorf("retried cut-over: %v, serving %q", ws, serving())
		}
		// Logs and exec reach the slot that serves.
		if ref, ok := r.Ref("api"); !ok || ref != slot("green") {
			t.Errorf("Ref = %+v, %v; want the green slot", ref, ok)
		}
	})
	t.Run("a crashed live slot restarts in place", func(t *testing.T) {
		fake.crash(slot("green"))
		applies, _ := fake.counts()
		ws := only(t, r.Once(ctx))
		if a, _ := fake.counts(); a != applies+1 || !fake.isRunning(slot("green")) || fake.isRunning(slot("blue")) {
			t.Errorf("after a crash: %d applies (was %d), green %v, blue %v", a, applies,
				fake.isRunning(slot("green")), fake.isRunning(slot("blue")))
		}
		if !ws.GetIngressLoaded() || serving() != "api-green:8080" {
			t.Errorf("after a crash: %v, serving %q", ws, serving())
		}
	})
	t.Run("the switch survives a restart of the agent", func(t *testing.T) {
		again := &Reconciler{State: st, Cache: c, Provider: fake, Ingress: &fakeIngress{}}
		applies, _ := fake.counts()
		ws := only(t, again.Once(ctx))
		if a, _ := fake.counts(); a != applies || !ws.GetIngressLoaded() {
			t.Errorf("after restart: %v, %d applies (was %d)", ws, a, applies)
		}
	})
}

// TestCanarySplit is ADR-0049 on the node, failing cases first (rule 5): a canary that
// never turns healthy never serves; a healthy one serves its weight beside the live
// slot; one that turns unhealthy leaves the live slot serving alone; assigning it with
// no weight cuts over without applying anything again.
func TestCanarySplit(t *testing.T) {
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
	fake, ing := newFake(), &fakeIngress{}
	fake.broken = map[string]bool{"r2": true}
	// Each slot has served a different number of requests (ADR-0050).
	traffic := fakeTraffic{"api:8080": 100, "api-blue:8080": 7}
	r := &Reconciler{State: st, Cache: c, Provider: fake, Ingress: ing, Traffic: traffic}
	base := provider.ResourceRef{Environment: "env", App: "api"}
	slot := func(s string) provider.ResourceRef { ref := base; ref.Slot = s; return ref }
	site := func() ingress.Site {
		ing.mu.Lock()
		defer ing.mu.Unlock()
		for _, s := range ing.sites {
			if s.Host == "api.example.com" {
				return s
			}
		}
		return ingress.Site{}
	}
	pass := func(gen uint64, release string, weight uint32) *agentv1.WorkloadStatus {
		t.Helper()
		w := withRoutes(release, "api.example.com")
		w.CanaryWeight = weight
		cacheWorkloads(t, signer, st, c, gen, w)
		return only(t, r.Once(ctx))
	}
	alone := func(upstream string) bool {
		s := site()
		return len(s.Upstreams) == 1 && s.Upstreams[0] == upstream && len(s.Weights) == 0
	}

	if ws := pass(1, "r1", 0); !ws.GetIngressLoaded() || !alone("api:8080") || ws.GetTraffic().GetRequests() != 100 {
		t.Fatalf("first deploy: %v, site %+v", ws, site())
	}
	t.Run("a canary that never turns healthy never serves", func(t *testing.T) {
		ws := pass(2, "r2", 10)
		if ws.GetIngressLoaded() || ws.GetState() == agentv1.WorkloadState_WORKLOAD_STATE_HEALTHY || !alone("api:8080") {
			t.Errorf("broken canary: %v, site %+v", ws, site())
		}
		if ws := pass(3, "r1", 0); !alone("api:8080") || fake.isRunning(slot("blue")) || !ws.GetIngressLoaded() {
			t.Errorf("after going back: %v, site %+v", ws, site())
		}
	})
	t.Run("a healthy canary serves its weight beside the live slot", func(t *testing.T) {
		ws := pass(4, "r3", 10)
		s := site()
		if !ws.GetIngressLoaded() || ws.GetReleaseId() != "r3" ||
			!slices.Equal(s.Upstreams, []string{"api:8080", "api-blue:8080"}) || !slices.Equal(s.Weights, []int{90, 10}) {
			t.Errorf("canary: %v, site %+v", ws, s)
		}
		// The report carries the canary slot's traffic only, the Release it reports.
		if tr := ws.GetTraffic(); tr.GetRequests() != 7 || tr.GetServerErrors() != 1 || len(tr.GetLatencyBuckets()) != 12 {
			t.Errorf("canary traffic: %v", tr)
		}
		if !fake.isRunning(slot("")) || !fake.isRunning(slot("blue")) {
			t.Error("both slots must run during a canary")
		}
	})
	t.Run("a canary that turns unhealthy leaves the live slot serving alone", func(t *testing.T) {
		fake.mu.Lock()
		fake.broken["r3"] = true
		fake.mu.Unlock()
		fake.crash(slot("blue"))
		ws := only(t, r.Once(ctx))
		if ws.GetIngressLoaded() || !alone("api:8080") {
			t.Errorf("unhealthy canary: %v, site %+v", ws, site())
		}
		fake.mu.Lock()
		delete(fake.broken, "r3")
		fake.mu.Unlock()
		fake.crash(slot("blue"))
		if ws := only(t, r.Once(ctx)); !ws.GetIngressLoaded() || len(site().Weights) != 2 {
			t.Errorf("recovered canary: %v, site %+v", ws, site())
		}
	})
	t.Run("promotion cuts over without applying again", func(t *testing.T) {
		applies, _ := fake.counts()
		ws := pass(5, "r3", 0)
		if a, _ := fake.counts(); a != applies {
			t.Errorf("promotion applied %d times; the weight is not part of the spec", a-applies)
		}
		if !ws.GetIngressLoaded() || !alone("api-blue:8080") || fake.isRunning(slot("")) {
			t.Errorf("after promotion: %v, site %+v, live still running %v", ws, site(), fake.isRunning(slot("")))
		}
		if ws.GetTraffic().GetRequests() != 7 {
			t.Errorf("after promotion, traffic %v; want the promoted slot's", ws.GetTraffic())
		}
	})
}

// fakeTraffic serves each endpoint's request count, one server error per endpoint
// that served any.
type fakeTraffic map[string]uint64

func (f fakeTraffic) Counts(endpoints ...string) slo.Counts {
	c := slo.Counts{Latency: make([]uint64, len(slo.LatencyBoundsMS)+1)}
	for _, ep := range endpoints {
		if n := f[ep]; n > 0 {
			c.Requests += n
			c.ServerErrors++
			c.Latency[0] += n
		}
	}
	return c
}
