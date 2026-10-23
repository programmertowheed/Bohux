package drill

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
)

// catchupWindow is how late a missed drill still runs. A drill is weekly and proves
// the same archives either way, so one that was missed by more than an hour waits for
// the next Sunday rather than piling onto a host that has just come back.
const catchupWindow = time.Hour

// Schedule is the weekly schedule's shape.
type Schedule struct {
	// Cron is a 5-field cron expression in UTC; empty means DefaultSchedule.
	Cron string
	// TaskQueue overrides TaskQueue (tests use their own).
	TaskQueue string
	// ID overrides ScheduleID. An install has one drill schedule, so this is for
	// tests: the ID is a server-wide name, and two tests sharing it would delete
	// each other's schedule.
	ID string
}

func (s Schedule) id() string {
	if s.ID == "" {
		return ScheduleID
	}
	return s.ID
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

// Queue is the task queue this schedule starts workflows on, which is the queue the
// shared worker has to poll.
func (s Schedule) Queue() string { return s.queue() }

func (s Schedule) action() *client.ScheduleWorkflowAction {
	return &client.ScheduleWorkflowAction{ID: WorkflowID, Workflow: WorkflowName, TaskQueue: s.queue()}
}

// Register adds the drill workflow and its activities to w, which must be the same
// worker the backup registered on: they share a task queue, and a worker that polls
// it must know every workflow type on it (ADR-0051 item 1).
func Register(w worker.Worker, acts *Activities) {
	w.RegisterWorkflowWithOptions(Workflow, workflow.RegisterOptions{Name: WorkflowName})
	w.RegisterActivity(acts)
}

// EnsureSchedule creates the weekly schedule, or brings an existing one in line with
// s. Safe to call on every start.
func EnsureSchedule(ctx context.Context, c client.Client, s Schedule) error {
	spec := client.ScheduleSpec{CronExpressions: []string{s.cron()}}
	_, err := c.ScheduleClient().Create(ctx, client.ScheduleOptions{
		ID: s.id(), Spec: spec, Action: s.action(),
		Overlap: enums.SCHEDULE_OVERLAP_POLICY_SKIP, CatchupWindow: catchupWindow,
	})
	if err == nil {
		return nil
	}
	if !errors.Is(err, temporal.ErrScheduleAlreadyRunning) {
		return fmt.Errorf("create drill schedule: %w", err)
	}
	err = c.ScheduleClient().GetHandle(ctx, s.id()).Update(ctx, client.ScheduleUpdateOptions{
		DoUpdate: func(in client.ScheduleUpdateInput) (*client.ScheduleUpdate, error) {
			sch := in.Description.Schedule
			sch.Spec = &spec
			sch.Action = s.action()
			sch.Policy = &client.SchedulePolicies{Overlap: enums.SCHEDULE_OVERLAP_POLICY_SKIP, CatchupWindow: catchupWindow}
			return &client.ScheduleUpdate{Schedule: &sch}, nil
		},
	})
	if err != nil {
		return fmt.Errorf("update drill schedule: %w", err)
	}
	return nil
}

// RemoveSchedule deletes the schedule s names; one that does not exist is not an error.
func RemoveSchedule(ctx context.Context, c client.Client, s Schedule) error {
	err := c.ScheduleClient().GetHandle(ctx, s.id()).Delete(ctx)
	var notFound *serviceerror.NotFound
	if err == nil || errors.As(err, &notFound) {
		return nil
	}
	return fmt.Errorf("delete drill schedule: %w", err)
}
