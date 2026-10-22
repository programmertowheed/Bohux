package deploy

import (
	"errors"
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

// Activity options. Postgres-only activities retry without an overall limit: while
// Postgres is down there are no deploys, and the run resumes when it is back
// (01-architecture.md, failure model). Only non-retryable errors and cancellation end
// them.
var (
	storeOptions = workflow.ActivityOptions{
		StartToCloseTimeout: 30 * time.Second,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    time.Second,
			BackoffCoefficient: 2,
			MaximumInterval:    time.Minute,
		},
	}
	// fallbackMargin is how long past the health gate the agent waits for this run's
	// own compensation before it falls back on its own (ADR-0031).
	fallbackMargin = 2 * time.Minute
	// healthHeartbeat is how often WaitHealthy heartbeats; twice it is its timeout.
	healthHeartbeat = 30 * time.Second
)

// Workflow runs one Deployment (ADR-0026), then hands its outcome to a NotifyWorkflow
// child for the project's notification channels and closes without waiting for it
// (ADR-0038, ADR-0048). Its errors are infrastructure errors only; a deploy that fails
// or rolls back returns its Result with a nil error.
func Workflow(ctx workflow.Context, in Input) (Result, error) {
	timeout := in.HealthTimeout
	if timeout <= 0 {
		timeout = DefaultHealthTimeout
	}
	d := &run{in: in, timeout: timeout}
	res, err := d.deploy(ctx)
	if err == nil {
		d.notifyEnd(ctx, res)
	}
	return res, err
}

// deploy is the run itself, up to the terminal state.
func (d *run) deploy(ctx workflow.Context) (Result, error) {
	var a *Activities
	in := d.in

	var building bool
	if err := workflow.ExecuteActivity(storeCtx(ctx), a.RecordStart, in).Get(ctx, &building); err != nil {
		switch {
		case temporal.IsCanceledError(err):
			return d.cancelled(ctx)
		case isType(err, ErrTypeInvalid):
			f := &Failure{Reason: ReasonInvalidRelease}
			return d.finish(ctx, StateFailed, f)
		}
		return Result{}, err
	}
	// A Release still building is waited for; nothing is assigned until it is ready.
	if building {
		if err := d.set(ctx, StateBuilding, nil); err != nil {
			if temporal.IsCanceledError(err) {
				return d.cancelled(ctx)
			}
			return Result{}, err
		}
		if err := d.waitBuilt(ctx); err != nil {
			switch {
			case temporal.IsCanceledError(err):
				return d.cancelled(ctx)
			case isType(err, ErrTypeBuildFailed):
				return d.finish(ctx, StateFailed, &Failure{Reason: ReasonBuildFailed})
			}
			return Result{}, err
		}
	}
	var last string
	if err := workflow.ExecuteActivity(storeCtx(ctx), a.LastHealthyRelease, LastHealthyInput{
		TenantID: in.TenantID, AppID: in.AppID, DeploymentID: in.DeploymentID,
	}).Get(ctx, &last); err != nil {
		if temporal.IsCanceledError(err) {
			return d.cancelled(ctx)
		}
		return Result{}, err
	}
	d.last = last
	if err := d.set(ctx, StateApplying, nil); err != nil {
		if temporal.IsCanceledError(err) {
			return d.cancelled(ctx)
		}
		return Result{}, err
	}

	// From here on something may be assigned, so every failure compensates. The node
	// also gets the workload being replaced, in case this run cannot compensate.
	fallbackAfter := workflow.Now(ctx).Add(d.timeout + fallbackMargin)
	assigned, err := d.assign(ctx, in.ReleaseID, fallbackAfter)
	if err != nil {
		return d.compensate(ctx, failureOf(err, ReasonAssignFailed), temporal.IsCanceledError(err))
	}
	if err := d.set(ctx, StateHealthGate, nil); err != nil {
		return d.compensate(ctx, failureOf(err, ReasonAssignFailed), temporal.IsCanceledError(err))
	}
	if err := d.wait(ctx, assigned, in.ReleaseID); err != nil {
		return d.compensate(ctx, failureOf(err, ReasonHealthTimeout), temporal.IsCanceledError(err))
	}
	if err := d.set(ctx, StateSucceeded, nil); err != nil {
		// Healthy, but cancelled before it was recorded: a cancel means undo.
		return d.compensate(ctx, failureOf(err, ReasonCancelled), temporal.IsCanceledError(err))
	}
	return Result{State: StateSucceeded}, nil
}

// run is one execution's inputs and what it learned, shared by its steps.
type run struct {
	in      Input
	timeout time.Duration
	// last is the Release of the App's newest succeeded Deployment, or "".
	last string
}

