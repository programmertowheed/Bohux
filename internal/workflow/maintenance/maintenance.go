// Package maintenance serves Bohux's own instance-level schedules — the nightly
// backup (ADR-0033) and the weekly restore drill (ADR-0051) — on shared workers.
//
// It exists because the two share a task queue: a worker that polls a queue has to
// know every workflow type started on it, so two workers each knowing only their own
// would fail each other's tasks. One worker per queue, registering everything on it.
package maintenance

import (
	"context"
	"log/slog"
	"time"

	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"

	backupwf "github.com/bohumix/bohux/internal/workflow/backup"
	drillwf "github.com/bohumix/bohux/internal/workflow/drill"
)

// retry is how long Run waits between attempts while Temporal is unreachable.
const retry = 5 * time.Second

// Config is what to serve. A nil Activities switches that schedule off and removes
// one an earlier configuration created, so turning a feature off takes effect rather
// than leaving an orphan schedule firing into a worker that no longer serves it.
type Config struct {
	Backups        *backupwf.Activities
	BackupSchedule backupwf.Schedule
	Drills         *drillwf.Activities
	DrillSchedule  drillwf.Schedule
}

// Run makes Temporal match cfg, then serves until ctx ends. Temporal being
// unreachable is never fatal: it keeps trying, and the control plane serves meanwhile
// (rule 1 is about workloads, but the same applies to the API).
func Run(ctx context.Context, c client.Client, cfg Config) {
	if cfg.Backups == nil {
		until(ctx, "remove backup schedule", func() error { return backupwf.RemoveSchedule(ctx, c, cfg.BackupSchedule) })
	}
	if cfg.Drills == nil {
		until(ctx, "remove drill schedule", func() error { return drillwf.RemoveSchedule(ctx, c, cfg.DrillSchedule) })
	}
	if cfg.Backups == nil && cfg.Drills == nil {
		return
	}

	// Group registrations by queue, so each worker knows every type on its own queue.
	registrations := map[string][]func(worker.Worker){}
	if cfg.Backups != nil {
		q := cfg.BackupSchedule.Queue()
		acts := cfg.Backups
		registrations[q] = append(registrations[q], func(w worker.Worker) { backupwf.Register(w, acts) })
	}
	if cfg.Drills != nil {
		q := cfg.DrillSchedule.Queue()
		acts := cfg.Drills
		registrations[q] = append(registrations[q], func(w worker.Worker) { drillwf.Register(w, acts) })
	}

	var started []worker.Worker
	defer func() {
		for _, w := range started {
			w.Stop()
		}
	}()
	for q, register := range registrations {
		var w worker.Worker
		until(ctx, "start maintenance worker on "+q, func() error {
			// A fresh worker per attempt: one whose Start failed is not reused.
			w = worker.New(c, q, worker.Options{})
			for _, add := range register {
				add(w)
			}
			return w.Start()
		})
		if ctx.Err() != nil {
			return
		}
		started = append(started, w)
	}

	if cfg.Backups != nil {
		until(ctx, "ensure backup schedule", func() error {
			return backupwf.EnsureSchedule(ctx, c, cfg.BackupSchedule)
		})
		slog.InfoContext(ctx, "backup schedule set", "dir", cfg.Backups.Dir, "keep", cfg.Backups.Keep)
	}
	if cfg.Drills != nil {
		until(ctx, "ensure drill schedule", func() error {
			return drillwf.EnsureSchedule(ctx, c, cfg.DrillSchedule)
		})
		slog.InfoContext(ctx, "restore drill schedule set", "dir", cfg.Drills.Dir, "keep", cfg.Drills.Keep)
	}
	if ctx.Err() != nil {
		return
	}
	<-ctx.Done()
}

// until calls f every retry until it succeeds or ctx ends.
func until(ctx context.Context, what string, f func() error) {
	for {
		if ctx.Err() != nil {
			return
		}
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
