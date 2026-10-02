# Bohux

A self-hostable deployment platform: a reconciliation kernel plus pluggable providers.


## Where to start

| You want to | Read |
|---|---|
| Understand what this is and is not | `docs/plan/00-vision.md` |
| Understand how it works | `docs/plan/01-architecture.md` |
| Know why a decision was made | `docs/adr/` |
| Know what is in scope now | `docs/plan/02-roadmap.md` |
| Add a deployment target | `docs/specs/provider-interface.md` |
| See the API plan | `docs/specs/api-contract.md` |
| Work in this repo with Claude Code | `CLAUDE.md` |

## Status

Pre-v0.1. The documentation is ahead of the code on purpose — the decisions were made
first so the implementation does not have to re-litigate them.

Current milestone: **v0.1 walking skeleton** — one service, one VPS, deploy and roll back
from a CLI. Exit criterion: Bohux deploys Bohux.

## Layout

```
CLAUDE.md              rules that apply to every task
.claude/skills/        procedures for recurring work
docs/plan/             living plans — edited as reality intervenes
docs/adr/              decisions — numbered, dated, immutable
docs/specs/            interface and data contracts
docs/runbooks/         operational procedures
proto/                 the API contract; source of truth (ADR-0006)
cmd/bohux/              single binary; modes: controlplane, agent, cli
internal/              implementation
providers/             deployment targets + conformance suite
web/                   dashboard
deploy/                how Bohux deploys itself
```
