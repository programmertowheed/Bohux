# ADR-0008: Secret values on the node — sealed cache, separate secret files

- **Status:** accepted
- **Date:** 2026-10-02
- **Deciders:** Towheed

## Context
Three accepted rules collide:

1. Rule 1 / ADR-0003: the agent restarts and re-applies workloads while the control plane
   is down. A restarted container needs its secret values.
2. Rule 2 / `provider-interface.md`: Apply writes readable artifacts so a user who deletes
   Kiln keeps a running system. That system also needs its secret values.
3. Rule 3 / conformance: secret values never appear "in any emitted artifact or log line".

Secret handles resolve against OpenBao in the control plane. As written, either the
agent cannot self-heal offline, or deleting Kiln leaves containers that cannot start, or
secrets end up in the Compose file. None is acceptable.

## Decision
- **Resolution happens on the control plane, delivery to the node.** When the control
  plane sends an assignment (ADR-0007), it resolves the handles that workload needs and
  sends the values on the same mTLS stream, encrypted to that node's public key.
- **The agent keeps a sealed cache** of the values for workloads assigned to *that node
  only*. Sealed with a node key (TPM-bound where available, else a 0600 key file owned by
  the agent user). This cache is what makes offline self-healing possible.
- **Providers render secrets into dedicated secret files, never into artifacts.** For
  Docker: `kiln/<service>/compose.yaml` (artifact, readable, no values) plus
  `kiln/<service>/secrets.env` (0600, values) referenced via `env_file`. Kubernetes
  provider uses a native `Secret` object; systemd uses `LoadCredential=`. The provider
  spec names each target's secret file mechanism.
- **Rule 3 is restated precisely:** secret values may exist only in (a) the secrets
  backend, (b) the encrypted assignment stream, (c) the node's sealed cache, (d) the
  provider's declared secret file/object. Never in Postgres, logs, traces, errors,
  artifacts, or Temporal workflow history. Conformance checks (c)/(d) are the only places
  values appear on disk.
- **Revocation is not offline expiry.** A cached assignment does not expire while the
  control plane is unreachable (that would turn an outage into an app outage, rule 1).
  Revoking a node is: revoke its mTLS cert, rotate the secrets it held.

## Alternatives considered
- **Agent fetches secrets from OpenBao directly at start.** Clean in theory. Lost on rule
  1: if OpenBao lives with the control plane, it is down at the same moment; and it
  requires nodes to reach OpenBao, a second outbound dependency and credential.
- **No offline restarts for workloads with secrets.** Keeps values off the node. Lost
  because nearly every real workload has a secret, so rule 1 would hold for almost
  nothing.
- **Secrets inline in the Compose file.** Simplest "delete Kiln and it still runs". Lost
  on rule 3 — artifacts are meant to be read, diffed, and committed by users.
- **Short-lived leases that expire offline.** Best security posture. Lost on rule 1 for
  outages longer than the lease; we choose availability and document the trade.

## Consequences
- A compromised node exposes the secrets of workloads assigned to it — the same as every
  orchestrator that runs containers there. Documented in the threat model.
- Deleting Kiln leaves `compose.yaml` + `secrets.env`: `docker compose up` still works.
- Temporal workflow inputs must carry handles only; values never pass through workflow
  history. Activities that need values resolve them inside the activity and do not return
  them.
- The node needs key material at install time; bootstrap flow must create it.
- Rotation means re-sending assignments and re-rendering secret files; providers must
  restart affected workloads. Becomes part of the conformance suite.

## Revisit when
A user's compliance regime forbids secret values persisting on nodes. Then add an opt-in
"no offline restart" mode per environment rather than changing the default.
