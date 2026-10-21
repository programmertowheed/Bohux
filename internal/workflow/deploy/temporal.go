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
}

// Start starts the App's deploy run for in and returns its workflow and run IDs.
func (s *Starter) Start(ctx context.Context, in Input) (workflowID, runID string, err error) {
	run, err := s.Client.ExecuteWorkflow(ctx, client.StartWorkflowOptions{
		ID:                       WorkflowID(in.AppID),
		TaskQueue:                TaskQueue,
		WorkflowIDConflictPolicy: enums.WORKFLOW_ID_CONFLICT_POLICY_FAIL,
		WorkflowIDReusePolicy:    enums.WORKFLOW_ID_REUSE_POLICY_ALLOW_DUPLICATE,
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

// workerRetry is how long RunWorker waits between attempts to start polling.
const workerRetry = 5 * time.Second

// RunWorker polls TaskQueue with the deploy workflow and acts until ctx ends. Temporal
// being unreachable is never fatal: the worker keeps trying, and the control plane
// serves its API meanwhile (deploys wait for Temporal, nothing else does).
func RunWorker(ctx context.Context, c client.Client, acts *Activities) {
	var w worker.Worker
	for {
		// A fresh worker per attempt: one whose Start failed is not reused.
		w = worker.New(c, TaskQueue, worker.Options{})
		w.RegisterWorkflow(Workflow)
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
	slog.InfoContext(ctx, "deploy worker polling", "task_queue", TaskQueue)
	<-ctx.Done()
	w.Stop()
}
