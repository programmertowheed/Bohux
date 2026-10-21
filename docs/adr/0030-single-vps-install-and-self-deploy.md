# ADR-0030: Single-VPS install layout, and the control plane as an App of itself

- **Status:** proposed
- **Date:** 2026-10-21
- **Deciders:** Towheed

## Context
v0.1's exit criterion is "Bohux deploys Bohux, and rolls itself back" (ADR-0011,
roadmap). Everything a single VPS runs has been built (control plane, agent, Docker
provider, Temporal workflows, OpenBao, backups), but not how it is installed, or what
"Bohux deploys Bohux" means mechanically:

1. Some parts must exist before Bohux can deploy anything: Postgres, Temporal, OpenBao,
   and the agent (which runs workloads). They cannot be Bohux Apps without a Bohux to
   deploy them.
2. The control plane can be: an App in a `bohux` project whose Releases are new Bohux
   versions. But its deploy workflow runs in the control plane being replaced. Temporal
   makes the run durable, so the new version's worker continues it, and a new version
   that runs but fails its health check compensates normally. **A new version that
   cannot start at all leaves no worker to compensate.** ADR-0031 covers that case.
3. Rule 2: a user who deletes Bohux must keep a running, comprehensible system.

## Decision
1. **Three layers, each with an artifact on disk:**
   - **Base** (installed once, upgraded by hand in v0.1): dockerd with `live-restore`,
     the `bohux` binary, and the agent as a **systemd unit** (`/etc/systemd/system/
     bohux-agent.service`). The agent runs on the host, outside what it manages, so
     no deploy can take it down.
   - **Infra**: Postgres, Temporal, OpenBao (and nothing else) as one Compose project,
     `/etc/bohux/infra/compose.yaml`, started by the installer with `docker compose`.
     Not managed by Bohux deploys in v0.1.
   - **Control plane**: an App, `bohux/system/controlplane`, deployed by Bohux through
     its own agent like any other App. Its Releases are Bohux versions (an image, or a
     build of the Bohux repository).
2. **`scripts/install.sh`** does, in order: check Docker (`live-restore`, buildx),
   write and start the infra Compose project, generate secrets (Postgres password,
   OpenBao token, kept 0600 under `/etc/bohux`), run `bohux admin bootstrap`, start a
   **seed control plane** as a plain Compose project, register the agent, create the
   `bohux` project, `system` environment, and `controlplane` App, deploy it, and when
   that deploy succeeds, remove the seed. From then on the control plane is upgraded
   with `bohux deploy controlplane -p bohux -e system --image …` and rolled back with
   `bohux rollback`.
3. **The control plane App publishes its API and agent ports on the host**, so the
   agent's configured address stays valid across versions (recreate, with the measured
   sub-second-to-second outage, ADR-0020 item 5). Its data directory (node CA) is a host
   path mounted into the container.
4. **Self-rollback** relies on ADR-0031 when the new version never comes up; otherwise
   the ordinary compensation path (ADR-0026) does it.

## Alternatives considered
- **Control plane as a systemd service, upgraded by `bohux upgrade`.** Simple and safe,
  but then Bohux does not deploy Bohux: the exit criterion would be met by a different
  code path than every user's deploy. Lost on the exit criterion's intent: the deploy
  path must be good enough for Bohux itself.
- **Infra as Bohux Apps too.** Uniform, but Postgres and Temporal hold the state a
  deploy needs; a broken Postgres "deploy" cannot be compensated by a workflow that
  needs Postgres. Deferred until there is a data-plane story (v0.3 data rollbacks).
- **The agent in a container with the Docker socket.** No systemd dependency, but a
  container that manages its own runtime is restarted by the very daemon it may need to
  recover, and it hides the agent from the host's service manager. Lost on blast
  radius.
- **Blue/green control plane** (new version on other ports, switch when healthy). No
  outage and no fallback needed, but the agent's address must move, which needs the
  ingress that arrives in v0.2. Revisit then.

## Consequences
- Easy: upgrading Bohux is `bohux deploy`; rolling it back is `bohux rollback`; the
  self-deploy exercises exactly the path users rely on.
- Hard: the installer is a shell script with many steps; it must be idempotent and is
  tested end to end (S9). Infra upgrades stay manual in v0.1 and need a runbook.
- The control plane's API is briefly down during its own upgrade (recreate). Running
  workloads are not affected (rule 1).
- Deleting Bohux leaves: the infra Compose file, the agent unit, and each App's Compose
  artifact — all readable (rule 2).

## Revisit when
Ingress lands (v0.2: blue/green the control plane), or a second node joins (the
control plane may then move off the serving node).
