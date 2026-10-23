package deploy

import (
	"context"
	"errors"
	"sort"
	"sync"
	"testing"

	"github.com/stretchr/testify/mock"
	"go.temporal.io/sdk/testsuite"
)

// TestNotifyEnd: after a rollback the outcome goes to every channel that wants it, a
// channel that keeps failing is retried and then dropped, and the Deployment's result
// is the same either way (ADR-0038). The rolled-back path is tested first (rule 5).
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
			s := &script{last: goodRelease, waitErr: tt.waitErr}
			s.register(env)
			var a *Activities
			var mu sync.Mutex
			listed := State("")
			attempts := map[string]int{}
			env.OnActivity(a.NotificationChannels, mock.Anything, mock.Anything).Return(
				func(_ context.Context, in NotifyInput) ([]string, error) {
					listed = in.State
					return []string{"slack", "broken"}, nil
				})
			env.OnActivity(a.NotifyChannel, mock.Anything, mock.Anything).Return(
				func(_ context.Context, in NotifyInput) error {
					mu.Lock()
					defer mu.Unlock()
					attempts[in.ChannelID]++
					if in.State != tt.want || in.StartedBy != "api_token" {
						t.Errorf("notified %+v", in)
					}
					if in.ChannelID == "broken" {
						return errors.New("chat service down")
					}
					return nil
				})
			env.ExecuteWorkflow(Workflow, input())
			if err := env.GetWorkflowError(); err != nil {
				t.Fatal(err)
			}
			var res Result
			if err := env.GetWorkflowResult(&res); err != nil || res.State != tt.want {
				t.Fatalf("result %+v, %v; want %s whatever the channels do", res, err, tt.want)
			}
			if listed != tt.want {
				t.Errorf("listed channels for %q", listed)
			}
			got := []string{}
			for ch := range attempts {
				got = append(got, ch)
			}
			sort.Strings(got)
			if len(got) != 2 || attempts["slack"] != 1 || attempts["broken"] != 5 {
				t.Errorf("attempts = %v; want slack once and broken five times", attempts)
			}
		})
	}
}
