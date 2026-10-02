# API contract plan

**Status:** draft — plan only. `proto/` is written from this once the two open questions at
the bottom are answered. Governs ADR-0006 (proto is truth), ADR-0007 (agent protocol),
ADR-0008 (secrets), ADR-0010 (rollback), ADR-0011 (v0.1 scope).

## Two packages, two compatibility promises

| Package | Who calls it | Compatibility |
|---|---|---|
| `kiln.v1` | CLI, web UI, SDKs, scripts | Public API. `buf breaking` at FILE level; breaking changes need `v2`. |
| `kiln.agent.v1` | Agents only | Agent protocol. Control plane supports protocol N and N-1 (ADR-0003). |

Kept separate so agent changes never force a public API version bump, and vice versa.

## Conventions (apply to every message)

1. **Every resource starts with `ResourceMeta meta = 1`:** `id`, `tenant_id`,
   `revision_id`, `created_at`, `updated_at`, `created_by`, `updated_by`. All
   output-only.
2. **Tenant and actor come from authentication, never from the request.** No request
   message has `tenant_id`, `actor`, `approver`, or `user` fields. Fixes the forgeable
   `ApproveGateRequest.approver` in the current draft (rule 10).
3. **IDs** are server-generated UUIDv7 strings (time-ordered, index-friendly). Resources a
   human types also have a `slug`, unique within the parent (`kiln deploy api -e prod`).
4. **Every mutation** takes `idempotency_key` and, for updates/deletes,
   `expected_revision_id` (optimistic concurrency — a stale write fails with `ABORTED`).
5. **Every mutation** writes a revision and an audit event in one transaction (ADR-0004);
   the response returns the new `revision_id`.
6. **Lists** take `page_size`, `page_token`; return `next_page_token`.
7. **Errors** use Connect codes plus a typed `ErrorDetail` (`reason`, `field`). Error
   text never contains secret values (rule 3).
8. **Secrets** appear only as `SecretRef { key, handle }`. The one exception is
   `SetSecretRequest.value`, marked sensitive and redacted by an interceptor before any
   logging or tracing (ADR-0014).
9. Field ordering and numbering: never reuse a field number; `reserved` on removal.

## Resources

```
Tenant ─▶ Project ─▶ Environment ─▶ App ─▶ Release ─▶ Deployment
                │                 └─ (Resource: db/cache — v0.2, not in v0.1 proto)
                └─ RoleBinding (principal → viewer | deployer | owner)
Node (tenant-scoped; bound to Environments via provider binding)
ApiToken, AuditEvent, Revision (read-only views)
```

| Resource | Key fields | Mutability |
|---|---|---|
| Tenant | slug, display_name | Created at bootstrap; rename only |
| Project | slug, display_name | CRUD |
| RoleBinding | project_id, principal_id, role | Create/delete |
| Environment | project_id, slug, `ProviderBinding { provider, node_id \| target_ref }`, config | CRUD |
| App | environment_id, slug, Source, Runtime, `repeated SecretRef` | CRUD; each update = revision |
| Release | app_id, artifact_digest, config_revision_id, source_ref, provenance_ref, sbom_ref | **Immutable.** No update or delete RPC; retention GC (ADR-0010) |
| Deployment | release_id, environment_id, strategy, state, workflow_id, `rollback_of` | Created only; state is output-only |
| Node | slug, agent_version, protocol_version, last_seen, provider descriptors, state | Registered by agent; revoke via API |
| ApiToken | name, role scope, expires_at, last_used_at | Create (secret shown once) / revoke |

`DeploymentState` gains `COMPENSATING` and `CANCELLED`. `AWAITING_APPROVAL` stays
reserved but unused until approval gates ship (not v0.1).

## Public services (`kiln.v1`)

Marked **0.1** = in v0.1 per ADR-0011; **later** = reserved shape, not written yet.

