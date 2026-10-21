//go:build integration

package agent

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	agentv1 "github.com/bohumix/bohux/gen/bohux/agent/v1"
	bohuxv1 "github.com/bohumix/bohux/gen/bohux/v1"
	"github.com/bohumix/bohux/internal/agentapi"
	"github.com/bohumix/bohux/internal/api"
	"github.com/bohumix/bohux/internal/auth"
	"github.com/bohumix/bohux/internal/pki"
	"github.com/bohumix/bohux/internal/store"
	"github.com/bohumix/bohux/internal/store/fixture"
	"github.com/bohumix/bohux/internal/store/storetest"
)

type liveRestore bool

func (l liveRestore) LiveRestoreEnabled(context.Context) (bool, error) { return bool(l), nil }

// controlPlane is a real agent listener on a fresh database.
type controlPlane struct {
	t      *testing.T
	srv    *agentapi.Server
	st     *store.Store
	pki    *pki.Authority
	tenant uuid.UUID
	addr   string
	stop   func()
}

func newControlPlane(t *testing.T, cpVersion string) *controlPlane {
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
	tenant, _, err := api.Bootstrap(ctx, st, "acme", "")
	if err != nil {
		t.Fatal(err)
	}
	authority, err := pki.LoadOrCreate(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	cp := &controlPlane{t: t, st: st, pki: authority, tenant: tenant}
	cp.serve(cpVersion, "127.0.0.1:0")
	return cp
}

func (cp *controlPlane) serve(cpVersion, addr string) {
	cp.t.Helper()
	s := &agentapi.Server{Store: cp.st, PKI: cp.pki, Version: cpVersion, PingInterval: 50 * time.Millisecond,
		Secrets: fakeSecrets{"openbao:db": "postgres://u:hunter2@db/app"}}
	cp.srv = s
	tlsCfg, err := s.TLSConfig()
	if err != nil {
		cp.t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle(s.Handler())
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		cp.t.Fatal(err)
	}
	srv := &http.Server{Handler: mux, TLSConfig: tlsCfg, ReadHeaderTimeout: time.Second}
	go func() { _ = srv.ServeTLS(ln, "", "") }()
	cp.addr = ln.Addr().String()
	cp.stop = func() { _ = srv.Close() }
	cp.t.Cleanup(cp.stop)
}

func (cp *controlPlane) bootstrapToken(slug string) string {
	cp.t.Helper()
	id := uuid.Must(uuid.NewV7())
	raw, hash, err := auth.Issue(auth.KindBootstrap, cp.tenant, id)
	if err != nil {
		cp.t.Fatal(err)
	}
	err = cp.st.InTenant(context.Background(), cp.tenant, func(tx *store.Tx) error {
		return tx.Exec(context.Background(), `INSERT INTO node_bootstrap_tokens
			(id, tenant_id, node_slug, token_hash, expires_at, created_by)
			VALUES ($1, $2, $3, $4, $5, $6)`, id, cp.tenant, slug, hash, time.Now().Add(time.Hour),
			store.SystemActorID)
	})
	if err != nil {
		cp.t.Fatal(err)
	}
	return raw
}

func (cp *controlPlane) waitState(node, want string) {
	cp.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		var state string
		err := cp.st.InTenant(context.Background(), cp.tenant, func(tx *store.Tx) error {
			return tx.QueryRow(context.Background(), `SELECT r.state FROM nodes_heads h
				JOIN nodes_revisions r ON r.id = h.revision_id WHERE h.id = $1`, node).Scan(&state)
		})
		if err != nil {
			cp.t.Fatal(err)
		}
		if state == want {
			return
		}
		if time.Now().After(deadline) {
			cp.t.Fatalf("node state %q, want %q", state, want)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func (cp *controlPlane) register(t *testing.T, slug string) *State {
	t.Helper()
	dir := t.TempDir()
	err := Register(context.Background(), RegisterOptions{StateDir: dir, ControlPlane: cp.addr,
		Token: cp.bootstrapToken(slug), CACertSHA256: cp.pki.CACertSHA256(), Docker: liveRestore(true)})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	st, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

type fakeSecrets map[string]string

func (f fakeSecrets) Resolve(_ context.Context, _ uuid.UUID, handles []string) (map[string]string, error) {
	out := map[string]string{}
	for _, h := range handles {
		out[h] = f[h]
	}
	return out, nil
}

// startRun runs the agent in the background; the returned func stops it and waits.
func startRun(t *testing.T, st *State) func() {
	t.Helper()
	cache, err := OpenCache(st)
	if err != nil {
		t.Fatal(err)
	}
	return startRunWith(t, st, cache)
}

func startRunWith(t *testing.T, st *State, cache *Cache) func() {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, RunOptions{State: st, Cache: cache,
			MinBackoff: 20 * time.Millisecond, MaxBackoff: 100 * time.Millisecond})
	}()
	return func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Run returned %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("Run did not stop")
		}
	}
}

