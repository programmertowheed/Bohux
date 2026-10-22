package deploy

import (
	"path/filepath"
	"testing"

	"go.temporal.io/sdk/worker"
)

// TestReplay replays recorded histories of earlier versions of the deploy workflow
// against the code as it is now, so a change that would break runs still open fails
// here instead of in production (the new-workflow skill). Record new ones with
// TestRecordHistory before changing the workflow's commands.
func TestReplay(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("testdata", "history-*.json"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no recorded histories: %v", err)
	}
	for _, f := range files {
		t.Run(filepath.Base(f), func(t *testing.T) {
			r := worker.NewWorkflowReplayer()
			r.RegisterWorkflow(Workflow)
			r.RegisterWorkflow(NotifyWorkflow)
			if err := r.ReplayWorkflowHistoryFromJSONFile(nil, f); err != nil {
				t.Fatal(err)
			}
		})
	}
}
