# ADR-0003: Pull-based agent, autonomous reconciliation

- **Status:** accepted
- **Date:** 2026-10-02

## Context
Managed nodes live behind NAT, in private subnets, on home connections, across clouds.
Separately: the single worst failure mode a platform can have is "the dashboard went down
so production went down".

## Decision
The agent dials out to the control plane over mTLS. No inbound ports on managed nodes.
The agent caches its assigned desired state locally and **continues reconciling it when
the control plane is unreachable**, including restarting failed containers.

## Alternatives considered
- **Push over SSH (Coolify's model).** Agentless is a genuine advantage — nothing to
  install, nothing to upgrade. But it requires reachable nodes with open SSH, and it
  cannot self-heal when the control plane is down, because there is nothing running on the
  node that knows what "correct" means. Rejected on the autonomy requirement.
- **Agent with a persistent control-plane-initiated connection.** Same NAT problem.
- **Full GitOps pull from a repo (Flux model).** Attractive, and we will support it as an
  input mode, but making the repo the only source of truth makes ephemeral preview
  environments and imperative operations (exec, one-off tasks) awkward.

## Consequences
- Node install is one curl plus a bootstrap token. Works behind NAT, no firewall changes.
- There is now an agent to version and upgrade. Needs a staged rollout mechanism and
  N-1 protocol compatibility — treat the agent protocol as a public API.
- Local state cache on the node is a new thing that can be corrupted; needs integrity
  checks and a clean resync path.
- Split-brain is prevented because agents never write desired state, only report status.

## Revisit when
Never — this is a load-bearing guarantee, listed as rule 1 in CLAUDE.md.
