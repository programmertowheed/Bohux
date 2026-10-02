# ADR-0001: Go for control plane, agent, and CLI

- **Status:** accepted
- **Date:** 2026-10-02

## Context
Self-hosted users run this on a 2GB VPS alongside their workloads. We need one artifact
for three roles (control plane, node agent, CLI), ten thousand concurrent SSH sessions
and log streams, and first-class access to the container ecosystem.

## Decision
Go for the control plane, agent, and CLI — one codebase, one static binary, three modes.

## Alternatives considered
- **Node/TypeScript (Dokploy's choice).** Shared types with the frontend is real value,
  but it costs a runtime, a build step, and a worse memory floor on the smallest target
  box. Rejected on the resource budget.
- **PHP/Laravel (Coolify's choice).** Excellent developer velocity and a mature ecosystem,
  but there is no credible single-binary agent story, and the container libraries are not
  there. Rejected on the agent requirement.
- **Rust.** Better resource profile still, and the WASM story is nicer. Rejected on
  ecosystem: containerd, BuildKit, client-go, Temporal, and every cloud SDK are Go-native.
  We would spend the first year writing bindings.
- **Ruby (Kamal's choice).** Fine for an SSH-driven deploy tool with no server. We have a
  server. Rejected.

## Consequences
- One binary to ship, cross-compile, and sign. Agent install is a curl and a systemd unit.
- CLI and server share types; no drift between what the API accepts and what the CLI sends.
- Frontend does not share types with the backend — mitigated by generating the TS client
  from `proto/`.
- Go's error handling verbosity is a tax we accept.

## Revisit when
Never for the core. A specific subsystem (e.g. a high-throughput log shipper) may be
written in Rust if profiling justifies it, under its own ADR.
