package backup

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
)

// catchupWindow is how late a missed night still runs (ADR-0033 item 1).
const catchupWindow = time.Hour

// Schedule is the nightly schedule's shape.
type Schedule struct {
	// Cron is a 5-field cron expression in UTC; empty means DefaultSchedule.
	Cron string
	// TaskQueue overrides TaskQueue (tests use their own).
	TaskQueue string
}

func (s Schedule) cron() string {
	if s.Cron == "" {
		return DefaultSchedule
	}
	return s.Cron
}

func (s Schedule) queue() string {
	if s.TaskQueue == "" {
		return TaskQueue
	}
	return s.TaskQueue
}

func (s Schedule) action() *client.ScheduleWorkflowAction {
	return &client.ScheduleWorkflowAction{ID: WorkflowID, Workflow: WorkflowName, TaskQueue: s.queue()}
}

// EnsureSchedule creates the nightly schedule, or brings an existing one in line with
// s. Safe to call on every start.
func EnsureSchedule(ctx context.Context, c client.Client, s Schedule) error {
	spec := client.ScheduleSpec{CronExpressions: []string{s.cron()}}
	_, err := c.ScheduleClient().Create(ctx, client.ScheduleOptions{
		ID: ScheduleID, Spec: spec, Action: s.action(),
		Overlap: enums.SCHEDULE_OVERLAP_POLICY_SKIP, CatchupWindow: catchupWindow,
	})
	if err == nil {
		return nil
	}
	if !errors.Is(err, temporal.ErrScheduleAlreadyRunning) {
		return fmt.Errorf("create backup schedule: %w", err)
	}
	err = c.ScheduleClient().GetHandle(ctx, ScheduleID).Update(ctx, client.ScheduleUpdateOptions{
		DoUpdate: func(in client.ScheduleUpdateInput) (*client.ScheduleUpdate, error) {
			sch := in.Description.Schedule
			sch.Spec = &spec
			sch.Action = s.action()
			sch.Policy = &client.SchedulePolicies{Overlap: enums.SCHEDULE_OVERLAP_POLICY_SKIP, CatchupWindow: catchupWindow}
			return &client.ScheduleUpdate{Schedule: &sch}, nil
		},
	})
	if err != nil {
		return fmt.Errorf("update backup schedule: %w", err)
	}
	return nil
}

// RemoveSchedule deletes the nightly schedule; one that does not exist is not an error.
func RemoveSchedule(ctx context.Context, c client.Client) error {
	err := c.ScheduleClient().GetHandle(ctx, ScheduleID).Delete(ctx)
	var notFound *serviceerror.NotFound
	if err == nil || errors.As(err, &notFound) {
		return nil
	}
	return fmt.Errorf("delete backup schedule: %w", err)
}

// retry is how long Run waits between attempts while Temporal is unreachable.
const retry = 5 * time.Second

// Run makes Temporal match the configuration, then serves backups until ctx ends.
// With acts nil (no recipient configured), it removes the schedule and returns.
// Temporal being unreachable is never fatal: it keeps trying, and the control plane
// serves meanwhile.
func Run(ctx context.Context, c client.Client, s Schedule, acts *Activities) {
	if acts == nil {
		until(ctx, "remove backup schedule", func() error { return RemoveSchedule(ctx, c) })
		return
	}
	var w worker.Worker
	until(ctx, "start backup worker", func() error {
		// A fresh worker per attempt: one whose Start failed is not reused.
		w = worker.New(c, s.queue(), worker.Options{})
		w.RegisterWorkflowWithOptions(Workflow, workflow.RegisterOptions{Name: WorkflowName})
		w.RegisterActivity(acts)
		return w.Start()
	})
	if ctx.Err() != nil {
		return
	}
	defer w.Stop()
	until(ctx, "ensure backup schedule", func() error { return EnsureSchedule(ctx, c, s) })
	slog.InfoContext(ctx, "backup schedule set", "cron", s.cron(), "dir", acts.Dir, "keep", acts.Keep)
	<-ctx.Done()
}

// until calls f every retry until it succeeds or ctx ends.
func until(ctx context.Context, what string, f func() error) {
	for {
		err := f()
		if err == nil {
			return
		}
		slog.WarnContext(ctx, what+": Temporal unavailable; retrying", "err", err, "in", retry)
		select {
		case <-ctx.Done():
			return
		case <-time.After(retry):
		}
	}
}
