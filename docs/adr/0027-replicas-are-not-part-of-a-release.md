# ADR-0027: Replicas are not part of a Release; ScaleApp assigns in its own transaction

- **Status:** proposed
- **Date:** 2026-10-21
- **Deciders:** Towheed

## Context
A Release captures the App revision it was created from (ADR-0010, data-model.md): its
source, runtime, and secret refs. The deploy workflow builds the node's workload from
that captured revision (ADR-0026). `ScaleApp` writes a new App revision with only
`replicas` changed, and api-contract.md says it also writes a new assignment, with no
new Release.

Two problems follow if replicas stay inside the captured revision:

1. **Scale is lost on the next deploy or rollback.** Deploying any Release, including a
   rollback to an old one, would reset replicas to whatever they were when that Release
   was created.
2. **Where the assignment is written.** Writing the App revision and then, separately,
   the node assignment can leave them apart after a crash. A Temporal workflow fixes
   that but `ScaleApp` returns the App synchronously, conflicts with a running deploy's
   per-App workflow ID (ADR-0026), and has nothing to compensate.

## Decision
1. **Replicas come from the App's current revision at assign time**; everything else in
   the runtime (ports, health, cpu, memory) and the secret refs come from the Release's
   captured revision. A deploy, a rollback, and compensation all keep the current
   scale.
2. **`ScaleApp` writes the App revision and the node's next generation in one Postgres
   transaction**, under the node lock: the App's current workload (whatever Release it
   runs) with only `replicas` changed. If the App has no workload on the node, only the
   revision is written; the next deploy uses it. The agent is woken after commit.
3. This is not a deploy (rule 6 does not apply): one atomic desired-state write, no
   steps, no retries, nothing to compensate. The agent converges as for any
   generation.

## Alternatives considered
- **A scale workflow on the App's workflow ID.** Serializes with deploys, but a scale
  during a deploy would be refused or queued, the API would answer before the
  assignment exists, and the workflow would only wrap one transaction. Lost on
  complexity for no added guarantee.
- **Scale creates a new Release.** Keeps "Release = everything", but every scale event
  would become a Release and a Deployment in history, crowd out real rollback targets
  under retention (5 per App, ADR-0020), and need a health gate it does not need.
- **Keep replicas in the Release and accept reset on deploy.** Lost: a rollback during
  an incident would silently scale the App back down.

## Consequences
- Easy: scale survives deploys and rollbacks; `ScaleApp` is immediate and atomic.
- Harder: a Release no longer fully describes what runs; replicas are read from the App.
  The API and docs must say so (data-model.md).
- A scale and a deploy can interleave on the node lock; whichever writes last, the
  generation carries the App's current replicas and the deploy's Release, so they
  converge.

## Revisit when
Another runtime field turns out to be operational rather than part of a release (e.g.
cpu/memory limits changed during an incident), or autoscaling arrives and needs its own
source of replica counts.
