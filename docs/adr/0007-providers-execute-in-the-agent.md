# ADR-0007: Providers execute in the agent; workflows assign, agents converge

- **Status:** proposed
- **Date:** 2026-10-02
- **Deciders:** Towheed (pending review)

## Context
The docs never said where a provider runs. `provider-interface.md` has `Status` "called
continuously by the agent", while ADR-0002 makes provider calls Temporal activities in the
control plane. Both cannot be true, and the answer decides three other things:

- Rule 1 (control plane down ≠ apps down) needs something on the node that can call
  `Apply` with no control plane present.
- ADR-0003 says nodes accept no inbound connections, so the control plane cannot call a
  provider on a node directly.
- Targets driven by a remote API (Kubernetes, ECS, Cloud Run) have no "node" to sit on.

## Decision
**Providers execute only inside an agent. The control plane never calls a provider.**

- A Temporal deploy workflow does not "apply". Its activities write an **assignment**
  (target revision for a service on a node or target) to Postgres and push it down the
  agent's outbound stream. A later activity waits, with heartbeats and a timeout, until
  the agent reports that revision observed and healthy.
- The agent runs one reconcile loop: `desired = cached assignment`, `observed =
  provider.Status`, `provider.Apply` until they match. It is the same loop online and
  offline. Online it gets new assignments; offline it keeps the last one.
- For API-driven targets the agent runs wherever it can reach that API (for example as a
  Deployment inside the cluster, or on any host with credentials for ECS). One agent,
  one target binding. "Agent" means "the thing that holds the provider", not "a VM".
- Compensation in the saga is also an assignment: assign the previous revision, wait for
  healthy.

## Alternatives considered
- **Providers as Temporal activities in the control plane.** Simplest saga code. Lost on
  rule 1: with the control plane down nothing can re-apply, so self-healing depends on the
  control plane. It also needs inbound access to nodes, contradicting ADR-0003.
- **Agent runs a Temporal worker and executes activities directly.** Keeps "Apply is an
  activity". Lost on blast radius and tenancy: every node would hold credentials to the
  Temporal cluster and could poll other tenants' task queues unless we run a namespace
  per tenant; and offline self-healing still needs a separate loop anyway, so we would
  have two apply paths.
- **Split by target type (local providers in agent, API providers in control plane).**
  Lost because it creates two execution models every provider author must understand,
  and the control-plane side again fails rule 1 for Kubernetes users.

## Consequences
- One apply path, used by deploys, rollbacks, and self-healing. Resumability (`Apply`
  after a crash) is just "the loop runs again".
- The saga's steps become "assign, then wait for convergence", which is coarser than
  calling `Apply` directly. Step-level progress inside one Apply is reported via status,
  not via workflow history.
- The agent protocol (assignments down, status/logs up) is now the most important wire
  contract after the public API. It goes in `proto/` and gets N-1 compatibility (ADR-0003).
- Logs and Exec are streamed through the agent's outbound connection, relayed by the
  control plane. Exec therefore requires the control plane up — acceptable, Exec is an
  operator action, not a workload dependency.
- An in-cluster agent for Kubernetes means Kiln runs a pod in user clusters. That is a
  known pattern (Argo CD, Flux) but must be documented for security review.

## Revisit when
A provider cannot be expressed as "converge to an assignment" — e.g. a target whose only
API is fire-and-forget with no observable state. One such provider is a carve-out; three
is a reason to reopen.
