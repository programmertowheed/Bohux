package agent

import (
	"context"
	"fmt"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	agentv1 "github.com/bohumix/bohux/gen/bohux/agent/v1"
	bohuxv1 "github.com/bohumix/bohux/gen/bohux/v1"
	"github.com/bohumix/bohux/internal/pki"
)

// cacheWorkloads caches a signed assignment of exactly these workloads.
func cacheWorkloads(t *testing.T, signer *pki.Authority, st *State, c *Cache, gen uint64, ws ...*agentv1.Workload) {
	t.Helper()
	payload, err := proto.Marshal(&agentv1.AssignmentPayload{NodeId: st.NodeID(), Generation: gen, Workloads: ws})
	if err != nil {
		t.Fatal(err)
	}
	sig, keyID := signer.SignAssignment(payload)
	if ok, err := c.Accept(&agentv1.Assignment{Payload: payload, Signature: sig, KeyId: keyID}); err != nil || !ok {
		t.Fatalf("Accept gen %d: %v %v", gen, ok, err)
	}
}

func deployed(release, deployment string) *agentv1.Workload {
	return &agentv1.Workload{AppId: "api", EnvironmentId: "env", Provider: "fake", ReleaseId: release,
		DeploymentId: deployment, Image: "img@sha256:" + release, Runtime: &bohuxv1.Runtime{Replicas: 1}}
}

// withFallback is what a forward deploy assigns: the new workload, the one it
// replaces, and the deadline (ADR-0031).
func withFallback(w, previous *agentv1.Workload, after time.Time) *agentv1.Workload {
	w.Fallback = proto.Clone(previous).(*agentv1.Workload)
	w.FallbackAfter = timestamppb.New(after)
	return w
}

func only(t *testing.T, rep *agentv1.StatusReport) *agentv1.WorkloadStatus {
	t.Helper()
	if len(rep.GetWorkloads()) != 1 {
		t.Fatalf("report %v, want one workload", rep)
	}
	return rep.GetWorkloads()[0]
}

// TestFallback is the agent-side rollback, tested before the code that sets fallbacks
// (CLAUDE.md rule 5): no control plane exists here at all.
func TestFallback(t *testing.T) {
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
	fake.broken = map[string]bool{"r2": true}
	now := time.Unix(1_000_000, 0)
	r := &Reconciler{State: st, Cache: c, Provider: fake, Now: func() time.Time { return now }}

	good := deployed("r1", "d1")
	cacheWorkloads(t, signer, st, c, 1, good)
	if ws := only(t, r.Once(ctx)); ws.GetReleaseId() != "r1" || ws.GetState() != agentv1.WorkloadState_WORKLOAD_STATE_HEALTHY {
		t.Fatalf("first deploy: %v", ws)
	}

	cacheWorkloads(t, signer, st, c, 2, withFallback(deployed("r2", "d2"), good, now.Add(5*time.Minute)))
	t.Run("before the deadline the new release stays", func(t *testing.T) {
		if ws := only(t, r.Once(ctx)); ws.GetReleaseId() != "r2" || ws.GetState() == agentv1.WorkloadState_WORKLOAD_STATE_HEALTHY {
			t.Errorf("the broken release reads %v", ws)
		}
		now = now.Add(4 * time.Minute)
		ws := only(t, r.Once(ctx))
		if ws.GetReleaseId() != "r2" || ws.GetFailure() != nil {
			t.Errorf("before the deadline: %v", ws)
		}
	})
	t.Run("past the deadline, never healthy, the fallback is applied", func(t *testing.T) {
		now = now.Add(2 * time.Minute)
		ws := only(t, r.Once(ctx))
		if ws.GetReleaseId() != "r1" || ws.GetState() != agentv1.WorkloadState_WORKLOAD_STATE_HEALTHY ||
			ws.GetFailure().GetReason() != "FALLBACK_APPLIED" {
			t.Errorf("after the deadline: %v", ws)
		}
	})
	t.Run("the fallback survives a restart", func(t *testing.T) {
		applies, _ := fake.counts()
		again := &Reconciler{State: st, Cache: c, Provider: fake, Now: func() time.Time { return now }}
		ws := only(t, again.Once(ctx))
		if ws.GetReleaseId() != "r1" {
			t.Errorf("after restart: %v", ws)
		}
		if a, _ := fake.counts(); a != applies {
			t.Errorf("restart re-applied (%d applies, was %d)", a, applies)
		}
	})
	t.Run("a newer assignment wins, without churn when it is the same release", func(t *testing.T) {
		applies, _ := fake.counts()
		// The control plane's compensation: the last healthy release, no fallback.
		cacheWorkloads(t, signer, st, c, 3, deployed("r1", "d3"))
		ws := only(t, r.Once(ctx))
		if ws.GetReleaseId() != "r1" || ws.GetFailure() != nil {
			t.Errorf("after compensation: %v", ws)
		}
		if a, _ := fake.counts(); a != applies {
			t.Errorf("compensation to the fallen-back release re-applied (%d applies, was %d)", a, applies)
		}
	})
	t.Run("a deploy that was healthy keeps no fallback", func(t *testing.T) {
		cacheWorkloads(t, signer, st, c, 4, withFallback(deployed("r3", "d4"), deployed("r1", "d3"), now.Add(time.Minute)))
		if ws := only(t, r.Once(ctx)); ws.GetReleaseId() != "r3" || ws.GetState() != agentv1.WorkloadState_WORKLOAD_STATE_HEALTHY {
			t.Fatalf("r3: %v", ws)
		}
		// Later it breaks: a runtime problem, which self-healing handles, not a bad
		// deploy to undo.
		fake.broken["r3"] = true
		fake.crash(ref("api"))
		now = now.Add(time.Hour)
		if ws := only(t, r.Once(ctx)); ws.GetReleaseId() != "r3" || ws.GetFailure() != nil {
			t.Errorf("a once-healthy deploy fell back: %v", ws)
		}
	})
}

