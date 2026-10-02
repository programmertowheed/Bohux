# ADR-0004: Postgres as the only datastore, with immutable revisions

- **Status:** accepted
- **Date:** 2026-10-02

## Context
We need desired state, audit trail, rollback, and change history. Teams will eventually
ask "who changed prod at 3am and what did it look like before". That question has to be
answerable from the data model, not from grepping logs.

## Decision
Postgres only. Every mutation writes an immutable revision row (actor, timestamp, diff,
parent revision) rather than updating in place. Current state is a materialised view over
the latest revision per resource. Audit events are emitted in the same transaction as the
revision — if the change committed, the audit record exists.

## Alternatives considered
- **etcd (the Kubernetes approach).** Good at watch semantics, bad at the relational
  queries we need for cost attribution, RBAC, and reporting. Would force a second store.
- **Postgres with in-place updates plus a separate audit table.** Cheaper to write, and
  the audit table drifts from reality within a year. Every system that does this ends up
  with audit records that do not reconcile against state.
- **Event sourcing with a separate read model.** Correct in theory. Rejected as too much
  machinery for v1; the revision table gives us 80% of the benefit with a schema a new
  contributor can read in ten minutes.

## Consequences
- Rollback is "apply revision N-1", not a special code path.
- Audit and compliance evidence are queries, not features.
- Table growth is real; needs a partitioning and archival plan before v1.0.
- Writes are heavier. Acceptable — this is a control plane, not a hot path.

## Revisit when
Revision table growth becomes a measured operational problem for a real user.
