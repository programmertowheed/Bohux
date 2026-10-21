package deploy

import (
	"context"
	"errors"
)

// errNotYet marks activities whose bodies arrive in S6b; the workflow and its tests
// mock them until then.
var errNotYet = errors.New("deploy activity not implemented yet (S6b)")

// Activities are the deploy workflow's I/O: Postgres and the agent API. Every method is
// idempotent, because Temporal retries it (new-workflow skill).
type Activities struct{}

// RecordStart writes the Deployment row if it is absent (ADR-0026 item 3), records the
// run, and validates the Release. An invalid Release is a non-retryable
// ErrTypeInvalid error, returned after the row exists.
func (a *Activities) RecordStart(ctx context.Context, in Input) error { return errNotYet }

// SetState moves the Deployment to a new state; a repeat of the current state is a
// no-op.
func (a *Activities) SetState(ctx context.Context, c StateChange) error { return errNotYet }

// LastHealthyRelease returns the Release of the App's newest succeeded Deployment
// other than in.DeploymentID, or "" if there is none.
func (a *Activities) LastHealthyRelease(ctx context.Context, in LastHealthyInput) (string, error) {
	return "", errNotYet
}

// AssignRelease sets or removes the App's workload on its node (ADR-0026 item 5).
func (a *Activities) AssignRelease(ctx context.Context, in AssignInput) (Assigned, error) {
	return Assigned{}, errNotYet
}

// WaitHealthy polls the node's stored StatusReport until the assignment runs the
// Release healthy (ADR-0026 item 6).
func (a *Activities) WaitHealthy(ctx context.Context, in WaitInput) error { return errNotYet }
