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
| `<name>_revisions` | Append-only, one table per versioned resource, full snapshots (ADR-0017). actor, timestamp, parent revision. |
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
- Current state is a head table pointing at the latest revision, updated in the same transaction with a CAS on the parent revision (ADR-0017). Not a separate truth.

## Schema rules (as built in S2b)

Migrations are embedded SQL in `internal/store/migrations/`, applied by goose (ADR-0021).

- **Heads and revisions.** Each versioned resource has `<name>_heads` and
  `<name>_revisions` (ADR-0017). The head holds identity that never changes (`id`,
  `tenant_id`, parent IDs, `slug`, `created_at`, `created_by`), a `deleted` flag, and
  `revision_id`. The revision holds a full snapshot of everything mutable, plus
  `actor_id`, `created_at`, `parent_revision_id`, `deleted`. `updated_at`/`updated_by`
  are the current revision's `created_at`/`actor_id`.
- **History is linear and append-only.** One root revision per resource, each parent
  has at most one child and belongs to the same resource, and a trigger rejects
  `UPDATE`, `DELETE`, and `TRUNCATE` on revision tables and `audit_events`. The head's
  pointer to its revision is a deferred foreign key, so head and revision are written in
  one transaction.
- **Deletion** writes a revision with `deleted = true` and sets the head's `deleted`.
  Slug uniqueness is a partial unique index over live heads only.
- **Observations are not state.** `nodes_heads.last_seen_at` and
  `api_tokens_heads.last_used_at` are updated in place and never versioned.
- **Credentials** are stored as hashes (`api_tokens_heads.token_hash`,
  `node_bootstrap_tokens.token_hash`). Bootstrap tokens are not a versioned resource;
  creating and using one are audit events.
- **Enums** are lowercase `text` with a `CHECK`, named like the proto value without its
  prefix (`DEPLOYMENT_STATE_HEALTH_GATE` → `'health_gate'`).
- **Nested proto messages** (`App.source`, `App.runtime`, failures) are `jsonb` in
  protojson form. Secret values never appear in any column (rule 3).
- **Tenant isolation.** Every table has `tenant_id`, row-level security `ENABLE`d and
  `FORCE`d, and the policy `tenant_id = bohux_current_tenant()`. The store sets the
  tenant per transaction with `set_config('bohux.tenant_id', <id>, true)`; unset means
  no rows. Cross-tenant references are impossible: child foreign keys include
  `tenant_id`.
- **The database role** owns the schema and serves requests, and is neither a superuser
  nor `BYPASSRLS` (superusers ignore row-level security). `store.Migrate` refuses
  otherwise. The dev stack creates `bohux_app` for this.

## Deferred

Partitioning and archival of `revisions` and `audit_events`. Required before v1.0, and the
trigger is a real user's measured table growth (ADR-0004).