// TestRouteChangeDoesNotReapply: routes are the ingress's business (ADR-0034), so a
// generation that changes only them leaves the containers alone.
func TestRouteChangeDoesNotReapply(t *testing.T) {
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
	r := &Reconciler{State: st, Cache: c, Provider: fake, Ingress: &fakeIngress{}}
	cacheWorkloads(t, signer, st, c, 1, withRoutes("r1"))
	r.Once(ctx)
	routed := func(gen uint64, hosts ...string) {
		t.Helper()
		w := withRoutes("r1")
		for _, h := range hosts {
			w.Routes = append(w.Routes, &bohuxv1.Route{Host: h, Port: 8080})
		}
		cacheWorkloads(t, signer, st, c, gen, w)
		if ws := only(t, r.Once(ctx)); ws.GetState() != agentv1.WorkloadState_WORKLOAD_STATE_HEALTHY {
			t.Fatalf("gen %d: %v", gen, ws)
		}
	}
	// The first route joins the workload to the ingress: one apply (into the other
	// slot, cut over in the same pass, ADR-0035).
	before, _ := fake.counts()
	routed(2, "api.example.com")
	if a, _ := fake.counts(); a != before+1 {
		t.Errorf("joining the ingress: %d applies, want %d", a, before+1)
	}
	// Changing which hosts leaves the containers alone.
	before, _ = fake.counts()
	routed(3, "www.example.com", "api.example.com")
	if a, _ := fake.counts(); a != before {
		t.Errorf("a route change re-applied (%d applies, was %d)", a, before)
	}
}

type link bool

func (l *link) Rejected() bool { return bool(*l) }

// A release that turns healthy while the control plane refuses this agent, as a new
// control plane that rejects its own node's agent does, is not seen healthy by the
// control plane; it still falls back at the deadline (ADR-0042). An unreachable
// control plane is not a rejection: a healthy release stays (rule 1).
func TestFallbackNeedsAcceptedLink(t *testing.T) {
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
	now := time.Unix(1_000_000, 0)
	rejected := link(false)
	r := &Reconciler{State: st, Cache: c, Provider: fake, Now: func() time.Time { return now }, Link: &rejected}

	good := deployed("r1", "d1")
	cacheWorkloads(t, signer, st, c, 1, good)
	only(t, r.Once(ctx))

	tests := []struct {
		name     string
		rejected bool
		release  string
		failure  string
	}{
		{"rejected while healthy: falls back", true, "r1", "FALLBACK_APPLIED"},
		{"accepted, or unreachable, while healthy: stays", false, "r2", ""},
	}
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dep := fmt.Sprintf("d%d", i+2)
			cacheWorkloads(t, signer, st, c, uint64(10*i+2), withFallback(deployed("r2", dep), good, now.Add(5*time.Minute)))
			rejected = link(tt.rejected)
			if ws := only(t, r.Once(ctx)); ws.GetReleaseId() != "r2" || ws.GetState() != agentv1.WorkloadState_WORKLOAD_STATE_HEALTHY {
				t.Fatalf("new release: %v", ws)
			}
			now = now.Add(6 * time.Minute)
			ws := only(t, r.Once(ctx))
			if ws.GetReleaseId() != tt.release || ws.GetFailure().GetReason() != tt.failure {
				t.Errorf("after the deadline: release %s failure %q, want %s %q",
					ws.GetReleaseId(), ws.GetFailure().GetReason(), tt.release, tt.failure)
			}
			// The control plane takes over again with a plain assignment.
			cacheWorkloads(t, signer, st, c, uint64(10*i+5), deployed("r1", dep+"-back"))
			rejected = link(false)
			only(t, r.Once(ctx))
		})
	}
}
