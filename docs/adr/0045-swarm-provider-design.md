# ADR-0045: Swarm provider — one single-manager swarm per node, driven by stack files

- **Status:** proposed (rewritten as decided 2026-10-22 after the owner's answers, O37:
  a single-manager swarm, image-only in the sense of no registry push, and live-restore
  waived on a swarm manager; the rest built on)
- **Date:** 2026-10-21, rewritten 2026-10-22
- **Deciders:** Towheed

## Context
v0.3 lists "second provider (Swarm) — proves the boundary is real". The interface
(`internal/provider/provider.go`) and the conformance suite say *what* a provider must
do. Swarm differs from the Docker provider (ADR-0025) in ways that are costly to change
once nodes run it:

1. **Which swarm, and which node runs the agent** (ADR-0007: one agent, one target).
2. **Live-restore.** The agent refuses a node whose dockerd has live-restore off
   (ADR-0020), so a dockerd restart never stops workloads. Swarm mode refuses to run
   with live-restore on.
3. **Health checks.** The Docker provider dials each container from the host
   (ADR-0025). Swarm tasks sit on overlay networks the host cannot route to.
4. **The ingress.** Caddy (ADR-0034) shares the `bohux-ingress` bridge network with
   the workloads it serves. Swarm services can join only swarm-scoped networks.
5. **Images.** A node builds into its local image store (ADR-0028); a swarm that
   spans machines would need a registry.

The owner answered on 2026-10-22: a **single-manager** swarm is enough to prove the
boundary; Swarm Apps need **no registry push credentials** on agents; and the
agent's own daemon is the swarm manager, with **live-restore waived** there.

## Decision
1. **A Swarm target is the node's own dockerd, a swarm of exactly one manager.** The
   provider is registered as `swarm`; `Describe` fails unless the daemon is an active
   swarm manager with one node, so a task can never land on a machine the agent does
   not run on. Builds stay local (ADR-0028); services run local images with
   `--resolve-image never`, so nothing is pushed anywhere.
2. **One stack per workload**, named and laid out like the Docker provider's project
   (`bohux-<app>-<env>-<hash>[-<slot>]`), with a readable `stack.yaml` and a 0600
   `secrets.env` in its directory (rule 2), deployed with `docker stack deploy
   --resolve-image never --prune`. The file runs without Bohux the same way. Secrets
   stay environment variables, as Compose inlines them for the Docker provider
   (rule 3 is met either way; Swarm secrets as files would change the App contract).
3. **Live-restore is waived on a swarm manager.** Registration accepts a node whose
   dockerd is a swarm manager in place of live-restore, and says what that costs: a
   dockerd restart restarts every container on the node, Swarm's tasks and Bohux's own
   services alike, and Swarm starts its tasks again. The control plane being down
   still never stops a workload (rule 1); dockerd going down does, briefly.
4. **Health checks run inside each task's network namespace.** The agent (root on the
   node, as it already is) enters the container's network namespace on a locked OS
   thread, dials `127.0.0.1:<port>`, and makes the App's HTTP check. It needs no route
   to the overlay, no tool in the image, and no `HEALTHCHECK`; an image that defines one
   still counts, since Swarm restarts tasks that fail it.
5. **Endpoints are DNS round-robin.** Services use `endpoint_mode: dnsrr`, so the
   ingress alias resolves to every task, exactly as the Docker provider's shared alias
   does; no virtual IP and no IPVS. Host ports are therefore published in `host` mode
   (Swarm refuses ingress-mode ports with `dnsrr`), like the Docker provider's.
6. **On a swarm manager, `bohux-ingress` is an attachable overlay.** Swarm services
   and standalone containers (Caddy, Compose workloads of the Docker provider) can all
   join it, so one ingress serves both providers. The provider creates it when it is
   missing; when a `bohux-ingress` bridge already exists (a node that became a manager
   after install), applying a routed Swarm App fails and names the fix (recreate the
   network as an attachable overlay).
7. **Instances are tasks** (`<stack>_app.<slot>`): logs come from `docker service
   logs`, exec goes to the task's container with `docker exec` (all tasks are local).
8. **`Spec.Raw` accepts `networks` (swarm-scoped, external) and `volumes` (named,
   external)**, as ADR-0032 does for Docker; `publish_address` is refused, since
   Swarm's port syntax has no host address.
9. **A private image is pulled before the deploy** with the throwaway credential of
   ADR-0041, so the credential never enters the swarm's state
   (`--with-registry-auth` would store it in the service spec).

## Alternatives considered
- **A multi-node swarm.** What Swarm is for, but it needs a registry, remote health
  checks and remote exec. Lost for v0.3 (owner); revisit with a registry.
- **A separate swarm daemon reached over `DOCKER_HOST`**, keeping live-restore on the
  agent's own daemon. Caddy could not share a network with its services, so routed
  Apps would need host ports; more moving parts. Lost (owner).
- **Health through the image's `HEALTHCHECK` only.** No namespace entry, but every
  image would need a check and a tool to run it, and `Runtime.health` would mean
  nothing on Swarm. Kept as an addition, lost as the mechanism.
- **Health from the host via `docker_gwbridge`.** Works where Swarm attaches tasks to
  it, which depends on the daemon's network setup; the namespace probe does not. Lost.
- **Service virtual IPs.** One address per service, but they need IPVS and lose the
  Docker provider's resolve-every-replica model. Lost to `dnsrr`.
- **Swarm's service API over the Docker socket instead of `docker stack deploy`.** No
  readable artifact on disk; rule 2 lost.
- **A Kubernetes provider first.** The roadmap puts it at v1.0; Swarm is the smaller
  step that exercises the same boundary.

## Consequences
- Easy: an Environment bound to `swarm` on a manager node deploys, scales, rolls back
  and routes like one bound to `docker`; the conformance suite is the same.
- Hard: a node that runs Swarm runs without live-restore; the health probe needs the
  agent as root on Linux (it already is). CI needs a second dockerd in swarm mode,
  since the job's main daemon runs with live-restore.
- `docker stack rm` removes a stack's networks asynchronously; `Destroy` waits for
  them, or the next deploy of the same stack races the removal.

## Revisit when
A user needs Swarm across machines (then: a registry, remote health, remote exec), or
the agent stops running as root.
