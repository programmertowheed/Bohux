package deploy

import (
	"testing"

	"go.temporal.io/sdk/worker"
)

// TestReplay replays recorded histories of runs started by earlier versions of the
// workflow, so a change that would break a run in flight fails here (new-workflow
// skill). Each is a rollback notifying two channels, one of which fails:
//   - deploy-v1-notify-inline.json: notified inline, before ADR-0048;
//   - deploy-v2-notify-child.json and notify-v1.json: the run and its notification
//     child after ADR-0048.
func TestReplay(t *testing.T) {
	for _, file := range []string{
		"testdata/deploy-v1-notify-inline.json",
		"testdata/deploy-v2-notify-child.json",
		"testdata/notify-v1.json",
	} {
		t.Run(file, func(t *testing.T) {
			r := worker.NewWorkflowReplayer()
			r.RegisterWorkflow(Workflow)
			r.RegisterWorkflow(NotifyWorkflow)
			if err := r.ReplayWorkflowHistoryFromJSONFile(nil, file); err != nil {
				t.Fatal(err)
			}
		})
	}
}
