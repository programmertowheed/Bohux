# ADR-0017: Current state is a head table updated in the write transaction

- **Status:** accepted
- **Date:** 2026-10-02
- **Deciders:** Md Towheedul Islam

Clarifies ADR-0004. Replaces only its sentence "Current state is a materialised view over
the latest revision per resource". Everything else in ADR-0004 stands.

## Context
In Postgres a `MATERIALIZED VIEW` changes only on `REFRESH`, which recomputes the whole
view and does not run inside the writing transaction. Reads go stale after every commit.
That contradicts ADR-0004's own promise that a committed change and its audit record are
atomic, and it gives no place to enforce optimistic concurrency.

## Decision
Each versioned resource has an immutable `<resource>_revisions` table and a
`<resource>_heads` table mapping resource ID to its current revision ID. A mutation does,
in one transaction: insert the revision (full snapshot, with actor, timestamp and parent
revision ID); update the head row with a compare-and-swap on the expected parent revision;
insert the audit event. A CAS miss is a conflict returned to the caller. The current
revision ID is the resource's etag in the API (ADR-0014 preconditions). Revisions store
full snapshots; diffs are computed on read.

## Alternatives considered
- **Materialised view (as written in ADR-0004).** Rejected: stale until refresh, full
  recompute cost, refresh outside the write transaction.
- **Plain view with `DISTINCT ON` over revisions.** Always fresh and no extra table, but
  every read of current state scans an index over the full history, and it gives no row
  to lock or CAS against, so concurrency control moves to advisory locks. Rejected.
- **Store diffs instead of snapshots.** Smaller, but reconstructing state needs a replay
  chain and one corrupt diff poisons everything after it. Rejected; storage is cheap and
  partitioning is already planned.

## Consequences
- Reads of current state are a single indexed join; always consistent with the last commit.
- Lost updates are impossible by construction; clients must handle conflict errors.
- Two tables per versioned resource; the head can never disagree with revisions because
  both change in the same transaction.
- Snapshot storage grows faster than diffs; the ADR-0004 partitioning/archival plan before
  v1.0 still applies.

## Revisit when
Measured revision-table size or write latency becomes an operational problem for a real
user (same signal as ADR-0004).
