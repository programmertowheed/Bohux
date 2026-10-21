package agent

import (
	"context"
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
