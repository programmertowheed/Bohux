// Package drill runs Bohux's restore drill on a weekly Temporal schedule (ADR-0051):
// a workflow that drills the newest archive the nightly backup wrote (ADR-0033) and
// records what it found. It shares the backup's task queue and worker, so both
// schedules are served by one worker that knows both workflow types.
package drill

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"time"

	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	"github.com/bohumix/bohux/internal/drill"
	backupwf "github.com/bohumix/bohux/internal/workflow/backup"
)

const (
	// ScheduleID names the weekly schedule.
	ScheduleID = "bohux-restore-drill-weekly"
	// WorkflowName is the workflow's type name in Temporal.
	WorkflowName = "DrillWorkflow"
	// WorkflowID is the ID each scheduled run starts with (Temporal appends the time).
	WorkflowID = "drill/weekly"
	// DefaultSchedule is Sunday 04:47 UTC, after the nightly archive has landed.
	DefaultSchedule = drill.DefaultSchedule
	// VerdictDir is the subdirectory of the backup directory verdicts are written to.
	VerdictDir = "drills"
)

// TaskQueue is the backup's queue: one worker serves both schedules (ADR-0051 item 1).
const TaskQueue = backupwf.TaskQueue

// nameLayout dates a verdict file, matching the archives' UTC layout.
const nameLayout = "20060102T150405Z.json"

var verdictName = regexp.MustCompile(`^\d{8}T\d{6}Z\.json$`)

// Activities holds the drill's configuration. None of it enters workflow history: the
// DSNs, the OpenBao token and the age identity stay in the worker (rule 3).
type Activities struct {
	// Config is what every run shares. Its Archive is set per run, to the newest
	// archive in Dir.
	Config drill.Config
	// Dir is the backup directory: its newest archive is drilled, and verdicts go to
	// its VerdictDir subdirectory.
	Dir string
	// Keep is how many verdicts stay, matching the archives' retention; zero means
	// backupwf.DefaultKeep.
	Keep int
	// Drill replaces drill.Run in tests.
	Drill func(context.Context, drill.Config) (*drill.Verdict, error)
	// Now replaces time.Now in tests.
	Now func() time.Time
}

// Workflow drills the newest archive. One activity: the drill's own teardown is what
// keeps the next run able to start, so there is nothing to compensate afterwards, and
// a verdict is recorded by the activity itself rather than by a second step that a
// failed drill would skip.
func Workflow(ctx workflow.Context) (drill.Verdict, error) {
	var a *Activities
	opts := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		// A restore of a real install's archive is the long part; the heartbeat is
		// what notices a dead worker before this expires.
		StartToCloseTimeout: 2 * time.Hour,
		HeartbeatTimeout:    time.Minute,
		RetryPolicy:         &temporal.RetryPolicy{MaximumAttempts: 2, InitialInterval: time.Minute},
	})
	var v drill.Verdict
	if err := workflow.ExecuteActivity(opts, a.RunDrill).Get(ctx, &v); err != nil {
		return v, fmt.Errorf("restore drill: %w", err)
	}
	return v, nil
}

// RunDrill drills the newest archive in Dir, records the verdict, and prunes older
// verdicts. A failed check is reported as non-retryable: re-drilling the same archive
// would reach the same verdict, so the retry is spent on a worker or database that was
// not there, not on a finding.
func (a *Activities) RunDrill(ctx context.Context) (drill.Verdict, error) {
	archive, err := backupwf.Newest(a.Dir)
	if err != nil {
		if errors.Is(err, backupwf.ErrNoArchives) {
			// Nothing to prove yet: a fresh install, or drills enabled before the
			// first nightly archive. Retrying within the hour would not change it.
			return drill.Verdict{}, temporal.NewNonRetryableApplicationError(
				"no archive to drill yet", "NoArchive", err)
		}
		return drill.Verdict{}, fmt.Errorf("find the newest archive: %w", err)
	}
	cfg := a.Config
	cfg.Archive = archive
	if cfg.Now == nil {
		cfg.Now = a.Now
	}

	stop := heartbeat(ctx)
	v, drillErr := a.drill()(ctx, cfg)
	stop()

	// The verdict is the point of the run, so it is recorded before any failure is
	// returned: an operator reads it to find out which check failed and why.
	if v != nil {
		if err := a.record(ctx, v); err != nil {
			return *v, err
		}
	}
	if drillErr == nil {
		return *v, nil
	}
	if v != nil && !v.OK() {
		return *v, temporal.NewNonRetryableApplicationError(
			"the archive did not pass the drill: "+fmt.Sprint(v.Failed()), "DrillFailed", drillErr)
	}
	// No verdict means the drill could not get as far as the archive: a database or
	// mount that was not reachable, or a configuration it refused. Worth one retry.
	return drill.Verdict{}, fmt.Errorf("drill %s: %w", archive, drillErr)
}

// record writes the verdict as JSON beside the archives and prunes older ones. It is
// the drill's own history: Temporal keeps the runs, this keeps the findings where an
// operator already looks.
func (a *Activities) record(ctx context.Context, v *drill.Verdict) error {
	dir := filepath.Join(a.Dir, VerdictDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("verdict directory: %w", err)
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("encode the verdict: %w", err)
	}
	name := a.now().UTC().Format(nameLayout)
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, append(b, '\n'), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	outcome := "passed"
	if !v.OK() {
		outcome = "failed"
	}
	slog.InfoContext(ctx, "restore drill recorded", "verdict", path, "outcome", outcome,
		"archive", v.Archive, "failed", v.Failed())
	return a.prune(ctx, dir)
}

// prune deletes the oldest verdicts beyond Keep, touching only files that are
// verdicts. The UTC name sorts lexically, as the archives' does.
func (a *Activities) prune(ctx context.Context, dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("read %s: %w", dir, err)
	}
	var verdicts []string
	for _, e := range entries {
		if e.Type().IsRegular() && verdictName.MatchString(e.Name()) {
			verdicts = append(verdicts, e.Name())
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(verdicts)))
	keep := a.Keep
	if keep <= 0 {
		keep = backupwf.DefaultKeep
	}
	if len(verdicts) <= keep {
		return nil
	}
	for _, n := range verdicts[keep:] {
		if err := os.Remove(filepath.Join(dir, n)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("prune %s: %w", n, err)
		}
	}
	slog.InfoContext(ctx, "drill verdicts pruned", "removed", len(verdicts)-keep, "kept", keep)
	return nil
}

func (a *Activities) drill() func(context.Context, drill.Config) (*drill.Verdict, error) {
	if a.Drill != nil {
		return a.Drill
	}
	return drill.Run
}

func (a *Activities) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

// heartbeat records a heartbeat every 10 s until stopped, so a dead worker is noticed
// within the heartbeat timeout while a long restore runs.
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
