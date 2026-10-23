//go:build integration

package drill

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filippo.io/age"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/bohumix/bohux/internal/auth"
	"github.com/bohumix/bohux/internal/backup"
	"github.com/bohumix/bohux/internal/pki"
	"github.com/bohumix/bohux/internal/secrets/openbao"
	"github.com/bohumix/bohux/internal/store"
	"github.com/bohumix/bohux/internal/store/fixture"
	"github.com/bohumix/bohux/internal/store/storetest"
)

const secretValue = "postgres://u:hunter2@db/app"

// liveMount is the dev server's own KV v2 mount, which a drill must never touch.
const liveMount = "secret"

// bao returns a client for the OpenBao dev server, skipping without one.
func bao(t *testing.T, mount string) *openbao.Client {
	t.Helper()
	addr, token := os.Getenv("BOHUX_TEST_OPENBAO_ADDR"), os.Getenv("BOHUX_TEST_OPENBAO_TOKEN")
	if addr == "" || token == "" {
		t.Skip("BOHUX_TEST_OPENBAO_ADDR and BOHUX_TEST_OPENBAO_TOKEN are not set")
	}
	return &openbao.Client{Addr: addr, Token: token, Mount: mount}
}

// newMount enables a fresh KV v2 mount, as the installer does for the drill.
func newMount(t *testing.T, c *openbao.Client) *openbao.Client {
	t.Helper()
	name := "drill-" + uuid.NewString()[:8]
	req, err := http.NewRequest(http.MethodPost, c.Addr+"/v1/sys/mounts/"+name,
		strings.NewReader(`{"type": "kv", "options": {"version": "2"}}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Vault-Token", c.Token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode/100 != 2 {
		t.Fatalf("enable mount %s: %v %v", name, resp, err)
	}
	_ = resp.Body.Close()
	return &openbao.Client{Addr: c.Addr, Token: c.Token, Mount: name}
}

// superuser connects as the role that can create roles and databases.
func superuser(t *testing.T) *pgx.Conn {
	t.Helper()
	conn, err := pgx.Connect(context.Background(), storetest.AdminDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	return conn
}

// drillRoles creates the two roles ADR-0051 item 3 describes and returns their DSNs:
// the scratch role creates, owns and migrates the database, and the admin role loads
// rows past row-level security. Neither gets a grant in any live database.
func drillRoles(t *testing.T) (scratchDSN, adminDSN string) {
	t.Helper()
	ctx := context.Background()
	conn := superuser(t)
	suffix := uuid.NewString()[:8]
	scratch, admin := "drill_"+suffix, "drill_admin_"+suffix
	for _, q := range []string{
		`CREATE ROLE ` + identifier(scratch) + ` LOGIN PASSWORD 'pw' CREATEDB`,
		`CREATE ROLE ` + identifier(admin) + ` LOGIN PASSWORD 'pw' BYPASSRLS`,
		// Restore loads rows past triggers and foreign keys, which needs this GUC.
		`GRANT SET ON PARAMETER session_replication_role TO ` + identifier(admin),
	} {
		if _, err := conn.Exec(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	t.Cleanup(func() {
		for _, q := range []string{
			`REVOKE SET ON PARAMETER session_replication_role FROM ` + identifier(admin),
			`DROP ROLE IF EXISTS ` + identifier(scratch),
			`DROP ROLE IF EXISTS ` + identifier(admin),
		} {
			if _, err := conn.Exec(context.Background(), q); err != nil {
				t.Logf("cleanup %s: %v", q, err)
			}
		}
	})
	return asRole(t, scratch), asRole(t, admin)
}

// asRole points the test's admin DSN at another role, keeping its host and database.
func asRole(t *testing.T, role string) string {
	t.Helper()
	u, err := url.Parse(storetest.AdminDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	u.User = url.UserPassword(role, "pw")
	return u.String()
}

// install is a live Bohux whose state an archive was taken from.
type install struct {
	archive  string
	identity age.Identity
	tenant   uuid.UUID
	secrets  int
	caPin    string
}

// installOpts shapes the install an archive is taken from, so a test can produce the
// archive of a broken install as well as a sound one.
type installOpts struct {
	// ownerToken gives the install a credential someone could administer it with.
	ownerToken bool
	// secretValues backs the secret's values up with it. False is an install whose
	// backup ran without OpenBao configured: the metadata is in the archive and the
	// values are not, which is exactly the kind of archive a drill exists to catch.
	secretValues bool
}

// newInstall builds a control plane with state and writes an archive of it, as the
// nightly backup does.
func newInstall(t *testing.T, src *openbao.Client, opts installOpts) install {
	t.Helper()
	ctx := context.Background()
	appDSN := storetest.NewAppDSN(t)
	if err := store.MigrateDSN(ctx, appDSN); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(ctx, appDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)

	tenant := fixture.Tenant(t, st, "acme")
	node := fixture.Node(t, st, tenant, "vps-1")
	app, env := fixture.DeployableApp(t, st, tenant, node, "api", 1)
	fixture.Release(t, st, tenant, app, "ready", true)

	// A secret with history: version 1 replaced by version 2, as a rotation leaves it.
	secret := uuid.Must(uuid.NewV7())
	path := openbao.Path(tenant, secret)
	t.Cleanup(func() { _ = src.Destroy(context.Background(), path) })
	if _, err := src.Put(ctx, path, secretValue+"-old"); err != nil {
		t.Fatal(err)
	}
	version, err := src.Put(ctx, path, secretValue)
	if err != nil {
		t.Fatal(err)
	}
	system := store.Audit{Actor: store.Actor{ID: store.SystemActorID, Kind: store.ActorSystem}, Action: "test"}
	if err := st.InTenant(ctx, tenant, func(tx *store.Tx) error {
		a := system
		a.ResourceType = "bohux.v1.Secret"
		_, err := tx.CreateWithID(ctx, "secret_refs", secret,
			[]store.Col{{Name: "environment_id", Value: env}, {Name: "key", Value: "DATABASE_URL"},
				{Name: "handle", Value: openbao.Handle(tenant, secret)}},
			[]store.Col{{Name: "value_version", Value: version}}, a)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if opts.ownerToken {
		if err := st.InTenant(ctx, tenant, func(tx *store.Tx) error {
			a := system
			a.ResourceType = "bohux.v1.ApiToken"
			// Tenant-wide, as the installer's first token is (ADR-0023).
			_, _, _, err := auth.CreateAPIToken(ctx, tx, uuid.Nil, auth.RoleOwner, "installer", nil, a)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}

	dataDir := t.TempDir()
	authority, err := pki.LoadOrCreate(dataDir, nil)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	name := filepath.Join(t.TempDir(), "bohux-20261023t031700z.backup")
	f, err := os.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	values := src
	if !opts.secretValues {
		values = nil
	}
	_, err = backup.Write(ctx, f, []age.Recipient{identity.Recipient()},
		backup.Source{AdminDSN: storetest.AdminDSNFor(t, appDSN), Bao: values, DataDir: dataDir})
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		t.Fatal(err)
	}
	return install{archive: name, identity: identity, tenant: tenant, secrets: 1, caPin: authority.CACertSHA256()}
}

// scratchDatabases lists the drill databases on the server, which must be none once
// a run has returned.
func scratchDatabases(t *testing.T) []string {
	t.Helper()
	rows, err := superuser(t).Query(context.Background(),
		`SELECT datname FROM pg_database WHERE datname LIKE 'bohux_drill_%' ORDER BY datname`)
	if err != nil {
		t.Fatal(err)
	}
	names, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	return names
}

// assertCleanedUp is the invariant every drill has to keep, pass or fail: no scratch
// database, no secret left in the drill mount, no temporary data directory.
func assertCleanedUp(t *testing.T, cfg Config, v *Verdict) {
	t.Helper()
	if left := scratchDatabases(t); len(left) > 0 {
		t.Errorf("scratch databases left behind: %v", left)
	}
	if cfg.Bao != nil {
		paths, err := walk(context.Background(), cfg.Bao, "")
		if err != nil {
			t.Fatalf("list the drill mount: %v", err)
		}
		if len(paths) > 0 {
			t.Errorf("secrets left in the drill mount: %v", paths)
		}
	}
	if v != nil {
		for _, c := range v.Checks {
			if c.Name == "cleanup" && !c.OK {
				t.Errorf("cleanup check failed: %s", c.Detail)
			}
		}
	}
	matches, err := filepath.Glob(filepath.Join(os.TempDir(), "bohux-drill-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) > 0 {
		t.Errorf("temporary data directories left behind: %v", matches)
	}
}

// TestDrill covers the failure paths before the passing one (CLAUDE.md rule 5): a
// drill that cannot prove the archive must still leave nothing behind, because the
// state it creates is what the next run needs to be empty.
func TestDrill(t *testing.T) {
	ctx := context.Background()
	src := bao(t, liveMount)
	scratchDSN, adminDSN := drillRoles(t)
	inst := newInstall(t, src, installOpts{ownerToken: true, secretValues: true})

	cfg := func(t *testing.T) Config {
		t.Helper()
		return Config{
			Archive: inst.archive, Identities: []age.Identity{inst.identity},
			ScratchDSN: scratchDSN, AdminDSN: adminDSN,
			Bao: newMount(t, src), LiveMount: liveMount,
		}
	}

	t.Run("a truncated archive fails and leaves nothing", func(t *testing.T) {
		whole, err := os.ReadFile(inst.archive)
		if err != nil {
			t.Fatal(err)
		}
		cut := filepath.Join(t.TempDir(), "cut.backup")
		if err := os.WriteFile(cut, whole[:len(whole)/2], 0o600); err != nil {
			t.Fatal(err)
		}
		c := cfg(t)
		c.Archive = cut
		v, err := Run(ctx, c)
		if err == nil {
			t.Fatal("Run() = nil, want a failure on a truncated archive")
		}
		if v == nil {
			t.Fatal("Run() returned no verdict to record")
		}
		if got := v.Failed(); len(got) == 0 || got[0] != "restore" {
			t.Errorf("Failed() = %v, want the restore check to have failed", got)
		}
		assertCleanedUp(t, c, v)
	})

	t.Run("a wrong identity fails and leaves nothing", func(t *testing.T) {
		other, err := age.GenerateX25519Identity()
		if err != nil {
			t.Fatal(err)
		}
		c := cfg(t)
		c.Identities = []age.Identity{other}
		v, err := Run(ctx, c)
		if err == nil || !strings.Contains(err.Error(), "decrypt") {
			t.Fatalf("Run() = %v, want a decryption failure", err)
		}
		assertCleanedUp(t, c, v)
	})

	t.Run("an archive nobody could administer fails the owner-token check", func(t *testing.T) {
		noToken := newInstall(t, src, installOpts{secretValues: true})
		c := cfg(t)
		c.Archive, c.Identities = noToken.archive, []age.Identity{noToken.identity}
		v, err := Run(ctx, c)
		if !errors.Is(err, ErrFailed) {
			t.Fatalf("Run() = %v, want ErrFailed", err)
		}
		if got := strings.Join(v.Failed(), ","); got != "owner-token" {
			t.Errorf("Failed() = %q, want just the owner-token check", got)
		}
		// The rest of the archive is sound, so the drill reports a finding rather
		// than refusing to look: everything else passed.
		if v.Tenants != 1 || v.Secrets != 1 || v.CAPin == "" {
			t.Errorf("verdict stopped early: %+v", v)
		}
		assertCleanedUp(t, c, v)
	})

	t.Run("an archive that holds secrets is refused without a mount to put them in", func(t *testing.T) {
		// Restore itself refuses this before any check runs: it will not drop the
		// archive's secret values on the floor.
		c := cfg(t)
		c.Bao = nil
		v, err := Run(ctx, c)
		if err == nil || !strings.Contains(err.Error(), "no OpenBao is configured") {
			t.Fatalf("Run() = %v, want restore to refuse the archive", err)
		}
		if got := strings.Join(v.Failed(), ","); got != "restore" {
			t.Errorf("Failed() = %q, want the restore check", got)
		}
		assertCleanedUp(t, c, v)
	})

	t.Run("an archive whose values were never backed up fails the secrets check", func(t *testing.T) {
		// The backup ran without OpenBao: Postgres says the secret is at version 2
		// and no value is in the archive. Restore is happy; the install would not be.
		noValues := newInstall(t, src, installOpts{ownerToken: true})
		c := cfg(t)
		c.Archive, c.Identities = noValues.archive, []age.Identity{noValues.identity}
		v, err := Run(ctx, c)
		if !errors.Is(err, ErrFailed) {
			t.Fatalf("Run() = %v, want ErrFailed", err)
		}
		if got := strings.Join(v.Failed(), ","); got != "secrets" {
			t.Errorf("Failed() = %q, want the secrets check", got)
		}
		if v.Secrets != 0 {
			t.Errorf("Secrets = %d, want none proved", v.Secrets)
		}
		assertCleanedUp(t, c, v)
	})

	t.Run("a drill with no mount cannot prove an archive's secret refs", func(t *testing.T) {
		noValues := newInstall(t, src, installOpts{ownerToken: true})
		c := cfg(t)
		c.Archive, c.Identities, c.Bao = noValues.archive, []age.Identity{noValues.identity}, nil
		v, err := Run(ctx, c)
		if !errors.Is(err, ErrFailed) {
			t.Fatalf("Run() = %v, want ErrFailed", err)
		}
		if got := strings.Join(v.Failed(), ","); got != "secrets" {
			t.Errorf("Failed() = %q, want the secrets check", got)
		}
		assertCleanedUp(t, c, v)
	})

	t.Run("a good archive passes every check", func(t *testing.T) {
		c := cfg(t)
		v, err := Run(ctx, c)
		if err != nil {
			t.Fatalf("Run() = %v\nchecks: %+v", err, v.Checks)
		}
		if !v.OK() {
			t.Fatalf("checks failed: %+v", v.Checks)
		}
		switch {
		case v.Migration == 0:
			t.Error("verdict records no migration")
		case v.Tables < 20:
			t.Errorf("Tables = %d, want every table of a real schema", v.Tables)
		case v.Rows == 0:
			t.Error("Rows = 0, want the restored rows counted")
		case v.Tenants != 1:
			t.Errorf("Tenants = %d, want 1", v.Tenants)
		case v.OwnerTokens != 1:
			t.Errorf("OwnerTokens = %d, want 1", v.OwnerTokens)
		case v.Secrets != inst.secrets:
			t.Errorf("Secrets = %d, want %d", v.Secrets, inst.secrets)
		case v.CAPin != inst.caPin:
			t.Errorf("CAPin = %q, want the live install's %q", v.CAPin, inst.caPin)
		case !v.CAExpires.After(time.Now()):
			t.Errorf("CAExpires = %v, want a CA the agents would still trust", v.CAExpires)
		case v.PerTable["tenants_heads"] != 1:
			t.Errorf("PerTable[tenants_heads] = %d, want 1", v.PerTable["tenants_heads"])
		case v.FinishedAt.Before(v.StartedAt):
			t.Errorf("FinishedAt %v is before StartedAt %v", v.FinishedAt, v.StartedAt)
		case !strings.HasPrefix(v.Database, "bohux_drill_"):
			t.Errorf("Database = %q, want a recognisable scratch name", v.Database)
		}
		assertCleanedUp(t, c, v)
	})

	t.Run("two runs in a row both pass", func(t *testing.T) {
		// The second proves the drill left its mount and its database clean enough
		// for restore, which refuses anything that is not empty, to run again.
		c := cfg(t)
		for i := range 2 {
			if _, err := Run(ctx, c); err != nil {
				t.Fatalf("run %d: %v", i+1, err)
			}
		}
		assertCleanedUp(t, c, nil)
	})

	t.Run("the live mount is never touched", func(t *testing.T) {
		// The live secret is still readable at its version after every run above.
		paths, err := walk(ctx, src, "bohux/"+inst.tenant.String()+"/")
		if err != nil {
			t.Fatal(err)
		}
		if len(paths) != 1 {
			t.Fatalf("live mount holds %v, want the one secret the install wrote", paths)
		}
		got, err := src.Get(ctx, paths[0], 2)
		if err != nil || got != secretValue {
			t.Errorf("live secret = %q, %v, want it untouched", got, err)
		}
	})
}

// A verdict must carry no secret value, whatever the archive held: it is written to a
// file and logged (rule 3).
func TestVerdictHoldsNoValues(t *testing.T) {
	ctx := context.Background()
	src := bao(t, liveMount)
	scratchDSN, adminDSN := drillRoles(t)
	inst := newInstall(t, src, installOpts{ownerToken: true, secretValues: true})
	v, err := Run(ctx, Config{
		Archive: inst.archive, Identities: []age.Identity{inst.identity},
		ScratchDSN: scratchDSN, AdminDSN: adminDSN,
		Bao: newMount(t, src), LiveMount: liveMount,
	})
	if err != nil {
		t.Fatalf("Run() = %v", err)
	}
	rendered := fmt.Sprintf("%+v", *v)
	if strings.Contains(rendered, "hunter2") {
		t.Error("the verdict carries a secret value")
	}
	if !bytes.Contains([]byte(rendered), []byte(v.CAPin)) {
		t.Error("the verdict does not carry the CA pin an operator compares")
	}
}
