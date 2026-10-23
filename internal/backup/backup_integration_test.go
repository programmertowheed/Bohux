//go:build integration

package backup

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"filippo.io/age"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/bohumix/bohux/internal/pki"
	"github.com/bohumix/bohux/internal/secrets/openbao"
	"github.com/bohumix/bohux/internal/store"
	"github.com/bohumix/bohux/internal/store/fixture"
	"github.com/bohumix/bohux/internal/store/storetest"
)

const (
	firstValue  = "postgres://u:first-hunter2@db/app"
	secondValue = "postgres://u:second-hunter2@db/app"
)

// bao returns a client for the dev server on mount, skipping without one.
func bao(t *testing.T, mount string) *openbao.Client {
	t.Helper()
	addr, token := os.Getenv("BOHUX_TEST_OPENBAO_ADDR"), os.Getenv("BOHUX_TEST_OPENBAO_TOKEN")
	if addr == "" || token == "" {
		t.Skip("BOHUX_TEST_OPENBAO_ADDR and BOHUX_TEST_OPENBAO_TOKEN are not set")
	}
	return &openbao.Client{Addr: addr, Token: token, Mount: mount}
}

// newMount enables a fresh KV v2 mount, as an empty OpenBao on a new host would have.
func newMount(t *testing.T, c *openbao.Client) *openbao.Client {
	t.Helper()
	name := "restore-" + uuid.NewString()[:8]
	req, _ := http.NewRequest(http.MethodPost, c.Addr+"/v1/sys/mounts/"+name,
		strings.NewReader(`{"type": "kv", "options": {"version": "2"}}`))
	req.Header.Set("X-Vault-Token", c.Token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode/100 != 2 {
		t.Fatalf("enable mount: %v %v", resp, err)
	}
	_ = resp.Body.Close()
	return &openbao.Client{Addr: c.Addr, Token: c.Token, Mount: name}
}

func open(t *testing.T, dsn string) *store.Store {
	t.Helper()
	st, err := store.Open(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	return st
}

// counts returns every table's row count, seen by a role that bypasses RLS.
func counts(t *testing.T, adminDSN string) map[string]int {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, adminDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	tables, err := listTables(ctx, conn)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]int{}
	for _, tb := range tables {
		var n int
		if err := conn.QueryRow(ctx, "SELECT count(*) FROM public."+pgx.Identifier{tb}.Sanitize()).Scan(&n); err != nil {
			t.Fatal(err)
		}
		out[tb] = n
	}
	return out
}

func TestBackupRestore(t *testing.T) {
	ctx := context.Background()
	src := bao(t, "secret")

	// A control plane with state: a tenant, an App and its Release, a node, a secret
	// with two values (the first destroyed), and its node CA.
	appDSN := storetest.NewAppDSN(t)
	if err := store.MigrateDSN(ctx, appDSN); err != nil {
		t.Fatal(err)
	}
	st := open(t, appDSN)
	tenant := fixture.Tenant(t, st, "acme")
	node := fixture.Node(t, st, tenant, "vps-1")
	app, env := fixture.DeployableApp(t, st, tenant, node, "api", 1)
	fixture.Release(t, st, tenant, app, "ready", true)
	secret := uuid.Must(uuid.NewV7())
	path := openbao.Path(tenant, secret)
	t.Cleanup(func() { _ = src.Destroy(ctx, path) })
	if _, err := src.Put(ctx, path, firstValue); err != nil {
		t.Fatal(err)
	}
	v2, err := src.Put(ctx, path, secondValue)
	if err != nil {
		t.Fatal(err)
	}
	if err := src.DestroyVersions(ctx, path, []int{1}); err != nil {
		t.Fatal(err)
	}
	handle := openbao.Handle(tenant, secret)
	if err := st.InTenant(ctx, tenant, func(tx *store.Tx) error {
		_, err := tx.CreateWithID(ctx, "secret_refs", secret,
			[]store.Col{{Name: "environment_id", Value: env}, {Name: "key", Value: "DATABASE_URL"}, {Name: "handle", Value: handle}},
			[]store.Col{{Name: "value_version", Value: v2}},
			store.Audit{Actor: store.Actor{ID: store.SystemActorID, Kind: store.ActorSystem}, Action: "test", ResourceType: "bohux.v1.Secret"})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	dataDir := t.TempDir()
	if _, err := pki.LoadOrCreate(dataDir, nil); err != nil {
		t.Fatal(err)
	}

	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	adminDSN := storetest.AdminDSNFor(t, appDSN)
	var archive bytes.Buffer
	m, err := Write(ctx, &archive, []age.Recipient{identity.Recipient()}, Source{AdminDSN: adminDSN, Bao: src, DataDir: dataDir})
	if err != nil {
		t.Fatal(err)
	}
	if m.Migration == 0 || len(m.Tables) < 10 {
		t.Errorf("manifest = %+v", m)
	}
	if bytes.Contains(archive.Bytes(), []byte("hunter2")) || bytes.Contains(archive.Bytes(), []byte("manifest")) {
		t.Fatal("the archive is readable without the identity")
	}

	// A fresh host: an empty database, an empty OpenBao mount, an empty data dir.
	newApp := storetest.NewAppDSN(t)
	newAdmin := storetest.AdminDSNFor(t, newApp)
	newBao := newMount(t, src)
	newDir := t.TempDir()
	target := Target{AdminDSN: newAdmin, AppDSN: newApp, Bao: newBao, DataDir: newDir}

	t.Run("a wrong identity cannot restore", func(t *testing.T) {
		other, _ := age.GenerateX25519Identity()
		if _, err := Restore(ctx, bytes.NewReader(archive.Bytes()), []age.Identity{other}, target); err == nil {
			t.Fatal("restored with the wrong identity")
		}
	})

	if _, err := Restore(ctx, bytes.NewReader(archive.Bytes()), []age.Identity{identity}, target); err != nil {
		t.Fatal(err)
	}

	t.Run("every row is back", func(t *testing.T) {
		before, after := counts(t, adminDSN), counts(t, newAdmin)
		for table, n := range before {
			if after[table] != n {
				t.Errorf("%s: %d rows after restore, %d before", table, after[table], n)
			}
		}
	})
	t.Run("the secret resolves to the recorded version", func(t *testing.T) {
		r := &openbao.Resolver{Store: open(t, newApp), Bao: newBao}
		got, err := r.Resolve(ctx, tenant, []string{handle})
		if err != nil || got[handle] != secondValue {
			t.Errorf("Resolve = %v, %v", got, err)
		}
		if _, err := newBao.Get(ctx, path, 1); !errors.Is(err, openbao.ErrNotFound) {
			t.Errorf("destroyed version 1 = %v, want gone again", err)
		}
	})
	t.Run("the node CA is the same", func(t *testing.T) {
		for _, f := range []string{"node-ca.crt", "node-ca.key", "assignment-signing.key"} {
			a, _ := os.ReadFile(filepath.Join(dataDir, "pki", f))
			b, err := os.ReadFile(filepath.Join(newDir, "pki", f))
			if err != nil || !bytes.Equal(a, b) {
				t.Errorf("%s differs after restore: %v", f, err)
			}
		}
		if _, err := pki.LoadOrCreate(newDir, nil); err != nil {
			t.Errorf("restored CA does not load: %v", err)
		}
	})
	t.Run("the restored database serves", func(t *testing.T) {
		if err := store.MigrateDSN(ctx, newApp); err != nil {
			t.Fatalf("migrate after restore: %v", err)
		}
		var slug string
		if err := open(t, newApp).InTenant(ctx, tenant, func(tx *store.Tx) error {
			return tx.QueryRow(ctx, `SELECT slug FROM apps_heads WHERE id = $1`, app).Scan(&slug)
		}); err != nil || slug != "api" {
			t.Errorf("app after restore = %q, %v", slug, err)
		}
	})
	t.Run("a target that is not empty is refused", func(t *testing.T) {
		_, err := Restore(ctx, bytes.NewReader(archive.Bytes()), []age.Identity{identity}, target)
		if !errors.Is(err, ErrNotEmpty) {
			t.Errorf("second restore = %v, want ErrNotEmpty", err)
		}
	})
}
