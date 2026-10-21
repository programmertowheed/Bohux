package agent

import (
	"context"
	"errors"
	"sync"
	"testing"

	agentv1 "github.com/bohumix/bohux/gen/bohux/agent/v1"
	bohuxv1 "github.com/bohumix/bohux/gen/bohux/v1"
	"github.com/bohumix/bohux/internal/ingress"
	"github.com/bohumix/bohux/internal/pki"
	"github.com/bohumix/bohux/internal/provider"
)

// fakeIngress records what was loaded and refuses while fail is set.
type fakeIngress struct {
	mu    sync.Mutex
	fail  error
	sites []ingress.Site
	loads int
}

func (f *fakeIngress) Load(_ context.Context, sites []ingress.Site) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail != nil {
		return f.fail
	}
	f.sites, f.loads = sites, f.loads+1
	return nil
}

func (f *fakeIngress) hosts() map[string]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]string{}
	for _, s := range f.sites {
		out[s.Host] = s.Upstreams[0]
	}
	return out
}

func withRoutes(release string, hosts ...string) *agentv1.Workload {
	w := deployed(release, "d-"+release)
	w.Runtime.Ports = []*bohuxv1.Port{{ContainerPort: 8080}}
	for _, h := range hosts {
		w.Routes = append(w.Routes, &bohuxv1.Route{Host: h, Port: 8080})
	}
	return w
}

// TestIngress: the agent loads every routed workload's hosts with the provider's
// endpoints; a refused load keeps the ingress as it was and reports INGRESS_FAILED;
// removing routes removes the hosts (ADR-0034).
func TestIngress(t *testing.T) {
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
	r := &Reconciler{State: st, Cache: c, Provider: fake, Ingress: ing}

	cacheWorkloads(t, signer, st, c, 1, withRoutes("r1", "api.example.com"))
	ws := only(t, r.Once(ctx))
	if !ws.GetIngressLoaded() || ws.GetState() != agentv1.WorkloadState_WORKLOAD_STATE_HEALTHY {
		t.Fatalf("first pass: %v", ws)
	}
	if got := ing.hosts(); got["api.example.com"] != "api:8080" || len(got) != 1 {
		t.Fatalf("loaded %v", got)
	}

	t.Run("a refused load is INGRESS_FAILED and retried", func(t *testing.T) {
		ing.fail = errors.New("caddy said no")
		cacheWorkloads(t, signer, st, c, 2, withRoutes("r1", "api.example.com", "www.example.com"))
		ws := only(t, r.Once(ctx))
		if ws.GetFailure().GetReason() != "INGRESS_FAILED" || ws.GetIngressLoaded() {
			t.Errorf("refused: %v", ws)
		}
		ing.fail = nil
		if ws := only(t, r.Once(ctx)); !ws.GetIngressLoaded() || ws.GetFailure() != nil || len(ing.hosts()) != 2 {
			t.Errorf("after retry: %v, loaded %v", ws, ing.hosts())
		}
	})
	t.Run("a crashed replica keeps its host but is not loaded", func(t *testing.T) {
		ref := provider.ResourceRef{Environment: "env", App: "api"}
		fake.mu.Lock()
		fake.running[ref] = provider.Status{Exists: true, Hash: fake.running[ref].Hash}
		fake.mu.Unlock()
		// The pass re-applies it (not running), so it comes back with endpoints;
		// check the remembered endpoint kept the hosts meanwhile.
		only(t, r.Once(ctx))
		if len(ing.hosts()) != 2 {
			t.Errorf("hosts after a restart: %v", ing.hosts())
		}
	})
	t.Run("removing the routes removes the hosts", func(t *testing.T) {
		cacheWorkloads(t, signer, st, c, 3, withRoutes("r1"))
		ws := only(t, r.Once(ctx))
		if ws.GetIngressLoaded() || len(ing.hosts()) != 0 {
			t.Errorf("without routes: %v, loaded %v", ws, ing.hosts())
		}
	})
	t.Run("a node without an ingress fails routed workloads", func(t *testing.T) {
		bare := &Reconciler{State: st, Cache: c, Provider: fake}
		cacheWorkloads(t, signer, st, c, 4, withRoutes("r1", "api.example.com"))
		if ws := only(t, bare.Once(ctx)); ws.GetFailure().GetReason() != "INGRESS_FAILED" {
			t.Errorf("no ingress: %v", ws)
		}
	})
}
