package agent

import (
	"context"
	"errors"
	"slices"
	"testing"

	agentv1 "github.com/bohumix/bohux/gen/bohux/agent/v1"
	"github.com/bohumix/bohux/internal/pki"
	"github.com/bohumix/bohux/internal/provider"
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
