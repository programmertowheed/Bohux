# Progress — the shared memory between sessions

This file is the single source of truth for **where the work is**. Any session (any
machine, any account, after any disconnect) starts here and ends by updating it. If it is
not written here and pushed, it did not happen.

`02-roadmap.md` says *what* is in each version. This file says *what state each piece of
work is in right now*.

## Session protocol

**Start of every session**
1. `git fetch origin` and check out the working branch named under *Current focus*.
2. Read *Current focus*, *Waiting on the owner*, and the newest *Session log* entry.
3. Continue the slice marked 🔄. Do not start a new slice while one is 🔄 unless the
   owner says so.

**During**
- Tick task boxes as they finish, in the same commit as the work.
- A decision that is hard to reverse → ADR first (CLAUDE.md), then add it here.

**End of every session, or before any long-running step that could be cut off**
1. Update the slice tasks, *Current focus*, and add a *Session log* entry (newest first):
   what was done, what is half-done and where, the exact next step.
2. Commit and push. Unpushed work is lost when the cloud container is reclaimed.

**Session size (owner rule)**
- One task per session. Sessions are short; the owner runs `/clear` between them.
- If a task grows too big for one session, stop at a clean point, split the rest into a
  new sub-slice here, commit, and push.
- Every session ends by giving the owner **the exact prompt for the next session**, and
  that prompt is also written under *Next session prompt* below.
- Items under *Waiting on the owner* are handled by the owner outside the repo. Do not
  wait on them; work on the code.

**Status legend:** ⏳ not started · 🔄 in progress · ✅ done · ⛔ blocked (say on what)

## Current focus

- **Working branch:** `claude/relaxed-rubin-76m7dl`
- **Slice:** S11 — the CLI covers the API (🔄)
- **v0.1 is code-complete**: the exit criterion runs in CI (`selfdeploy`). Open for the
  owner: O2 (footprint on a 2 GB VPS) and the proposed ADRs.
- **Exact next step:** switch `scripts/selfdeploy-test.sh` (and the installer where it
  can) from `curl` to the new CLI commands; run the self-deploy test.

## Next session prompt

> Read CLAUDE.md and docs/plan/03-progress.md, then finish slice S11 (the CLI covers the
> API, ADR-0036) on branch `claude/relaxed-rubin-76m7dl`, following ADR-0019
> and ADR-0034. Start Postgres, Docker, Temporal and OpenBao (tracker *Known gaps*).
> Verify with build, vet, golangci-lint (also with the integration tag), unit and
> integration tests. Tick boxes, update the tracker and the next session prompt,
> commit, push, and give me the next prompt.

## Waiting on the owner

