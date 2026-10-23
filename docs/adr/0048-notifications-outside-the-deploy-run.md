# ADR-0048: Notifications run in their own workflow, so a finished deploy frees its App

- **Status:** proposed (slice chosen by the owner 2026-10-22; built on)
- **Date:** 2026-10-22
- **Deciders:** Towheed
- **Amends:** ADR-0038 item 3 (where the notification activities run)

## Context
A deploy run is the Temporal workflow `deploy/<app id>`, and the start of another run
for the same App is refused while one is open (ADR-0026 item 2: one deploy per App at a
time). ADR-0038 sends notifications from activities in that same workflow, after the
terminal state is written, each channel retried up to five times with backoff.

So after a Deployment visibly ends, the App stays locked for as long as notifying
takes: milliseconds normally, minutes when a chat service, webhook or mail relay is
failing. Meanwhile `bohux deploy` and `bohux rollback` answer "a deployment of this app
is already in progress". A rollback blocked after a failed deploy, by a notification,
works against rule 5. The end-to-end restore drill hit this race once.

What is costly to change: the shape of the workflow (runs may be open across an
upgrade) and where notification delivery lives.

## Decision
1. **A Deployment's notifications are their own workflow**, `NotifyWorkflow`, ID
   `notify/<deployment id>`, started by the deploy run as a child with
   `ParentClosePolicy: ABANDON` once the terminal state is written. The deploy run waits
   only until the child has started (so the start is recorded), then completes. The App
   is free for its next deploy at once; delivery goes on independently.
2. **Delivery itself is unchanged** (ADR-0038): one activity lists the project's
   channels for the state, one activity per channel posts, each with the same retry
   policy and timeouts, and a failed notification never changes the Deployment. The
   inputs stay IDs (rule 3).
3. **One notification run per Deployment**: the child's ID is unique per Deployment, and
   a duplicate start is refused, so a replayed parent never notifies twice.
4. **Runs already open keep the old path.** The change is gated with
   `workflow.GetVersion(ctx, "notify-child", DefaultVersion, 1)`; a run that started
   on the previous code notifies inline as before. Recorded histories of the previous
   version are replayed in a unit test (`testdata/`), as the new-workflow skill asks.
5. **Cancelling a deploy never cancels its notifications** (as before, when they ran on
   a disconnected context): an abandoned child is not cancelled with its parent.

## Alternatives considered
- **Let a new deploy start while the old run is still open** (conflict policy
  "terminate existing", or treating a run whose row is terminal as finished). The
  lock would mean nothing: a terminated run could be cut in the middle of a
  compensation. Lost on rule 5 and ADR-0026 item 2.
- **Notify from the API or a poller** when the row turns terminal. Hand-written
  retries over a status column (rule 6). Lost.
- **Fewer retries or a shorter timeout.** Shrinks the window but keeps the race, and
  drops notifications that a short outage would have let through. Lost.
- **Signal-with-start a long-lived notifier workflow per project.** One workflow for
  many Deployments, but its history grows without bound and needs continue-as-new.
  Lost on simplicity; a child per Deployment is short-lived.

## Consequences
- Easy: `bohux rollback` right after a deploy ends always starts; the restore drill no
  longer races its own notifications.
- Hard: a second workflow type to register on the worker and keep replay-compatible.
  A notification run can outlive its deploy run, so the next Deployment's message may
  arrive before the previous one's if the latter is being retried; each message names
  its Deployment and state.
- The previous inline branch stays in the code until no run started before this change
  can be open (deploy runs are bounded by their build wait, six hours, plus the health
  gate); then it and its recorded histories can go, in a later change.

## Revisit when
Notifications need ordering guarantees per App, or delivery moves to a queue outside
Temporal.
