package deploy

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
)

const (
	newRelease  = "release-new"
	goodRelease = "release-good"
)

// script is what the mocked activities do in one test case, and what they saw.
type script struct {
	recordErr error
	last      string
	// assignErr and waitErr are keyed by Release ("" = remove the workload).
	assignErr map[string]error
	waitErr   map[string]error
	// waitDelay delays the new Release's health gate, in workflow time.
	waitDelay time.Duration

	states  []StateChange
	assigns []AssignInput
	waits   []WaitInput
}

func (s *script) register(env *testsuite.TestWorkflowEnvironment) {
	var a *Activities
	env.RegisterActivity(&Activities{})
	env.OnActivity(a.RecordStart, mock.Anything, mock.Anything).Return(
		func(context.Context, Input) error { return s.recordErr })
	env.OnActivity(a.LastHealthyRelease, mock.Anything, mock.Anything).Return(
		func(context.Context, LastHealthyInput) (string, error) { return s.last, nil })
	env.OnActivity(a.SetState, mock.Anything, mock.Anything).Return(
		func(_ context.Context, c StateChange) error { s.states = append(s.states, c); return nil })
	env.OnActivity(a.AssignRelease, mock.Anything, mock.Anything).Return(
		func(_ context.Context, in AssignInput) (Assigned, error) {
			s.assigns = append(s.assigns, in)
			return Assigned{NodeID: "node-1", Generation: uint64(len(s.assigns))}, s.assignErr[in.ReleaseID]
		})
	wait := func(_ context.Context, in WaitInput) error {
		s.waits = append(s.waits, in)
		return s.waitErr[in.ReleaseID]
	}
	isNew := mock.MatchedBy(func(in WaitInput) bool { return in.ReleaseID == newRelease })
	isOther := mock.MatchedBy(func(in WaitInput) bool { return in.ReleaseID != newRelease })
	env.OnActivity(a.WaitHealthy, mock.Anything, isNew).After(s.waitDelay).Return(wait)
	env.OnActivity(a.WaitHealthy, mock.Anything, isOther).Return(wait)
}

func nonRetryable(typ string) error {
	return temporal.NewNonRetryableApplicationError("test: "+typ, typ, nil)
}

// stateList is the states written, in order, with their failure reasons.
func stateList(cs []StateChange) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = string(c.State)
		if c.Failure != nil {
			out[i] += ":" + c.Failure.Reason
		}
	}
	return out
}

func releases(as []AssignInput) []string {
	out := make([]string, len(as))
	for i, a := range as {
		out[i] = a.ReleaseID
	}
	return out
}

func waitReleases(ws []WaitInput) []string {
	out := make([]string, len(ws))
	for i, w := range ws {
		out[i] = w.ReleaseID
	}
	return out
}

