# ADR-0042: A release cancels its fallback only when healthy while the control plane accepts the agent

- **Status:** proposed
- **Date:** 2026-10-21
- **Deciders:** Towheed
- **Refines:** ADR-0031

## Context
ADR-0031 has the agent fall back to the previous workload when a deploy's release
"never turned healthy" by its deadline, because a control plane that cannot start
cannot compensate. Healthy meant the local health check.

Found while upgrading a test install: a new control plane started and passed its
health check, then refused its own node's agent by the version-skew policy (ADR-0021:
a release control plane refuses a non-release agent). The agent marked the release
healthy, so it never fell back. The control plane could not see the release healthy:
the deploy's health gate failed, and its compensation could not reach the node. The
rollback in the runbook needs the same link. Management was cut off with every App
still running (rule 1 held). The only way out was by hand, and the agent undoes a
hand edit on its next pass.

The same holds for any App deployed while the agent cannot report: the control plane
fails the gate and compensates later, while the agent has already settled on the new
release.

## Decision
1. **The agent counts a release as healthy, cancelling its fallback, only on a pass
   where the workload is healthy *and* the control plane accepts the agent** (a stream
   whose first message was not a version rejection). The agent tracks this as `Link`:
   up after an accepted message, down when the stream ends or is rejected.
2. Otherwise ADR-0031 is unchanged: past the deadline, a release not counted healthy
   falls back. Normally the link is up within seconds of a restart, long before the
   deadline (health timeout + 2 minutes).
3. Agents without a link (tests, other embedders) keep ADR-0031's behaviour.

## Alternatives considered
- **Exempt the control plane's own node from the version check.** It fixes this one
  case and weakens the skew policy; a control plane that refuses its agent for any
  other reason (protocol, a bug) still cuts management off. Lost.
- **Have the control plane accept the agent that runs it.** It needs the control plane
  to know which node runs it before it can talk to that node. Lost on complexity.
- **Leave it to the runbook.** No runbook step works while the agent re-applies the
  refused version. Lost.

## Consequences
- Easy: a control plane upgrade that cannot manage its node reverts by itself, like
  one that cannot start.
- Hard: a release that is healthy but whose node cannot reach the control plane for the
  whole window also falls back. That matches what the control plane does: its gate
  fails and it compensates once the link returns.

## Revisit when
The agent gains another way to learn that the control plane saw a release healthy
(an acknowledgement of the deployment's outcome).
