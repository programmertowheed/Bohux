# ADR-0005: Provider boundary with a fixed verb set

- **Status:** accepted; verb list amended by ADR-0010 (no Rollback verb); plugin mechanism superseded by ADR-0016 (gRPC plugins, WASM deferred)
- **Date:** 2026-10-02

## Context
"Deploy any kind of project anywhere" is not achievable by a core team. It is achievable
only if adding a new target is cheap enough that other people do it. That requires the
interface to be small, stable, and conformance-tested.

## Decision
Eight verbs: `Plan`, `Apply`, `Status`, `Logs`, `Exec`, `Scale`, `Rollback`, `Destroy`.
In-tree providers are Go packages. Third-party providers are WASM components (WASI
Preview 2) with explicit capability grants, or out-of-process gRPC plugins as the escape
hatch for anything needing real syscalls. A shared conformance suite gates every provider.

## Alternatives considered
- **A richer interface with per-target features.** Every tool that has tried this ends up
  with providers that implement 40% of the surface and users who cannot tell which
  features work where. Rejected — the narrow interface is the product.
- **Native shared libraries / dlopen.** A crashing plugin takes the control plane with it,
  and the ABI is a cross-compilation nightmare. Rejected on blast radius.
- **Subprocess plugins only (Terraform's model).** Proven and simple, but no sandboxing —
  a plugin can read the host filesystem and the secrets store. Kept as an escape hatch,
  not the default.
- **Shell-script plugins (Dokku's model).** Lowest barrier to entry of any option, and
  genuinely good for a single-host tool. No type safety, no sandboxing, and no sane way to
  do this over a network boundary. Rejected.

## Consequences
- A new provider is a weekend of work with a clear done criterion: conformance passes.
- Target-specific capability is expressed as declared capabilities plus a raw passthrough
  escape hatch, never as extra interface methods.
- WASI P2 is young; tooling will be rough for the first year. Accepted, because the
  alternative is a plugin ecosystem that can crash production.
- **Rule: no partial providers merge.** In CLAUDE.md as rule 4.

## Revisit when
Three or more real providers need the same ninth verb. One does not justify a change.
