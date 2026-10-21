//go:build integration

package openbao

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/bohumix/bohux/internal/store"
	"github.com/bohumix/bohux/internal/store/fixture"
	"github.com/bohumix/bohux/internal/store/storetest"
)

// client returns a client for the OpenBao dev server named by BOHUX_TEST_OPENBAO_ADDR
// and BOHUX_TEST_OPENBAO_TOKEN, skipping the test when it is unset.
func client(t *testing.T) *Client {
	t.Helper()
	addr, token := os.Getenv("BOHUX_TEST_OPENBAO_ADDR"), os.Getenv("BOHUX_TEST_OPENBAO_TOKEN")
	if addr == "" || token == "" {
		t.Skip("BOHUX_TEST_OPENBAO_ADDR and BOHUX_TEST_OPENBAO_TOKEN are not set")
	}
	return &Client{Addr: addr, Token: token, Mount: "secret"}
}

func TestClient(t *testing.T) {
	ctx := context.Background()
	c := client(t)
	if err := c.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	path := "bohux-test/" + uuid.NewString()
	t.Cleanup(func() { _ = c.Destroy(ctx, path) })

	v1, err := c.Put(ctx, path, "first")
	if err != nil || v1 != 1 {
		t.Fatalf("Put = %d, %v", v1, err)
	}
	v2, err := c.Put(ctx, path, "second")
	if err != nil || v2 != 2 {
		t.Fatalf("Put = %d, %v", v2, err)
	}
	for v, want := range map[int]string{1: "first", 2: "second"} {
		if got, err := c.Get(ctx, path, v); err != nil || got != want {
			t.Errorf("Get(v%d) = %q, %v; want %q", v, got, err, want)
		}
	}
	if _, err := c.Get(ctx, path, 9); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing version = %v, want ErrNotFound", err)
	}
	if err := c.Destroy(ctx, path); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Get(ctx, path, 1); !errors.Is(err, ErrNotFound) {
		t.Errorf("after Destroy = %v, want ErrNotFound", err)
	}
	if err := c.Destroy(ctx, path); err != nil {
		t.Errorf("second Destroy = %v, want nil", err)
	}

	bad := &Client{Addr: c.Addr, Token: "wrong", Mount: "secret"}
	if _, err := bad.Put(ctx, path, "hunter2"); err == nil || strings.Contains(err.Error(), "hunter2") {
		t.Errorf("bad token: %v", err)
	}
}

func TestResolver(t *testing.T) {
	ctx := context.Background()
	c := client(t)
	dsn := storetest.NewAppDSN(t)
	if err := store.MigrateDSN(ctx, dsn); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	tenant, other := fixture.Tenant(t, st, "acme"), fixture.Tenant(t, st, "other")
	_, env := fixture.App(t, st, tenant, "api", "prod")

	id := uuid.Must(uuid.NewV7())
	path := Path(tenant, id)
	t.Cleanup(func() { _ = c.Destroy(ctx, path) })
	v1, err := c.Put(ctx, path, "recorded")
	if err != nil {
		t.Fatal(err)
	}
	handle := Handle(tenant, id)
	if err := st.InTenant(ctx, tenant, func(tx *store.Tx) error {
		_, err := tx.CreateWithID(ctx, "secret_refs", id,
			[]store.Col{{Name: "environment_id", Value: env}, {Name: "key", Value: "DB"}, {Name: "handle", Value: handle}},
			[]store.Col{{Name: "value_version", Value: v1}},
			store.Audit{Actor: store.Actor{ID: store.SystemActorID, Kind: store.ActorSystem}, Action: "test", ResourceType: "bohux.v1.Secret"})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	// A newer value that no revision recorded must not be used.
	if _, err := c.Put(ctx, path, "never recorded"); err != nil {
		t.Fatal(err)
	}
	r := &Resolver{Store: st, Bao: c}
	got, err := r.Resolve(ctx, tenant, []string{handle})
	if err != nil || got[handle] != "recorded" {
		t.Errorf("Resolve = %v, %v; want the recorded version", got, err)
	}
	if _, err := r.Resolve(ctx, other, []string{handle}); err == nil {
		t.Error("another tenant resolved the handle")
	}
	if _, err := r.Resolve(ctx, tenant, []string{Handle(tenant, uuid.Must(uuid.NewV7()))}); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("unknown handle = %v, want ErrNotFound", err)
	}
}
