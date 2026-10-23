// Package backup runs Bohux's nightly backup as a Temporal schedule (ADR-0033): a
// workflow that writes an encrypted archive (ADR-0029) to a local directory, then
// prunes the oldest archives beyond the retention count.
package backup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"time"

	"filippo.io/age"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	"github.com/bohumix/bohux/internal/backup"
	"github.com/bohumix/bohux/internal/secrets/openbao"
)

const (
	// TaskQueue is the queue the backup worker polls.
	TaskQueue = "bohux-backup"
	// ScheduleID names the nightly schedule.
	ScheduleID = "bohux-backup-nightly"
	// WorkflowName is the workflow's type name in Temporal (the deploy package also
	// has a function called Workflow).
	WorkflowName = "BackupWorkflow"
	// WorkflowID is the ID each scheduled run starts with (Temporal appends the time).
	WorkflowID = "backup/nightly"
	// DefaultSchedule is 03:17 UTC: off the hour, when nobody deploys.
	DefaultSchedule = "17 3 * * *"
	// DefaultKeep is how many archives stay after a successful backup.
	DefaultKeep = 7
	// DefaultDir is where archives go in the install (its own volume).
	DefaultDir = "/var/lib/bohux-backups"
)

// nameLayout is the archive file name; the UTC time sorts lexically.
const nameLayout = "bohux-20060102T150405Z.backup"

var archiveName = regexp.MustCompile(`^bohux-\d{8}T\d{6}Z\.backup$`)

// Result is what one run wrote and removed. No values, only names and sizes.
type Result struct {
	File      string
	Bytes     int64
	Migration int64
	Pruned    []string
}

// Activities holds the backup's configuration. None of it enters workflow history:
// the DSN and the OpenBao token stay in the worker (rule 3).
type Activities struct {
	// Recipient is the age public key archives are encrypted to.
	Recipient string
	// DatabaseURL is the read-only BYPASSRLS role's DSN (ADR-0033 item 3).
	DatabaseURL string
	// Bao is OpenBao, nil when the control plane runs without secrets.
	Bao *openbao.Client
	// DataDir is the control plane's data directory (the node CA).
	DataDir string
	// Dir is where archives are written.
	Dir string
	// Keep is how many archives stay; zero means DefaultKeep.
	Keep int
	// Write replaces backup.Write in tests.
	Write func(context.Context, io.Writer, []age.Recipient, backup.Source) (*backup.Manifest, error)
	// Now replaces time.Now in tests.
	Now func() time.Time
}

// Workflow writes one archive, then prunes. A failed write never prunes: the older
// archives are all that is left.
func Workflow(ctx workflow.Context) (Result, error) {
	var a *Activities
	write := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: time.Hour,
		HeartbeatTimeout:    time.Minute,
		RetryPolicy:         &temporal.RetryPolicy{MaximumAttempts: 3, InitialInterval: time.Minute},
	})
	var res Result
	if err := workflow.ExecuteActivity(write, a.WriteBackup).Get(ctx, &res); err != nil {
		return Result{}, fmt.Errorf("write backup: %w", err)
	}
	prune := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: time.Minute,
		RetryPolicy:         &temporal.RetryPolicy{MaximumAttempts: 3},
	})
	if err := workflow.ExecuteActivity(prune, a.PruneBackups).Get(ctx, &res.Pruned); err != nil {
		return res, fmt.Errorf("prune backups: %w", err)
	}
	return res, nil
}

// WriteBackup writes a new archive under Dir. It goes to a .partial file first and
// is renamed only once complete and synced, so a crash or failure never leaves a
// file that looks like a backup. Every attempt writes a new name, so a retry never
// overwrites a finished archive.
func (a *Activities) WriteBackup(ctx context.Context) (Result, error) {
	to, err := age.ParseX25519Recipient(a.Recipient)
	if err != nil {
		return Result{}, temporal.NewNonRetryableApplicationError("backup recipient is not an age public key", "BadRecipient", err)
	}
	if err := os.MkdirAll(a.Dir, 0o700); err != nil {
		return Result{}, fmt.Errorf("backup directory: %w", err)
	}
	name := a.now().UTC().Format(nameLayout)
	final := filepath.Join(a.Dir, name)
	partial := final + ".partial"
	f, err := os.OpenFile(partial, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return Result{}, fmt.Errorf("create %s: %w", partial, err)
	}
	ok := false
	defer func() {
		if !ok {
			_ = f.Close()
			_ = os.Remove(partial)
		}
	}()
	stop := heartbeat(ctx)
	m, err := a.write()(ctx, f, []age.Recipient{to},
		backup.Source{AdminDSN: a.DatabaseURL, Bao: a.Bao, DataDir: a.DataDir})
	stop()
	if err != nil {
		return Result{}, fmt.Errorf("backup: %w", err)
	}
	if err := f.Sync(); err != nil {
		return Result{}, fmt.Errorf("sync %s: %w", partial, err)
	}
	st, err := f.Stat()
	if err != nil {
		return Result{}, fmt.Errorf("stat %s: %w", partial, err)
	}
	if err := f.Close(); err != nil {
		return Result{}, fmt.Errorf("close %s: %w", partial, err)
	}
	if err := os.Rename(partial, final); err != nil {
		return Result{}, fmt.Errorf("rename %s: %w", partial, err)
	}
	ok = true
	slog.InfoContext(ctx, "backup written", "file", final, "bytes", st.Size(), "migration", m.Migration)
	return Result{File: final, Bytes: st.Size(), Migration: m.Migration}, nil
}

// PruneBackups deletes the oldest archives beyond Keep, and leftover .partial files
// of earlier runs. Files that are not Bohux archives are never touched.
func (a *Activities) PruneBackups(ctx context.Context) ([]string, error) {
	entries, err := os.ReadDir(a.Dir)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", a.Dir, err)
	}
	var archives, pruned []string
	for _, e := range entries {
		n := e.Name()
		switch {
		case !e.Type().IsRegular():
		case archiveName.MatchString(n):
			archives = append(archives, n)
		case archiveName.MatchString(trimPartial(n)):
			pruned = append(pruned, n)
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(archives)))
	keep := a.Keep
	if keep <= 0 {
		keep = DefaultKeep
	}
	if len(archives) > keep {
		pruned = append(pruned, archives[keep:]...)
	}
	for _, n := range pruned {
		if err := os.Remove(filepath.Join(a.Dir, n)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("prune %s: %w", n, err)
		}
	}
	if len(pruned) > 0 {
		slog.InfoContext(ctx, "backups pruned", "files", pruned, "kept", min(len(archives), keep))
	}
	return pruned, nil
}

func trimPartial(n string) string {
	const suffix = ".partial"
	if len(n) > len(suffix) && n[len(n)-len(suffix):] == suffix {
		return n[:len(n)-len(suffix)]
	}
	return ""
}

func (a *Activities) write() func(context.Context, io.Writer, []age.Recipient, backup.Source) (*backup.Manifest, error) {
	if a.Write != nil {
		return a.Write
	}
	return backup.Write
}

func (a *Activities) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

// heartbeat records a heartbeat every 10 s until stopped, so a dead worker is noticed
// within the heartbeat timeout while a long archive streams.
func heartbeat(ctx context.Context) (stop func()) {
	if !activity.IsActivity(ctx) {
		return func() {}
	}
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(10 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				activity.RecordHeartbeat(ctx)
			}
		}
	}()
	return func() { close(done) }
}
