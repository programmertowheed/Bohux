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

- **Working branch:** `claude/loving-shannon-rplqcg`. The session of 2026-10-23 was
  assigned this branch rather than `claude/relaxed-rubin-76m7dl`, which is merged into
  `main` (PR #1) and no longer carries unmerged work. New work starts from `main`.
- **Slice:** S28 — hardening **done**: the Swarm end-to-end run, the agent bug it found
  and its fix, the conformance gap that let the bug through, the `e2e-swarm` CI job, and
  the ADR audit. S27 (scheduled restore drills, ADR-0051) is done before it.
- **CI has now run for the first time (O24 answered) and it is red:** five of ten jobs
  fail on PR #2. O45 has the diagnosis. Until that is fixed, "verified" in this tracker
  means verified in the cloud container, not by CI.
- **v0.1 is code-complete**: the exit criterion runs in CI (`selfdeploy`). v0.2 is done
  except the items O35 lists. Open for the owner: O2 (footprint on a 2 GB VPS) and the
  proposed ADRs (O19–O44).
- **Exact next step:** **get CI green (O45)** before any further feature work — nothing
  in this repo has ever been proven outside one cloud container, and the first run says
  the pipeline does not pass. It needs the owner for one thing only: `buf.lock`, from a
  machine with Buf Schema Registry access. After that, every remaining roadmap item
  needs an owner decision. Ask the owner to pick: v0.2's O35 (managed Postgres/Redis, envelope encryption, team invites)
  or a v0.3 item (the SLO gate's optional PromQL source with OTEL correlation and
  deploy markers, preview environments, migrations as a gated step, cron jobs, Compose
  apps, volumes and S3 backups), or hardening: a Swarm end-to-end test, and checking
  the CI matrix that ran on `main` with PR #1 (O24). Review the proposed ADRs
  (O19–O44); **O44 (ADR-0051) is the one with a security trade-off to weigh**, since a
  drill-enabled install holds an age private key that decrypts its own archives.

## Next session prompt

> Read CLAUDE.md and docs/plan/03-progress.md. Start the services with the scratchpad
> `services.sh` pattern in *Known gaps*, and regenerate `gen/` through the
> scratch-workspace workaround — it is not committed and buf.build is blocked here.
> Then work **O45: get PR #2's CI green**, on branch `claude/loving-shannon-rplqcg`.
> Read the failing jobs' logs from the runner rather than guessing (`proto`,
> `integration`, `e2e`, `conformance-swarm`, `selfdeploy`), fix what the repo can fix,
> and for anything needing Buf Schema Registry access tell me exactly what to run and
> where — do not work around a missing `buf.lock` by weakening `buf breaking`, which is
> rule 9's guard. Expect the fixes to be in `.github/workflows/ci.yml` and the
> generation steps, not in the proto contract. Verify each fix by pushing and reading
> the run, not only locally. Tick boxes, update the tracker and the next session prompt,
> commit, push, and give me the next prompt. If you hit a decision only I can make, ask
> me before building.

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
| O23 | Accept or reject ADR-0037 (Git push webhooks: Webhook resource, HMAC in OpenBao, deploys as the webhook). Adds `webhook.proto` | S12 |
| O24 | Open a PR for this branch (or allow CI on it) so the CI matrix runs at least once; nothing has run on GitHub yet | Confidence in every slice since S6 |
| O25 | Accept or reject ADR-0038 (deploy notifications: project channels, URL in OpenBao, sent by activities after the terminal state; no email yet). Adds `notification.proto` | S13 |
| O26 | Accept or reject ADR-0039 (web UI sign-in with an API token in `sessionStorage`, strict CSP, no cookie sessions until user accounts exist) | S15 |
| O27 | Logs in the UI: keep the plain `<pre>` (no ANSI colors) or render them with xterm.js. Since ADR-0046 xterm.js works under the unchanged CSP, so this is now only a product choice | S15 log view |
| O28 | Accept or reject ADR-0040 (resource view: agents sample node and workloads every 30 s, carried in status reports, no history) | S16 |
| O29 | ~~Web terminal transport~~ — **answered 2026-10-22: a WebSocket bridge.** Built in S22 (ADR-0046, see O38) | — |
| O30 | Accept or reject ADR-0041 (registry credentials and Git deploy keys are environment secrets the App names; sealed apart from its env vars; on the node only for the one command). Adds `App.image_pull_secret`, migration 00021 | S17 |
| O31 | Git host keys for deploy-key fetches: trusted on first use per node and pinned in `<state>/builds/known_hosts` (now). Alternative: a known_hosts secret per environment, refusing unknown hosts | S17 |
| O32 | ~~Buildpacks or Nixpacks~~ — **answered 2026-10-22: Nixpacks, then Railpack** once Nixpacks turned out to be in maintenance mode upstream (Railpack is its named successor); auto when there is no Dockerfile, plus a per-App override. Built in S23 (ADR-0047, see O39) | — |
| O33 | Accept or reject ADR-0042 (refines ADR-0031: a release healthy while the control plane *rejects* its agent does not cancel its fallback; an unreachable control plane changes nothing, rule 1; found when a new control plane refused its own agent) | S15 check |
| O34 | Accept or reject ADR-0043 (email notifications: `EMAIL` channels with a `mailto:` list in OpenBao; one SMTP relay per install, TLS required). Adds a proto enum value, migration 00022, four control-plane settings | S19 |
| O35 | Remaining v0.2 items need a product call before code: **managed Postgres/Redis** (needs app-to-app networking in an environment, which does not exist: each App is its own Compose network; plus image pinning, volume lifecycle, backups); **envelope encryption** (what threat beyond OpenBao's own barrier: crypto-shredding environments? its backups then need the keys); **team invites** (user accounts); **Buildpacks/Nixpacks** (O32); **web terminal** (O29) | next v0.2 items |
| O36 | Accept or reject ADR-0044 (webhook watch paths: path prefixes, no globs; changed files from the push payload; deploys anyway when the list may be incomplete). Adds `Webhook.paths`, migration 00023 | S20 |
| O37 | ~~Swarm questions~~ — **answered 2026-10-22: a single-manager swarm, no registry push, and live-restore waived on a swarm manager** (the agent's own daemon, so Caddy, Compose and Swarm share an attachable `bohux-ingress` overlay). Built in S21 (ADR-0045 rewritten as decided, see O40) | — |
| O38 | Accept or reject ADR-0046 (web terminal: `GET /sockets/v1/exec`, token in the first frame, binary protobuf `ExecRequest`/`ExecResponse` frames, close code 4000 + Connect code; the Connect handler's body shared, so the same permission check and audit; xterm.js with its `<style>` elements copied into adopted stylesheets so the CSP is unchanged; adds `github.com/coder/websocket` and two npm packages) | S22 |
| O39 | Accept or reject ADR-0047 (builds without a Dockerfile: `GitSource.strategy` UNSPECIFIED now means auto — the Dockerfile if present, else Railpack; new `BUILD_STRATEGY_RAILPACK`; one frontend image pinned by digest runs `railpack prepare` in a locked-down container and is the BuildKit frontend; the App ID is Railpack's cache key; `BUILD_PLAN_FAILED`). Changes app.proto and agent.proto | S23 |
| O40 | Accept or reject ADR-0045 as rewritten (Swarm provider: the node's own dockerd as a one-node swarm manager; one stack per workload with `stack.yaml` + 0600 `secrets.env`; health checks inside each task's network namespace; `dnsrr` endpoints on an attachable `bohux-ingress` overlay; live-restore waived on a manager; the agent runs Swarm beside Docker and records each workload's provider) | S21 |
| O41 | Accept or reject ADR-0048 (a Deployment's notifications run in their own `notify/<deployment id>` workflow, a child abandoned when the deploy run ends, so the App is free at once; runs from before keep notifying inline behind a version gate; recorded histories replayed in a unit test). Amends ADR-0038 item 3 | S24 |
| O42 | Accept or reject ADR-0049 (canary deploys: strategy CANARY with one weight 1–99% and a bake 1m–24h per deployment; the weight rides on the workload outside the spec hash; Caddy weighted round robin across the two slots; the workflow's CANARY state watches health with a 30 s grace, then promotes by reassigning with weight 0; PromoteDeployment ends the bake, CancelDeployment aborts). Changes deployment.proto and agent.proto; migration 00024 | S25 |
| O43 | Accept or reject ADR-0050 (SLO gate: Caddy writes an access log beside its admin socket with only upstream, status and duration; the agent tails it and reports per-slot counters in memory; `App.slo` = availability 0.9–0.99999, optional latency threshold (a histogram bound) and target, max burn rate (default 10), watch 1m–1h (default 10m); burn rate over the window since the hold began, no verdict under 50 requests; judged during a canary's bake and, otherwise, in a new `VERIFYING` state; violations roll back with `SLO_AVAILABILITY`/`SLO_LATENCY`). Item 6 amended while proposed: the CLI, not a deploy log, says an unrouted App is judged by health alone. Changes app.proto, agent.proto, deployment.proto; migration 00025 | S26 |
| O44 | Accept or reject ADR-0051 (scheduled restore drills: a weekly Temporal schedule on the backup's queue; opt-in through a drill age identity on the host whose public half becomes a second recipient on every archive; throwaway state per run — a scratch database created by a `CREATEDB` `bohux_drill` role, a `BYPASSRLS` `bohux_drill_admin` role with `GRANT SET ON PARAMETER session_replication_role` to load the rows, a temp data dir, and a KV mount of its own that the drill wipes; checks beyond restore's own; the verdict logged and written as JSON under the archives' retention; `bohux admin drill` to run one by hand). **The host holding an age private key is the trade-off to weigh** — item 2 keeps the operator's own key off the host, and drills stay off unless an identity is configured. Item 7 (the config keys) was added while proposed, and item 7 again in S27b to accept the identity as a value as well as a file (the control plane as an App gets its config as env vars). S27a and S27b are built on it | S27a, S27b |
| O45 | **CI is red, and this is the first time it has ever run on this work (O24 answered).** PR #2's run: `go`, `web`, `compose`, `image`, `conformance-docker` pass; `proto`, `integration`, `e2e`, `conformance-swarm`, `selfdeploy` fail. Diagnosed: `proto` fails because `buf breaking --against <remote main>` cannot resolve `google/api/field_behavior.proto` — `main` has no committed `buf.lock`, the gap *Known gaps* has listed since S1. The fix needs Buf Schema Registry access, which the cloud container does not have (buf.build is blocked) but CI does. Either commit `buf.lock` from a machine with registry access, or let a CI step emit it once so it can be committed. The other four need their logs read on a runner; their Generate step shares the same `buf.lock` root cause, so fix that first and re-run before diagnosing further | Every claim that a slice is verified; nothing has been proven outside this container |
| O46 | **Which rule decides an ADR's status?** `.claude/skills/write-adr/` says "Status starts `proposed`, becomes `accepted` on merge", which would make ADRs 0020–0051 accepted already (they merged in PR #1). This tracker instead asks you to accept each one (O1, O6–O44). S28's audit confirms all 32 are referenced from code, so the two rules disagree only on paperwork — but they do disagree. Pick one: either the skill's merge rule (and the O-items close), or per-ADR acceptance (and the skill should say so). Statuses were deliberately left untouched | 32 ADRs reading `proposed` while the code depends on them |
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

### S11 — The CLI covers the API (ADR-0036) ✅
Rule 8: the UI gets nothing the CLI lacks, so the CLI comes first.

- [x] Proposed ADR-0036 (O22): `bohux <noun> <verb>`, `-o json` = the API's protojson,
      secret values only from stdin, `--idempotency-key`, no prompts
- [x] `project list|create`, `env list|create --node`, `app list|show|create`,
      `route list|add|remove`, `secret list|set [--app]`, `node list|token`; unit
      tests and an integration test against a real API server (OpenBao included)
- [x] The self-deploy test sets up its App with the CLI (checked here against a live
      install: project, env, app with a route, deploy, served over HTTPS). The installer
      keeps `curl`: it runs before any CLI config exists and sets provider options and
      secret refs, which `app create` does not take
- Not covered yet (no command): role bindings, API tokens, app update of source and
  runtime, environment and project update/delete, node revoke. Add with the UI work

### S12 — Git push webhooks (ADR-0037) ✅
- [x] Proposed ADR-0037 (O23): `Webhook` resource per App (branch, flavor github or
      generic), `POST /hooks/v1/<tenant>/<webhook>`, HMAC-SHA256 with a secret kept in
      OpenBao and shown once, the deploy runs as the webhook (new actor kind) through
      the API's own handlers, idempotent per commit, `409` while a deploy runs
- [x] Proto `webhook.proto` (WebhookService); migration 00019 (`webhooks` heads and
      revisions, actor kind `webhook`); RPC permissions; `SecretBackend.Get`
- [x] Integration test: refuses unsigned, wrongly signed, other-body, unknown and
      revoked deliveries with `404` and starts nothing; ping and other branches `204`;
      a push builds and deploys its commit once across redeliveries, audited as the
      webhook, from the App's repository (never the payload's); viewer cannot create;
      image Apps refused; the secret never reaches Postgres
- [x] CLI `webhook list|create|remove` (ADR-0036)
- [ ] With the owner: how a Git host reaches the API on a single VPS (a route to the
      control plane through the ingress, ADR-0037 consequences)

### S13 — Deploy notifications (ADR-0038) ✅
- [x] Proposed ADR-0038 (O25): `NotificationChannel` per project (Slack, Discord,
      signed webhook), owner-only, URL in OpenBao (https only, never returned), end
      states to post (default failed / rolled back / cancelled); email deferred
- [x] Proto `notification.proto`; migration 00020; permissions; CLI `channel
      list|create|remove` (URL from stdin)
- [x] Workflow: after the terminal state, `NotificationChannels` then one
      `NotifyChannel` per channel (5 attempts each), behind
      `workflow.GetVersion("notify-channels")`; a notification never changes the
      outcome. Unit test (rolled back first; a failing channel retried 5 times and
      dropped); integration tests for the API (owner-only, the URL never reaches
      Postgres or a response) and the activities (real TLS posts, the webhook's
      signature, other projects and states skipped)

### S14 — MCP server (ADR-0018, accepted) ✅
- [x] `proto/bohux/v1/options.proto`: method option `ToolPolicy` (read-only, mutating,
      destructive, hidden), set on every RPC; credentials and streams hidden
- [x] `internal/mcp`: tools read from the proto registry at run time (no hand list),
      JSON Schema from the request message (output-only fields left out, required
      kept, idempotency key filled in), dynamic Connect calls with the user's token,
      API errors as tool errors; `bohux mcp` over stdio. Dependency
      `github.com/modelcontextprotocol/go-sdk` v1.2.0 (the newest on Go 1.23; ADR-0018
      took the dependency)
- [x] Tests: every RPC has a policy and no tool carries a credential; schemas; a real
      MCP client against a real API (list, create without a key, invalid input, bad
      token); stdio smoke test against the live install
- [x] Docs: `docs/reference/config.md` (`bohux mcp`, scoped tokens; the resource commands)

### S15 — Web UI, read-only (ADR-0012, ADR-0013, ADR-0039) ✅ (web terminal: S22)
- [x] Proposed ADR-0039 (O26): sign in with an API token kept in `sessionStorage`
      only, no new auth path on the API, strict CSP; the first UI is read-only
- [x] `web/`: Vite + React 18 + TypeScript, Tailwind (tokens as CSS variables, light
      and dark), TanStack Query through Connect-Query and TanStack Router; client
      generated from `proto/` with `protoc-gen-es` (`buf.gen.web.yaml`, gitignored
      output). Pages: sign-in, projects, environments, an environment's Apps and recent
      deployments (refreshing), nodes. Vitest unit tests
- [x] `internal/webui`: the bundle embedded (`go:embed`), served under `/ui/` with the
      CSP and security headers, SPA fallback, immutable caching for hashed assets, `/`
      redirects there; a binary built without it says so. `make web`; CI jobs `web`
      and `image` (which now embeds the UI)
- [x] Checked in Chromium (Playwright) against a real control plane: sign-in,
      navigation, reload, no console errors or CSP violations, nothing in
      `localStorage` or cookies
- [x] Deployment page following `WatchDeployment` live (server streaming over
      connect-web), reconnecting with backoff like the CLI; linked from the
      environment's deployments. Checked in Chromium against the live install while a
      real deploy ran (health gate → succeeded, no errors)
- [x] Log tail: follows `StreamLogs` like `bohux logs -f` (pause, clear, last 2000
      lines, stderr marked), checked live against the control plane App. **Deviation
      from ADR-0013:** a plain `<pre>` with ANSI stripped instead of xterm.js, which
      injects `<style>` elements the CSP refuses (ADR-0039); owner to decide (O27)
- [x] Deploy an image and roll back from an App's row, each behind a confirm step,
      through the CLI's RPCs, then following the new deployment. Checked in Chromium
      against the live install: a UI deploy and a UI rollback both succeeded and the
      route served each release
- [x] `AuditService.ListAuditEvents` (newest first; a project's viewer reads that
      project's events, the tenant's trail needs a tenant-wide token; filter by
      resource; pagination), `PrincipalKind` gains NODE, SYSTEM, WEBHOOK; CLI `bohux
      audit list [-p]`; UI project activity page
- [x] `ListRevisions` / `GetRevision`: every revision as the full resource it was
      (`Any` snapshot), deleted ones included, for ten resource types, each read by
      its current-state select joined on the chosen revision; authorized by the
      resource's project (tenant-wide token for resources outside a project). A test
      runs every type's historical query
- [x] Forms mirroring the CLI: new environment (node picker), new App (image, port,
      health, route), scale, add/remove routes. Checked in Chromium against the live
      install: an App created, deployed, scaled to 2, and given a second route in the
      UI served on both hosts over HTTPS
- [x] App page: releases (state, source, pruned images), the App's deployments, the
      environment's secrets (keys only; which the App uses) with a set-secret form
      (password field; the value is never shown back). Checked in Chromium
- [x] Webhooks on a Git App's page and notification channels on the project page:
      list, create (the secret shown once, as the CLI prints it once; a channel's URL
      sent once and never shown back), remove with a confirmation. Checked in
      Chromium against the live install
- [x] API tokens: the CLI lacked them, so first `bohux token list|create|revoke`
      (`-p` scopes, `--expires`, secret printed once), then a Tokens page (create with
      role, project, lifetime; secret shown once; revoke). The binary's own noun list
      had drifted from the CLI's (`token` was "unknown command" though the CLI tests
      passed); it now asks the CLI, with a test over every noun in the usage text.
      Checked on the live install in Chromium and with the CLI: a project-scoped viewer
      token sees only its project and is refused once revoked
- [x] The pull-secret and deploy-key fields clicked through in Chromium on the upgraded
      install (server refusals shown; the good value saved)
- [x] The web terminal needs bidirectional streaming, which Connect in a browser does
      not have: built in S22 over a WebSocket bridge the owner chose (O29, ADR-0046)

### S16 — Resource view (ADR-0040) ✅
- [x] Proposed ADR-0040 (O28): agents sample the node (`/proc`, `statfs`) and, through
      the optional `provider.UsageReader`, their workloads every 30 s off the reconcile
      loop; samples ride in `StatusReport` (stale after 2 min); `Node.usage` and
      `WorkloadService.GetAppUsage` read the stored report; no history (OTLP's job)
- [x] Proto `usage.proto`, agent.proto and node/workload fields; Docker `Usage` (one
      `docker stats`, limits from `docker inspect`, slots from the project name);
      `agent.Sampler`; reports sum a workload's slots
- [x] Tests: `/proc` parsing (fixture), report attachment, Docker against a real
      daemon, the API reading a stored report (viewer-scoped); CLI `node list` and
      `app show` columns; UI nodes page and App usage column
- [x] The self-deploy test checks the installed UI's CSP and the samples end to end.
      **Passed here** on an image with the UI embedded, together with everything else
      (self-deploy, rollback, fallback, backup, route, 128 polled requests without a
      failure, samples)

### S17 — Private images and Git credentials (ADR-0041) ✅
- [x] Proposed ADR-0041 (O30): a credential is an environment secret the App names by
      key; it is sealed to the node in its own `SecretValues` field, never among the
      environment variables; it exists on the node only for the command that uses it
- [x] Registry: proto `App.image_pull_secret`, `Workload.image_pull_secret`,
      `SecretValues.image_pull_config`; migration 00021; the API checks the key is a
      secret of the environment (`UNKNOWN_SECRET`); the deploy resolves its handle
      (a deleted secret fails the deploy with a clear reason); assignment seals it and
      a rotation re-sends it; the agent hands it to the provider (`Spec.PullConfig`,
      opened only on use, not in the spec hash); Docker runs `compose up` with a
      throwaway `DOCKER_CONFIG`
- [x] Tests: `pullEnv` (modes, cleanup, plugin link), agent reconcile (the provider
      gets it, not as an env var; rotation does not restart), assignment sealing and
      rotation, CLI end to end (`--image-pull-secret`, `app pull-secret`, validation),
      and a **real registry with htpasswd auth**: the pull fails without the
      credential, succeeds with it, no credential on disk afterwards
- [x] CLI `app create --image-pull-secret`, `app pull-secret --secret|--clear`,
      `app show` column; UI fields in New app and app settings; docs in
      `docs/providers/docker.md`
- [x] Git: proto `GitSource.ssh_key_secret`, `BuildRequest.ssh_key` (handle) and
      `sealed_secrets`, `SecretValues.ssh_key`; the API checks the key and that the URL
      is SSH; the build activity resolves the handle (a deleted secret fails the build
      with a clear reason); the relay seals it to the node (callers cannot pass sealed
      bytes); the agent fetches with a throwaway key file and forwards it to
      `buildx --ssh`; CLI `--ssh-key-secret`, `app deploy-key`; UI field for Git apps
      (type-checked and built, not clicked through in a browser); docs
- [x] Tests: API validation (unknown key, https URL, bad name, sibling environment),
      the build activity (handle only, deleted key), relay sealing, and the agent
      against a real buildx with a stand-in `ssh` that accepts only the right key file
      (the sandbox has no ssh client): fetch, `RUN --mount=type=ssh`, key removed,
      wrong key → `SOURCE_FETCH_FAILED`, unopenable key → `BUILD_REQUEST_INVALID`

### S18 — Interactive exec (spec'd, never implemented) ✅
- [x] Docker provider: TTY sessions on an agent-side pseudo-terminal (`x/sys/unix`, no
      new dependency), sized at start and resized; stdin forwarding already worked
- [x] CLI `bohux exec -i` (forward stdin) and `-t` (raw local terminal, size and
      SIGWINCH), Linux and macOS; `-t` refuses a non-terminal stdin
- [x] Conformance case (rule 4): stdin to `cat`, and a TTY `sh` sized 24x80, resized to
      40x100, exit 3; the in-memory reference provider gained both; Docker passes
- [x] Tests: CLI against a fake API (stdin, no stdin, `-t` refusal), agent relay
      (stdin, resize, close), e2e `exec -i` (written; the build e2e cannot run in this
      sandbox: the base system leaves under 10% free, so the agent refuses builds)
- [x] Found on the live install: **logs and exec never reached Apps with routes** since
      S10f, because the agent looked up the workload without its blue/green slot. Fixed,
      with a test. Then a real `bohux exec -t -- sh` on the install: prompt, `/dev/pts/0`,
      the local 30x90 size, exit status 5 passed back
- [x] The binary did not build for macOS (S16 `statfs` types); fixed, and CI now
      cross-builds for macOS

### S19 — Email notifications (ADR-0043) ✅
- [x] Proposed ADR-0043 (O34): `EMAIL` channel kind, recipients as a `mailto:` URL kept
      in OpenBao like any channel URL; one relay per install (`--smtp-*`), TLS always
      (465 implicit, otherwise STARTTLS or nothing is sent), password from a file or the
      control plane App's secret
- [x] Proto enum value, migration 00022 (kind check), API (refused without a relay,
      `mailto:` parsed: no headers, no display names, at most ten; the list shows
      domains), deploy activity (non-retryable without a relay), control-plane flags,
      installer variables, CLI `--kind email` (plain addresses accepted), UI kind
- [x] Tests: a fake SMTP relay (`internal/notify/smtptest`): STARTTLS before anything,
      AUTH only after TLS, implicit TLS, a relay without STARTTLS gets no credentials, an
      untrusted certificate fails before auth; recipients parsing; API; the activity
      end to end; flags; config

### S22 — Web terminal (ADR-0046) ✅
- [x] Owner chose the transport (O29): a WebSocket bridge. Proposed ADR-0046 (O38):
      `GET /sockets/v1/exec`; the first frame is the API token (no header, URL, or
      subprotocol, which reach logs); then binary protobuf `ExecRequest` /
      `ExecResponse`; closes with 1000 after the exit or 4000 + the Connect code and its
      message; pings every 30 s; 64 KiB frames; cross-origin upgrades refused
- [x] One implementation: the Connect `Exec` body takes any `Receive`/`Send` stream;
      the bridge authenticates through the auth interceptor's own check, so the role,
      project scope, and audit event are the CLI's. No proto change (comment only)
- [x] UI: a Terminal page per App (`terminal` next to `logs`), xterm.js loaded only
      there (a 339 KB chunk); command field, connect/disconnect, the exit status or the
      refusal shown. xterm.js's `<style>` elements are copied into adopted stylesheets,
      so the CSP is unchanged
- [x] Tests: the bridge's refusals (no token, unknown token, viewer, bad frames, missing
      command, offline node) with close codes, the audit event, and cross-origin
      (integration); a whole TTY session over the socket against a real agent with
      exit status 4 (e2e); frame encoding and close messages (vitest)
- [x] Checked in Chromium against the test install upgraded to this branch: a shell
      in `demo/prod/hello`, red renders red under the real CSP, `stty size` matches the
      browser's terminal, `exit 5` shows status 5, a viewer token gets "permission
      denied". Found and fixed there: coder/websocket v1.8.12 sent a second close frame
      and Chromium lost the exit status (now v1.8.14)
- [x] The build e2e test ran in the cloud container for the first time (disk freed,
      below) and caught a wrong audit count in last session's exec test (4 sessions, not 2)

### S23 — Builds without a Dockerfile: Railpack (ADR-0047) ✅
- [x] Owner chose Nixpacks (O32); upstream marks it maintenance-only and names
      Railpack, so the owner was asked again: Railpack, auto when there is no
      Dockerfile, plus an override. Proposed ADR-0047 (O39)
- [x] Proto: `BUILD_STRATEGY_RAILPACK`; UNSPECIFIED documented as auto;
      `BuildRequest.strategy`, `BuildResult.strategy` (the builder used)
- [x] API validates the strategy; the deploy workflow passes it to the agent
- [x] Agent: decides after the fetch (a regular `Dockerfile` at the context root);
      `railpack prepare` in a throwaway container from
      `ghcr.io/railwayapp/railpack-frontend:v0.43.0@sha256:355ef61f…` (checkout
      read-only, `--cap-drop ALL`, `no-new-privileges`, 512 MB, 256 processes, the
      agent's UID, proxy variables passed); the build on the `bohux` builder with that
      image as `BUILDKIT_SYNTAX`, `-f` the plan, `cache-key=<app id>`; exit 1 is
      `BUILD_PLAN_FAILED`, Railpack's error lines go to the agent log
- [x] CLI `app create --build auto|dockerfile|railpack`, a BUILD column in
      `app show`; the UI's App page says how a Git App builds (the UI creates image
      Apps only, so no control there: rule 8)
- [x] Tests: strategy choice, info-file errors, proxy variables (unit); validation
      (unit); the workflow forwards the strategy; the CLI flag and column; the agent
      plans for real (unplannable repo → `BUILD_PLAN_FAILED`, `dockerfile` without one
      → `BUILD_FAILED`, auto with a Dockerfile → DOCKERFILE) and, where ghcr.io is
      reachable (CI), builds and runs a Railpack image
- [x] In the cloud container: ghcr.io blobs are blocked, so the integration test ran
      with a local stand-in image (the released `railpack` binary, checksum verified,
      on busybox with the sandbox CA), host networking, and the full build skipped
      (`BOHUX_TEST_RAILPACK_*`, Known gaps). A full build got through Railpack's
      frontend and stopped at pulling `ghcr.io/railwayapp/railpack-builder`. **The
      full Railpack build has not run anywhere yet**; the first CI run will be its
      first

### S24 — Notifications no longer hold the App (ADR-0048) ✅
- [x] Owner chose this slice (2026-10-22). Proposed ADR-0048 (O41), amending ADR-0038
      item 3
- [x] Recorded first: histories of a succeeded and a rolled-back run with
      notifications, made by the previous code on the Temporal dev server
      (`TestRecordHistory`, kept for the next change), in `testdata/`
- [x] `NotifyWorkflow` (`notify/<deployment id>`), started as a child with
      `ParentClosePolicy: ABANDON` and duplicate starts refused; the deploy run waits
      only for the child's start. Delivery unchanged. Gated with
      `GetVersion("notify-child")`: older runs notify inline
- [x] Tests: `TestReplay` replays the recorded histories (and fails when the gate is
      removed: checked); `TestNotifyEnd` still sees every channel and five attempts
      on the broken one; on the real dev server a deploy whose channel always fails
      ends and the next deploy of the App starts at once while the notification run
      still retries (`TestNotificationsDoNotHoldTheApp`, which fails on the old code:
      checked)
- [x] Found on the way: the agent's hello `Send` returned a bare `EOF` when the
      control plane had already answered, losing its reason; the agent and the
      agentapi test helper now read it from `Receive` (TestConnect's flake: 30/30
      under load after)

## Hardening slices

### S28 — Swarm end-to-end, the gate's missing case, and the ADR audit ✅
No new features. The owner picked hardening over a v0.3 feature, and it paid for itself:
the first end-to-end run against Swarm found a bug that defeated rule 5 on any provider
that updates in place.

- [x] **A bug in the agent, found by the Swarm e2e and fixed.** `reconcile.go` re-read
      Status after Apply and mapped running-and-healthy to HEALTHY without checking the
      status described the spec it had just applied. A provider that updates in place
      keeps the previous release running and healthy under its own hash while the new
      one rolls out, so the agent reported the **new** release as healthy on the **old**
      release's health. The deploy's health gate matches on release ID and generation —
      both already correct — so it accepted that: a broken Release reported SUCCEEDED in
      two seconds and never rolled back. Docker recreates its container, so the stale
      status is never both running and healthy there and the bug was invisible
- [x] **The conformance suite never checked the unhealthy direction.** It waited for
      workloads to become healthy and never required that a failing probe reports
      unhealthy, so a provider reporting every running workload healthy passed the gate
      (rule 4) with the health gate turned into a no-op. That is how the bug above
      reached `main`. New case added; the in-memory reference provider now models a
      target that serves only `/healthz` instead of reporting healthy whenever it runs,
      and `alwaysHealthy` joins the broken providers that prove each check bites
- [x] `cmd/bohux/e2e_swarm_test.go`: failure path first (a Release that never turns
      healthy leaves the previous one serving), then deploy, serve, redeploy, roll back
      with the CLI, and the artifacts-on-disk check (rules 2 and 3). `startStack` takes
      the provider as a parameter; swarm stacks are torn down beside Compose projects
- [x] CI job `e2e-swarm`, its own job because a swarm manager cannot have live-restore
      (ADR-0045) and the Docker e2e job requires it (ADR-0020)
- [x] **ADR audit.** Every proposed ADR from 0019 to 0051 is referenced from code
      (2–27 non-generated files each), so the tracker's "built on already" is true for
      all of them: there is no orphan paper decision, and rejecting one now would mean
      reverting shipped code. Statuses were **not** changed — that is O46
- [x] **The PR's first CI run is red**, which is the whole value of O24: see O45. Five
      of ten jobs fail, and the root cause of `proto` is the long-known missing
      `buf.lock`. Not fixed here: it needs Buf Schema Registry access, which this
      container does not have

## v0.3 slices

### S27 — Scheduled restore drills (ADR-0051)
Split in two: the drill itself, then the schedule around it. The vision's "backups are
proven by scheduled restore drills" is only half kept until S27b ships, because
`TestRestoreDrill` in CI proves the restore *code* against a stack the test built, not
an install's own archives.

#### S27a — the drill, and `bohux admin drill` ✅
- [x] Proposed ADR-0051 (O44). The design was checked against a real Postgres 16 before
      it was written down: a `CREATEDB` owner passes `MigrateTo`'s privileged-role
      check, the owner **cannot** `COPY` into a `FORCE ROW LEVEL SECURITY` table
      ("COPY FROM not supported with row-level security"), a `BYPASSRLS` role can once
      the owner grants it, `SET session_replication_role` needs
      `GRANT SET ON PARAMETER`, and a role with no grants in the live database sees
      none of its tables. That is what fixed the two-role split in item 3
- [x] `internal/drill`: `Run` restores an archive into a scratch database, a temp data
      directory and its own KV mount, and tears all three down pass or fail; `Verdict`
      and `Check` carry names, counts and times only (rule 3)
- [x] Checks beyond restore's own: the manifest's tables all present, row counts per
      table, a live tenant, a live **owner token** (an install nobody can administer is
      not restored), the node CA present, loadable, unexpired and reported by pin, and
      every live secret ref readable at the version Postgres records for it
- [x] Failure path tested first (rule 5): a truncated archive, a wrong identity, an
      archive with no owner token, an archive whose values were never backed up, and a
      drill with no mount — each leaves no scratch database, no secret in the drill
      mount and no temp directory, and the verdict comes back for the caller to record
- [x] Found by the tests: restore refuses a secrets-bearing archive with no OpenBao
      before any check runs, so that case is a `restore` failure, not a `secrets` one;
      and the reachable `secrets` failure is an archive whose **metadata** is there and
      whose values are not (a backup taken with no OpenBao configured) — now its own
      test
- [x] `bohux admin drill` (`--json` for the verdict the schedule will record); the
      drill-mount guard moved ahead of opening the identity file, so being pointed at
      the live mount is refused before anything is touched
- [x] Checked live on a real install: a backup taken before the control plane had ever
      started failed `node-ca` ("every agent would have to re-register"), and one taken
      after it passed every check with a CA pin matching `openssl`'s on the live file.
      No scratch database, mount path or temp directory left behind
- [x] `docs/reference/config.md` (with the private-key-on-the-host warning), runbook
      Section D (drill an archive, read each failure by check name, create the roles and
      mount on an upgraded install), `docs/runbooks/README.md`

#### S27b — the weekly schedule and the install ✅
- [x] `internal/workflow/drill`: `DrillWorkflow` and the `bohux-restore-drill-weekly`
      schedule, drilling the newest archive `backupwf.Newest` finds. A failed check is
      returned **non-retryable** (re-drilling reaches the same verdict and costs another
      full restore); no verdict at all is infrastructure and gets the one retry
- [x] **One worker, not two.** Both schedules fire onto the backup's task queue, and a
      worker that polls a queue must know every workflow type on it — two workers each
      knowing only their own would fail each other's tasks. So `backupwf.Run` was split
      into `Register` + schedule calls, and the new `internal/workflow/maintenance`
      owns the worker lifecycle, the retry-while-Temporal-is-down loop, and both
      schedules. Its integration test triggers both to prove the shared worker serves
      both types
- [x] The backup activity takes `Recipients []string`, so the drill key's public half
      rides on every archive (ADR-0051 item 2). The activity takes no arguments, so its
      input shape in workflow history did not change and no worker versioning was
      needed — ADR-0051's consequence about that was over-cautious
- [x] `controlplane.DrillConfig` with `--drill-*` flags: off unless an identity is
      configured, refused at start if it names the live KV mount or lacks a drill role,
      and switched off (with a warning) when nothing writes archives to drill
- [x] **ADR-0051 item 7 amended while proposed:** the identity is also accepted as a
      value (`BOHUX_DRILL_IDENTITY`), file winning, because the control plane runs as
      an App of itself and gets its configuration as secret env vars (ADR-0032) — the
      same pair the OpenBao token and SMTP password use. A path alone could not reach it
- [x] The verdict written to `<backup dir>/drills/<UTC time>.json`, 0600, pruned under
      the archives' retention; it is recorded **before** a failure is returned, since a
      workflow cannot return both a value and an error and the finding is the point
- [x] Installer: `bohux_drill` (CREATEDB, no BYPASSRLS) and `bohux_drill_admin`
      (BYPASSRLS + `GRANT SET ON PARAMETER session_replication_role`), granted nothing
      in `bohux_app`; the `bohux-drill` KV mount; `BOHUX_DRILL_IDENTITY` documented with
      the host-key warning, and refused without a backup recipient
- [x] Failure path first again: a failed check recorded and not retried, an empty
      archive directory, an infrastructure failure retried once and recording nothing,
      and switching either schedule off removing it
- [x] Found on the way: both schedule IDs are fixed server-wide names, so the new
      package's tests and the backup's deleted each other's schedules when `go test` ran
      the packages in parallel. `Schedule.ID` is now overridable for tests, and
      `RemoveSchedule` takes the schedule it is removing
- [x] Tick the roadmap's "Scheduled automated restore drills"
- [ ] **Not done, deliberately:** `scripts/selfdeploy-test.sh` does not trigger a drill.
      It would need an age **private** key at test time, and committing one to the repo
      is not acceptable; generating one needs a keygen the binary does not have. The
      maintenance integration test covers the schedule against a real Temporal instead.
      Worth revisiting if `bohux` ever grows a key-generation command

### S26 — SLO gate and automatic rollback from ingress traffic (ADR-0050) ✅
- [x] Owner chose: metrics from the node's ingress (PromQL optional, later), an SLO as
      a target with a burn-rate threshold, judged during a canary's bake and a watch
      window after other deploys. Proposed ADR-0050 (O43)
- [x] Proto: `App.slo` (`SLO`, `LatencyObjective`), `WorkloadStatus.traffic`
      (`TrafficStats`), `DEPLOYMENT_STATE_VERIFYING`. Migration 00025 (the slo column,
      the state)
- [x] `internal/slo`: fixed latency buckets, the window over cumulative counters (a
      counter going back restarts it), the burn-rate verdict with a 50-request floor
- [x] Ingress: with `Caddy.AccessLog`, each route starts with `log_append upstream`
      and a file logger (roll 10 MB, keep 1) whose filter encoder deletes the request,
      response headers and user; the agent's log sits beside the admin socket
- [x] Agent: `Traffic` tails the log (starts at the end of a file present at start,
      reads a later file from its start, survives rolls and truncation, keeps partial
      lines), counts per upstream in memory; the reconciler reports the counters of the
      slot running each routed workload's Release
- [x] API: SLO validated, stored, returned, kept by scale and every App update path;
      the deploy run gets it from the App's current revision
- [x] Workflow: `HoldHealthy` judges the traffic with the objective (non-retryable
      `SLO_AVAILABILITY`/`SLO_LATENCY`); a canary's bake uses it; other deploys hold in
      `VERIFYING` for the watch window; failures compensate. No version gate: only runs
      with an SLO take the new path (replay test passes)
- [x] CLI `bohux app slo` (`--availability --latency 250ms@95 --burn --watch | --clear`,
      warns for an App without routes), SLO column in `app show`, follow explains
      VERIFYING; CLI and UI updates keep the SLO; UI shows it on the App page
- [x] Tests: verdicts (unit), workflow SLO gate (6 cases), tailer, render, real Caddy
      2.8.4 writing only upstream/status/duration, reconciler per-slot counters during
      a canary and after promotion, validation (unit + Postgres), CLI, `HoldHealthy` on
      Postgres (6 cases), a deploy rolled back by its SLO and a verifying deploy
      blocking another and cancelled, on real Temporal
- [x] Live on the test install upgraded to this branch: the access log held only the
      three fields; the report carried hello's counters; a release whose
      `/cgi-bin/boom` returns 500 (health still green) was rolled back from
      `verifying` with `SLO_AVAILABILITY` about 8 s into the watch, under 80 failing
      requests
- [ ] Not done: the optional PromQL source (its own ADR), OTEL correlation and deploy
      markers; the App page's SLO line not looked at in a browser (typechecked and
      unit-tested only)

### S25 — Canary deploys through Caddy weights (ADR-0049) ✅
- [x] Owner chose canary, promoted automatically after a bake (with promote and abort),
      one step, per deployment. Proposed ADR-0049 (O42)
- [x] Proto: `DEPLOYMENT_STRATEGY_CANARY`, `Canary{weight_percent, bake}`, state
      `CANARY`, `PromoteDeployment`; `Workload.canary_weight`. Migration 00024 (strategy,
      weight, bake on the head; the state)
- [x] API: validation (1–99%, whole seconds 1m–24h, only Apps that switch slots),
      the canary stored and returned, `PromoteDeployment` (deployer, audited, refused
      outside CANARY)
- [x] Workflow: assign with the weight, health gate, CANARY, `HoldHealthy` through the
      bake (30 s grace) raced against the promote signal, then assign with weight 0 and
      wait; any failure compensates. No version gate needed: only runs with a canary
      take the new path (the replay test still passes)
- [x] Agent: weight outside the spec hash; once the new slot is healthy both slots
      are served weighted; an unhealthy canary leaves the live slot alone; promotion
      cuts over without re-applying. Ingress: `Site.Weights` → Caddy
      `weighted_round_robin`
- [x] CLI `deploy --canary N --bake D`, `deployment promote|cancel` (cancel had no
      CLI before); UI: canary shown, Promote now / Cancel / Abort canary
- [x] Tests: workflow (unhealthy canary, cancel, never healthy, bake end, promote
      signal); agent (broken, split, unhealthy, promotion); render; real Caddy splits
      40 requests 3:1; real Temporal API test (refusals, rollback, abort, promote,
      viewer refused, audit) — which found that `AssignApp` dropped a promotion as
      "unchanged" (fixed); `HoldHealthy` on Postgres; CLI usage
- [x] Live on the test install upgraded to this branch: a canary at 30% served exactly
      70/30 of 100 requests through Caddy; abort went back to the live slot; promote
      sent 40 of 40 to the new release and removed the old slot; no failed request
- [ ] Not clicked in a browser: the UI's promote and abort buttons (typechecked and
      unit-tested only)


### S21 — Swarm provider, single manager (ADR-0045) ✅
- [x] Owner answered O37 (single manager, no registry push) and, when it turned out
      Swarm cannot run with live-restore, chose to waive live-restore on a manager with
      `bohux-ingress` as an attachable overlay. ADR-0045 rewritten as decided (O40)
- [x] `providers/swarm`: Describe (active manager, exactly one node), Plan, Apply
      (`stack.yaml` + 0600 `secrets.env` first, private image pulled with the throwaway
      credential, `docker stack deploy --resolve-image never --prune`, forced update
      for a restart), Status (running tasks, spec-hash labels, health), Logs (`docker
      service logs`, stderr marked), Exec (`docker exec` into the task), Scale,
      Destroy (waits for the stack's tasks and networks); `Spec.Raw` networks and
      volumes, `publish_address` refused
- [x] Health checks inside the task's network namespace (locked thread, `setns`),
      image-agnostic; `dnsrr` endpoints aliased on the ingress overlay; slugs cut so
      service names fit Swarm's 63 characters
- [x] The Docker provider's exec session, limits, pull credential, atomic write, and
      replica edit exported for Swarm (no copies)
- [x] Agent: runs Swarm beside Docker when its daemon is a one-node manager; each
      workload, its logs and exec go to the provider it names; `applied.json` records
      the owner, and records without one (older agents) belong to Docker, so an upgrade
      destroys nothing. Registration and the installer accept a swarm manager without
      live-restore and warn; the installer creates the overlay there
- [x] Tests: **conformance green against a real single-manager swarm** (16 cases,
      175 s, a second dockerd in the cloud container); Docker conformance still green;
      render, names, logs, options (unit); two providers in the reconciler, legacy
      records (unit); a manager registers without live-restore (integration); a
      Compose container on the overlay reachable by alias (by hand)
- [x] CI job `conformance-swarm` (the runner's dockerd becomes the manager);
      `docs/providers/swarm.md`
- [x] The end-to-end run of the control plane and agent deploying to Swarm — done in
      S28, and it found a real agent bug on its first try
- [ ] Not done: the resource view does not sample Swarm workloads


### S20 — Monorepo watch paths (ADR-0044) ✅
- [x] Proposed ADR-0044 (O36): up to 20 repository-relative prefixes per `github`
      webhook; the push's commits' added/modified/removed files decide; no file list,
      GitHub's 2048-commit cap, or a shorter Gitea list than `total_commits` deploys
      anyway; `generic` webhooks cannot have paths
- [x] Proto `Webhook.paths`, migration 00023, API (normalized: `./` and trailing `/`
      dropped; absolute, `..`, `.`, empty refused), receiver, CLI `--path` and a PATHS
      column, UI field and column
- [x] Tests: prefix matching and the fail-open cases (unit), create validation and
      signed deliveries inside and outside the paths (integration), CLI

## Known gaps and notes
- **The container's Docker bridge can break beyond the documented recipe (2026-10-23,
  S28).** After several dockerd restarts in one session, `docker0` lost its veth
  attachments: `Failed to add veth… to bridge via netlink: Device does not exist`, so
  `bohux-mirror` cannot start, the buildx builder cannot reach the mirror, and every
  build test fails with `BUILD_FAILED`. Restarting dockerd did **not** fix it this time,
  and the container has no `ip` tooling to repair the bridge by hand. It is not a code
  problem — `TestBuildEndToEnd` fails the same way on the parent commit — so a fresh
  session is the remedy. Restart the main dockerd as few times as possible: start it
  once, with the swarm daemon on its own data root and socket.
- **A second swarm daemon, for the Swarm e2e (S28).** `BOHUX_TEST_SWARM_HOST` points the
  test at it, and `BOHUX_TEST_BUILD_NETWORK=none` is needed because that daemon runs
  with `--bridge none`, so builds have no default network. Start it with the
  swarm-daemon recipe below, then `docker swarm init --advertise-addr 127.0.0.1`. Do not
  guard the start with `pgrep -f <its own command line>`: the pattern matches the
  starting shell and silently skips the daemon.
- **`TestRestoreDrill` still races occasionally (seen once, 2026-10-23, S27b).** The
  rollback after a restore failed with `failed_precondition: a deployment of this app is
  already in progress` — the race ADR-0048 and S24 addressed. It did not reproduce in 3
  isolated runs, a repeat full-suite run, or on the parent commit, so it is rare and not
  tied to the drill work. If it appears again, the in-progress deployment the restored
  rows carry is the thing to look at: the test waits for a node report newer than the
  restore, which proves the agent is back but not that the previous deployment has been
  reconciled to a terminal state.
- **Starting the services after a worker restart:** the session's services all die with
  the worker. The scratchpad `services.sh` of 2026-10-23 brings back Postgres, Docker,
  Temporal, OpenBao, the registry mirror and the buildx builder in one go, and prints a
  health line for each; the Docker image steps can take several minutes on a cold
  container, while the four core services are up within about 15 s.
- **A swarm daemon in the cloud container (S21):** the main dockerd has live-restore,
  so swarm runs on a second one, without iptables or a default bridge so it leaves the
  main daemon alone:
  ```
  mkdir -p /var/lib/bohux-swarm-test /run/bohux-swarm
  setsid nohup dockerd --data-root /var/lib/bohux-swarm-test --host unix:///run/bohux-swarm/docker.sock \
    --pidfile /run/bohux-swarm/docker.pid --exec-root /run/bohux-swarm/exec --bridge none \
    --iptables=false --ip-masq=false --default-address-pool base=10.200.0.0/16,size=24 > /tmp/dockerd-swarm.log 2>&1 &
  DOCKER_HOST=unix:///run/bohux-swarm/docker.sock docker swarm init --advertise-addr 127.0.0.1 --default-addr-pool 10.201.0.0/16
  docker save busybox:1.36 | DOCKER_HOST=unix:///run/bohux-swarm/docker.sock docker load
  go test ./providers/_conformance -provider=swarm -target=unix:///run/bohux-swarm/docker.sock -v
  ```
  Service VIPs need IPVS, which this daemon lacks; the provider uses `dnsrr`, so it
  does not matter. Builds there need `--network none`.
- **After the cloud container restarts, the main dockerd can lose `docker0`** (builds
  fail: "adding interface … to bridge docker0 failed"). Restart dockerd (live-restore
  keeps containers), then `docker restart bohux-mirror`: its port forward does not
  come back by itself.
- **Railpack in the cloud container (S23):** ghcr.io blobs are blocked here. To run
  `TestBuilderRailpack`'s planning cases: download
  `railpack-v0.43.0-x86_64-unknown-linux-musl.tar.gz` and `checksums.txt` from the
  GitHub release, check the SHA-256, build `FROM localhost:5000/library/busybox:1.36`
  with `/root/.ccr/ca-bundle.crt` copied to `/etc/ssl/certs/ca-certificates.crt` and the
  binary at `/railpack`, tag it `localhost:5000/railpack-frontend:v0.43.0-local`, then
  set `BOHUX_TEST_RAILPACK_IMAGE` to it, `BOHUX_TEST_RAILPACK_NETWORK=host` (the proxy
  listens on localhost) and `BOHUX_TEST_RAILPACK_OFFLINE=1`.
- **Services die when the session's worker restarts.** Start them detached with
  `setsid` (the scratchpad `services.sh` of 2026-10-22 does); the tmpfs Go caches go
  with a container restart and must be recreated (`mkdir -p /dev/shm/gomod
  /dev/shm/gobuild /dev/shm/e2e`).
- **Resolved 2026-10-22 (S24):** the restore drill's race with the previous deploy's
  notifications (ADR-0048), and `TestConnect`'s occasional EOF in place of
  "unauthenticated" (a Send-side EOF read as the error).
- **Disk workaround for builds in the cloud container (2026-10-22):** with ~9% free the
  agent refuses builds. Move the Go caches to tmpfs and run e2e with `TMPDIR` there:
  ```
  mv /root/go/pkg/mod /dev/shm/gomod && ln -s /dev/shm/gomod /root/go/pkg/mod
  rm -rf /root/.cache/go-build && mkdir -p /dev/shm/gobuild && ln -s /dev/shm/gobuild /root/.cache/go-build
  mkdir -p /dev/shm/e2e && TMPDIR=/dev/shm/e2e go test -tags=integration,e2e ./cmd/bohux
  ```
  That lifted free space to 10.7% and the build e2e passed.
- **Control-plane images when Docker Hub rate-limits `golang:`:** build the binary
  locally (`CGO_ENABLED=0`, `-X …/internal/version.Version=selfdeploy`) and layer it on
  the previous image: `FROM localhost:5000/bohux:<prev>` + `COPY bohux /bohux`.
- **`web` dev dependencies have npm audit advisories** (vitest/tinypool, vite/esbuild,
  tailwind's braces): fixes are major upgrades; dev and build time only, nothing in the
  shipped bundle. Not taken on in S22.
- **`bohux <noun> <verb> --help`** logs the usage as an `ERROR bohux failed` line
  instead of printing it plainly.
- **CI has not run on this branch.** `ci.yml` triggers on pushes to `main` and on
  pull requests; there is no PR for `claude/relaxed-rubin-76m7dl`, so every check so
  far ran locally in the cloud container (lint, unit, integration, Docker conformance,
  the self-deploy test). Opening a PR runs the whole matrix, `selfdeploy` included.
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

### 2026-10-23 (S28) — branch `claude/loving-shannon-rplqcg`
- Owner picked hardening over a v0.3 feature, and said to open the PR. Both paid off.
- **PR #2 opened: CI ran for the first time ever and five of ten jobs fail** (O45).
  `proto`'s root cause is the missing `buf.lock` that *Known gaps* has carried since
  S1; it needs registry access this container does not have. Every "verified" in this
  tracker so far meant one cloud container's word for it, and now we know the pipeline
  itself does not pass.
- **The Swarm end-to-end run found a real bug on its first attempt**, and it was in the
  agent, not the provider: a broken Release deployed to Swarm reported SUCCEEDED in two
  seconds. The reconciler trusted a Status it had not checked the hash of, so a rolling
  update let the previous release's health stand in for the new one's and the health
  gate rule 5 arms was bypassed. Fixed, with a reconciler regression test written first.
- **And the gate had a hole that let it through:** the conformance suite only ever waited
  for healthy and never required unhealthy to be reported. Closed, and the in-memory
  reference provider had to start modelling a probe instead of reporting healthy
  whenever a workload runs.
- ADR audit: all 32 proposed ADRs (0019–0051) are referenced from code, so "built on
  already" is true for every one. Statuses left alone — the repo's own `write-adr` skill
  and this tracker disagree about who marks an ADR accepted (O46).
- Verified: gofmt, build, vet (all three tag sets), golangci-lint plain and integration
  (0 issues), full unit suite with race, integration 25/26 (the documented
  `TestBuilderRailpack` ghcr.io gap), **both** conformance suites green with the new
  case, and the Swarm e2e green. The Docker e2e's `TestRestoreDrill` passed 3/3 in
  isolation and in the final full run; `TestBuildEndToEnd` fails in this container now
  for an environment reason that also fails on the parent commit — see *Known gaps*.

### 2026-10-23 (S27b) — branch `claude/loving-shannon-rplqcg`
- The restore drill is on a weekly schedule. The design change the code forced: both
  schedules fire onto one task queue, and a Temporal worker that polls a queue must
  know every workflow type on it, so two packages each starting their own worker would
  have failed each other's tasks. `backupwf.Run` is split into `Register` plus the
  schedule calls, and `internal/workflow/maintenance` now owns the worker. Its
  integration test triggers both schedules to prove it.
- ADR-0051 item 7 amended while proposed: the drill identity is also accepted as a
  value, because the control plane runs as an App of itself and gets its config as
  secret env vars, so a file path alone never reaches it.
- Found on the way: the two schedule IDs are fixed server-wide names, so the new
  package's tests and the backup's deleted each other's schedules when `go test` ran
  the packages in parallel. `Schedule.ID` is overridable now and `RemoveSchedule` takes
  the schedule it removes. Both suites then passed twice in a row.
- Verified: gofmt, build, vet (both tag sets), golangci-lint (plain and integration, 0
  issues), the full unit suite with race, the integration suite (25 of 26 packages;
  the one failure is the documented `TestBuilderRailpack` ghcr.io gap, unchanged from
  before this slice), and the full e2e suite.
- **One e2e flake worth knowing about:** on the first full e2e run `TestRestoreDrill`
  failed with `rollback: failed_precondition: a deployment of this app is already in
  progress` — the ADR-0048 race S24 was meant to close. It did not reproduce: 3 isolated
  runs and a repeat full-suite run on this branch all passed, and the same commit's
  parent passed both isolated and full-suite. So it is not this slice's regression, but
  the race is narrowed rather than gone. Recorded under *Known gaps*.

### 2026-10-23 (S27a) — branch `claude/loving-shannon-rplqcg`
- A fresh container and a new branch: `claude/relaxed-rubin-76m7dl` is merged into
  `main` (PR #1), so this branch was fast-forwarded to `main` and work continues from
  there. Full environment setup was needed again — Postgres, Docker, Temporal, OpenBao,
  `buf` and the protoc plugins installed from scratch, and `gen/` regenerated through
  the scratch-workspace workaround in *Known gaps* (buf.build is still blocked).
- Owner picked scheduled restore drills. ADR-0051 proposed (O44) after checking the
  Postgres role mechanics empirically — that is where the two-role split came from.
  The slice was too big for one session, so it is split: S27a (the drill and the
  command) is done, S27b (the schedule and the installer) is next.
- The drill found a real failure on its first live run: an archive taken before the
  control plane had ever started carries no node CA, which would force every agent to
  re-register. Exactly the class of failure a drill exists for.
- Verified: gofmt, build, vet, golangci-lint (plain and with the integration tag, 0
  issues both), the full unit suite with race, and the integration suite — 23 of 24
  packages green, including `internal/drill`. The one failure is `TestBuilderRailpack`,
  the documented gap in *Known gaps* (ghcr.io blobs are blocked here, so the Railpack
  frontend image has to be hand-built); it was failing before this slice and the slice
  touches nothing near it. The other four build and image failures of the first run
  were the missing registry mirror and buildx builder, and pass once *Known gaps*'
  recipe is applied. Also drilled two real archives by hand on a real install. No
  provider and no proto changed, so no conformance run; the web UI is untouched.

### 2026-10-22 (S26) — branch `claude/relaxed-rubin-76m7dl`
- Owner picked the SLO gate (ingress metrics, target + burn rate, canary bake + watch
  window). ADR-0050 proposed (O43), item 6 amended (no deployment log exists). Tests
  found the tailer skipping the first lines of a log created after the agent started;
  fixed. Checked live: a 5xx-returning release rolled back with `SLO_AVAILABILITY`.
- Verified: build, vet, golangci-lint (plain, integration, integration+e2e), macOS
  build, unit with race, full integration suite, e2e (deploy, build, restore drill)
  green, web typecheck/tests/build. No provider changed, so no conformance run.

### 2026-10-22 (S25) — branch `claude/relaxed-rubin-76m7dl`
- Owner picked canary deploys (auto-promote after a bake, one step, per deployment).
  ADR-0049 proposed (O42). The real-Temporal test found a promotion lost in
  `AssignApp`; fixed. Checked live on the test install (70/30 split, abort, promote).
- Verified: build, vet, golangci-lint (both tag sets), macOS build, unit with race,
  integration (22 packages), e2e green. No provider changed, so no conformance run.

### 2026-10-22 (S24) — branch `claude/relaxed-rubin-76m7dl`
- Owner picked the notification race. ADR-0048 proposed (O41); histories of the old
  code recorded before the change and replayed after it.
- Verified: build, vet, golangci-lint (both tag sets), unit with race, the full
  integration suite (22 packages) twice, e2e (deploy, build, restore drill) green.
  No provider changed, so no conformance run.

### 2026-10-22 (S21) — branch `claude/relaxed-rubin-76m7dl`
- Swarm cannot run with live-restore; the owner chose to waive it on a manager.
  ADR-0045 rewritten as decided (O40); the provider passed the conformance suite on
  its first full run against a real swarm. The agent runs it beside Docker.
- Verified: build, vet, golangci-lint (both tag sets), macOS build, unit with race,
  full integration suite, e2e (deploy, build, restore drill all green this time),
  Docker and Swarm conformance. The session's worker restarted four times; services
  are now started detached.

### 2026-10-22 (S23) — branch `claude/relaxed-rubin-76m7dl`
- Nixpacks is maintenance-only upstream; the owner switched O32 to Railpack (auto
  when there is no Dockerfile, plus an override). S23 done with ADR-0047 (O39).
- Verified: build, vet, golangci-lint (plain and integration+e2e tags), unit with
  race, full integration suite, e2e deploy and build green. `TestRestoreDrill` failed
  once in the full e2e run on a notification race (Known gaps), passed 3/3 alone.
  The full Railpack build could not run here (ghcr.io blocked).

### 2026-10-22 (S22) — branch `claude/relaxed-rubin-76m7dl`
- The session prompt arrived with its `<O-items>` placeholder unfilled and nothing
  answered in the tracker, so the owner was asked directly: web terminal next, over a
  WebSocket bridge (O29); Swarm single manager with image `HEALTHCHECK`, image-only
  (O37); Nixpacks (O32).
- S22 done: ADR-0046 proposed (O38), bridge, Terminal page, tests; checked in Chromium
  on the test install upgraded to this branch. The e2e build test ran here for the
  first time and is green.
- Full unit, race, and integration suites green; golangci-lint clean with and without
  the integration and e2e tags. `internal/agentapi` failed once in a full integration
  run (output not kept; the tail showed an "agent silent" stream close); it passed in
  the next full run and in six repeated runs under load. Not touched this session.
  Docker conformance not rerun (no provider change).

### 2026-10-21 (auto run, S17) — branch `claude/relaxed-rubin-76m7dl`
- S15 forms and App page done (checked in Chromium).
- S17 started: ADR-0041 proposed (O30). Private registry images work end to end
  against a real authenticated registry; CLI, UI, docs. Full integration suite green.
- S17 done: Git deploy keys (sealed in the build request, throwaway key file, buildx
  `--ssh`). Host key policy raised as O31; Buildpacks/Nixpacks choice as O32.
- S15: webhooks and notification channel screens, checked in Chromium.
- Upgrading the test install's control plane to check the UI found a gap: an image
  stamped as a release refused the sandbox's non-release agent; the release was
  healthy locally, so the agent never fell back and the rollback could not reach it.
  Fixed (ADR-0042 proposed, O33): healthy counts only while the control plane accepts
  the agent. Verified on the install: the new agent fell back on its own.
- Tokens in the CLI (they were missing) and the UI; a dispatch drift bug found on the
  live install and fixed with a test over every noun. The test install now runs this
  branch's control plane (upgraded with `bohux deploy controlplane`, migration 00021
  applied).
- S18: interactive exec (`-i`, `-t`), conformance case; found and fixed logs/exec
  missing slotted Apps, and the macOS build.
- S19: email notifications (ADR-0043 proposed, O34). O35 lists what the rest of v0.2
  needs decided.
- A code review of the session's commits found eight real defects, all fixed with
  tests: a secret used as a pull secret or deploy key could be deleted; the pull
  credential dropped the operator's Docker context and proxies; a failed SMTP QUIT
  after acceptance re-sent mail; `token create` was scoped by `BOHUX_PROJECT`; a data
  race in exec `Close`; a silent slot-read error; the mailto: conversion duplicated in
  clients; and ADR-0042 as first written let a control-plane *outage* revert a
  healthy release (rule 1). It now keys on a version rejection only.
- Live install: deploy and rollback of a routed App with 312 polled requests, all 200;
  exec follows the serving slot. The UI's new pages pass the installed CSP.
- One unexplained failure: a Docker conformance run right after the integration suite
  failed once (log not kept); three reruns, including the same ordering, passed.
- S20 (v0.3): webhook watch paths (ADR-0044 proposed, O36).
- S21 (Swarm) not started: a second daemon in swarm mode works in the sandbox (the
  main one has `--live-restore`, which swarm refuses), but health, exec, and images
  differ in ways that need the owner (ADR-0045 proposed as questions, O37).
- **Next:** see Current focus.

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
- S11 done: the CLI covers the API's main resources; the self-deploy test uses it.
- S12 done: Git push webhooks (ADR-0037 proposed, O23), API, CLI, tests.
- Found: CI never ran on this branch (no PR); added as O24.
- S13 done: deploy notifications (ADR-0038 proposed, O25), API, workflow, CLI, tests.
- S14 done: `bohux mcp` (ADR-0018), tool policies in proto, tests.
- S15 started: ADR-0039 proposed (O26); the read-only web UI, embedded and served
  with a strict CSP, checked in a real browser.
- S15 continued: live deployment page, logs, deploy and rollback, activity page;
  audit events and revisions in the API; S16 resource view (ADR-0040, O28). Full
  self-deploy test passed on an image with the UI embedded.
- **Next:** UI forms, then the next v0.2 item.

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