| Service | RPCs | When |
|---|---|---|
| IdentityService | WhoAmI, CreateApiToken, ListApiTokens, RevokeApiToken | 0.1 |
| TenantService | GetTenant | 0.1 (CreateTenant is a bootstrap CLI step, not an RPC) |
| ProjectService | Create/Get/List/Update/DeleteProject, Set/Delete/ListRoleBindings | 0.1 |
| EnvironmentService | Create/Get/List/Update/DeleteEnvironment | 0.1 |
| AppService | Create/Get/List/Update/DeleteApp, ScaleApp | 0.1 |
| ReleaseService | CreateRelease (from Git ref → build, or from image digest), Get/ListReleases | 0.1 |
| DeploymentService | CreateDeployment, Get/ListDeployments, WatchDeployment (server stream), Rollback, CancelDeployment | 0.1 |
| WorkloadService | StreamLogs (server stream), Exec (bidi) | 0.1 |
| NodeService | CreateBootstrapToken, Get/ListNodes, RevokeNode | 0.1 |
| SecretService | SetSecret, ListSecretRefs, DeleteSecret | 0.1 |
| AuditService | ListAuditEvents, ListRevisions(resource), GetRevision | 0.1 |
| ApprovalService | ApproveGate, RejectGate | later |
| ResourceService | databases/caches as Resources | later (v0.2) |

\* Name pending open question 1.

Notes:
- **Build is not its own service in v0.1.** `CreateRelease` from a Git ref starts the
  build workflow; the Release appears when the build finishes (`state` on Release).
- **Rollback** = `CreateDeployment` of an older Release with `rollback_of` set.
  `DeploymentService.Rollback` is a convenience that picks the last healthy Release
  (ADR-0010). There is no other rollback path.
- **ScaleApp** writes a new App revision and a new assignment; no new Release.
- **Exec is bidirectional streaming**, which ConnectRPC cannot carry from browsers. The
  CLI gets Exec in v0.1; the UI does not. That is allowed — rule 8 forbids the UI having
  *more* than the CLI, not less.

## Agent protocol (`kiln.agent.v1`)

```
AgentService.Register(RegisterRequest) returns (RegisterResponse)
  bootstrap_token + CSR + node public key  →  signed client cert, node_id

AgentService.Connect(stream AgentMessage) returns (stream ControlMessage)   // mTLS, agent dials out
```

| Agent → control plane | Control plane → agent |
|---|---|
| `Hello` — node_id, agent_version, protocol_version, provider Descriptors, last applied generation | `Assignment` — full desired state for this node |
| `StatusReport` — generation, per-workload observed state + health | `LogRequest` / `LogCancel` |
| `LogChunk` — request_id, lines | `ExecOpen` / `ExecData` / `ExecClose` |
| `ExecData` — session_id, bytes | `Ping` |
| `Ack` — generation applied | |

`Assignment` design choices:
- **Full state, not a diff.** Every assignment lists every workload for the node. Resync
  after a crash or reconnect is "send the latest one" (ADR-0003, ADR-0009).
- **Monotonic `generation` per node.** The agent ignores any assignment with a lower
  generation than it has cached.
- **Signed by the control plane key.** The agent verifies the signature before caching —
  this is the integrity check the node cache needs (ADR-0003 consequences).
- **Secret values sealed to the node's public key**, inside the assignment, for that
  node's workloads only (ADR-0008). Never in Temporal history: the activity that sends
  the assignment resolves handles itself and returns nothing secret.

Deploy flow mapped to the protocol (ADR-0007):
`CreateDeployment` → workflow writes new assignment (gen N+1) → pushed on `Connect` →
agent Apply loop → `StatusReport(gen N+1, healthy)` → workflow finalizes. Unhealthy or
timeout → compensation writes gen N+2 with the previous Release.

## Files

```
proto/kiln/v1/       common.proto  identity.proto  tenancy.proto  project.proto
                     environment.proto  app.proto  release.proto  deployment.proto
                     workload.proto  node.proto  secret.proto  audit.proto
proto/kiln/agent/v1/ agent.proto
```

## Decided (ADR-0014)

1. **The deployable resource is `App`, not `Service`.** RPC service is `AppService`;
   CLI is `kiln app …`; table is `apps`.
2. **Secret values may pass through the API.** `SecretService.SetSecret` accepts the
   value, writes it to OpenBao, and stores only the handle. The value field is redacted
   by an interceptor before any log, trace, or error; a test asserts it never appears.
3. **Field rules use `google.api.field_behavior` annotations** (`REQUIRED`,
   `OUTPUT_ONLY`, `IMMUTABLE`), pulled in as a `buf` dependency.

## Still open (block writing `proto/`)

4. **Final project name.** The package name (`kiln.v1` today) goes into the wire format
   and every generated client; renaming after `proto/` ships is breaking. Deferred by
   the owner on 2026-10-02.
5. **Go module path** (`github.com/<owner>/<name>`). Depends on 4.

Everything else in this plan can be reviewed now; `proto/` is written only after 4 and 5.