| # | Item | Blocks |
|---|---|---|
| O1 | Accept or reject ADR-0020 (v0.1 baseline). ADR-0019 (Caddy) accepted 2026-10-21 | S5, S7, S8 scope |
| O2 | Run `make run-dev && make footprint` on a 2GB VPS; record results in `docs/footprint.md` | ADR-0002 stays or reopens; nothing before S6 |
| O3 | Create the `bohumix` GitHub org and move the repo (ADR-0015). Code already uses `github.com/bohumix/bohux` | Public `go get`, not local work |
| O4 | Confirm the vision change: parity is demand-driven, not the goal | Nothing technical |
| O6 | Accept or reject ADR-0021 (migrations: goose on pgx). Owner allowed building on it before acceptance | S2b onward is built on it |
| O7 | Accept or reject ADR-0022 (token format `bxa_<tenant>_<id>_<secret>`, tenant-first lookup, SHA-256, idempotency records). Owner allowed building on it before acceptance | S3c onward is built on it |
| O8 | Accept or reject ADR-0023 (tenant-wide API tokens when `project_id` is empty; two-step authorization). Changed `identity.proto`. Owner allowed building on it | S3c3 onward |
| O9 | Accept or reject ADR-0024 (node CA + signing key as 0600 files in `--data-dir`, CA pinning by hash at install, 90-day node certs, Ed25519 assignment signatures, nacl/box sealing). Owner allowed building on it | S4b onward |
| O10 | Review the provider types in `internal/provider/provider.go` and the *Types* section of `docs/specs/provider-interface.md` (hash-based drift detection, no list verb: agent records what it applied). Built on already | S5 implements them |
| O11 | Accept or reject ADR-0025 (Docker provider drives `docker compose` on its own artifact; agent-side HTTP health checks; layout and labels). Owner allowed building on it | S5c onward |
| O12 | Accept or reject ADR-0026 (deploy workflow shape: worker in the control plane, workflow ID per App, row written by API and workflow, per-App assign, polling health activity, compensation by reassignment). Built on already | S6b onward |
| O13 | Accept or reject ADR-0027 (replicas are not part of a Release; ScaleApp writes the App revision and the assignment in one transaction). Built on already | S6e onward |
| O14 | Accept or reject ADR-0028 (builds run in the agent over its stream, on a resource-limited buildx `docker-container` builder named `bohux`, `--load` into dockerd; public Git only in v0.1). Built on already | S7c onward |
| O15 | Accept or reject ADR-0029 (backup archive: age-encrypted tar; logical Postgres copy over a privileged DSN; every OpenBao version; node CA). Adds `filippo.io/age`. Built on already | S8c onward |
| O16 | Accept or reject ADR-0030 (single-VPS layout: agent as a systemd unit, infra as one Compose project, control plane as an App of itself installed through a seed). Built on already | S9 |
| O17 | Accept or reject ADR-0031 (agent-side fallback: previous workload + deadline, applied only when the control plane cannot compensate). Changes agent.proto. Built on already | S9b onward |
| O18 | Accept or reject ADR-0032 (`App.provider_options` → `Spec.Raw`, owner-only; Docker accepts external networks and named volumes). Changes `app.proto` and agent.proto. Built on already | S9c onward |
| O19 | Accept or reject ADR-0033 (nightly backup: Temporal schedule in the control plane, read-only `BYPASSRLS` role `bohux_backup`, local directory with retention 7; S3 deferred). Built on already | S9f |
| O20 | Accept or reject ADR-0034 (routes on Apps, hosts unique across tenants, routes are App state, the agent programs its node's Caddy, providers report endpoints). Changes app.proto and agent.proto. Built on already | S10 |
| O21 | Accept or reject ADR-0035 (zero-downtime deploys: two slots per routed App, switched by the agent through one ingress load). Changes the provider ResourceRef. Built on already | S10f |
| O22 | Accept or reject ADR-0036 (CLI resource commands: `bohux <noun> <verb>`, JSON output is the API's protojson, secret values only from stdin) | S11 |
| O5 | Where do an App's **non-secret** env vars live? The spec gives App only Source, Runtime, SecretRefs; `Environment.config` is env-wide. Options: `App.env` map, or Environment.config only | Adding a field to App before v0.1 (non-breaking) |

## v0.1 slices

Each slice ends green in CI and pushed. Order matters: later slices build on earlier ones.

### S0 — Foundations ✅
- [x] `go.mod` (`github.com/bohumix/bohux`, Go 1.23), dependencies pinned to match the
      code generators
- [x] `buf.yaml` / `buf.gen.yaml`; generated code goes to `gen/` (gitignored)
- [x] `cmd/bohux` skeleton with `version`, table-driven test
- [x] CI: buf lint + breaking, go mod tidy check, vet, golangci-lint, test, build,
      compose validation
- [x] `deploy/dev/compose.yaml`: Postgres, Temporal, OpenBao (dev), BuildKit, Caddy
- [x] `scripts/footprint.sh` + `make footprint` (ADR-0020 item 1)
- [x] This file, linked from `CLAUDE.md`

### S1 — Proto contract ✅
Plan: `docs/specs/api-contract.md` (all questions answered there). Split into four
sub-slices, one session each.

#### S1a — common and tenancy ✅
- [x] `buf.build/googleapis/googleapis` declared in `buf.yaml` (ADR-0014). `buf.lock` is
      **not committed yet**: the registry is unreachable from the cloud container, so CI
      runs `buf dep update` when the lock is missing. See *Known gaps*.
- [x] `common.proto` (`ResourceMeta`, `ErrorDetail`). Pagination and idempotency are
      plain request fields on each RPC, per the spec's conventions
- [x] `identity.proto`, `tenancy.proto`, `project.proto`, `environment.proto`
- Choices the spec left open, made in S1a (change before v0.1 ships if wrong):
  - `ApiToken` "role scope" = exactly one `project_id` + one `Role`.
  - `GetProject` / `GetEnvironment` look up by id **or** slug (CLI uses slugs).
  - `Environment.config` is `map<string, string>` of non-secret values.
  - `Update*` replaces the mutable fields as a whole; no field masks.
  - Token secret uses `debug_redact = true`; the interceptor (S3) is still required.

#### S1b — app, release, deployment ✅
- [x] `app.proto` (App replaces Service), `release.proto`, `deployment.proto`
      (no `approver`/`actor` request fields), `workload.proto`
- [x] Draft `service.proto` deleted early (it defined the same symbols in `bohux.v1`, so
      the new files could not build beside it); its `buf.yaml` ignores removed
- Choices the spec left open, made in S1b (change before v0.1 ships if wrong):
  - `SecretRef` lives in `common.proto` (shared by App now, agent protocol in S1c).
  - `Source` is Git or image only; the draft's `StaticSource` dropped (not v0.1).
    `ImageSource` = `repository` + `digest` (a digest alone cannot be pulled).
  - `BuildStrategy` has only `DOCKERFILE`; `DeploymentStrategy` has only `RECREATE`
    (ADR-0011 item 7, ADR-0020 item 5). More values are added when they ship.
  - `Port.host_port` (0 = unpublished) replaces the draft's `public` flag (no proxy in
    v0.1). `HealthCheck` is HTTP with a required `port`.
  - `ScaleApp` changes `replicas` only, not cpu/memory.
  - `Release.state` (`BUILDING`/`READY`/`FAILED`), `artifact_available` (ADR-0020
    "not rollback-able"), `build_workflow_id`, `failure`. `CreateRelease` takes an
    optional `git_ref` or `ImageSource`; neither means "the App's current source".
  - `Deployment` keeps the draft states and adds `COMPENSATING`, `CANCELLED`; adds
    `reason`, `failure`. `rollback_of` is a Deployment id. `CancelDeployment` takes no
    `expected_revision_id` (workflow-driven state changes constantly).
  - `Rollback` is per App; empty `to_release_id` = last healthy Release.
  - `WorkloadService` addresses instances by provider-defined `instance` name.

#### S1c — node, secret, audit, agent protocol ✅
- [x] `node.proto`, `secret.proto` (value redacted), `audit.proto`
- [x] `proto/bohux/agent/v1/agent.proto`, including version fields for skew (ADR-0020)
- [x] (was S1d) `buf lint` clean with no ignores; `buf generate` + `go build ./...` pass
      with every file from the spec's *Files* list present
- [x] CI `buf breaking` skips only while the base branch lacks `common.proto` (it holds
      just the unreleased draft that S1 replaces); it runs normally after S1 merges
- Choices the spec left open, made in S1c (change before v0.1 ships if wrong):
  - Node slug is chosen in `CreateBootstrapToken` (`node_slug`); Node is output-only.
    `NodeState`: `ONLINE`, `OFFLINE`, `DEGRADED` (disk pressure), `REJECTED` (version
    skew), `REVOKED`.
  - `ProviderDescriptor` (name, version, `capabilities` as verb names) lives in
    `node.proto` and is reused by the agent protocol.
  - `Secret` is a resource (`environment_id`, `key`, output-only `handle`); the handle
    stays stable across `SetSecret` value changes. `ListSecretRefs` returns `Secret`s.
    `SetSecret` takes `expected_revision_id` only when overwriting.
  - `AuditEvent` and `Revision` are records without `ResourceMeta`. They name resources
    by `ResourceRef { type, id }`. `Revision.snapshot` is a `google.protobuf.Any`.
  - Agent protocol: the spec's `AgentMessage`/`ControlMessage` are `ConnectRequest`/
    `ConnectResponse` (buf STANDARD lint naming). Agent exec output is `ExecOutput`;
    control-plane input stays `ExecData`. A `VersionRejected` message closes the stream.
  - `Assignment` = serialized `AssignmentPayload` bytes + signature + `key_id`. The
    payload carries `node_id` and `generation` so it cannot be replayed to another node.
  - `Workload` reuses public `bohux.v1.Runtime`, `SecretRef`, `LogLine`,
    `TerminalSize`, `ExecExit`. Secret values travel in `sealed_secrets` bytes (sealing
    format decided in S4). No `Spec.Raw` passthrough yet.

#### S1d — cleanup ✅ (folded into S1b/S1c)
- [x] Delete `proto/bohux/v1/service.proto` and its ignore in `buf.yaml` (done in S1b)
- Final lint/generate check moved to S1c.

### S2 — Storage ✅
Split into sub-slices, one session each.

#### S2a — migration tool ✅
- [x] Migration tool choice, as a proposed ADR (it is a new dependency): ADR-0021,
      goose v3.26.0 on pgx v5.7.6 (last versions supporting Go 1.23)

#### S2b — schema ✅
- [x] Schema: tenants, projects, environments, apps, releases, deployments, nodes,
      secret_refs, revisions, audit_events; `tenant_id` + row-level security everywhere;
      `<resource>_revisions` + `<resource>_heads` per ADR-0017
- [x] Also: role_bindings, api_tokens, node_bootstrap_tokens; append-only triggers;
      linear-history constraints; `store.Migrate` (refuses superuser/BYPASSRLS roles);
      file-convention unit test; integration tests (RLS, invariants, up/down/up);
      CI `integration` job with a Postgres service; dev stack `bohux_app` role
- Choices made in S2b (documented in data-model.md "Schema rules"): identity in heads,
  mutable state in revisions; enums as checked lowercase text; nested messages as
  protojson `jsonb`; observations (`last_seen_at`, `last_used_at`) not versioned;
  bootstrap tokens not versioned. Idempotency-key storage is left to S3 (it must not
  store responses that contain secrets, e.g. `CreateApiToken`).
- **Open for S3:** authentication looks up token hashes *before* the tenant is known,
  which row-level security blocks. Options: tokens embed the tenant ID, or a narrow
  non-RLS `token_hash → tenant_id` index table. Decide in S3.

#### S2c — write path ✅
- [x] Revision + audit event written in one transaction (ADR-0004, ADR-0017):
      `store.Open` (pgxpool, refuses privileged roles), `Store.InTenant`, and
      `Tx.Create` / `Tx.Update` / `Tx.Delete` with head CAS, UUIDv7 IDs
      (`github.com/google/uuid`), and `ErrNotFound` / `ErrConflict` / `ErrAlreadyExists`
- [x] Integration tests against Postgres 16 (`integration` tag): conflict writes
      nothing, failed transaction rolls back the audit event, cross-tenant writes are
      `ErrNotFound`, delete keeps the snapshot and frees the slug

### S3 — API and access ✅
Split into sub-slices, one session each.

#### S3a — access design ✅
- [x] Proposed ADR-0022: token formats, pre-tenant lookup, hashing, idempotency storage

#### S3b — control plane mode ✅
- [x] `bohux controlplane` serves ConnectRPC (h2c) with health; runs `store.Migrate` at
      start; `bohux admin migrate`. Config from flags/env, documented in one place
      (`docs/reference/config.md`). Default listen `127.0.0.1:7400` (a choice; change
      freely before v0.1). Binary smoke-tested: migrate, serve, `/healthz`, SIGTERM → 0,
      superuser DSN refused

#### S3c1 — tokens and authentication ✅
- [x] Migration `00012_idempotency_keys` (ADR-0022)
- [x] `internal/auth`: `bxa_`/`bxb_` token issue/parse, SHA-256 hash, constant-time
      match, secret never printed (String/GoString/LogValue, tested); `Principal`,
      `Role`; `Authenticator.AuthenticateAPIToken` (tenant-first lookup, expiry,
      revocation, throttled `last_used_at`); `CreateAPIToken`
- [x] `store.Tx.CreateWithID`, `Exec`, `Tenant`; `store.NewID`

#### S3c2 — authorization interceptor ✅
- [x] Interceptor: bearer token → Principal in ctx; per-RPC permission table
      (`internal/api/permissions.go`); test that every bohux.v1 RPC has an entry and
      no entry is stale; unary and streaming covered; CI integration job generates
      `gen/` before tests
- [x] Tenant-level RPCs: proposed ADR-0023 (O8). Empty `project_id` = tenant-wide
      token; `identity.proto` changed first; migration `00013`; `Principal.TenantWide`,
      `CanAccessProject`, `RequireProject` (handler step), `CanGrant` (no escalation)

#### S3c3 — bootstrap and identity handlers ✅
- [x] `bohux admin bootstrap` (migrates, creates the tenant and a tenant-wide owner
      token, prints it once); `api.Bootstrap`; `store.SystemActorID`
- [x] IdentityService (WhoAmI, CreateApiToken with no-escalation, ListApiTokens scoped
      and paged, RevokeApiToken) and TenantService.GetTenant, mounted in the control
      plane behind the interceptor; error mapping that never leaks internals
- [x] ADR-0022 idempotency: request hash with `debug_redact` fields cleared (tested),
      replay returns metadata without the secret, same key + different request is
      `FAILED_PRECONDITION`
- [x] Integration tests over real ConnectRPC; binary smoke test: bootstrap, duplicate
      slug refused, `WhoAmI` over HTTP JSON, unauthenticated call refused

#### S3d — redaction ✅
- [x] `internal/secrets`: `Redacted` (clears every `debug_redact` field, any depth) and
      the `Message` wrapper (String/GoString/Format/LogValue all redacted), tested for
      every redacted field in bohux.v1 and the agent protocol
- [x] Access-log interceptor (outermost; procedure, code, duration, principal; never a
      body). Rule 3 integration test: `SetSecretRequest.value` and token secrets absent
      from logs and error strings on success, bad token, denial, and unimplemented
- [x] Every bohux.v1 service is mounted; unwritten ones answer `Unimplemented` behind
      auth and logging
- Traces: no tracing exists yet. When OpenTelemetry is added, span attributes must
  go through `secrets.Message` too.

#### S3e1 — ProjectService ✅
- [x] Create (tenant-wide owner), Get by id/slug (hidden across projects), List
      (scoped, paged), Update (owner, CAS), Delete (tenant-wide owner, refuses live
      environments, frees the slug); idempotent; audited. Slugs are DNS labels
      (`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`, a choice)
- [x] RoleBinding RPCs answer `Unimplemented` with the reason: bindings bind user
      principals, which arrive with user accounts (v1.0); tokens carry their role
      (ADR-0023). Stored bindings would have no effect

#### S3e2 — EnvironmentService ✅
- [x] CRUD per api-contract.md; `node_id` must name a live node in the tenant;
      delete refuses live apps; config stored as a JSON object

#### S3e3 — AppService ✅
- [x] CRUD + ScaleApp. Validation to write: source is git (repo_url) or image
      (repository + `sha256:` digest); replicas ≥ 0; ports 1–65535, host ports
      0–65535 and unique; health port is one of the container ports; secret keys are
      env-var names (`^[A-Z_][A-Z0-9_]*$`), unique, and each (key, handle) is a live
      secret_ref in the app's environment. ScaleApp writes a revision only; the new
      assignment comes with S4/S6
- Not validated (the provider will): `cpu`/`memory` quantity syntax. DeleteApp does
  not yet remove the workload from nodes; that is an assignment change (S4/S6)

### S4 — Agent ✅
Split into sub-slices, one session each.

#### S4a — node PKI and crypto ✅
- [x] Proposed ADR-0024 (O9): node CA key storage, cert lifetime/renewal, URI SAN,
      assignment signing, secret sealing

#### S4b1 — PKI and NodeService ✅
- [x] `internal/pki`: CA + Ed25519 signing key created or loaded from
      `--data-dir/pki` (0600 enforced, never overwritten, partial set refused); CSR →
      90-day client cert with the URI SAN only; agent listener server cert for
      `control-plane.bohux.internal`; `NodeIdentity`; domain-separated assignment
      signatures. Unit-tested
- [x] `CreateBootstrapTokenResponse.ca_cert_sha256` (proto first)
- [x] NodeService: CreateBootstrapToken (tenant-wide owner, default 1 h, max 24 h,
      hashed, audited, shown once), Get/List (tenant-wide), Revoke (CAS, state
      `revoked`); `store.Tx.AuditOnly` for non-versioned records
- [x] `bohux controlplane --data-dir` (`BOHUX_DATA_DIR`, default `/var/lib/bohux`)

#### S4b2 — AgentService.Register ✅
- [x] Agent TLS listener (`--agent-listen`, default `127.0.0.1:7443`, TLS 1.3, node-CA
      server cert, client certs verified if given); `internal/agentapi.Register`:
      single-use unexpired bootstrap token (tenant-first), CSR → client cert, node row
      (state online, providers, sealing key), token marked used, audit events, one
      transaction. Tested over real TLS: bad CSR leaves the token unspent; reuse,
      expiry, wrong kind, slug conflict, short sealing key refused

#### S4c1 — control-plane Connect ✅
- [x] `Connect`: identity from the verified client cert (`WithPeerCert`), first message
      must be `Hello` for that node, revoked/unknown nodes refused; skew policy in
      `internal/version` (protocol N and N-1; agent minor equal or one behind, same
      major; newer agents refused; `dev` control plane accepts any) →
      `VersionRejected` and node state `rejected`; online while open, pinged, offline
      when the newest stream for the node ends (a quick reconnect is not marked offline)
- **Note:** a client that only cancels its context does not reset an HTTP/2 stream
  whose request body is open; the agent must `CloseRequest` on shutdown. A crashed
  agent drops TCP, which the server sees. Half-open links: once StatusReports flow
  (S4d/S4e), close streams silent for 3 report intervals.

#### S4c2 — agent process ✅
- [x] `internal/agent`: state dir (0700/0600; ECDSA key, X25519 sealing key, cert,
      pinned CA, identity); `Register` pins the CA by hash from the server's chain
      (the agent listener now sends the CA in its chain); `Run` keeps the stream open,
      reconnects with capped backoff, waits 5 min after `VersionRejected`, never exits
      on control-plane absence, and closes its side in order on shutdown
- [x] Refuses to register without Docker `live-restore` (`docker info`, behind an
      interface); refuses a second registration into the same state dir
- [x] `bohux agent register` / `bohux agent run`, documented in config.md
- [x] Integration tests against a real agent listener: lifecycle online → offline,
      wrong pin, live-restore off, rejected version keeps running, control plane down
      then back. Not smoke-tested as a binary here (no Docker daemon)

#### S4d1 — control-plane assignments ✅
- [x] `SecretValues` (the sealed plaintext, `debug_redact`) in agent.proto first
- [x] Migration `00014_assignments` (append-only, RLS) + `nodes_heads.acked_generation`
- [x] `agentapi.Server.Assign`: next generation per node (node row locked), full state,
      handles only, audited; wakes the node's open stream
- [x] On send: resolve handles (`SecretResolver`, OpenBao in S7), seal per workload
      with `nacl/box` to the node's key, sign the exact bytes. **Choice:** nothing
      secret, not even sealed, is stored in Postgres; sealing and signing happen per
      send, so a re-send also picks up rotated values
- [x] Pushed on `Connect` when the agent is behind and on every new generation; `Ack`
      recorded (unknown generations ignored). Tested over mTLS

#### S4d2 — agent assignment cache ✅
- [x] `agent.Cache`: verify (signature, key id, node), accept only newer generations,
      cache atomically (0600, secrets still sealed), signal changes; `Ack`;
      `Hello.applied_generation` from the cache; `State.OpenSecrets` for the provider
- [x] A cache that no longer verifies is moved to `assignment.pb.corrupt` and the
      agent starts empty and resyncs (ADR-0003). **Rule for S4e:** an empty cache means
      "change nothing", never "remove every workload"
- [x] Unit + integration tests: forged, foreign, stale refused; restart keeps the cache
      and receives newer generations; secrets sealed at rest and open with the node key

#### S4e1 — provider interface and offline reconcile ✅
- [x] `internal/provider.Provider` exactly as the spec (Describe + seven verbs) with its types;
      types documented in provider-interface.md (O10)
- [x] `agent.Reconciler`: desired = cached assignment, observed = `Status`, `Apply`
      when missing, drifted (hash), or not running; timer + cache-change trigger; no
      assignment changes nothing; removed workloads destroyed via `applied.json`
      (written before `Apply`); secrets opened lazily; rotation re-applies
- [x] Tested with a fake provider and no control plane at all (rule 1): converges,
      idempotent, crash restarted, rotation, removal (also after restart), empty
      assignment, wrong provider
- [x] Wired into `bohux agent run` beside the stream; until S5 there is no provider in
      the build, so the agent says so and only caches

#### S4e2 — status reports ✅
- [x] The reconciler publishes each pass's report; the stream sends it on change and
      at least every 30 s; without a reconciler the agent heartbeats an empty report
      with its cached generation
- [x] Control plane stores the latest report per node (migration `00015`, observation
      columns) and closes streams silent for 90 s (configurable); the node goes offline
- [x] Tested over mTLS: report stored, silent stream closed, a quiet healthy agent
      causes no state changes, reconciler reports (fake provider) reach the control
      plane

### S5 — Docker provider ✅
Split into sub-slices, one session each.

#### S5a — plan ✅
- [x] A Docker daemon runs in the cloud container (see *Known gaps*), so conformance
      runs here against real Docker
- [x] Proposed ADR-0025 (O11): drive `docker compose` against the written artifact, no
      Go Docker client; agent-side HTTP health; layout, project names, labels; json-file
      log rotation in the artifact
- [x] `Workload.app_slug` / `environment_slug` (additive proto change, ADR-0025)
- [x] Provider interface moved to `internal/provider/provider.go`, where the
      new-provider skill says it lives (not a public contract; third-party providers
      are gRPC plugins, ADR-0016)

#### S5b — conformance suite ✅
- [x] `internal/provider/registry.go` (explicit `Registry`, no init-time globals)
- [x] `internal/provider/conformance`: 13 ordered cases covering every case in the
      spec (plan does not mutate, apply → healthy, no-op re-apply without churn,
      secrets only in the secret file and seen by the workload, rotation, changed spec,
      interrupted applies without duplicates, rollback without orphans, scale, logs
      and follow ending cleanly, exec output and exit code, destroy complete and
      repeatable), with an `Inspector` per provider
- [x] `internal/provider/memory`: a full in-memory provider that passes; four broken
      variants each fail exactly the case meant to catch them (runs in `go test ./...`)
- [x] `providers/_conformance`: `go test ./providers/_conformance -provider=memory`

#### S5c — Docker: Describe, Plan, Apply, Status, Destroy ✅
- [x] All eight methods (`providers/docker`, one file per verb), Inspector,
      busybox-httpd fixture, unit tests, `docs/providers/docker.md`, CI job
      `conformance-docker`
- [x] **Docker conformance: all 13 cases green** against Docker 29.9 / Compose 5.6 in
      the cloud container (91 s; recreate steps take ~10 s because busybox httpd
      ignores SIGTERM and waits for the stop timeout). No leftovers afterwards
- [x] Compose artifact + 0600 `secrets.env` (rule 2, ADR-0008), labels, spec hash,
      HTTP health; lifecycle conformance cases green against the local daemon
      (re-confirmed in S5e)

#### S5d — Docker: Logs, Exec, Scale ✅ (done within S5c)
- [x] Logs, Exec (no TTY yet, documented), Scale (rewrites the artifact); their
      conformance cases are green

#### S5e — ADR-0020 duties and wiring ✅
- [x] `bohux agent run` builds the Docker provider through an `internal/provider`
      `Registry`; artifacts under `<state dir>/workloads`; target is the local daemon
      or `DOCKER_HOST`. No new flags (one agent, one target, ADR-0007)
- [x] Control plane `Assign` resolves each workload's App in the same transaction and
      fills `environment_id`, `app_slug`, `environment_slug`; an unknown App (or one in
      another tenant) or a mismatched environment is refused
- [x] Image retention as an agent task (`agent.Retention`, hourly): per App keep the
      current Release + the 5 newest others by the `io.bohux.*` image labels, never an
      image any container (running or stopped) uses, nothing without a cached
      assignment. `provider.ImageStore` is an optional interface, not a verb; Docker
      implements it. Tested against the real daemon
- [x] Disk-pressure fail-closed (`agent.DiskMonitor`, 30 s): below 10% free on the state
      dir's or Docker data root's filesystem, every StatusReport carries
      `degraded` + `DISK_PRESSURE`, sent at once on a flip. `Degraded()` is what S7's
      build path must check; there is no build path yet
- [x] Fixed a race in `Assign`: the node lock joined heads to revisions with
      `FOR UPDATE OF h`, so a concurrent state change (node coming online) made Postgres
      drop the row on recheck and `Assign` failed with "not found". Now the head is
      locked alone, then its state read
- [x] `docs/providers/docker.md`, `docs/reference/config.md`; Docker conformance still
      green (92 s)

### S6 — Deploy workflow ✅
Split into sub-slices in S6a, one session each. Shape: ADR-0026 (O12).

#### S6a — plan, local Temporal, rollback test first ✅
- [x] Temporal dev server running in the cloud container; how, in *Known gaps*
- [x] `.claude/skills/new-workflow/` (referenced by ADR-0002)
- [x] Temporal Go SDK `go.temporal.io/sdk` v1.41.1 (last version supporting Go 1.23).
      No ADR of its own: ADR-0002 chose Temporal. It raised `google.golang.org/protobuf`
      to v1.36.6, so CI's `protoc-gen-go` pin moved to v1.36.6 too
- [x] Proposed ADR-0026 (O12): worker in the control plane, one workflow ID per App,
      row written by API and workflow, per-App assign under the node lock, polling
      health activity, compensation by reassignment
- [x] Rest of S6 split into sub-slices (below)
- [x] Rollback/compensation test written first (rule 5): `internal/workflow/deploy`
      workflow against mocked activities: never healthy → last healthy reassigned;
      failed workload; first deploy removes its workload; assign error; compensation
      that fails; cancel during the gate; cancel before assign; invalid Release. The
      activities are stubs until S6b

#### S6b — activities ✅
- [x] Migration `00016`: `deployments_revisions.workflow_run_id`
- [x] `RecordStart` (insert-if-absent with the pre-generated ID under the App lock,
      then validate Release ready, artifact available, same App and environment),
      `SetState` (no-op on repeat or once terminal, audited as `bohux.workflow/Deploy`
      with the caller as actor), `LastHealthyRelease` (newest `succeeded` by
      `finished_at`). `InsertDeployment` and `ValidateRelease` are exported for the API
- [x] `agentapi.Server.AssignApp`: per-App read-modify-write of the latest generation
      under the node lock; idempotent per Deployment; nil removes the workload; other
      workloads kept as stored. `Assign` shares the locked path. `ErrRevokedNode`,
      `ErrUnknownApp`, `ErrWrongEnv` exported
- [x] `AssignRelease` builds the workload from the Release and its captured App
      revision (image source → `repository@digest`, else the artifact digest)
- [x] `WaitHealthy`: polls the stored report (2 s default) with heartbeats; target
      Release `HEALTHY` with ready ≥ replicas; `FAILED` fails at once; deadline →
      `HealthTimeout`
- [x] Integration tests against Postgres for every activity; fixtures `Node`,
      `DeployableApp`, `Release`
- **Found:** Temporal treats a *wrapped* non-retryable error as retryable, so
  activities return the `ApplicationError` bare (`applicationError`). Tested
- **Not handled yet:** `ScaleApp` and App edits change no running workload until the
  next deploy (a Release captures its App revision). Decide in S6c whether ScaleApp
  starts a deploy of the current Release

#### S6c1 — worker and ReleaseService ✅
- [x] `bohux controlplane --temporal-address --temporal-namespace` (default
      `127.0.0.1:7233`, `default`); lazy Temporal client; worker on `bohux-deploy`
      restarted every 5 s until Temporal answers. Tested: the control plane serves and
      stops cleanly with nothing listening on the Temporal address
- [x] `deploy.Starter` (start with "fail if running" → `ErrRunning`; cancel one run);
      `api.Server.Deploys`
- [x] ReleaseService: CreateRelease from an image (explicit, or the App's image source)
      → `READY`, `source_ref` = `repository@digest`, captures the App revision; Git →
      `Unimplemented` until S7. Get, List (newest first, paged). Integration-tested
- [x] The workload runs the Release's `source_ref` when it is a pinned image, else the
      artifact digest (an explicit image can differ from the App's source)
- [x] `fixture.Tenant`; agentapi and deploy tests no longer import `api` (it imports
      `deploy` now, which imports `agentapi`)

#### S6c2 — DeploymentService ✅
- [x] Create: validate (Release ready, available, in this environment; `rollback_of`
      a Deployment of the same App), `Deploys.Start`, then `deploy.InsertDeployment`
      with the run IDs; `ErrRunning` → `FAILED_PRECONDITION`; Temporal down or no
      `Deploys` → `UNAVAILABLE`; idempotent
- [x] Get, List (newest first, optional App filter), Watch (re-reads every 1 s, sends
      each new revision, ends when terminal), Rollback (empty target = newest succeeded
      Release other than the current one; `rollback_of` = the current Deployment),
      Cancel (audited, cancels the stored run; terminal returned unchanged)
- [x] Integration test on the real Temporal dev server (CI starts one too): deploy →
      succeeded; failed workload → rolled back to r1; concurrent deploy refused; cancel
      at the gate → cancelled with r1 back; rollback → r1; list, replay, watch. Stable
      over repeated runs
- **Found:** the SDK's `ExecuteWorkflow` returns the *running* run instead of failing
  unless `WorkflowExecutionErrorWhenAlreadyStarted` is set. And a run stays open a
  moment after its last state write, so a deploy right after a finished one retries
  for up to 5 s when no Deployment of the App is open
- **Open:** if the run starts but the API's row write fails, a retry with the same key
  is refused while that run is open (the run still writes its row). Acceptable for
  v0.1; revisit with the CLI (S8)

#### S6e — ScaleApp reaches the node ✅
- [x] Proposed ADR-0027 (O13): replicas are not part of a Release; a workload always
      runs the App's current replicas; ScaleApp writes the App revision and the node
      generation in one transaction (not a deploy, no workflow)
- [x] `agentapi.Server.ScaleAppTx` (inside the caller's transaction, under the node
      lock; no workload or same replicas → nothing written) and `Wake`; `write` split
      into `writeTx`
- [x] `ScaleApp` assigns through `api.Server.Agents`; deploy activities take replicas
      from the App's current revision. Integration-tested at all three layers
- [x] data-model.md and api-contract.md say so

### S7 — Build and secrets ✅
Split into sub-slices, one session each.

#### S7a — secret storage ✅
- [x] `internal/secrets/openbao`: KV v2 client over plain HTTP (Put/Get version/
      Destroy/Ping; bodies never copied into errors), handles
      `openbao:bohux/<tenant>/<secret id>`, `Resolver` (agentapi.SecretResolver) that
      reads the **version Postgres recorded** and refuses another tenant's handle
- [x] SecretService: SetSecret (create; overwrite only with `expected_revision_id`;
      value to OpenBao first, revision records the version; backend down →
      `UNAVAILABLE`), ListSecretRefs, DeleteSecret (refused while a live App
      references it; destroys the value after commit)
- [x] `bohux controlplane --openbao-address --openbao-token-file --openbao-kv-mount`;
      the resolver is wired into the agent API, so assignments with secrets are sealed
      and sent
- [x] Tests: client and resolver on a real OpenBao dev server (CI starts one);
      SecretService with an in-memory backend, including a scan of every Postgres
      table for the value (rule 3)

#### S7b — rotation reaches nodes ✅
- [x] `agentapi.Server.RotateTx`: a new generation (same workloads, re-checked under
      the node lock) for every node whose latest assignment references the handle;
      revoked nodes skipped. SetSecret's overwrite calls it in its transaction and wakes
      the nodes after commit; the new generation is sealed with the new value at send
      time and the agent re-applies (its spec hash covers secret values, keyed)
- [x] Tested over mTLS (the node receives generation 2 whose sealed values open to the
      new value; a node without the secret stays at 1) and through SetSecret

#### S7c1 — where builds run, and the agent's builder ✅
- [x] Proposed ADR-0028 (O14): the agent builds, over its stream, with a buildx
      `docker-container` builder named `bohux` (memory 1 GB, one CPU, 10 GB cache cap;
      an operator-made `bohux` builder is used as is), `--load` into dockerd; public
      Git over `git fetch --depth 1`; a build workflow per Release
- [x] `BuildRequest` / `BuildResult` in agent.proto (additive)
- [x] `agent.Builder`: disk pressure → `DISK_PRESSURE`, request validation (no
      `..`, no option-looking ref or URL), fetch by branch/tag/SHA, labelled image,
      recent results answered without rebuilding; one build at a time. The stream
      runs builds beside its loop and sends results; `bohux agent run` wires it
- [x] `agent register` refuses a node without buildx (`ErrNoBuildx`)
- [x] Tested against the real daemon: build by tag and by SHA, labels, cached repeat,
      unknown ref, failing Dockerfile, bad subpath/ref, disk pressure

#### S7c2 — build workflow and CreateRelease from Git ✅
- [x] `agentapi.Server.Build`: sends a `BuildRequest` on the node's open stream (the
      session's sender loop) and waits for the matching `BuildResult`; no stream or a
      stream that ends → `ErrNodeOffline`. Tested over mTLS
- [x] `BuildWorkflow` (`build/<release id>`, same worker and queue): `RunBuild` sends
      and waits with heartbeats (node offline and a not-yet-written Release are
      retried; 2 h overall), `FinishBuild` writes the Release `READY` (image ID,
      commit, available) or `FAILED` (reason); a Release no longer building is left
      alone
- [x] CreateRelease from a Git ref or the App's Git source: build started first, then
      the Release row `BUILDING` with `build_workflow_id` and the requested ref
- [x] Deploy of a `BUILDING` Release: `RecordStart` reports it, the deploy waits in
      `BUILDING` (`WaitReleaseReady`, up to 6 h), then rolls out; a failed build fails
      the deploy with `BUILD_FAILED` and assigns nothing. Workflow-tested with mocks,
      activities against Postgres, and build → deploy on real Temporal

#### S7c3 — build end to end ✅
- [x] `cmd/bohux/e2e_test.go` split into a `stack` helper and two tests.
      `TestBuildEndToEnd`: an App with a Git source (a local repository), Releases
      created through the API from tags, built by the real agent with buildx, deployed
      (build + deploy 4.6 s); a broken build rolls back to the healthy one. Recreate
      downtime measured again: 0.35–1.5 s across runs
- **Found:** the cloud container's disk allowance makes free space read ~9.5%, under
  the 10% threshold, so the agent correctly refused builds (`DISK_PRESSURE`). See
  *Known gaps*

#### S7d — OpenBao in the install bundle ✅ (done in S9d)
- [x] Auto-unseal with a node-local key (ADR-0020 item 6): the static seal, key at
      `/etc/bohux/openbao-unseal.key` (0400, the container's openbao uid); runbook
      `openbao-unseal-key.md`

### S8 — CLI ✅
Split into sub-slices, one session each.

#### S8a — deploy, rollback, scale ✅
- [x] `internal/cli`, wired into `bohux deploy | rollback | scale`: slugs resolved
      through Get*-by-slug (`-p` / `BOHUX_PROJECT`, `-e`), `BOHUX_API_URL` (h2c for
      `http://`) and `BOHUX_TOKEN`; no config file. `deploy` creates a Release (`--ref`,
      `--image`, or the App's source) and follows the Deployment over
      `WatchDeployment`, exiting non-zero unless it succeeded; the host-port downtime
      note is printed first (ADR-0020 item 5); `rollback`; `scale`. Fresh idempotency
      key per invocation
- [x] Unit tests (argument order, image refs, refusals before any call) and the CLI
      driving `TestBuildEndToEnd`: deploy from Git, a broken build rolls back, deploy,
      rollback, scale to 0 and back
- [x] `docs/reference/config.md` CLI section

#### S8b — logs and exec ✅
- [x] agentapi `Logs` / `Exec` relays over the node's stream session (waiters per
      request/session; slow log clients drop lines rather than stall the stream; exec
      output waits up to 2 s); `CloseStdin` vs detach kept apart (found: detaching on
      client EOF dropped the exit and hung the call)
- [x] Agent `relays`: log requests (cancellable, follow), exec sessions (stdout/stderr
      pumps, stdin, resize, exit; 126 when it cannot start), through the provider's
      Logs and Exec via the Reconciler; one outbox into the stream's sender
- [x] WorkloadService.StreamLogs (viewer) and Exec (deployer, audited per session;
      first message must be `start`)
- [x] `bohux logs`, `bohux exec … -- cmd` (exit status propagated by the binary). Unit
      tests for the relays; the CLI drives both in `TestBuildEndToEnd`, which also
      checks each exec session is audited

#### S8c — backup and restore ✅
- [x] Proposed ADR-0029 (O15): tar stream encrypted with age (`filippo.io/age` v1.2.1,
      last for Go 1.23) to an operator-held X25519 recipient; Postgres copied
      logically per table (`COPY`, one repeatable-read snapshot) over a privileged
      DSN; every version of every OpenBao value under `bohux/<tenant>/` (restored in
      order so version numbers match what Postgres records; destroyed versions
      re-destroyed); the node CA
- [x] `internal/backup` (`Write`, `Restore`: empty target only, schema recreated at the
      archive's migration with `store.MigrateTo`, rows copied with
      `session_replication_role = replica`); `store.LatestMigration`; OpenBao client
      `List`, `Versions`, `DestroyVersions`; `storetest.AdminDSNFor`
- [x] `bohux admin backup | restore` (`--admin-database-url` /
      `BOHUX_ADMIN_DATABASE_URL`), documented in config.md
- [x] Round trip tested (rows, secret at its recorded version, destroyed version gone,
      node CA identical and loading, wrong identity and non-empty target refused,
      nothing readable in the archive), and the **drill end to end** (`TestRestoreDrill`,
      CI job `e2e`): deploy v1 and v2, back up, wipe the control plane, restore, the
      same agent reconnects, `bohux rollback` → v1 serves
- **Fixed on the way:** the control plane's shutdown waited 10 s on open streams and
  reported an error; every handler's context now ends when shutdown begins
- **Known gap:** node state stays "online" in Postgres after the control plane dies
  until the agent reconnects (no stream ends to mark it offline). Status only;
  workloads unaffected. Fix with a start-up sweep once tenants can be listed by the
  control plane (RLS blocks it today)
- **Not done (ADR-0020 item 7):** the scheduled nightly backup (a Temporal schedule)
  and S3 targets. Planned with the install bundle (S9)

### S9 — Exit: Bohux deploys Bohux ✅
Split into sub-slices, one session each. Includes S7d (OpenBao auto-unseal).

#### S9a — install layout and self-deploy design ✅
- [x] Proposed ADR-0030 (O16): base (dockerd, binary, agent as a systemd unit), infra
      (Postgres, Temporal, OpenBao as one Compose project), and the control plane as an
      App of itself (`bohux/system/controlplane`), installed by `scripts/install.sh`
      through a seed control plane
- [x] Proposed ADR-0031 (O17): agent-side fallback — a workload carries the previous
      workload and a deadline; the agent applies it only if the new one was never
      healthy, the deadline passed, and nothing newer arrived

#### S9b — agent-side fallback ✅
- [x] Proto first: `Workload.fallback`, `Workload.fallback_after`
- [x] Rollback test first (rule 5, `internal/agent/fallback_test.go`, no control
      plane): never healthy past the deadline → fallback applied and reported
      (`FALLBACK_APPLIED`); kept across a restart (`fallback.json`, per deployment);
      a newer assignment wins with no churn when it is the same Release; a deploy that
      was healthy keeps no fallback
- [x] Reconciler: `effective` workload per pass, spec hash ignores the fallback fields
- [x] `AssignApp(…, fallbackAfter, …)`: a forward deploy over another Release carries
      the replaced workload (its own fallback stripped); compensation passes zero;
      sending seals the fallback's secrets; rotation counts fallback references
- [x] Workflow: forward assign sets health timeout + 2 min; compensation none. Tested
      in the workflow suite, agentapi, and the e2e suite (all green)

#### S9c1 — provider options on Apps ✅
- [x] Proposed ADR-0032 (O18): `App.provider_options` (Struct) → `Workload.provider_options`
      (JSON) → `Spec.Raw`; owner role to set or change; the Docker provider accepts only
      external `networks` and named `volumes` (no host paths), refuses unknown keys;
      the control plane App gets its config as secrets (O5 stays open)
- [x] Proto first; migration `00017`; AppService (owner check on any change, kept by
      ScaleApp and by updates that leave them alone); deploy activity carries the
      Release revision's options; agent passes them as `Spec.Raw`
- [x] Docker render (external networks incl. `default`, external volumes) with unit
      tests and a real-daemon test (attached, mounted, both left in place by Destroy;
      CI integration now also runs `./providers/docker/...`); API and activity tests
- [x] `BOHUX_OPENBAO_TOKEN` for the control plane when no token file is given
- **Fixed:** `TestRegisterRefusals/wrong CA pin` failed one run in 256 (its "wrong" pin
  was the right one when the real pin started with `00`)

#### S9c2 — the control plane as an image ✅
- [x] `Dockerfile`: `gen/` used when in the context, else generated with buf (needs the
      Buf registry); `vendor/` used when present (offline builds), else modules
      downloaded; static binary on `distroless/static:nonroot` (45.6 MB); listens on
      0.0.0.0:7400/7443, data dir `/var/lib/bohux` owned by the runtime user so a fresh
      named volume inherits it; `make image`
- [x] Smoke-run here against local Postgres and Temporal: healthy, node CA written into
      the named volume (0600, uid 65532), deploy worker polling. CI job `image` builds
      from a clean checkout and checks the stamped version
- **Cloud container note:** build containers cannot reach the Go module proxy here (the
  egress proxy's CA is not in them); build from a copy with `go mod vendor` and `gen/`

#### S9d — install bundle ✅ (also S7d)
- [x] `deploy/install/compose.yaml`: Postgres (unprivileged `bohux_app`; `temporal`
      owning its two databases, auto-setup told to skip creating them), Temporal,
      OpenBao 2.4 with **file storage and the static seal** (ADR-0020 item 6 / S7d:
      verified it comes back unsealed after a restart), on the `bohux-infra` network;
      Postgres published on 127.0.0.1 only, for admin backups
- [x] `deploy/install/bohux-agent.service`; `scripts/install.sh` (idempotent; secrets
      0600 under `/etc/bohux`; OpenBao init with one recovery key and KV v2 at
      `secret`; tenant bootstrap; seed control plane; agent registration and unit;
      project `bohux`, environment `system`, config as secrets, the `controlplane` App
      with provider options; first deploy handed over from the seed to the App
      mid-run)
- [x] Docker option `publish_address` (ADR-0032 amended while proposed): the control
      plane's ports bind to 127.0.0.1, since v0.1 has no TLS ingress
- [x] Health gate: only `SPEC_INVALID` / `PROVIDER_MISSING` end it at once; failures the
      agent retries (a port still held) wait for the deadline (ADR-0026 amended while
      proposed). This is what lets the seed hand over
- [x] `bohux deploy|rollback` keep following through a control-plane restart (reconnect
      for up to 10 minutes)
- [x] **Run here end to end (`BOHUX_SYSTEMD=0`)**: install from nothing → the control
      plane runs as `bohux/system/controlplane`; `bohux deploy controlplane --image v2`
      → succeeded (the new control plane's worker finished the run); `bohux rollback`
      → v1; a version that cannot start → the agent fell back at 7 min (5 min gate +
      2 min margin, ADR-0031), the control plane came back and finished the run as
      `rolled_back`. **The v0.1 exit criterion works by hand.**

#### S9e — Bohux deploys Bohux, in CI ✅
- [x] `scripts/selfdeploy-test.sh` and CI job `selfdeploy` (systemd, real runner) do
      what S9d did by hand: local registry, image, `scripts/install.sh`, deploy v2,
      rollback, broken version → fallback, plus (S9f) a triggered nightly backup.
      Installer option `BOHUX_DEPLOY_HEALTH_TIMEOUT` (a secret, like the rest of its
      config) shortens the gate to 60 s. **Passed here** (`BOHUX_SYSTEMD=0`): v2 in
      19 s, rollback in 35 s, broken version recovered in 242 s (60 s gate + 2 min
      margin + restart)
- [x] Found by the test: a container whose first start failed (seed still holding the
      port) came back from `compose up` on its first network only, so the control
      plane lost `bohux-infra`. The Docker provider now re-creates on a restart plan
      (`--force-recreate`); regression test against a real daemon
- [ ] Footprint re-measured and published (with O2): needs the owner's 2 GB VPS

#### S9f — schedules and runbooks ✅
- [x] Proposed ADR-0033 (O19): nightly backup as a Temporal schedule
      `bohux-backup-nightly` owned by the control plane (created/updated on start,
      removed when no recipient), `BackupWorkflow` = `WriteBackup` (`.partial` → rename,
      heartbeat, 3 attempts) + `PruneBackups` (keep 7, never after a failed write);
      a read-only `bohux_backup` role (`BYPASSRLS`, `SELECT` + default privileges);
      local directory on its own volume. S3 deferred
- [x] `internal/workflow/backup`, failure path tested first (a failed write never
      prunes, leaves no file); integration tests with the read-only role on real
      Postgres and the schedule on Temporal (create, update in place, trigger, remove)
- [x] Control plane flags `--backup-*` (docs/reference/config.md); installer option
      `BOHUX_BACKUP_RECIPIENT`, role, volume `bohux-backups`, image directory owned by
      the runtime user
- [x] Runbooks (`write-runbook`): `backup-and-restore.md` (key custody, nightly,
      by hand, restore), `control-plane-upgrade.md`, `openbao-unseal-key.md`,
      `infra-upgrade.md`

## v0.2 slices

Scope: `02-roadmap.md` v0.2. Ingress first: zero-downtime deploys, TLS and domains
build on it, and the CLI warns about host-port downtime until it lands.

### S10 — Routes and the node ingress ✅
Shape: ADR-0034 (O20). Split into sub-slices, one session each.

#### S10a — design and contract ✅
- [x] Proposed ADR-0034 (O20): `App.routes` (host, port), hosts unique across tenants
      via `route_hosts`, routes are App state (re-assign at once, no deploy), the agent
      owns its node's Caddy config (one `POST /load`, written to disk), providers
      report endpoints, Caddy in the infra project with its admin API on a Unix
      socket, health includes the loaded route
- [x] Proto: `bohux.v1.Route`, `App.routes`, `Create/UpdateAppRequest.routes`,
      `Workload.routes`, `WorkloadStatus.ingress_loaded`, reason `INGRESS_FAILED`

#### S10b — routes in the store and the API ✅
- [x] Migration 00018: `apps_revisions.routes` (jsonb array), `route_hosts` (host
      primary key, tenant RLS); `claimHosts` in the App create/update/delete
      transactions is the one write path
- [x] Validation (lowercase DNS name of two or more labels, no duplicates, port among
      the container ports); a taken host is `ALREADY_EXISTS`/`HOST_IN_USE`, naming no
      tenant (test with two tenants)
- [x] `ScaleAppTx` became `FollowAppTx`: replicas and routes follow the running
      workload in the same transaction, from UpdateApp and ScaleApp alike. Note:
      UpdateApp now also applies a replica change at once (ADR-0027 already said the
      workload always runs the App's current replicas)
- [x] `AssignRelease` carries the App's current routes; tested on an older Release
      (the rollback case) first. The agent's spec hash excludes routes (a route change
      never re-applies containers; unit test)

#### S10c — provider endpoints ✅
- [x] `provider.Spec.Ingress`, `provider.Status.Endpoints`; the agent sets `Ingress`
      when a workload has routes (in the spec hash; the hosts are not)
- [x] Docker joins such workloads to `bohux-ingress` (created if missing, never
      destroyed) under the project alias, reports `<project>:<port>`; the memory
      provider reports its own
- [x] Conformance case "ingress endpoints are reported and reachable" (Inspector
      `Reach`: Docker dials from a container on the ingress network) and a broken
      provider it catches; Docker conformance green on a real daemon

#### S10d — the agent's ingress ✅
- [x] `internal/ingress`: `Caddy.Render` (deterministic; admin listener kept on the
      socket with origin `localhost`; `.localhost` hosts on Caddy's internal CA, others
      ACME with `--acme-email`) and `Load` (skips an unchanged config, `POST /load`
      over the Unix socket, then writes `ingress/caddy.json`, so the file Caddy starts
      from only holds accepted configs)
- [x] Reconciler: routed workloads' endpoints (remembered across a replica restart)
      → one load per pass; `ingress_loaded` once all its routes are served;
      `INGRESS_FAILED` when the load fails or the node has no ingress; hosts removed
      with their routes. `agent run --ingress-admin-socket --acme-email`
- [x] Health gate: a routed workload is healthy only with `ingress_loaded`
      (`Assigned.Routed` → `WaitInput.Routed`)
- [x] Real-Caddy test (`caddy:2.8.4-alpine`): serves a `.localhost` host from a Docker
      alias, a failed load writes nothing and is retried, a restarted Caddy serves from
      the file alone. CI pulls the image
- [x] Found on the way: Docker's restart path never ran (a stopped container reports no
      hash) and `--force-recreate` broke an interrupted apply; a restart now removes
      stopped containers and runs `up`

#### S10e — Caddy in the install bundle ✅
- [x] `caddy` service (`caddy:2.8.4-alpine`, pinned by digest) in the infra project:
      ports `BOHUX_HTTP_PORT`/`BOHUX_HTTPS_PORT` (80/443), the external `bohux-ingress`
      network (the installer creates it; the Docker provider may too), certificates on
      volume `caddy`, started from the agent's `ingress/caddy.json` (the installer
      writes a first, admin-only one), admin socket in `BOHUX_INGRESS_SOCKET_DIR`
      (`/run/bohux-caddy`) at the same path inside and out
- [x] The agent gets the socket (systemd `Environment=`) and `BOHUX_ACME_EMAIL`
- [x] Self-deploy test: an App `hello` with route `hello.localhost` deployed through the
      CLI is served over HTTPS by the ingress. **Passed here**, with everything before it

#### S10f — zero-downtime deploys ✅
- [x] Proposed ADR-0035 (O21): the agent switches a routed App without host ports
      between two slots (`ResourceRef.Slot`, `blue`/`green`): apply the other slot, cut
      the ingress over in one load once it is healthy, record it, destroy the old one;
      a Release that never turns healthy never takes traffic
- [x] `ResourceRef.Slot` in the provider names (Docker: project suffix, empty = today's
      names); conformance case "slots of one app are independent workloads"
- [x] Agent: `slots.json` (live slot and its spec hash), the switch, the cut-over after
      a successful ingress load, the old slot destroyed after the switch is recorded;
      tested broken release first, then compensation, cut-over, failed cut-over,
      crash in place, agent restart. ADR-0035 item 6 amended while proposed: any spec
      change switches (replicas, secrets), routes alone do not
- [x] Self-deploy test: a second deploy of `hello` while a client polls every 100 ms:
      **127 requests, none failed** (passed here, with everything before it)

### S11 — The CLI covers the API (ADR-0036) 🔄
Rule 8: the UI gets nothing the CLI lacks, so the CLI comes first.

- [x] Proposed ADR-0036 (O22): `bohux <noun> <verb>`, `-o json` = the API's protojson,
      secret values only from stdin, `--idempotency-key`, no prompts
- [x] `project list|create`, `env list|create --node`, `app list|show|create`,
      `route list|add|remove`, `secret list|set [--app]`, `node list|token`; unit
      tests and an integration test against a real API server (OpenBao included)
- [ ] The self-deploy test and the installer use the CLI instead of `curl` where a
      command exists

## Known gaps and notes
- **`buf.lock` missing.** Whoever has registry access (CI, or a local machine) runs
  `buf dep update`, commits `buf.lock`, and removes the "Resolve deps if buf.lock is
  missing" steps in `.github/workflows/ci.yml`.
- **How to verify protos locally in the cloud container** (registry blocked): copy
  `proto/` and `buf.gen.yaml` into a scratch dir, fetch
  `google/api/field_behavior.proto` from raw.githubusercontent.com/googleapis/googleapis
  into `gapi/google/api/`, write a `buf.yaml` with modules `proto` and `gapi` (lint
  `ignore: [gapi]`), change the
  managed-mode `disable` entry to `path: google/api`, then `buf lint`, `buf build`,
  `buf generate proto`, copy `gen/` back, and `go build ./...`. `buf` and the
  `protoc-gen-*` plugins are in `/root/go/bin` (not on `PATH` by default).
- `gen/` is not committed. Run `make generate` (or `buf generate`) after cloning, before
  `go build ./...`. CI does this.
- The Buf Schema Registry was unreachable from the cloud container on 2026-10-21; S1
  may need to run `buf dep update` where the registry is reachable (CI or locally).
- The cloud container has no Docker daemon running at start; one can be started (below).
- **Local Docker in the cloud container:** `dockerd` is installed and runs as root:
  ```
  mkdir -p /var/lib/bohux-dockerd-test
  nohup dockerd --data-root /var/lib/bohux-dockerd-test --live-restore > /tmp/dockerd.log 2>&1 &
  docker run --rm busybox:1.36 echo ok      # image pulls work through the proxy
  ```
  Docker 29.9, Compose v5.6. Enough for provider conformance (S5).
- **Local Postgres in the cloud container:** Postgres 16 binaries are in
  `/usr/lib/postgresql/16/bin`. The scratchpad is not readable by the `postgres` user,
  so use its home:
  ```
  D=/var/lib/postgresql/bohux-test
  su postgres -c "mkdir -p $D && /usr/lib/postgresql/16/bin/initdb -D $D/data -U postgres --auth=trust && /usr/lib/postgresql/16/bin/pg_ctl -D $D/data -o '-p 55432 -k $D -c listen_addresses=127.0.0.1' -l $D/log start"
  export BOHUX_TEST_POSTGRES_URL='postgres://postgres@127.0.0.1:55432/postgres?sslmode=disable'
  go test -tags=integration -race -count=1 ./internal/store/...
  ```
  (skip `initdb` if `$D/data` exists).
- **Local Temporal in the cloud container:** `temporal.download` is blocked, GitHub
  releases are not:
  ```
  mkdir -p /opt/temporal && cd /opt/temporal
  curl -sSL https://github.com/temporalio/cli/releases/download/v1.4.1/temporal_cli_1.4.1_linux_amd64.tar.gz | tar xz
  nohup ./temporal server start-dev --headless --ip 127.0.0.1 --port 7233 --db-filename /opt/temporal/dev.db > /tmp/temporal.log 2>&1 &
  ./temporal operator cluster health --address 127.0.0.1:7233   # SERVING
  export BOHUX_TEST_TEMPORAL_ADDRESS=127.0.0.1:7233
  ```
  CLI 1.4.1 = Server 1.28.0. Namespace `default` exists. The SDK's
  `testsuite.StartDevServer` downloads from the blocked host, so do not use it.
- **Local OpenBao in the cloud container:** from GitHub releases:
  ```
  mkdir -p /opt/openbao && cd /opt/openbao
  curl -sSL https://github.com/openbao/openbao/releases/download/v2.4.1/bao_2.4.1_Linux_x86_64.tar.gz | tar xz
  nohup ./bao server -dev -dev-root-token-id=bohux-dev-root -dev-listen-address=127.0.0.1:8200 > /tmp/bao.log 2>&1 &
  export BOHUX_TEST_OPENBAO_ADDR=http://127.0.0.1:8200 BOHUX_TEST_OPENBAO_TOKEN=bohux-dev-root
  ```
  The dev server has KV v2 at `secret/` and keeps everything in memory.
- **BuildKit in the cloud container:** the `docker-container` builder cannot reach the
  egress proxy from its own network and Docker Hub rate-limits it, so builds here use a
  local registry as a `docker.io` mirror and a host-network builder the agent then
  finds as is (ADR-0028):
  ```
  docker run -d --name bohux-mirror --restart=always -p 127.0.0.1:5000:5000 registry:2
  docker tag busybox:1.36 localhost:5000/library/busybox:1.36 && docker push localhost:5000/library/busybox:1.36
  printf '[worker.oci]\n  gc = true\n  gckeepstorage = 10000000000\n[registry."docker.io"]\n  mirrors = ["localhost:5000"]\n[registry."localhost:5000"]\n  http = true\n' > /tmp/bkd.toml
  docker buildx create --name bohux --driver docker-container --driver-opt memory=1g \
    --driver-opt network=host --driver-opt image=moby/buildkit:v0.18.2 --buildkitd-config /tmp/bkd.toml
  ```
  CI runners need none of this: the agent creates the builder itself.
- **Disk pressure in the cloud container:** the session's disk allowance makes
  `statfs` report ~9.5% free, under the agent's 10% threshold, and the agent refuses
  builds (`DISK_PRESSURE`) as designed. `go clean -cache` frees ~3 GB and lifts it
  above.
- **Image builds in the cloud container:** `docker build` cannot download Go modules
  here. Build from a scratch copy: `git ls-files` copied with `gen/`, then
  `go mod vendor`, then `docker build` there (the Dockerfile uses `vendor/`).
- **Dev stack:** `deploy/dev/postgres-init/` creates `bohux_app` only on a fresh
  `postgres` volume. An existing volume needs `docker compose down -v` first.
- **Go 1.23 pin holds dependencies back** (goose ≤ v3.26, pgx ≤ v5.7.6; newer need Go
  1.24–1.26). Raising the `go` directive is cheap and its own commit when wanted.

## Session log (newest first)

### 2026-10-21 (auto run, S9e–S9f) — branch `claude/relaxed-rubin-76m7dl`
- S9e done: `scripts/selfdeploy-test.sh` and the CI `selfdeploy` job; passed here. The
  test found a Docker behaviour that cost the control plane its infra network after a
  failed first start; the provider now re-creates on a restart plan (regression test).
- S9f done: ADR-0033 proposed (O19), nightly backup schedule, read-only backup role,
  installer option, runbooks for backup/restore, control-plane upgrade, OpenBao unseal
  key, and infra upgrades. S7d closed (done in S9d). **S9 and v0.1 are done** except O2.
- S10 split into S10a–S10f. S10a done: ADR-0034 proposed (O20), proto for routes.
- S10b done: routes in the store and API, hosts unique across tenants, routes follow
  the running workload and survive rollbacks.
- S10c done: provider endpoints, Docker on `bohux-ingress`, conformance case.
- S10d done: the agent programs Caddy; health includes the route; real-Caddy test.
- S10e done: Caddy in the install bundle; the self-deploy test serves an App on a
  route. ADR-0035 (zero-downtime) proposed (O21).
- S10f done: zero-downtime deploys by slots; 127 polled requests, none failed. S10 done.
- S11 started: ADR-0036 proposed (O22), resource commands with tests.
- **Next:** finish S11.

### 2026-10-21 (auto run 2, S6a) — branch `claude/relaxed-rubin-76m7dl`
- Owner asked again for an automatic run, slice after slice, until the session ends.
- S6a done: Temporal dev server via the CLI from GitHub releases; `new-workflow` skill;
  SDK v1.41.1; ADR-0026 proposed (O12); S6 split into S6b–S6d; the deploy workflow and
  its compensation test (mocked activities) green; lint clean.
- **Next:** S6b.
- S6b done: migration `00016`, real activities, `agentapi.Server.AssignApp`, fixtures,
  integration tests. Wrapped non-retryable errors were silently retryable; fixed.
- **Next:** S6c.
- S6c split into S6c1/S6c2. S6c1 done: Temporal client and worker in the control plane,
  ReleaseService for images, image taken from the Release's `source_ref`.
- **Next:** S6c2.
- S6c2 done: DeploymentService on real Temporal, CI starts a Temporal dev server.
  ScaleApp's assignment split out as S6e (needs a workflow, not an API side effect).
- **Next:** S6d.
- S6d done: end-to-end deploy and rollback on Docker + Temporal, CI job `e2e`,
  recreate downtime ~0.35 s.
- **Next:** S6e.
- S6e done: ADR-0027 proposed (O13), ScaleApp assigns atomically, replicas follow the
  App. **S6 done.**
- **Next:** S7, split first.
- S7 split into S7a–S7d. S7a done: OpenBao client and resolver, SecretService, control
  plane flags; CI starts OpenBao.
- **Next:** S7b.
- S7b done: rotated secrets reach the nodes that use them.
- **Next:** S7c.
- S7c split into S7c1–S7c3. S7c1 done: ADR-0028 proposed (O14), build messages in the
  agent protocol, the agent's buildx builder, buildx required at registration.
- **Next:** S7c2.
- S7c2 done: build dispatch over the stream, build workflow, CreateRelease from Git,
  deploys wait for builds.
- **Next:** S7c3.
- S7c3 done: Git → agent build → deploy → rollback end to end.
- **Next:** S8, split first.
- S8 split into S8a–S8c. S8a done: `bohux deploy | rollback | scale`, driven end to end.
- **Next:** S8b.
- S8b done: logs and exec through WorkloadService and the agent stream, CLI commands,
  exec audited.
- **Next:** S8c.
- S8c done: ADR-0029 proposed (O15), `bohux admin backup | restore`, the restore drill
  end to end; control-plane shutdown no longer waits on streams. **S8 done.**
- **Next:** S9, split first.
- S9 split into S9a–S9f. S9a done: ADR-0030 (O16) and ADR-0031 (O17) proposed.
- **Next:** S9b.
- S9b done: agent-side fallback (proto, reconciler, AssignApp, workflow), tested first.
- **Next:** S9c.
- S9c split into S9c1/S9c2. S9c1 done: ADR-0032 proposed (O18), provider options from
  the API to the Docker provider, owner-only. Fixed a 1-in-256 flaky test.
- **Next:** S9c2.
- S9c2 done: the control-plane image, smoke-run, CI builds it.
- **Next:** S9d.
- S9d done (and S7d): the install bundle, run end to end here: install, self-deploy,
  self-rollback, and agent fallback from a control plane that cannot start.
- **Next:** S9e.

### 2026-10-21 (S5e) — branch `claude/relaxed-rubin-76m7dl`
- S5e done, so S5 is done: agent wired to Docker through the Registry, slugs filled by
  `Assign`, image retention and disk pressure (real-daemon test), `Assign` lock race
  fixed. Build, vet, golangci-lint (both tags), unit, integration, Docker conformance
  all pass locally.
- New test helper package `internal/store/fixture` (creates project/env/App rows).
- **Next:** S6a.

### 2026-10-21 (auto run) — branch `claude/relaxed-rubin-76m7dl`
- Owner asked for an automatic run, slice after slice, until the session ends. Owner
  rule for gates: write the ADR as *proposed*, build on it, list it under *Waiting on
  the owner* (O6 onward).
- S2a done: ADR-0021 (goose on pgx).
- S3e split into S3e1–S3e3, all done: Project, Environment, App services. **S3 done.**
- Split S4 into S4a–S4e. S4a done: ADR-0024 proposed (O9).
- S4c split; S4c1 done: control-plane `Connect` with skew policy and online state.
  S4c2 done: the agent process (`bohux agent register|run`).
- S4d split; S4d1 done: assignments written, sealed, signed, pushed, acked. S4d2
  done: the agent verifies, caches, and acks them.
- S4e split; S4e1 done: provider interface (O10) and the offline reconcile loop. S4e2
  done: status reports and silent-stream detection. **S4 done.**
- S5c/S5d done: Docker provider, all 13 conformance cases green on a real daemon.
- S5b done: conformance suite, memory provider, runner.
- S5 split into S5a–S5e. S5a done: ADR-0025 proposed (O11), Docker runs locally,
  provider interface moved to `internal/provider`.
- S4b split into S4b1/S4b2, both done: `internal/pki`, NodeService, data dir, agent
  TLS listener, Register.
- S3d done: `internal/secrets` redaction, access-log interceptor, rule 3 test.
- S3c3 done: bootstrap command, Identity/Tenant handlers, idempotency records, all
  tested over real ConnectRPC; end-to-end binary smoke test passed.
- S3c2 done: ADR-0023 (O8), proto + migration for tenant-wide tokens, `internal/api`
  auth interceptor and permission table, tests green.
- S3c split into S3c1–S3c3. S3c1 done: idempotency table, `internal/auth` tokens and
  API-token authentication, integration-tested (expired, revoked, wrong secret,
  tenant swapped, wrong kind, unknown ID all `ErrUnauthenticated`).
- S3b done: `internal/controlplane` (h2c server, `/healthz`, clean shutdown),
  `bohux controlplane`, `bohux admin migrate`, `internal/store/storetest` helper,
  config reference. CI integration job now covers `./internal/... ./cmd/...`.
- S3a done: ADR-0022 proposed (O7).
- S2c done: store write path (Open, InTenant, Create/Update/Delete with head CAS and
  audit event), integration tests green locally.
- Split S3 into S3a–S3e.
- S2b done: 11 migrations (all v0.1 resources), `store.Migrate`, unit + integration
  tests passing on local Postgres 16.15; build, vet, golangci-lint (with and without the
  integration tag) clean. Added CI integration job and dev `bohux_app` role.

### 2026-10-21 (S1c) — branch `claude/relaxed-rubin-76m7dl`
- S1c done, so S1 is done: node, secret, audit protos and
  `proto/bohux/agent/v1/agent.proto`. All 13 files from the spec exist. buf lint (no
  ignores), buf build, buf format, generate, go build and vet pass locally.
- `buf breaking` against `main` fails by design (main holds only the unreleased draft),
  so the CI step now skips until the base branch has `common.proto`.
- Split S2 into S2a–S2c.
- **Next:** S2a, migration tool ADR.

### 2026-10-21 (S1b) — branch `claude/relaxed-rubin-76m7dl`
- S1b done: app, release, deployment, workload protos; `SecretRef` added to common.
  buf lint (no ignores), buf build, buf format, generate, go build and vet pass locally
  (scratch workaround).
- Deleted the draft `service.proto` now, not in S1d: same symbols, same package. S1d's
  remaining check folded into S1c.
- Asked the owner O5 (App non-secret env vars).
- **Next:** S1c, starting with `node.proto`.

### 2026-10-21 (S1a) — branch `claude/relaxed-rubin-76m7dl`
- S1a done: common, identity, tenancy, project, environment protos. buf lint, buf build,
  generate, go build, vet, golangci-lint all pass locally (with the scratch workaround).
- Added `google.golang.org/genproto/googleapis/api` (pinned for Go 1.23) for the
  generated annotations import.
- **Next:** S1b, starting with `app.proto`.

### 2026-10-21 (planning) — branch `claude/relaxed-rubin-76m7dl`
- Owner set session rules: one task per session, split when large, always hand over the
  next prompt. S1 split into S1a–S1d.

### 2026-10-21 (later) — branch `claude/relaxed-rubin-76m7dl`
- Owner accepted ADR-0019: Caddy is the only ingress proxy through v1 (Traefik and nginx rejected).
- **Next:** unchanged — S1, starting with `common.proto`.

### 2026-10-21 — branch `claude/relaxed-rubin-76m7dl`
- Proposed ADR-0019 (Caddy only) and ADR-0020 (v0.1 operational baseline); updated
  architecture, roadmap, and vision.
- Completed S0 Foundations (see above). Lint, test, and build pass locally.
- **Next:** S1, starting with `common.proto`.
