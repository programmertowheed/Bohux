//go:build integration

package maintenance

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"filippo.io/age"
	"github.com/google/uuid"
	"go.temporal.io/sdk/client"

	"github.com/bohumix/bohux/internal/backup"
	"github.com/bohumix/bohux/internal/drill"
	backupwf "github.com/bohumix/bohux/internal/workflow/backup"
	drillwf "github.com/bohumix/bohux/internal/workflow/drill"
)

func dial(t *testing.T) client.Client {
	t.Helper()
	addr := os.Getenv("BOHUX_TEST_TEMPORAL_ADDRESS")
	if addr == "" {
		t.Skip("BOHUX_TEST_TEMPORAL_ADDRESS is not set")
	}
	c, err := client.Dial(client.Options{HostPort: addr})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	return c
}

// ids gives this test its own server-wide schedule names: the real IDs are fixed, and
// other packages' tests run in parallel against the same Temporal.
func ids(t *testing.T) (backupwf.Schedule, drillwf.Schedule) {
	t.Helper()
	suffix := uuid.NewString()[:8]
	queue := "bohux-maintenance-test-" + suffix
	return backupwf.Schedule{Cron: "0 0 1 1 *", TaskQueue: queue, ID: "bohux-backup-test-" + suffix},
		drillwf.Schedule{Cron: "0 0 2 1 *", TaskQueue: queue, ID: "bohux-drill-test-" + suffix}
}

func recipient(t *testing.T) string {
	t.Helper()
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	return id.Recipient().String()
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

// Both schedules fire workflows onto one task queue, so one worker has to serve both
// types. Two workers each knowing only their own would fail each other's tasks, which
// is the whole reason this package exists — and the reason this test triggers both.
func TestOneWorkerServesBothSchedules(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	c := dial(t)
	backupSchedule, drillSchedule := ids(t)
	t.Cleanup(func() {
		_ = backupwf.RemoveSchedule(context.Background(), c, backupSchedule)
		_ = drillwf.RemoveSchedule(context.Background(), c, drillSchedule)
	})

	dir := t.TempDir()
	// An archive for the drill to pick, as the nightly backup would have left it.
	if err := os.WriteFile(filepath.Join(dir, "bohux-20261025T031700Z.backup"), []byte("archive"), 0o600); err != nil {
		t.Fatal(err)
	}

	backedUp := make(chan struct{}, 1)
	drilled := make(chan string, 1)
	cfg := Config{
		Backups: &backupwf.Activities{Recipients: []string{recipient(t)}, Dir: dir, Keep: 2,
			Write: fakeWrite(backedUp)},
		BackupSchedule: backupSchedule,
		Drills: &drillwf.Activities{Dir: dir, Keep: 2,
			Drill: func(_ context.Context, dc drill.Config) (*drill.Verdict, error) {
				v := &drill.Verdict{Archive: dc.Archive, Migration: 25}
				v.Checks = []drill.Check{{Name: "restore", OK: true, Detail: "29 tables"}}
				select {
				case drilled <- dc.Archive:
				default:
				}
				return v, nil
			}},
		DrillSchedule: drillSchedule,
	}

	runCtx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); Run(runCtx, c, cfg) }()
	defer func() { stop(); <-done }()

	backups := c.ScheduleClient().GetHandle(ctx, backupSchedule.ID)
	drills := c.ScheduleClient().GetHandle(ctx, drillSchedule.ID)
	for _, h := range []client.ScheduleHandle{backups, drills} {
		waitFor(t, ctx, func() bool { _, err := h.Describe(ctx); return err == nil }, "the schedule to appear")
	}

	if err := backups.Trigger(ctx, client.ScheduleTriggerOptions{}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-backedUp:
	case <-ctx.Done():
		t.Fatal("the triggered backup never ran")
	}

	if err := drills.Trigger(ctx, client.ScheduleTriggerOptions{}); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-drilled:
		if want := filepath.Join(dir, "bohux-20261025T031700Z.backup"); got != want {
			t.Errorf("drilled %q, want %q", got, want)
		}
	case <-ctx.Done():
		t.Fatal("the triggered drill never ran: the shared worker did not serve its type")
	}

	// The verdict is beside the archives, where an operator already looks. The drill
	// signals before the activity records it, so wait for the file rather than racing
	// the write.
	waitFor(t, ctx, func() bool {
		entries, err := os.ReadDir(filepath.Join(dir, drillwf.VerdictDir))
		return err == nil && len(entries) == 1
	}, "the verdict to be recorded")
}

// Switching a schedule off removes it, so it stops firing into a worker that no
// longer serves it.
func TestNilActivitiesRemoveTheirSchedules(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	c := dial(t)
	backupSchedule, drillSchedule := ids(t)
	t.Cleanup(func() {
		_ = backupwf.RemoveSchedule(context.Background(), c, backupSchedule)
		_ = drillwf.RemoveSchedule(context.Background(), c, drillSchedule)
	})

	if err := backupwf.EnsureSchedule(ctx, c, backupSchedule); err != nil {
		t.Fatal(err)
	}
	if err := drillwf.EnsureSchedule(ctx, c, drillSchedule); err != nil {
		t.Fatal(err)
	}

	// Both off: Run removes both and returns rather than serving anything.
	Run(ctx, c, Config{BackupSchedule: backupSchedule, DrillSchedule: drillSchedule})

	for name, h := range map[string]client.ScheduleHandle{
		"backup": c.ScheduleClient().GetHandle(ctx, backupSchedule.ID),
		"drill":  c.ScheduleClient().GetHandle(ctx, drillSchedule.ID),
	} {
		if _, err := h.Describe(ctx); err == nil {
			t.Errorf("the %s schedule is still there after being switched off", name)
		}
	}
}

// Drills off but backups on leaves the backup schedule alone and removes the drill's.
func TestDrillsOffKeepsBackups(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	c := dial(t)
	backupSchedule, drillSchedule := ids(t)
	t.Cleanup(func() {
		_ = backupwf.RemoveSchedule(context.Background(), c, backupSchedule)
		_ = drillwf.RemoveSchedule(context.Background(), c, drillSchedule)
	})

	if err := drillwf.EnsureSchedule(ctx, c, drillSchedule); err != nil {
		t.Fatal(err)
	}
	cfg := Config{
		Backups: &backupwf.Activities{Recipients: []string{recipient(t)}, Dir: t.TempDir(), Keep: 1,
			Write: fakeWrite(nil)},
		BackupSchedule: backupSchedule,
		DrillSchedule:  drillSchedule,
	}
	runCtx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); Run(runCtx, c, cfg) }()
	defer func() { stop(); <-done }()

	waitFor(t, ctx, func() bool {
		_, err := c.ScheduleClient().GetHandle(ctx, backupSchedule.ID).Describe(ctx)
		return err == nil
	}, "the backup schedule to appear")
	if _, err := c.ScheduleClient().GetHandle(ctx, drillSchedule.ID).Describe(ctx); err == nil {
		t.Error("the drill schedule is still there with drills off")
	}
}

func waitFor(t *testing.T, ctx context.Context, ok func() bool, what string) {
	t.Helper()
	for {
		if ok() {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("timed out waiting for %s", what)
		case <-time.After(200 * time.Millisecond):
		}
	}
}
