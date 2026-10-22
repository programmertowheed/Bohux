# ADR-0045: Swarm provider — what it must decide before it is built

- **Status:** proposed — the owner answered on 2026-10-22 (O37): **a single-manager
  swarm** for v0.3, health from the image's `HEALTHCHECK`, and Swarm Apps
  **image-only** (no registry push credentials on agents). To be rewritten as decided
  before the Swarm provider's code
- **Date:** 2026-10-21
- **Deciders:** Towheed

## Context
v0.3 lists "second provider (Swarm) — proves the boundary is real". The interface
(`internal/provider/provider.go`) and the conformance suite say *what* a provider must
do. Building it in the sandbox showed where Swarm differs from the Docker provider in
ways the suite does not settle, and every one of them is costly to change once nodes run
it:

1. **Health checks.** The Docker provider dials each container's bridge IP from the
   host (ADR-0025). A Swarm task's address is on an overlay network that the host
   cannot route to, and on a multi-node swarm the task may run on another node.
2. **Logs and exec.** `docker service logs` reaches every node's tasks; `docker exec`
   reaches only containers on the node the agent runs on.
3. **Images.** A Docker node builds and keeps its images locally (ADR-0028). A task
   scheduled on another node needs the image from a registry, which Bohux does not
   run.
4. **Which node runs the agent**, and what a "node" is in Bohux's model when one
   environment's workloads spread over several machines (ADR-0007: one agent, one
   target).
5. **The ingress.** Caddy (ADR-0034) joins a Docker network on its own node; for a
   swarm it must join an attachable overlay, and it runs on one node only.

## Decision (proposed)
1. **A Swarm target is one swarm, driven from one agent on a manager node**: the agent
   writes a stack file per workload to disk (rule 2) and runs `docker stack deploy`;
   the environment binds to that agent's node as today.
2. **Health checks run inside the task's network namespace on the manager's node**
   when the task is local (setns from the agent, image-agnostic), and otherwise through
   Docker's own task health, so the image must define a `HEALTHCHECK` for remote tasks.
   The alternative is a probe container attached to each overlay; see below.
3. **Logs** use `docker service logs` (all nodes). **Exec** reaches local tasks only and
   answers `FAILED_PRECONDITION` naming the node for a remote task.
4. **Images**: builds on a Swarm node push to a registry the operator names in the
   provider's target configuration; image Apps must name a registry-pinned digest. No
   registry configured: services are constrained to the manager node.
5. **Ingress**: an attachable overlay `bohux-ingress`; Caddy on the manager node.
6. **Secrets** stay environment variables, inlined into the service spec by
   `docker stack deploy` from the 0600 `secrets.env`, as Compose inlines them into the
   container config. Swarm secrets (files under `/run/secrets`) would change the App
   contract (rule 3 is met either way).

## Questions for the owner
- Is a multi-node swarm in scope for v0.3, or is a single-manager swarm enough to
  "prove the boundary"? A single node proves the interface but not the cases above.
- Health for remote tasks: require an image `HEALTHCHECK`, or run a probe container
  on every overlay (one more moving part per App)?
- May the agent hold registry push credentials (needed for builds on multi-node
  swarms), or are Swarm Apps image-only?

## Alternatives considered
- **Swarm's service API over the Docker socket instead of `docker stack deploy`.** No
  readable artifact on disk; rule 2 lost.
- **A Kubernetes provider first.** The roadmap puts it at v1.0; Swarm is the smaller
  step that exercises the same boundary.

## Consequences
- Easy, once answered: the Docker provider's renderer, options, and tests carry over;
  the conformance suite needs an Inspector whose `Reach` runs on the overlay.
- Hard: a second daemon mode in CI (swarm needs `--live-restore` off), and the
  sandbox's daemon runs with it on: the tests need their own daemon.

## Revisit when
The owner answers the questions above; then this ADR is rewritten as decided.
