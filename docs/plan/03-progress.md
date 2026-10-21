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
- **Slice:** S4d2 — agent assignment cache (⏳, next to start)
- **Exact next step:** in `internal/agent`, verify incoming assignments (signature,
  node id, generation), cache them atomically on disk, `Ack`, and send
  `Hello.applied_generation` from the cache.

## Next session prompt

> Read CLAUDE.md and docs/plan/03-progress.md, then do slice S4d2 (agent assignment
> cache) on branch `claude/relaxed-rubin-76m7dl`, following ADR-0003, ADR-0008,
> ADR-0024. In internal/agent: on Assignment, verify the signature with the stored
> signing key (pki.VerifyAssignment) and key id, that the payload names this node, and
> that its generation is higher than the cached one; otherwise ignore it with a
> warning. Cache the signed Assignment atomically in the state dir (0600; secrets stay
> sealed), re-verify it on load, Ack the generation, and send Hello.applied_generation
> from the cache. Add a function that opens a workload's sealed secrets with the
> sealing key, for the provider in S5, never logging values. Test against the real
> control-plane listener on the local Postgres: assignment cached and acked, tampered
> signature and wrong node rejected, older generation ignored, cache survives an agent
> restart (applied_generation sent, no re-send), cache file holds no clear secret, and
> sealed secrets open. Verify with build, vet, golangci-lint (also with the integration
> tag) and tests. Tick boxes, update the tracker and the next session prompt, commit,
> push, and give me the next prompt.

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

### S2 — Storage ⏳
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

### S4 — Agent ⏳
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

#### S4d2 — agent assignment cache ⏳
- [ ] Verify (signature, key id, node, generation), cache atomically, `Ack`,
      `Hello.applied_generation` from cache; open sealed secrets for the provider

#### S4e — offline reconcile ⏳
- [ ] Reconcile loop against the cached assignment with a fake provider; test with
      the control plane killed (rule 1)

### S5 — Docker provider ⏳
- [ ] Conformance suite in `providers/_conformance`
- [ ] `Describe` + all seven verbs; Compose artifact + `secrets.env` on disk (rule 2)
- [ ] Image retention (current + last 5), log rotation, disk-pressure fail-closed (ADR-0020)

### S6 — Deploy workflow ⏳
- [ ] `.claude/skills/new-workflow/` (referenced by ADR-0002)
- [ ] **Rollback/compensation test first** (rule 5)
- [ ] Temporal workflow: build → (push no-op single-node) → assign → wait healthy →
      finalize / compensate

### S7 — Build and secrets ⏳
- [ ] BuildKit Dockerfile builds, exported to the local image store, resource-limited
- [ ] OpenBao in the install bundle, auto-unseal (ADR-0020)
- [ ] Secret handles resolved only inside activities, never in workflow history

### S8 — CLI ⏳
- [ ] `bohux deploy | logs | rollback | exec | scale`; host-port downtime warning
- [ ] `bohux admin backup | restore`, restore tested in CI (ADR-0020)

### S9 — Exit: Bohux deploys Bohux ⏳
- [ ] Bohux deploys itself and rolls itself back, end to end
- [ ] Footprint re-measured and published
- [ ] Runbooks: backup key handling, OpenBao unseal key (`write-runbook`)

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
- The cloud container has no running Docker daemon, so `make run-dev` cannot run there.
- **Local Postgres in the cloud container:** Postgres 16 binaries are in
  `/usr/lib/postgresql/16/bin`. The scratchpad is not readable by the `postgres` user,
  so use its home:
  ```
  D=/var/lib/postgresql/bohux-test
  su postgres -c "mkdir -p $D && /usr/lib/postgresql/16/bin/initdb -D $D/data -U postgres --auth=trust && /usr/lib/postgresql/16/bin/pg_ctl -D $D/data -o '-p 55432 -k $D -c listen_addresses=127.0.0.1' -l $D/log start"
  export BOHUX_TEST_POSTGRES_URL='postgres://postgres@127.0.0.1:55432/postgres?sslmode=disable'
  go test -tags=integration -race -count=1 ./internal/store/...
  ```
  (skip `initdb` if `$D/data` exists). Temporal/OpenBao still need CI or a local machine.
- **Dev stack:** `deploy/dev/postgres-init/` creates `bohux_app` only on a fresh
  `postgres` volume. An existing volume needs `docker compose down -v` first.
- **Go 1.23 pin holds dependencies back** (goose ≤ v3.26, pgx ≤ v5.7.6; newer need Go
  1.24–1.26). Raising the `go` directive is cheap and its own commit when wanted.

## Session log (newest first)

### 2026-10-21 (auto run) — branch `claude/relaxed-rubin-76m7dl`
- Owner asked for an automatic run, slice after slice, until the session ends. Owner
  rule for gates: write the ADR as *proposed*, build on it, list it under *Waiting on
  the owner* (O6 onward).
- S2a done: ADR-0021 (goose on pgx).
- S3e split into S3e1–S3e3, all done: Project, Environment, App services. **S3 done.**
- Split S4 into S4a–S4e. S4a done: ADR-0024 proposed (O9).
- S4c split; S4c1 done: control-plane `Connect` with skew policy and online state.
  S4c2 done: the agent process (`bohux agent register|run`).
- S4d split; S4d1 done: assignments written, sealed, signed, pushed, acked.
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
