package deploy

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
)

var (
	// ErrRunning means the App already has a deploy in progress (ADR-0026 item 2).
	ErrRunning = errors.New("a deployment of this app is already in progress")
	// ErrUnavailable means Temporal could not be reached; nothing was started.
	ErrUnavailable = errors.New("deploy orchestration is unavailable")
)

// Starter starts and cancels deploy runs. It is what the API holds.
type Starter struct {
	Client client.Client
	// TaskQueue overrides TaskQueue (tests use their own).
	TaskQueue string
	// HealthTimeout is applied to runs whose Input sets none.
	HealthTimeout time.Duration
}

// Start starts the App's deploy run for in and returns its workflow and run IDs.
func (s *Starter) Start(ctx context.Context, in Input) (workflowID, runID string, err error) {
	if in.HealthTimeout <= 0 {
		in.HealthTimeout = s.HealthTimeout
	}
	run, err := s.Client.ExecuteWorkflow(ctx, client.StartWorkflowOptions{
		ID:                       WorkflowID(in.AppID),
		TaskQueue:                queueOr(s.TaskQueue),
		WorkflowIDConflictPolicy: enums.WORKFLOW_ID_CONFLICT_POLICY_FAIL,
		WorkflowIDReusePolicy:    enums.WORKFLOW_ID_REUSE_POLICY_ALLOW_DUPLICATE,
		// Without this the SDK hands back the running run instead of refusing.
		WorkflowExecutionErrorWhenAlreadyStarted: true,
	}, Workflow, in)
	var started *serviceerror.WorkflowExecutionAlreadyStarted
	switch {
	case errors.As(err, &started):
		return "", "", ErrRunning
	case err != nil:
		return "", "", fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	return run.GetID(), run.GetRunID(), nil
}

// StartBuild starts the Release's build run. Starting it again while it runs, or after
// it finished, is a no-op: one Release is built once.
func (s *Starter) StartBuild(ctx context.Context, in BuildInput) (string, error) {
	_, err := s.Client.ExecuteWorkflow(ctx, client.StartWorkflowOptions{
		ID:                       BuildWorkflowID(in.ReleaseID),
		TaskQueue:                queueOr(s.TaskQueue),
		WorkflowIDConflictPolicy: enums.WORKFLOW_ID_CONFLICT_POLICY_USE_EXISTING,
		WorkflowIDReusePolicy:    enums.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE,
	}, BuildWorkflow, in)
	var started *serviceerror.WorkflowExecutionAlreadyStarted
	if err != nil && !errors.As(err, &started) {
		return "", fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	return BuildWorkflowID(in.ReleaseID), nil
}

// Cancel requests cancellation of one run; the workflow compensates (ADR-0026 item 8).
// A run that already finished is not an error.
func (s *Starter) Cancel(ctx context.Context, workflowID, runID string) error {
	err := s.Client.CancelWorkflow(ctx, workflowID, runID)
	var notFound *serviceerror.NotFound
	if err == nil || errors.As(err, &notFound) {
		return nil
	}
	return fmt.Errorf("%w: %w", ErrUnavailable, err)
}

// Promote signals a canary run to end its bake now (ADR-0049).
func (s *Starter) Promote(ctx context.Context, workflowID, runID string) error {
	if err := s.Client.SignalWorkflow(ctx, workflowID, runID, SignalPromote, nil); err != nil {
		return fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	return nil
}

// workerRetry is how long RunWorker waits between attempts to start polling.
const workerRetry = 5 * time.Second

func queueOr(q string) string {
	if q == "" {
		return TaskQueue
	}
	return q
}

// RunWorker polls queue (TaskQueue when empty) with the deploy workflow and acts until
// ctx ends. Temporal being unreachable is never fatal: the worker keeps trying, and the
// control plane serves its API meanwhile (deploys wait for Temporal, nothing else does).
func RunWorker(ctx context.Context, c client.Client, queue string, acts *Activities) {
	queue = queueOr(queue)
	var w worker.Worker
	for {
		// A fresh worker per attempt: one whose Start failed is not reused.
		w = worker.New(c, queue, worker.Options{})
		w.RegisterWorkflow(Workflow)
		w.RegisterWorkflow(BuildWorkflow)
		w.RegisterWorkflow(NotifyWorkflow)
		w.RegisterActivity(acts)
		err := w.Start()
		if err == nil {
			break
		}
		slog.WarnContext(ctx, "deploy worker cannot reach Temporal; retrying", "err", err, "in", workerRetry)
		select {
		case <-ctx.Done():
			return
		case <-time.After(workerRetry):
		}
	}
	slog.InfoContext(ctx, "deploy worker polling", "task_queue", queue)
	<-ctx.Done()
	w.Stop()
}
