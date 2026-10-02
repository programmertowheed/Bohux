# ADR-0006: gRPC over ConnectRPC, `proto/` as the source of truth

- **Status:** accepted
- **Date:** 2026-10-02

## Context
We need one contract serving a CLI, a browser UI, third-party SDKs, and the agent
protocol. Historically the UI gets privileged endpoints the CLI lacks, and the two drift.

## Decision
The gRPC contract in `proto/` is the source of truth. Served over ConnectRPC so browsers
call it directly without a translating proxy, with a generated REST/OpenAPI gateway for
curl and scripting. The CLI, the TS client, and the agent protocol are all generated.
**The UI gets no capability the CLI lacks** (CLAUDE.md rule 8).

## Alternatives considered
- **REST-first with hand-written OpenAPI.** The spec drifts from the implementation within
  months unless generated, and generating it properly means an IDL anyway.
- **GraphQL.** Good fit for a UI, poor fit for a CLI and an agent protocol, and the
  authorisation story for a multi-tenant control plane is genuinely harder.
- **Plain gRPC.** Needs grpc-web plus a proxy for browsers. ConnectRPC removes that
  component for free.

## Consequences
- Breaking the contract is a visible, reviewable diff in one directory.
- Generated code must never be hand-edited (CLAUDE.md rule 9).
- The agent protocol is now a public API with N-1 compatibility obligations.
- Protobuf tooling is a build dependency; `buf` enforces lint and breaking-change checks
  in CI.

## Revisit when
Never for the core contract. Additive REST conveniences are fine.
