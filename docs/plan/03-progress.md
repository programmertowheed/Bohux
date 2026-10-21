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

**Status legend:** ⏳ not started · 🔄 in progress · ✅ done · ⛔ blocked (say on what)

## Current focus

- **Working branch:** `claude/relaxed-rubin-76m7dl`
- **Slice:** S1 — proto contract (⏳, next to start)
- **Exact next step:** write `proto/bohux/v1/common.proto` (`ResourceMeta`) per
  `docs/specs/api-contract.md`, then the remaining files in its *Files* list.

## Waiting on the owner

| # | Item | Blocks |
|---|---|---|
| O1 | Accept or reject ADR-0019 (Caddy only) and ADR-0020 (v0.1 baseline) | S5, S7, S8 scope |
| O2 | Run `make run-dev && make footprint` on a 2GB VPS; record results in `docs/footprint.md` | ADR-0002 stays or reopens; nothing before S6 |
| O3 | Create the `bohumix` GitHub org and move the repo (ADR-0015). Code already uses `github.com/bohumix/bohux` | Public `go get`, not local work |
| O4 | Confirm the vision change: parity is demand-driven, not the goal | Nothing technical |

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

### S1 — Proto contract ⏳
Plan: `docs/specs/api-contract.md` (all questions answered there).
- [ ] Add `buf.build/googleapis/googleapis` dependency, pinned in `buf.lock` (ADR-0014)
- [ ] `proto/bohux/v1/`: common, identity, tenancy, project, environment, app, release,
      deployment, workload, node, secret, audit
- [ ] `proto/bohux/agent/v1/agent.proto`, including version fields for skew (ADR-0020)
- [ ] Delete `proto/bohux/v1/service.proto` and its lint/breaking ignore in `buf.yaml`
- [ ] `buf lint` clean with no ignores

### S2 — Storage ⏳
- [ ] Migration tool choice (ADR if it is a new dependency)
- [ ] Schema: tenants, projects, environments, apps, releases, deployments, revisions,
      audit_events; `tenant_id` + row-level security everywhere
- [ ] Revision + audit event written in one transaction (ADR-0004, ADR-0017)
- [ ] Integration tests against the dev Postgres (`integration` tag)

### S3 — API and access ⏳
- [ ] ConnectRPC server in `controlplane` mode
- [ ] API tokens (hashed), roles `viewer`/`deployer`/`owner`, permission check on every RPC
- [ ] Tenant and actor taken from authentication only
- [ ] Audit event on every mutation; secret-redacting interceptor (rule 3)

### S4 — Agent ⏳
- [ ] Bootstrap-token registration, mTLS, outbound `Connect` stream
- [ ] Signed, generation-numbered assignments; sealed secret cache (ADR-0008)
- [ ] Offline reconcile loop (rule 1) — test with the control plane killed
- [ ] Version skew check, N-1 minor (ADR-0020)
- [ ] Refuse to register without Docker `live-restore` (ADR-0020)

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
- `gen/` is not committed. Run `make generate` (or `buf generate`) after cloning, before
  `go build ./...`. CI does this.
- The Buf Schema Registry was unreachable from the cloud container on 2026-10-21; S1
  may need to run `buf dep update` where the registry is reachable (CI or locally).
- The cloud container has no running Docker daemon, so `make run-dev` and integration
  tests cannot run there. Integration slices (S2+) need CI or a local machine.

## Session log (newest first)

### 2026-10-21 — branch `claude/relaxed-rubin-76m7dl`
- Proposed ADR-0019 (Caddy only) and ADR-0020 (v0.1 operational baseline); updated
  architecture, roadmap, and vision.
- Completed S0 Foundations (see above). Lint, test, and build pass locally.
- **Next:** S1, starting with `common.proto`.
