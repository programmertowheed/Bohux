//go:build integration

package backup

import (
	"context"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"filippo.io/age"
	"github.com/google/uuid"
	"go.temporal.io/sdk/client"

	"github.com/bohumix/bohux/internal/backup"
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
	a := &Activities{Recipient: id.Recipient().String(), DatabaseURL: roDSN, DataDir: dataDir,
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

func TestScheduleRunsTheWorkflow(t *testing.T) {
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
	_ = RemoveSchedule(ctx, c)

	written := make(chan struct{}, 1)
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	acts := &Activities{Recipient: id.Recipient().String(), Dir: dir, Keep: 1,
		Write: fakeWrite(written)}
	s := Schedule{Cron: "0 0 1 1 *", TaskQueue: "bohux-backup-test-" + uuid.NewString()[:8]}
	runCtx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); Run(runCtx, c, s, acts) }()
	defer func() { stop(); <-done }()

	// Run creates the schedule; ensuring it again updates it in place.
	h := c.ScheduleClient().GetHandle(ctx, ScheduleID)
	for {
		if _, err := h.Describe(ctx); err == nil {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("the schedule never appeared")
		case <-time.After(200 * time.Millisecond):
		}
	}
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

	if err := h.Trigger(ctx, client.ScheduleTriggerOptions{}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-written:
	case <-ctx.Done():
		t.Fatal("the triggered backup never ran")
	}
	if err := RemoveSchedule(ctx, c); err != nil {
		t.Fatal(err)
	}
	if err := RemoveSchedule(ctx, c); err != nil {
		t.Fatalf("removing a missing schedule: %v", err)
	}
}

// fakeWrite stands in for backup.Write and signals each call.
func fakeWrite(called chan<- struct{}) func(context.Context, io.Writer, []age.Recipient, backup.Source) (*backup.Manifest, error) {
	return func(_ context.Context, w io.Writer, _ []age.Recipient, _ backup.Source) (*backup.Manifest, error) {
		_, err := w.Write([]byte("archive"))
		select {
		case called <- struct{}{}:
		default:
		}
		return &backup.Manifest{Migration: 1}, err
	}
}
