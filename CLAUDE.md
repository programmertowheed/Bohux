# Bohux — working rules

Bohux is a self-hostable deployment platform. A reconciliation kernel plus pluggable
providers. Read `docs/plan/00-vision.md` before your first task in this repo.

**Product: Bohux** by Bohumix (bohumix.com). Website: bohux.com. Repo: `github.com/bohumix/bohux`. ADR-0015.

## Orientation (read in this order)

1. `docs/plan/00-vision.md` — what this is, and the explicit non-goals
2. `docs/plan/01-architecture.md` — control plane / data plane design
3. `docs/adr/` — decisions already made, with the alternatives rejected
4. `docs/specs/provider-interface.md` — the extension boundary
5. `docs/plan/02-roadmap.md` — what is in scope right now
6. `docs/plan/03-progress.md` — **where the work is right now.** Read it at the start
   of every session and follow its session protocol; update and push it before you stop.

## Hard rules

These are not style preferences. Violating one is a bug, not a disagreement.

1. **Control plane outage is not an app outage.** The agent keeps reconciling its last
   known good state when it cannot reach the control plane. Never add a code path where
   a running workload depends on the control plane being up.
2. **No lock-in by construction.** Every provider writes real, readable artifacts to disk
   (Compose files, K8s manifests, systemd units). A user who deletes Bohux still has a
   running, comprehensible system.
3. **Secrets never land in Postgres, logs, traces, or error strings.** Secret values are
   referenced by handle. If you are about to log a struct that might contain one, use the
   redacting wrapper in `internal/secrets`.
4. **Every provider implements the full verb set or does not merge.** No partial providers.
   See `docs/specs/provider-interface.md`. The conformance suite in
   `providers/_conformance` is the gate.
5. **The rollback path gets a test before the forward path merges.** Rollback is not a
   stretch goal, it is half the feature.
6. **Deploys are Temporal workflows, never ad-hoc goroutines or a cron poller over a
   status column.** If you are writing retry logic by hand, you are in the wrong layer.
7. **Boring storage.** Postgres until Postgres provably breaks, with a benchmark attached
   to the claim.
8. **The UI gets no capability the CLI lacks.** Both are clients of the same public API.
9. **`proto/` is the source of truth.** Change the contract there first, regenerate, then
   implement. Never hand-edit generated code.
10. **Multi-tenancy, RBAC, and audit are load-bearing from day one.** Any new resource
    needs a tenant scope, a permission check, and an audit event. No exceptions "for now".

## Conventions

- **Go 1.23+**, standard layout. `internal/` for anything not a public contract.
- **Errors:** wrap with `%w` and context. No bare `return err` across a package boundary.
- **No `panic` in library code.** `cmd/` may panic during startup only.
- **Logging:** `log/slog`, structured, no `fmt.Println`. Request-scoped logger from ctx.
- **Tests:** table-driven. Integration tests behind the `integration` build tag.
  New provider work requires conformance suite passing, not just unit tests.
- **Migrations:** forward-only, one file per change, reversible or explicitly marked
  irreversible with a comment explaining why.
- **Commits:** conventional commits. One logical change per commit.

## When you make a decision

If a task forces a choice that is hard to reverse — a dependency, a data model shape, a
protocol — stop and write an ADR first. Use `.claude/skills/write-adr/`. Do not bury an
architectural decision inside a feature PR.

## When you are unsure

Ask. Do not invent a provider, a config key, or an API field to make a task compile.
Scope creep in this repo compounds badly — the whole project dies if it becomes
"everything for everyone" before v1 works for one thing.
