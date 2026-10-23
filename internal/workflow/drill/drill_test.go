package drill

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"go.temporal.io/sdk/testsuite"

	"github.com/bohumix/bohux/internal/drill"
)

var at = time.Date(2026, 10, 25, 4, 47, 0, 0, time.UTC)

// archives puts finished archive names in dir, as the nightly backup leaves them.
func archives(t *testing.T, dir string, names ...string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("archive"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// verdicts lists the verdict files written under dir.
func verdicts(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(dir, VerdictDir))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

// failing is a drill that restores the archive and finds a problem with it.
func failing(calls *int) func(context.Context, drill.Config) (*drill.Verdict, error) {
	return func(_ context.Context, cfg drill.Config) (*drill.Verdict, error) {
		*calls++
		v := &drill.Verdict{Archive: cfg.Archive, Migration: 25}
		v.Checks = []drill.Check{
			{Name: "restore", OK: true, Detail: "29 tables"},
			{Name: "owner-token", Detail: "no live owner token"},
		}
		return v, drill.ErrFailed
	}
}

// A failed check is a finding about the archive, not a flaky step. The workflow has to
// fail so Temporal shows it, the verdict has to be on disk so an operator can read
// which check failed, and the activity must not be retried: re-drilling the same
// archive reaches the same verdict and costs another full restore.
func TestWorkflowFailedCheckIsRecordedAndNotRetried(t *testing.T) {
	dir := archives(t, filepath.Join(t.TempDir(), "backups"), "bohux-20261025T031700Z.backup")
	calls := 0
	acts := &Activities{Dir: dir, Keep: 7, Drill: failing(&calls), Now: func() time.Time { return at }}

	var ts testsuite.WorkflowTestSuite
	env := ts.NewTestWorkflowEnvironment()
	env.RegisterActivity(acts)
	env.ExecuteWorkflow(Workflow)

	if env.GetWorkflowError() == nil {
		t.Fatal("workflow succeeded; want the failed check to fail it")
	}
	if calls != 1 {
		t.Errorf("the drill ran %d times, want 1: a finding is not retryable", calls)
	}
	got := verdicts(t, dir)
	if len(got) != 1 || got[0] != "20261025T044700Z.json" {
		t.Fatalf("verdicts = %v, want the run's verdict recorded", got)
	}
	b, err := os.ReadFile(filepath.Join(dir, VerdictDir, got[0]))
	if err != nil {
		t.Fatal(err)
	}
	var v drill.Verdict
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatalf("the recorded verdict is not JSON: %v", err)
	}
	if v.OK() || !slices.Equal(v.Failed(), []string{"owner-token"}) {
		t.Errorf("recorded verdict = %+v, want the failed check named", v)
	}
}

// Nothing to drill is not a failure to retry: a fresh install, or drills switched on
// before the first nightly archive exists.
func TestWorkflowWithNoArchive(t *testing.T) {
	dir := archives(t, filepath.Join(t.TempDir(), "backups"))
	calls := 0
	acts := &Activities{Dir: dir, Drill: failing(&calls), Now: func() time.Time { return at }}

	var ts testsuite.WorkflowTestSuite
	env := ts.NewTestWorkflowEnvironment()
	env.RegisterActivity(acts)
	env.ExecuteWorkflow(Workflow)

	err := env.GetWorkflowError()
	if err == nil {
		t.Fatal("workflow succeeded with no archive to drill")
	}
	if calls != 0 {
		t.Errorf("the drill ran %d times with no archive", calls)
	}
	if got := verdicts(t, dir); got != nil {
		t.Errorf("verdicts = %v, want none: there was nothing to drill", got)
	}
}

// A drill that could not get as far as a verdict is infrastructure, not a finding:
// Postgres or the mount was not there. That is worth the one retry.
func TestWorkflowRetriesWhenThereIsNoVerdict(t *testing.T) {
	dir := archives(t, filepath.Join(t.TempDir(), "backups"), "bohux-20261025T031700Z.backup")
	calls := 0
	acts := &Activities{Dir: dir, Now: func() time.Time { return at },
		Drill: func(context.Context, drill.Config) (*drill.Verdict, error) {
			calls++
			return nil, errors.New("connect as the scratch role: connection refused")
		}}

	var ts testsuite.WorkflowTestSuite
	env := ts.NewTestWorkflowEnvironment()
	env.RegisterActivity(acts)
	env.ExecuteWorkflow(Workflow)

	if env.GetWorkflowError() == nil {
		t.Fatal("workflow succeeded; want the infrastructure failure")
	}
	if calls != 2 {
		t.Errorf("the drill ran %d times, want the policy's 2 attempts", calls)
	}
	if got := verdicts(t, dir); got != nil {
		t.Errorf("verdicts = %v, want none: there was no verdict to record", got)
	}
}

func TestWorkflowDrillsTheNewestArchiveAndRecordsIt(t *testing.T) {
	dir := archives(t, filepath.Join(t.TempDir(), "backups"),
		"bohux-20261018T031700Z.backup", "bohux-20261025T031700Z.backup",
		"bohux-20261024T031700Z.backup.partial", "notes.txt")
	var drilled string
	acts := &Activities{Dir: dir, Keep: 7, Now: func() time.Time { return at },
		Drill: func(_ context.Context, cfg drill.Config) (*drill.Verdict, error) {
			drilled = cfg.Archive
			v := &drill.Verdict{Archive: cfg.Archive, Migration: 25, Rows: 1841, CAPin: "d78e3a7b"}
			v.Checks = []drill.Check{{Name: "restore", OK: true, Detail: "29 tables"}}
			return v, nil
		}}

	var ts testsuite.WorkflowTestSuite
	env := ts.NewTestWorkflowEnvironment()
	env.RegisterActivity(acts)
	env.ExecuteWorkflow(Workflow)

	if err := env.GetWorkflowError(); err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(dir, "bohux-20261025T031700Z.backup"); drilled != want {
		t.Errorf("drilled %q, want the newest finished archive %q", drilled, want)
	}
	var v drill.Verdict
	if err := env.GetWorkflowResult(&v); err != nil {
		t.Fatal(err)
	}
	if !v.OK() || v.Rows != 1841 || v.CAPin != "d78e3a7b" {
		t.Errorf("workflow result = %+v", v)
	}
	got := verdicts(t, dir)
	if len(got) != 1 {
		t.Fatalf("verdicts = %v, want one", got)
	}
	st, err := os.Stat(filepath.Join(dir, VerdictDir, got[0]))
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Errorf("verdict mode = %v, %v; want 0600", st, err)
	}
}

