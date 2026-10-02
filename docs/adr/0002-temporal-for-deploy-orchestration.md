# ADR-0002: Temporal for deploy orchestration

- **Status:** accepted
- **Date:** 2026-10-02

## Context
A deploy is a long-running, failure-prone, multi-step saga: build, scan, sign, push,
provision dependencies, run migrations, shift traffic, gate on health, then finalize or
compensate. It must survive a control plane restart that happens mid-deploy, and it must
be able to undo partial work.

## Decision
Deploys, promotions, backups, restore drills, and node lifecycle operations are Temporal
workflows. Provider calls are activities with explicit retry policies and compensations.

## Alternatives considered
- **Hand-rolled state machine in a Postgres table with a cron poller.** This is what
  nearly every tool in this space does, and it is where they accumulate their worst bugs:
  lost work on restart, double-applies, deploys stuck in `in_progress` forever, retry
  logic reinvented per code path. Rejected on evidence.
- **A job queue (BullMQ/River/Asynq).** Good at "run this once with retries", bad at "run
  these nine steps with compensation, survive a restart at step six, and let a human
  approve step seven". A saga is not a job.
- **Argo Workflows / Tekton.** Drags in a Kubernetes dependency for the control plane
  itself, which contradicts the single-VPS target.

## Consequences
- Retries, timeouts, compensation, and human-approval steps are framework features.
- Deploy history and replay come free; debugging a failed deploy means reading a workflow
  history, not correlating log lines.
- **Cost: an extra service to run and upgrade.** Mitigated by embedding Temporal in the
  single-node install bundle so users never see it.
- Workflow code has determinism constraints that contributors must learn. Documented in
  `.claude/skills/new-workflow/`.

## Revisit when
Temporal's operational burden demonstrably outweighs the saga guarantees for single-node
users — measured, not felt.
