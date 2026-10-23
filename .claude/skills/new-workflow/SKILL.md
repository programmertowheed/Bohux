---
name: new-workflow
description: Use when adding or changing a Temporal workflow or activity in Bohux (deploys, rollbacks, builds, backups, restore drills, node lifecycle). Covers determinism rules, where I/O goes, activity idempotency, compensation-first testing, running Temporal locally, and versioning a workflow that already has runs in flight.
---

# Writing a workflow

Read ADR-0002 (why Temporal), ADR-0010 (rollback is a deploy), and ADR-0026 (deploy
workflow shape) first. Code lives in `internal/workflow/`.

## Rules

1. **Workflow code is deterministic.** It is replayed from history after every restart.
   In a workflow function never: read the clock (`time.Now`), sleep (`time.Sleep`),
   start goroutines (`go`), use `select` on Go channels, iterate a map where order
   matters, call `rand`, read env/config, log through `slog`, or touch Postgres, the
   network, or the disk. Use `workflow.Now`, `workflow.Sleep`, `workflow.Go`,
   `workflow.NewSelector`, `workflow.GetLogger`, and activities.
2. **All I/O is an activity.** Activities are methods on a struct with its
   dependencies (`*Activities`). The workflow references them through a nil pointer of
   that type (`var a *Activities; workflow.ExecuteActivity(ctx, a.AssignRelease, ...)`),
   so names stay checked by the compiler.
3. **Every activity is idempotent.** Temporal retries it, including after it succeeded
   but before the result was recorded. Write with a known ID and "insert if absent", or
   check "already done" first. A duplicate call must leave the same state.
4. **No hand-written retry loops** (CLAUDE.md rule 6). Set a `RetryPolicy` and timeouts
   on the activity options instead. A polling activity (waiting on an observation)
   heartbeats and has a `HeartbeatTimeout`, so a dead worker is noticed.
5. **Non-retryable errors are explicit.** Validation failures and "this will never
   work" return `temporal.NewNonRetryableApplicationError` with a type string the
   workflow can branch on.
6. **Secrets never enter workflow history** (rule 3). Inputs, outputs, and errors of
   activities are stored by Temporal. Pass handles; resolve values inside the activity
   that uses them, and never return them.
7. **Compensation runs on a disconnected context** (`workflow.NewDisconnectedContext`),
   so cancelling the workflow cannot cancel the undo.
8. **Postgres stays the system of record** for anything the API shows. Activities write
   revisions and audit events (rule 10); the workflow never assumes the row's state
   from its own variables alone after a restart — it re-reads through an activity when
   it matters.

## Tests

- **The rollback/compensation test comes first** (rule 5). Write it before the forward
  path, using `go.temporal.io/sdk/testsuite` with mocked activities
  (`env.OnActivity(a.X, ...)`). Assert the activity calls, their order, and the final
  state.
- Activities get their own tests against real Postgres (`integration` tag).
- One end-to-end test runs a real worker against a Temporal dev server
  (`BOHUX_TEST_TEMPORAL_ADDRESS`, `integration` tag) and skips when it is unset.
- Replay test: when a workflow already has runs in production, keep a recorded history
  in `testdata/` and replay it with `worker.NewWorkflowReplayer` after every change.

## Changing a workflow that has runs in flight

Changing the order or kind of commands breaks replay of running histories. Gate the
change with `workflow.GetVersion(ctx, "<change-id>", workflow.DefaultVersion, 1)` and
keep the old branch until no run of the old version is open. Never rename an activity
or workflow type that has open runs.

## Running Temporal locally

The dev stack (`deploy/dev/compose.yaml`) runs Temporal. Without Docker, use the
`temporal` CLI: `temporal server start-dev --headless --ip 127.0.0.1 --port 7233`
(in-memory unless `--db-filename` is given). See *Known gaps* in
`docs/plan/03-progress.md` for the cloud container.

## Checklist

- [ ] Deterministic workflow body (rule 1 list)
- [ ] Every activity idempotent, with timeouts and a retry policy
- [ ] Compensation path tested before the forward path
- [ ] No secret values in inputs, outputs, or errors
- [ ] State transitions written as revisions with audit events
- [ ] `GetVersion` gate if runs of the old code can be open
