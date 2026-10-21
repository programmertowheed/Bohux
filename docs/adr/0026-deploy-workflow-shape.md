# ADR-0026: Deploy workflow shape — one run per App, rows written by both sides, compensation by reassignment

- **Status:** proposed
- **Date:** 2026-10-21
- **Deciders:** Towheed

## Context
ADR-0002 makes deploys Temporal workflows and ADR-0010 makes rollback and compensation
"assign an earlier Release". Neither says how the workflow meets the rest of the system,
and several of those choices are expensive to change once Deployments exist in users'
databases and workflow histories:

1. **Two systems of record.** A Deployment is a Postgres row (API reads, audit, rule 10)
   and a Temporal run (progress, retries). Neither write can share a transaction with the
   other, so one of them can exist without the other after a crash.
2. **Concurrency.** Two deploys of the same App at once would race on the node's
   assignment and on "the last healthy Release".
3. **Health.** The workflow must learn when the agent has converged. The agent only
   talks to the control plane over its stream (ADR-0003), and the control plane stores
   the latest StatusReport per node (migration `00015`).
4. **Assignments are whole-node** (ADR-0007): `Assign` takes every workload on the node.
   A deploy changes one App.
5. Workflow code must be deterministic; everything that touches Postgres, the agent
   stream, or the clock runs in activities.

## Decision
1. **The worker runs inside `bohux controlplane`** on task queue `bohux-deploy`. No new
   process; the control plane is already the only thing that talks to Postgres.
2. **One workflow ID per App: `deploy/<app id>`,** started with the "fail if running"
   conflict policy and duplicates allowed after completion. Temporal itself refuses a
   second concurrent deploy of an App; the API maps that to `FAILED_PRECONDITION`. The
   Deployment row records the workflow ID and the **run ID** (new column), so cancel and
   history lookups name the exact run.
3. **The API starts the workflow, then writes the Deployment row; the workflow's first
   activity writes the same row too.** Both insert with the same pre-generated ID, and
   whichever comes second does nothing. A crash after the start leaves a row written by
   the workflow; a row without a workflow cannot exist. The API validates (Release
   ready and in this environment) before starting; the activity validates again.
4. **Deployment state is written only by activities**, one revision per transition, with
   the workflow's actor (the caller who created it) on the audit event.
5. **Assigning one App is a read-modify-write under the node lock** (`AssignRelease`):
   take the node's latest generation, replace or add this App's workload (built from the
   Release's artifact and its captured App revision), write the next generation. It is
   idempotent: if the latest generation already carries this Deployment's workload, it
   returns that generation instead of writing another.
6. **Health is a polling activity with a heartbeat** (`WaitHealthy`): every 2 s it reads
   the node's stored StatusReport and succeeds when the report covers the assigned
   generation and the App's workload runs the target Release, healthy, with all
   replicas ready. A `FAILED` workload fails it at once; otherwise the health timeout
   (default 5 min) fails it. This is an activity inside the saga, not a poller that
   drives state from outside it (rule 6).
7. **Last healthy Release** = the Release of the newest *succeeded* Deployment of the
   App (rollback Deployments included), read before assigning.
8. **Compensation** (rule 5, ADR-0010): on a failed health gate, an assign error, or
   cancellation after an assign, the workflow writes `COMPENSATING`, assigns the last
   healthy Release, and waits for it to be healthy → `ROLLED_BACK` (`CANCELLED` if it was
   a cancel). With no earlier healthy Release (a first deploy), it removes the App's
   workload from the node → `FAILED`. If compensation itself does not turn healthy,
   the Deployment ends `FAILED` with both failures recorded. Compensation runs on a
   disconnected context, so a cancel cannot cancel the undo.
9. **v0.1 deploys need a `READY` Release.** Waiting on a build joins the workflow with S7.

## Alternatives considered
- **Workflow ID = Deployment ID, concurrency checked in Postgres** (lock the App row,
  refuse if a Deployment is not terminal). Lost on the crash window: the row commits,
  the process dies before the start, and the App is blocked by a `PENDING` row nothing
  will ever finish — which needs a sweeper over a status column, the thing rule 6 bans.
- **The workflow creates the row and the API does not.** Lost because `GetDeployment`
  right after `CreateDeployment` could return not-found until a worker picks the task up.
- **Agent → control plane → signal the workflow when a report arrives.** Lower latency,
  but the stream handler would need to know which runs wait on which node, and a signal
  lost while the worker is down would stall the gate until timeout anyway. The polling
  activity reads the same row and is restart-safe for free. Revisit for latency only.
- **Compensation by restoring the previous whole-node generation.** Lost because it
  would also undo other Apps' deploys that landed in between; per-App reassignment does
  not.
- **Leave a failed first deploy's workload in place** (keeps logs for debugging). Lost
  because desired state would then contain a Release known to be broken, which the agent
  keeps restarting. Logs from the attempt stay readable until the workload is destroyed,
  and the failure is recorded on the Deployment.

## Consequences
- Easy: concurrent deploys of one App are impossible by construction; restarts of the
  control plane mid-deploy resume where they were; rollback and compensation share one
  path (`AssignRelease` + `WaitHealthy`) that the forward path also uses.
- Hard: the workflow ID is not the Deployment ID, so tools look a Deployment's run up
  through the row. Every activity must be idempotent, because Temporal retries them.
- Health latency is up to one poll (2 s) plus one agent report interval.
- Migration adds `deployments_revisions.workflow_run_id`.
- Committed to: Postgres row + Temporal run per Deployment, written from both sides by
  ID; per-App serialization in Temporal.

## Revisit when
Deploys of different Apps on one node contend on the node lock measurably (p95 assign
latency over 1 s), or users need parallel deploys of one App (canary arrives with
ingress in v0.2 and may need two live Releases per App).
