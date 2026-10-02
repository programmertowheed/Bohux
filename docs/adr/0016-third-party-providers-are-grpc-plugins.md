# ADR-0016: Third-party providers are out-of-process gRPC plugins; WASM deferred

- **Status:** accepted
- **Date:** 2026-10-02
- **Deciders:** Md Towheedul Islam

Supersedes the plugin-mechanism part of ADR-0005. The verb set, the conformance gate and
the no-partial-providers rule from ADR-0005 (as amended by ADR-0010) are unchanged.

## Context
ADR-0005 made WASM components (WASI Preview 2) the default for third-party providers,
on the grounds that the sandbox protects the control plane. ADR-0007 then moved provider
execution into the agent. Three facts now undercut the WASM default:

1. **The sandbox protects almost nothing.** A provider needs the Docker socket or a
   kubeconfig, network access, and write access to disk for its artifacts (hard rule 2).
   Granting those capabilities is close to granting the host.
2. **Go has no pure-Go Preview 2 runtime.** wazero supports Preview 1 only; wasmtime-go
   requires cgo, which breaks the single static agent binary and the ADR-0011 footprint gate.
3. **Authoring cost.** Building a Preview 2 component from Go is immature, which defeats
   ADR-0005's own goal of "a new provider is a weekend of work".

## Decision
Until v1, all shipped providers are in-tree Go packages. Third-party providers, when
opened, are out-of-process gRPC plugins launched and supervised by the agent over a
versioned plugin protocol defined in `proto/` (rule 9). A plugin runs with the agent's
privileges; it is trusted exactly as much as the operator who installs it. Trust is
enforced by signature verification and digest pinning at install time, not by a sandbox.
Plugins pass the same conformance suite as in-tree providers. WASM is deferred.

## Alternatives considered
- **Keep WASM as default via wasmtime-go.** Rejected: cgo breaks the static binary and
  cross-compilation, and the capability grants a real provider needs make the sandbox
  largely theatre.
- **Keep WASM, wait for a pure-Go Preview 2 runtime.** Rejected as the default: it blocks
  third-party providers on an external timeline we do not control.
- **No third-party mechanism at all until after v1.** Considered; the in-tree-only part is
  adopted for v0.x, but the mechanism is chosen now so the plugin protocol can be designed
  in `proto/` alongside the provider interface rather than retrofitted.
- **Native shared libraries / shell scripts.** Already rejected in ADR-0005; reasons stand
  (blast radius; no types, no network boundary).

## Consequences
- Agent stays a single static binary with no cgo.
- A crashing plugin cannot take down the agent; the agent restarts it. Running workloads
  are unaffected either way (rule 1).
- No isolation from a malicious plugin. This must be stated plainly in the plugin docs and
  the install UX; signing and pinning are mandatory, not optional.
- We now own a plugin wire protocol with its own versioning burden.
- Roadmap item "WASM plugin runtime opened to third parties" becomes "gRPC plugin protocol
  opened to third parties".

## Revisit when
A pure-Go WASI Preview 2 runtime ships a stable release, **or** a real user needs to run
providers they do not trust (e.g. a hosted multi-tenant offering where tenants supply
plugins).
