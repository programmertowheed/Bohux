//go:build integration

package backup

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"filippo.io/age"
	"github.com/google/uuid"
	"go.temporal.io/sdk/client"

	"github.com/bohumix/bohux/internal/store"
	"github.com/bohumix/bohux/internal/store/fixture"
	"github.com/bohumix/bohux/internal/store/storetest"
)

// backupRole creates the read-only BYPASSRLS role the way the install bundle does
// (deploy/install/postgres-init/10-bohux.sh): before the migrations run, so only the
// default privileges grant it the tables. It returns the role's DSN.
func backupRole(t *testing.T, appDSN string) string {
	t.Helper()
	ctx := context.Background()
	app, err := url.Parse(appDSN)
	if err != nil {
		t.Fatal(err)
	}
	owner := app.User.Username()
	name := owner + "_backup"
	pass := uuid.NewString()
	admin := storetest.Open(t, storetest.AdminDSN(t))
	if _, err := admin.ExecContext(ctx, `CREATE ROLE `+name+` LOGIN BYPASSRLS PASSWORD '`+pass+`'`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.ExecContext(ctx, `DROP OWNED BY `+name)
		_, _ = admin.ExecContext(ctx, `DROP ROLE IF EXISTS `+name)
	})
	db := storetest.Open(t, storetest.AdminDSNFor(t, appDSN))
	if _, err := db.ExecContext(ctx, `ALTER DEFAULT PRIVILEGES FOR ROLE `+owner+
		` IN SCHEMA public GRANT SELECT ON TABLES TO `+name); err != nil {
		t.Fatal(err)
	}
	u := *app
	u.User = url.UserPassword(name, pass)
	return u.String()
}

func TestWriteBackupWithTheReadOnlyRole(t *testing.T) {
	ctx := context.Background()
	appDSN := storetest.NewAppDSN(t)
	roDSN := backupRole(t, appDSN)
	if err := store.MigrateDSN(ctx, appDSN); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(ctx, appDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	fixture.Tenant(t, st, "acme")

	// The role reads past RLS but writes nothing.
	ro := storetest.Open(t, roDSN)
	var tenants int
	if err := ro.QueryRowContext(ctx, `SELECT count(*) FROM tenants_heads`).Scan(&tenants); err != nil || tenants != 1 {
		t.Fatalf("tenants seen by the backup role = %d, %v; want 1", tenants, err)
	}
	if _, err := ro.ExecContext(ctx, `DELETE FROM tenants_heads`); err == nil {
		t.Fatal("the backup role deleted rows")
	}

	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	dataDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dataDir, "pki"), 0o700); err != nil {
		t.Fatal(err)
	}
	a := &Activities{Recipients: []string{id.Recipient().String()}, DatabaseURL: roDSN, DataDir: dataDir,
		Dir: filepath.Join(t.TempDir(), "backups")}
	res, err := a.WriteBackup(ctx)
	if err != nil {
		t.Fatal(err)
	}
	latest, err := store.LatestMigration()
	if err != nil {
		t.Fatal(err)
	}
	if res.Migration != latest || res.Bytes == 0 {
		t.Fatalf("result %+v, want migration %d and a non-empty archive", res, latest)
	}
}

func TestEnsureAndRemoveSchedule(t *testing.T) {
	addr := os.Getenv("BOHUX_TEST_TEMPORAL_ADDRESS")
	if addr == "" {
		t.Skip("BOHUX_TEST_TEMPORAL_ADDRESS is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	c, err := client.Dial(client.Options{HostPort: addr})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	// Its own schedule ID: the name is server-wide, and other packages' tests run in
	// parallel against the same Temporal.
	s := Schedule{Cron: "0 0 1 1 *", TaskQueue: "bohux-backup-test-" + uuid.NewString()[:8],
		ID: "bohux-backup-test-" + uuid.NewString()[:8]}
	t.Cleanup(func() { _ = RemoveSchedule(context.Background(), c, s) })
	if err := EnsureSchedule(ctx, c, s); err != nil {
		t.Fatal(err)
	}
	h := c.ScheduleClient().GetHandle(ctx, s.ID)
	if _, err := h.Describe(ctx); err != nil {
		t.Fatalf("the schedule was not created: %v", err)
	}

	// Ensuring again updates in place rather than failing on the existing schedule.
	s.Cron = "0 0 2 1 *"
	if err := EnsureSchedule(ctx, c, s); err != nil {
		t.Fatal(err)
	}
	desc, err := h.Describe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := desc.Schedule.Spec.CronExpressions; len(got) != 1 || got[0] != s.Cron {
		// The server may normalise cron into calendars; then the action is what we check.
		if len(desc.Schedule.Spec.Calendars) == 0 {
			t.Fatalf("schedule spec = %+v, want cron %q", desc.Schedule.Spec, s.Cron)
		}
	}
	if got := desc.Schedule.Action.(*client.ScheduleWorkflowAction).TaskQueue; got != s.TaskQueue {
		t.Errorf("task queue = %q, want %q", got, s.TaskQueue)
	}

	if err := RemoveSchedule(ctx, c, s); err != nil {
		t.Fatal(err)
	}
	if err := RemoveSchedule(ctx, c, s); err != nil {
		t.Fatalf("removing a missing schedule: %v", err)
	}
}
