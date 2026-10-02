# Data model

Postgres only. Immutable revisions, not in-place updates — see ADR-0004.

## Hierarchy

```
Tenant ─▶ Project ─▶ Environment ─▶ App ─▶ Release ─▶ Deployment
                                   └─ Resource (db, cache, bucket, queue)
```

## Core tables (sketch — authoritative schema lives in migrations)

| Table | Notes |
|---|---|
| `tenants` | Root of every scope. No row anywhere without a tenant_id. |
| `projects` | Unit of grouping and RBAC assignment. |
| `environments` | dev/staging/prod/preview-*. Holds provider binding and config. |
| `apps` | A deployable unit (named App, not Service — ADR-0014). Points at a source (repo, image, static dir). |
| `resources` | Attached dependencies: databases, caches, buckets, queues. |
| `releases` | **Immutable.** artifact digest + config revision + provenance ref. |
| `deployments` | One attempt to reconcile a release onto an environment. |
| `revisions` | Append-only. actor, timestamp, diff, parent_revision, resource_ref. |
| `audit_events` | Written in the **same transaction** as the revision. |
| `nodes` | Registered agents: last_seen, version, capabilities, drain state. |
| `secret_refs` | Handles only. **No values, ever** (CLAUDE.md rule 3). |

## Invariants

- Every row carries `tenant_id`. Row-level security enforces it; application code does not
  get to be the only guard.
- `releases` are never updated. Promotion between environments reuses the same digest —
  build once, deploy everywhere.
- A state change without a `revisions` row is a bug.
- `audit_events` commit with the change. If the change committed, the audit record exists.
- Current state is a view over the latest revision per resource, not a separate truth.

## Deferred

Partitioning and archival of `revisions` and `audit_events`. Required before v1.0, and the
trigger is a real user's measured table growth (ADR-0004).