func TestRegisterRefusals(t *testing.T) {
	cp := newControlPlane(t, "")
	ctx := context.Background()
	tests := []struct {
		name string
		opts func(dir string) RegisterOptions
		is   error
	}{
		{"wrong CA pin", func(dir string) RegisterOptions {
			return RegisterOptions{StateDir: dir, ControlPlane: cp.addr, Token: cp.bootstrapToken("a"),
				CACertSHA256: "00" + cp.pki.CACertSHA256()[2:], Docker: liveRestore(true)}
		}, nil},
		{"live-restore off", func(dir string) RegisterOptions {
			return RegisterOptions{StateDir: dir, ControlPlane: cp.addr, Token: cp.bootstrapToken("b"),
				CACertSHA256: cp.pki.CACertSHA256(), Docker: liveRestore(false)}
		}, ErrLiveRestoreOff},
		{"malformed pin", func(dir string) RegisterOptions {
			return RegisterOptions{StateDir: dir, ControlPlane: cp.addr, Token: cp.bootstrapToken("c"),
				CACertSHA256: "zz", Docker: liveRestore(true)}
		}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			err := Register(ctx, tt.opts(dir))
			if err == nil {
				t.Fatal("Register succeeded")
			}
			if tt.is != nil && !errors.Is(err, tt.is) {
				t.Errorf("got %v, want %v", err, tt.is)
			}
			if _, err := Load(dir); !errors.Is(err, ErrNotRegistered) {
				t.Errorf("state written after a refused registration: %v", err)
			}
		})
	}

	t.Run("registering twice is refused", func(t *testing.T) {
		st := cp.register(t, "twice")
		_ = st
		dir := t.TempDir()
		opts := RegisterOptions{StateDir: dir, ControlPlane: cp.addr, Token: cp.bootstrapToken("twice-b"),
			CACertSHA256: cp.pki.CACertSHA256(), Docker: liveRestore(true)}
		if err := Register(ctx, opts); err != nil {
			t.Fatal(err)
		}
		opts.Token = cp.bootstrapToken("twice-c")
		if err := Register(ctx, opts); err == nil {
			t.Fatal("second Register into the same state dir succeeded")
		}
	})
}

func TestRunLifecycle(t *testing.T) {
	cp := newControlPlane(t, "")
	st := cp.register(t, "vps-1")
	stop := startRun(t, st)
	cp.waitState(st.NodeID(), "online")
	stop()
	cp.waitState(st.NodeID(), "offline")
}

func TestRunSurvivesRejectionAndOutage(t *testing.T) {
	t.Run("rejected version keeps running", func(t *testing.T) {
		cp := newControlPlane(t, "0.3.0") // this test binary's agent version is "dev"
		st := cp.register(t, "vps-old")
		stop := startRun(t, st)
		cp.waitState(st.NodeID(), "rejected")
		stop()
	})

	t.Run("control plane down, then back", func(t *testing.T) {
		cp := newControlPlane(t, "")
		st := cp.register(t, "vps-2")
		addr := cp.addr
		cp.stop()
		stop := startRun(t, st)
		time.Sleep(300 * time.Millisecond) // several failed attempts
		cp.serve("", addr)
		cp.waitState(st.NodeID(), "online")
		stop()
	})
}