func input() Input {
	return Input{
		TenantID: "tenant-1", DeploymentID: "deployment-1", AppID: "app-1",
		EnvironmentID: "env-1", ReleaseID: newRelease,
		Actor: Actor{ID: "actor-1", Kind: "api_token"}, HealthTimeout: 3 * time.Minute,
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestCompensation is the rollback path's test, written before the forward path's
// activities (CLAUDE.md rule 5, ADR-0010): a deploy whose Release never turns healthy
// reassigns the last healthy Release.
func TestCompensation(t *testing.T) {
	tests := []struct {
		name        string
		s           script
		cancelAfter time.Duration
		want        Result
		wantStates  []string
		wantAssigns []string
		wantWaits   []string
	}{
		{
			name: "never healthy rolls back to the last healthy release",
			s: script{last: goodRelease, waitErr: map[string]error{
				newRelease: nonRetryable(ErrTypeHealthTimeout)}},
			want:        Result{State: StateRolledBack, Failure: &Failure{Reason: ReasonHealthTimeout}},
			wantStates:  []string{"applying", "health_gate", "compensating:HEALTH_TIMEOUT", "rolled_back:HEALTH_TIMEOUT"},
			wantAssigns: []string{newRelease, goodRelease},
			wantWaits:   []string{newRelease, goodRelease},
		},
		{
			name: "failed workload rolls back to the last healthy release",
			s: script{last: goodRelease, waitErr: map[string]error{
				newRelease: nonRetryable(ErrTypeWorkloadFailed)}},
			want:        Result{State: StateRolledBack, Failure: &Failure{Reason: ReasonWorkloadFailed}},
			wantStates:  []string{"applying", "health_gate", "compensating:WORKLOAD_FAILED", "rolled_back:WORKLOAD_FAILED"},
			wantAssigns: []string{newRelease, goodRelease},
			wantWaits:   []string{newRelease, goodRelease},
		},
		{
			name: "first deploy that fails removes its workload",
			s: script{waitErr: map[string]error{
				newRelease: nonRetryable(ErrTypeWorkloadFailed)}},
			want:        Result{State: StateFailed, Failure: &Failure{Reason: ReasonWorkloadFailed}},
			wantStates:  []string{"applying", "health_gate", "compensating:WORKLOAD_FAILED", "failed:WORKLOAD_FAILED"},
			wantAssigns: []string{newRelease, ""},
			wantWaits:   []string{newRelease},
		},
		{
			name: "assign error compensates",
			s: script{last: goodRelease, assignErr: map[string]error{
				newRelease: nonRetryable(ErrTypeInvalid)}},
			want:        Result{State: StateRolledBack, Failure: &Failure{Reason: ReasonInvalidRelease}},
			wantStates:  []string{"applying", "compensating:INVALID_RELEASE", "rolled_back:INVALID_RELEASE"},
			wantAssigns: []string{newRelease, goodRelease},
			wantWaits:   []string{goodRelease},
		},
		{
			name: "compensation that never turns healthy fails the deployment",
			s: script{last: goodRelease, waitErr: map[string]error{
				newRelease:  nonRetryable(ErrTypeHealthTimeout),
				goodRelease: nonRetryable(ErrTypeHealthTimeout)}},
			want:        Result{State: StateFailed, Failure: &Failure{Reason: ReasonCompensationFailed}},
			wantStates:  []string{"applying", "health_gate", "compensating:HEALTH_TIMEOUT", "failed:COMPENSATION_FAILED"},
			wantAssigns: []string{newRelease, goodRelease},
			wantWaits:   []string{newRelease, goodRelease},
		},
		{
			name: "compensation whose assign fails fails the deployment",
			s: script{last: goodRelease,
				waitErr:   map[string]error{newRelease: nonRetryable(ErrTypeHealthTimeout)},
				assignErr: map[string]error{goodRelease: nonRetryable(ErrTypeInvalid)}},
			want:        Result{State: StateFailed, Failure: &Failure{Reason: ReasonCompensationFailed}},
			wantStates:  []string{"applying", "health_gate", "compensating:HEALTH_TIMEOUT", "failed:COMPENSATION_FAILED"},
			wantAssigns: []string{newRelease, goodRelease},
			wantWaits:   []string{newRelease},
		},
		{
			name:        "cancel during the health gate restores the last healthy release",
			s:           script{last: goodRelease, waitDelay: time.Hour},
			cancelAfter: time.Minute,
			want:        Result{State: StateCancelled},
			wantStates:  []string{"applying", "health_gate", "compensating:CANCELLED", "cancelled"},
			wantAssigns: []string{newRelease, goodRelease},
			wantWaits:   []string{goodRelease},
		},
		{
			name:        "invalid release fails without assigning",
			s:           script{last: goodRelease, recordErr: nonRetryable(ErrTypeInvalid)},
			want:        Result{State: StateFailed, Failure: &Failure{Reason: ReasonInvalidRelease}},
			wantStates:  []string{"failed:INVALID_RELEASE"},
			wantAssigns: []string{},
			wantWaits:   []string{},
		},
		{
			// The forward path, after its rollback cases.
			name:        "healthy release succeeds",
			s:           script{last: goodRelease},
			want:        Result{State: StateSucceeded},
			wantStates:  []string{"applying", "health_gate", "succeeded"},
			wantAssigns: []string{newRelease},
			wantWaits:   []string{newRelease},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var ts testsuite.WorkflowTestSuite
			env := ts.NewTestWorkflowEnvironment()
			s := tt.s
			s.register(env)
			if tt.cancelAfter > 0 {
				env.RegisterDelayedCallback(env.CancelWorkflow, tt.cancelAfter)
			}
			env.ExecuteWorkflow(Workflow, input())
			if !env.IsWorkflowCompleted() {
				t.Fatal("workflow did not complete")
			}
			if err := env.GetWorkflowError(); err != nil {
				t.Fatalf("workflow error: %v", err)
			}
			var got Result
			if err := env.GetWorkflowResult(&got); err != nil {
				t.Fatalf("result: %v", err)
			}
			if got.State != tt.want.State || (got.Failure == nil) != (tt.want.Failure == nil) ||
				(got.Failure != nil && got.Failure.Reason != tt.want.Failure.Reason) {
				t.Errorf("result = %+v (failure %+v), want %+v (failure %+v)", got, got.Failure, tt.want, tt.want.Failure)
			}
			if gs := stateList(s.states); !equal(gs, tt.wantStates) {
				t.Errorf("states = %q, want %q", gs, tt.wantStates)
			}
			if ga := releases(s.assigns); !equal(ga, tt.wantAssigns) {
				t.Errorf("assigned = %q, want %q", ga, tt.wantAssigns)
			}
			if gw := waitReleases(s.waits); !equal(gw, tt.wantWaits) {
				t.Errorf("waited on = %q, want %q", gw, tt.wantWaits)
			}
			for _, c := range s.states {
				if c.Actor != input().Actor || c.DeploymentID != "deployment-1" {
					t.Errorf("state change %+v lost the deployment or actor", c)
				}
			}
		})
	}
}

// TestHealthDeadline checks the gate's deadline is fixed by the workflow, so activity
// retries cannot extend it.
func TestHealthDeadline(t *testing.T) {
	var ts testsuite.WorkflowTestSuite
	env := ts.NewTestWorkflowEnvironment()
	s := script{last: goodRelease}
	s.register(env)
	start := env.Now()
	env.ExecuteWorkflow(Workflow, input())
	if err := env.GetWorkflowError(); err != nil {
		t.Fatal(err)
	}
	if len(s.waits) != 1 {
		t.Fatalf("waits = %d, want 1", len(s.waits))
	}
	got := s.waits[0].Deadline.Sub(start)
	if got < 3*time.Minute || got > 3*time.Minute+time.Second {
		t.Errorf("deadline is %v after start, want 3m", got)
	}
	if s.waits[0].Generation != 1 || s.waits[0].NodeID != "node-1" {
		t.Errorf("wait = %+v, want the assigned node and generation", s.waits[0])
	}
}

// TestCancelBeforeAssign ends cancelled without compensating: nothing was assigned.
func TestCancelBeforeAssign(t *testing.T) {
	var ts testsuite.WorkflowTestSuite
	env := ts.NewTestWorkflowEnvironment()
	s := script{last: goodRelease}
	var a *Activities
	env.RegisterActivity(&Activities{})
	starts := 0
	env.OnActivity(a.RecordStart, mock.Anything, mock.Anything).After(time.Hour).Return(
		func(context.Context, Input) error { starts++; return nil })
	env.OnActivity(a.SetState, mock.Anything, mock.Anything).Return(
		func(_ context.Context, c StateChange) error { s.states = append(s.states, c); return nil })
	env.RegisterDelayedCallback(env.CancelWorkflow, time.Minute)
	env.ExecuteWorkflow(Workflow, input())
	if err := env.GetWorkflowError(); err != nil {
		t.Fatal(err)
	}
	var got Result
	if err := env.GetWorkflowResult(&got); err != nil {
		t.Fatal(err)
	}
	if got.State != StateCancelled {
		t.Errorf("state = %s, want cancelled", got.State)
	}
	if gs := stateList(s.states); !equal(gs, []string{"cancelled"}) {
		t.Errorf("states = %q, want only cancelled", gs)
	}
	if starts < 1 {
		t.Error("RecordStart never ran on the disconnected context")
	}
}

func TestFailureOf(t *testing.T) {
	tests := []struct {
		err  error
		want string
	}{
		{nonRetryable(ErrTypeWorkloadFailed), ReasonWorkloadFailed},
		{nonRetryable(ErrTypeHealthTimeout), ReasonHealthTimeout},
		{nonRetryable(ErrTypeInvalid), ReasonInvalidRelease},
		{errors.New("boom"), ReasonAssignFailed},
	}
	for _, tt := range tests {
		if got := failureOf(tt.err, ReasonAssignFailed).Reason; got != tt.want {
			t.Errorf("failureOf(%v) = %s, want %s", tt.err, got, tt.want)
		}
	}
}

func TestImageRef(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	tests := []struct {
		name, sourceRef, want string
	}{
		{"imported image runs its pinned reference", "ghcr.io/acme/api@" + digest, "ghcr.io/acme/api@" + digest},
		{"built image runs the local image ID", "3f2a9c1", digest},
		{"no source ref", "", digest},
	}
	for _, tt := range tests {
		if got := imageRef(tt.sourceRef, digest); got != tt.want {
			t.Errorf("%s: imageRef = %q, want %q", tt.name, got, tt.want)
		}
	}
}