func TestPruneVerdicts(t *testing.T) {
	tests := []struct {
		name  string
		keep  int
		files []string
		want  []string
	}{
		{
			name:  "keeps the newest and leaves other files alone",
			keep:  2,
			files: []string{"20261011T044700Z.json", "20261018T044700Z.json", "20261025T044700Z.json", "notes.txt"},
			want:  []string{"20261018T044700Z.json", "20261025T044700Z.json", "notes.txt"},
		},
		{
			name:  "zero keep means the archives' default",
			keep:  0,
			files: []string{"20261025T044700Z.json"},
			want:  []string{"20261025T044700Z.json"},
		},
		{
			name:  "nothing to prune",
			keep:  2,
			files: []string{"20261025T044700Z.json"},
			want:  []string{"20261025T044700Z.json"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			sub := filepath.Join(dir, VerdictDir)
			if err := os.MkdirAll(sub, 0o700); err != nil {
				t.Fatal(err)
			}
			for _, f := range tt.files {
				if err := os.WriteFile(filepath.Join(sub, f), nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			a := &Activities{Dir: dir, Keep: tt.keep}
			if err := a.prune(context.Background(), sub); err != nil {
				t.Fatal(err)
			}
			entries, _ := os.ReadDir(sub)
			var got []string
			for _, e := range entries {
				got = append(got, e.Name())
			}
			if !slices.Equal(got, tt.want) {
				t.Fatalf("left %v, want %v", got, tt.want)
			}
		})
	}
}

// The drill schedule has to be on the backup's queue, or a worker polling it would
// receive drill tasks it cannot run (ADR-0051 item 1).
func TestScheduleDefaults(t *testing.T) {
	var s Schedule
	if s.cron() != "47 4 * * 0" || s.cron() != DefaultSchedule {
		t.Errorf("cron() = %q, want the weekly default", s.cron())
	}
	if s.Queue() != TaskQueue {
		t.Errorf("Queue() = %q, want the backup's queue %q", s.Queue(), TaskQueue)
	}
	s = Schedule{Cron: "0 5 * * 1", TaskQueue: "test"}
	if s.cron() != "0 5 * * 1" || s.Queue() != "test" {
		t.Errorf("overrides ignored: %q %q", s.cron(), s.Queue())
	}
}
