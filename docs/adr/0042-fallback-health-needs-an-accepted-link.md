# ADR-0042: A release healthy while the control plane rejects its agent does not cancel its fallback

- **Status:** proposed
- **Date:** 2026-10-21
- **Deciders:** Towheed
- **Refines:** ADR-0031

## Context
ADR-0031 has the agent fall back to the previous workload when a deploy's release
"never turned healthy" by its deadline, because a control plane that cannot start
cannot compensate. Healthy meant the local health check.

Found while upgrading a test install: a new control plane started and passed its
health check, then refused its own node's agent by the version-skew policy (ADR-0020:
a release control plane refuses a non-release agent). The agent marked the release
healthy, so it never fell back. The control plane could not see the release healthy:
the deploy's health gate failed, and its compensation could not reach the node. The
rollback in the runbook needs the same link. Management was cut off with every App
still running (rule 1 held). The only way out was by hand, and the agent undoes a
hand edit on its next pass.

An unreachable control plane is a different case. Rule 1 says its outage must change
nothing on the node, so it must not make the agent revert a healthy release; when it
returns, its own gate and compensation decide.

## Decision
1. **The agent records the control plane's last verdict on it** (`Link`): rejected
   after a version rejection, cleared by any other message. A stream that ends, or a
   control plane that cannot be reached, leaves the verdict as it was.
2. **A healthy pass cancels the fallback only while the agent is not rejected.**
   Otherwise ADR-0031 is unchanged: past the deadline, a release never counted healthy
   falls back.
3. An unreachable control plane never withholds the mark (rule 1): a release healthy
   during an outage stays.
4. Agents without a link (tests, other embedders) keep ADR-0031's behaviour.

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
- Hard: the rule keys on one failure, version rejection. A control plane that accepts
  its agent but cannot manage it for another reason (it starts and then fails every
  call) is not covered; its runbook path (Section B) needs the link it lacks.

## Revisit when
The agent gains another way to learn that the control plane saw a release healthy
(an acknowledgement of the deployment's outcome).