func TestAssignmentsReachTheCache(t *testing.T) {
	ctx := context.Background()
	cp := newControlPlane(t, "")
	st := cp.register(t, "vps-1")
	node := uuid.MustParse(st.NodeID())
	system := store.Actor{ID: store.SystemActorID, Kind: store.ActorSystem}
	app, _ := fixture.App(t, cp.st, cp.tenant, "api", "prod")
	work := []*agentv1.Workload{{AppId: app.String(), Provider: "docker",
		Secrets: []*bohuxv1.SecretRef{{Key: "DATABASE_URL", Handle: "openbao:db"}}}}

	cache, err := OpenCache(st)
	if err != nil {
		t.Fatal(err)
	}
	stop := startRunWith(t, st, cache)
	cp.waitState(st.NodeID(), "online")
	if _, err := cp.srv.Assign(ctx, cp.tenant, node, work, system); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return cache.Generation() == 1 })
	waitFor(t, func() bool { return cp.acked(node) == 1 })
	vals, err := st.OpenSecrets(cache.Current().GetWorkloads()[0])
	if err != nil || vals["DATABASE_URL"] != "postgres://u:hunter2@db/app" {
		t.Fatalf("OpenSecrets = %v, %v", vals, err)
	}
	stop()

	// Restart: the cache survives and the agent reports it, so nothing is re-sent;
	// a newer generation still arrives.
	again, err := OpenCache(st)
	if err != nil || again.Generation() != 1 {
		t.Fatalf("reopened cache: gen %d, %v", again.Generation(), err)
	}
	stop = startRunWith(t, st, again)
	defer stop()
	cp.waitState(st.NodeID(), "online")
	if _, err := cp.srv.Assign(ctx, cp.tenant, node, work, system); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return again.Generation() == 2 })
	waitFor(t, func() bool { return cp.acked(node) == 2 })
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met in time")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (cp *controlPlane) acked(node uuid.UUID) int64 {
	cp.t.Helper()
	var acked *int64
	err := cp.st.InTenant(context.Background(), cp.tenant, func(tx *store.Tx) error {
		return tx.QueryRow(context.Background(), `SELECT acked_generation FROM nodes_heads WHERE id = $1`,
			node).Scan(&acked)
	})
	if err != nil {
		cp.t.Fatal(err)
	}
	if acked == nil {
		return 0
	}
	return *acked
}

func TestReportsKeepTheNodeOnline(t *testing.T) {
	ctx := context.Background()
	cp := newControlPlane(t, "")
	cp.srv.SilenceTimeout = 400 * time.Millisecond

	runWith := func(st *State, cache *Cache, reports ReportSource) func() {
		rctx, cancel := context.WithCancel(ctx)
		done := make(chan error, 1)
		go func() {
			done <- Run(rctx, RunOptions{State: st, Cache: cache, Reports: reports,
				ReportInterval: 100 * time.Millisecond, MinBackoff: 20 * time.Millisecond})
		}()
		return func() {
			cancel()
			<-done
		}
	}

	t.Run("heartbeats alone keep a quiet agent online", func(t *testing.T) {
		st := cp.register(t, "quiet")
		cache, err := OpenCache(st)
		if err != nil {
			t.Fatal(err)
		}
		stop := runWith(st, cache, nil)
		defer stop()
		// Registration already says online; a stored report proves the stream is up.
		waitFor(t, func() bool {
			var at *time.Time
			err := cp.st.InTenant(ctx, cp.tenant, func(tx *store.Tx) error {
				return tx.QueryRow(ctx, `SELECT report_received_at FROM nodes_heads WHERE id = $1`,
					st.NodeID()).Scan(&at)
			})
			return err == nil && at != nil
		})
		revisions := func() int {
			var n int
			err := cp.st.InTenant(ctx, cp.tenant, func(tx *store.Tx) error {
				return tx.QueryRow(ctx, `SELECT count(*) FROM nodes_revisions WHERE resource_id = $1`,
					st.NodeID()).Scan(&n)
			})
			if err != nil {
				t.Fatal(err)
			}
			return n
		}
		before := revisions()
		time.Sleep(time.Second) // well past the silence timeout
		if after := revisions(); after != before {
			t.Fatalf("node state changed %d times while the agent was healthy", after-before)
		}
	})

	t.Run("reconciler reports reach the control plane", func(t *testing.T) {
		st := cp.register(t, "busy")
		cache, err := OpenCache(st)
		if err != nil {
			t.Fatal(err)
		}
		rec := &Reconciler{State: st, Cache: cache, Provider: newFake(), Interval: 50 * time.Millisecond}
		rctx, cancel := context.WithCancel(ctx)
		defer cancel()
		go func() { _ = rec.Run(rctx) }()
		stop := runWith(st, cache, rec)
		defer stop()
		cp.waitState(st.NodeID(), "online")
		node := uuid.MustParse(st.NodeID())
		app, _ := fixture.App(t, cp.st, cp.tenant, "api", "prod")
		work := []*agentv1.Workload{{AppId: app.String(), Provider: "fake",
			Runtime: &bohuxv1.Runtime{Replicas: 1}}}
		if _, err := cp.srv.Assign(ctx, cp.tenant, node, work,
			store.Actor{ID: store.SystemActorID, Kind: store.ActorSystem}); err != nil {
			t.Fatal(err)
		}
		waitFor(t, func() bool {
			var report *string
			err := cp.st.InTenant(ctx, cp.tenant, func(tx *store.Tx) error {
				return tx.QueryRow(ctx, `SELECT report::text FROM nodes_heads
					WHERE id = $1 AND report_generation = 1`, node).Scan(&report)
			})
			return err == nil && report != nil && strings.Contains(*report, "WORKLOAD_STATE_HEALTHY")
		})
	})
}