// compensate restores the last healthy Release, or removes the App's workload when
// there is none, on a context the workflow's cancellation cannot reach (ADR-0026).
func (d *run) compensate(ctx workflow.Context, f *Failure, cancelled bool) (Result, error) {
	dctx, _ := workflow.NewDisconnectedContext(ctx)
	if cancelled {
		f = &Failure{Reason: ReasonCancelled}
	}
	if err := d.set(dctx, StateCompensating, f); err != nil {
		return Result{}, err
	}
	final := StateRolledBack
	if d.last == "" {
		final = StateFailed
	}
	if cancelled {
		final = StateCancelled
	}
	assigned, err := d.assign(dctx, d.last, time.Time{})
	if err == nil && d.last != "" {
		err = d.wait(dctx, assigned, d.last)
	}
	if err != nil {
		workflow.GetLogger(ctx).Error("compensation failed", "deployment", d.in.DeploymentID, "error", err)
		return d.finish(dctx, StateFailed, &Failure{Reason: ReasonCompensationFailed})
	}
	if final == StateCancelled {
		f = nil
	}
	return d.finish(dctx, final, f)
}

// cancelled ends a run that was cancelled before anything was assigned.
func (d *run) cancelled(ctx workflow.Context) (Result, error) {
	dctx, _ := workflow.NewDisconnectedContext(ctx)
	var a *Activities
	// The row may not exist yet if RecordStart was the step cancelled.
	if err := workflow.ExecuteActivity(storeCtx(dctx), a.RecordStart, d.in).Get(dctx, nil); err != nil &&
		!isType(err, ErrTypeInvalid) {
		return Result{}, err
	}
	return d.finish(dctx, StateCancelled, nil)
}

// finish writes the terminal state and returns it as the Result.
func (d *run) finish(ctx workflow.Context, s State, f *Failure) (Result, error) {
	if err := d.set(ctx, s, f); err != nil {
		return Result{}, err
	}
	return Result{State: s, Failure: f}, nil
}

func (d *run) set(ctx workflow.Context, s State, f *Failure) error {
	var a *Activities
	return workflow.ExecuteActivity(storeCtx(ctx), a.SetState, StateChange{
		TenantID: d.in.TenantID, DeploymentID: d.in.DeploymentID, State: s, Failure: f, Actor: d.in.Actor,
	}).Get(ctx, nil)
}

func (d *run) assign(ctx workflow.Context, release string, fallbackAfter time.Time) (Assigned, error) {
	var (
		a   *Activities
		out Assigned
	)
	err := workflow.ExecuteActivity(storeCtx(ctx), a.AssignRelease, AssignInput{
		TenantID: d.in.TenantID, AppID: d.in.AppID, ReleaseID: release,
		DeploymentID: d.in.DeploymentID, Actor: d.in.Actor, FallbackAfter: fallbackAfter,
	}).Get(ctx, &out)
	return out, err
}

// wait runs the health gate with its own deadline, so activity retries cannot extend
// it; the schedule-to-close timeout is a backstop for a worker that never reports.
func (d *run) wait(ctx workflow.Context, assigned Assigned, release string) error {
	var a *Activities
	deadline := workflow.Now(ctx).Add(d.timeout)
	opts := storeOptions
	opts.StartToCloseTimeout = d.timeout + time.Minute
	opts.ScheduleToCloseTimeout = d.timeout + 2*time.Minute
	opts.HeartbeatTimeout = 2 * healthHeartbeat
	return workflow.ExecuteActivity(workflow.WithActivityOptions(ctx, opts), a.WaitHealthy, WaitInput{
		TenantID: d.in.TenantID, NodeID: assigned.NodeID, Generation: assigned.Generation,
		AppID: d.in.AppID, ReleaseID: release, Replicas: assigned.Replicas, Routed: assigned.Routed,
		Deadline: deadline,
	}).Get(ctx, nil)
}

// buildWait bounds how long a deploy waits for its Release's build.
const buildWait = 6 * time.Hour

func (d *run) waitBuilt(ctx workflow.Context) error {
	var a *Activities
	opts := storeOptions
	opts.StartToCloseTimeout = buildWait
	opts.ScheduleToCloseTimeout = buildWait
	opts.HeartbeatTimeout = 2 * healthHeartbeat
	return workflow.ExecuteActivity(workflow.WithActivityOptions(ctx, opts), a.WaitReleaseReady, ReleaseRef{
		TenantID: d.in.TenantID, ReleaseID: d.in.ReleaseID,
	}).Get(ctx, nil)
}

func storeCtx(ctx workflow.Context) workflow.Context {
	return workflow.WithActivityOptions(ctx, storeOptions)
}

// failureOf maps an activity error to a Deployment failure reason, using def for
// errors that carry none.
func failureOf(err error, def string) *Failure {
	switch {
	case temporal.IsCanceledError(err):
		return &Failure{Reason: ReasonCancelled}
	case isType(err, ErrTypeWorkloadFailed):
		return &Failure{Reason: ReasonWorkloadFailed}
	case isType(err, ErrTypeHealthTimeout), temporal.IsTimeoutError(err):
		return &Failure{Reason: ReasonHealthTimeout}
	case isType(err, ErrTypeInvalid):
		return &Failure{Reason: ReasonInvalidRelease}
	}
	return &Failure{Reason: def}
}

func isType(err error, typ string) bool {
	var app *temporal.ApplicationError
	return errors.As(err, &app) && app.Type() == typ
}
