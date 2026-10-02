# ADR-0009: No NATS — Postgres plus agent gRPC streams

- **Status:** proposed
- **Date:** 2026-10-02
- **Deciders:** Towheed (pending review)

## Context
`01-architecture.md` lists NATS JetStream (pub/sub, KV, object store) for log streaming,
agent fan-out, and work distribution. It never had an ADR. It contradicts ADR-0004
("Postgres as the only datastore") and CLAUDE.md rule 7 ("boring storage … with a
benchmark attached"). It is also a fourth stateful service on a 2GB VPS that already runs
Postgres, Temporal, and the workloads.

After ADR-0007, each NATS job already has an owner:

- **Agent fan-out:** each agent holds one outbound gRPC stream to the control plane;
  assignments go down it.
- **Work distribution:** Temporal task queues.
- **Log streaming:** agent → control plane → client, relayed over the same streams.
  Kiln does not store logs (OTLP to the user's backend).
- **KV / object store:** Postgres rows; build artifacts live in an OCI registry.

## Decision
Remove NATS from the architecture. Agent communication is gRPC bidirectional streams
defined in `proto/`. Cross-replica notification inside the control plane (e.g. "a new
assignment exists for node N" when the agent is connected to another replica) uses
Postgres `LISTEN/NOTIFY` with the row as the source of truth. A single-node install runs
one control-plane replica and needs neither.

## Alternatives considered
- **Keep NATS JetStream.** Genuinely good software and one Go binary. Lost because every
  duty above is already covered, and it adds a second store with its own durability,
  backup, and upgrade story — exactly what rule 7 forbids without a benchmark.
- **Redis pub/sub.** Lighter. Lost on the same argument: a second stateful service for a
  job we do not have yet.
- **Agents poll Postgres-backed HTTP endpoints.** Simplest. Lost on latency for exec/logs
  and on load at node counts in the hundreds; streams are already required for Exec.

## Consequences
- One fewer service to install, back up, and upgrade. The single-node footprint drops.
- Log relay through the control plane is not built for high volume. Bulk log shipping
  goes from the OTEL collector straight to the user's backend; `kiln logs` is for tailing,
  not archive.
- Multi-replica control planes need sticky routing or LISTEN/NOTIFY fan-out; designed in,
  not built in v0.1.
- `LISTEN/NOTIFY` is lossy across reconnects; agents must resync full state on reconnect
  (they already must, per ADR-0003).

## Revisit when
A benchmark shows Postgres `LISTEN/NOTIFY` or the stream relay is the bottleneck at a
real deployment's node count, or a measured log-tail fan-out exceeds what one control
plane replica can relay. Attach the benchmark to the superseding ADR.
