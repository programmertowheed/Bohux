# ADR-0048: Deploy notifications run in an abandoned child workflow

- **Status:** proposed
- **Date:** 2026-10-22
- **Deciders:** Towheed

## Context
ADR-0038 item 3 has the deploy workflow post its outcome itself, after the
Deployment's terminal state is written: one activity lists the channels, one per
channel posts, each retried up to 5 times (5 s initial backoff, ×2). The workflow
closes only when the last send is delivered or dropped.

A deploy workflow's ID is `deploy/<app id>` (ADR-0026 item 2), and `Starter.Start`
turns "already started" into `ErrRunning`. So while notifications are still being
retried, the App's next deploy **and its rollback** are refused with "a deployment of
this app is already in progress", although the Deployment row already says
`SUCCEEDED` or `FAILED`. Normally that is milliseconds; with a failing Slack, Discord,
webhook or SMTP target it is over a minute. A rollback blocked right after a failed
deploy works against rule 5, and `TestRestoreDrill` hit the same race once in a full
e2e run (2026-10-22).

The fix changes the shape ADR-0038 chose, with deploy runs possibly in flight.

## Decision
1. **Notifications are a workflow of their own, `NotifyWorkflow`**, started by the
   deploy workflow as a **child** once the terminal state is written, with the ID
   `notify/<deployment id>` and `ParentClosePolicy: ABANDON`. It does what the end of
   the deploy run did (list the channels, one retried activity per channel, a failed
   send is logged and dropped); ADR-0038's items 1, 2, 4 and the retry bounds are
   unchanged.
2. **The deploy workflow waits only until the child has started**, then returns its
   Result. The App's workflow ID is free as soon as the deploy itself is over; the
   notifications finish on their own.
3. **The child is started on a disconnected context**: cancelling the deploy run
   (ADR-0026 item 8) does not cancel its notifications, as before.
4. **A child that cannot be started is logged and dropped**, like a send that fails: a
   notification never changes the deployment's outcome (ADR-0038 item 3).
5. **Versioned with `workflow.GetVersion(ctx, "notify-child", DefaultVersion, 1)`**:
   runs that already reached the old inline notifications replay them unchanged; the
   inline path is removed once no run of the old version can be open.

## Alternatives considered
- **`Starter.Start` treats a running workflow whose Deployment row is terminal as
  finished** (terminate it, or start beside it). Terminating drops the outcome's
  notifications; starting beside it needs a different workflow ID per run, which
  gives up ADR-0026 item 2 — the one thing that makes "one deploy per App at a time"
  hold without a lock of our own. Lost on that.
- **Signal a long-lived notifier workflow** (one per project or install). One
  workflow for every outcome grows its history without bound and needs
  continue-as-new and a way to be started; a child per deployment is bounded by
  construction. Lost on the extra machinery for no gain.
- **Shorter retries on the inline sends.** Shrinks the window, never closes it, and
  makes delivery worse when a chat service is briefly down. Lost.
- **Send from the API or an outbox table polled by a goroutine.** A poller over a
  status column is what rule 6 forbids. Lost.
- **Fire-and-forget activity (start it, do not wait).** Temporal activities belong to
  their workflow: a workflow that completes abandons its pending activities, so the
  sends would be dropped. Lost.

## Consequences
- Easy: a deploy or rollback can start the moment the previous Deployment's row is
  terminal (save the milliseconds between that write and the workflow closing),
  whatever the notification targets are doing.
- Easy: notifications are visible on their own in Temporal (`notify/<deployment id>`),
  so a stuck target is easy to find.
- Hard: two workflow types per deployment instead of one, both registered on the
  deploy worker; a `notify/…` run may outlive its deploy run, and several may run at
  once for one App (one per recent Deployment). That is fine: they only read rows and
  post.
- Hard: until no deploy run of the old version is open, the deploy workflow carries
  both notification paths behind the version gate.
- There is still a short window — the row written as terminal, the run not yet
  closed — in which a Start is refused. It is the workflow task that starts the child
  and returns: milliseconds, not the notification retries.

## Revisit when
A Start is refused while the previous Deployment's row is terminal for longer than a
second in practice (an `ErrRunning` with a terminal latest Deployment in the logs), or
notifications need ordering or batching across deployments.
