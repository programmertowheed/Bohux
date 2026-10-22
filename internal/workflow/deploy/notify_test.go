package deploy

import (
	"context"
	"errors"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"
)

// TestNotifyEnd: a run hands its outcome to a notification child named after the
// Deployment and returns without waiting for it, so the App's next deploy or rollback
// is not refused while a channel keeps failing (ADR-0048). The rolled-back path is
// tested first (rule 5).
func TestNotifyEnd(t *testing.T) {
	for _, tt := range []struct {
		name    string
		waitErr map[string]error
		want    State
	}{
		{"rolled back", map[string]error{newRelease: nonRetryable(ErrTypeHealthTimeout)}, StateRolledBack},
		{"succeeded", nil, StateSucceeded},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var ts testsuite.WorkflowTestSuite
			env := ts.NewTestWorkflowEnvironment()
			// The child outlasts any health gate. The environment waits for detached
			// children, so the child finishing at all means it was abandoned, not
			// terminated; finishing after the run closed means the run did not wait.
			s := &script{last: goodRelease, waitErr: tt.waitErr, notifyDelay: 24 * time.Hour,
				runClosed: env.IsWorkflowCompleted}
			s.register(env)
			var started []string
			env.SetOnChildWorkflowStartedListener(func(info *workflow.Info, _ workflow.Context, _ converter.EncodedValues) {
				started = append(started, info.WorkflowExecution.ID)
			})
			env.ExecuteWorkflow(Workflow, input())
			if err := env.GetWorkflowError(); err != nil {
				t.Fatal(err)
			}
			var res Result
			if err := env.GetWorkflowResult(&res); err != nil || res.State != tt.want {
				t.Fatalf("result %+v, %v; want %s", res, err, tt.want)
			}
			if len(started) != 1 || started[0] != NotifyWorkflowID("deployment-1") {
				t.Fatalf("children started = %v, want one %s", started, NotifyWorkflowID("deployment-1"))
			}
			if len(s.notified) != 1 {
				t.Fatalf("notification child finished %d times, want 1", len(s.notified))
			}
			if !s.closedFirst[0] {
				t.Error("the run was still open when its notifications finished: it waited for them")
			}
			if got := s.notified[0]; got.State != tt.want || got.DeploymentID != "deployment-1" ||
				got.TenantID != "tenant-1" || got.StartedBy != "api_token" {
				t.Errorf("child input %+v", got)
			}
		})
	}
}

// TestNotifyWorkflow: the outcome goes to every channel that wants it, and a channel
// that keeps failing is retried and then dropped without failing the run (ADR-0038).
func TestNotifyWorkflow(t *testing.T) {
	var ts testsuite.WorkflowTestSuite
	env := ts.NewTestWorkflowEnvironment()
	env.RegisterActivity(&Activities{})
	var a *Activities
	var mu sync.Mutex
	attempts := map[string]int{}
	in := NotifyInput{TenantID: "tenant-1", DeploymentID: "deployment-1", State: StateRolledBack,
		Failure: &Failure{Reason: ReasonHealthTimeout}, StartedBy: "api_token"}
	env.OnActivity(a.NotificationChannels, mock.Anything, in).Return([]string{"slack", "broken"}, nil)
	env.OnActivity(a.NotifyChannel, mock.Anything, mock.Anything).Return(
		func(_ context.Context, c NotifyInput) error {
			mu.Lock()
			defer mu.Unlock()
			attempts[c.ChannelID]++
			if c.State != in.State || c.StartedBy != in.StartedBy || c.Failure == nil {
				t.Errorf("notified %+v", c)
			}
			if c.ChannelID == "broken" {
				return errors.New("chat service down")
			}
			return nil
		})
	env.ExecuteWorkflow(NotifyWorkflow, in)
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("a failing channel failed the run: %v", err)
	}
	got := []string{}
	for ch := range attempts {
		got = append(got, ch)
	}
	sort.Strings(got)
	if len(got) != 2 || attempts["slack"] != 1 || attempts["broken"] != 5 {
		t.Errorf("attempts = %v; want slack once and broken five times", attempts)
	}
}
