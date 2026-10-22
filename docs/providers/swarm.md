# Swarm provider

Runs workloads as Docker Swarm services on a swarm of **one manager, the agent's own
node**, driving `docker stack deploy` against a stack file it writes for each workload
(ADR-0045). Source: `providers/swarm/`. It passes the same conformance suite as the
Docker provider.

## Prerequisites

- Docker Engine 25 or later, in swarm mode, as the swarm's only node
  (`docker swarm init`). `Describe` refuses a daemon that is not a manager, or a swarm
  with more than one node, so a task can never land on a machine the agent does not run
  on.
- **Live-restore is off.** Swarm mode refuses it. `bohux agent register` and the
  installer accept a swarm manager without it and warn: a dockerd restart then
  restarts every container on the node, Bohux's own services included, and Swarm starts
  its tasks again. The control plane being down still never stops a workload (rule 1).
- The agent runs as root on Linux: health checks enter each task's network namespace.
- `bohux-ingress` is an **attachable overlay** network (the installer creates it so on a
  swarm manager). A node that became a manager after install still has the bridge of
  that name; recreate it (`docker network rm bohux-ingress && docker network create
  --driver overlay --attachable bohux-ingress`, then restart the ingress container) or
  routed Swarm Apps fail to apply with that instruction.

## Choosing it

The agent on a swarm manager runs this provider **beside** the Docker provider, against
the same daemon: Bohux's own control plane and every Environment bound to `docker` keep
running on Compose, and an Environment bound to `swarm` on that node runs on Swarm.

```sh
bohux env create prod -p shop --node <node slug> --provider swarm
```

A workload naming a provider its node does not run reports `PROVIDER_MISSING`.

## What it writes

One directory per workload under `<agent state dir>/workloads/swarm/`, named after the
stack: `bohux-<app slug>-<environment slug>-<8 hex>[-<slot>]` (slugs cut to 18
characters, so the service name `<stack>_app` stays within Swarm's 63).

- `stack.yaml` — the whole workload, readable, with a header saying how to run it
  without Bohux: `docker stack deploy -c stack.yaml --resolve-image never <stack>`.
- `secrets.env` — mode 0600, the secret values as environment variables. Like Compose,
  `docker stack deploy` copies them into the service definition (visible to whoever can
  run `docker service inspect`); Swarm secrets as files would change the App contract.
- `.bohux-state.json` — replicas, health check, and ingress ports.

The service runs with `endpoint_mode: dnsrr` and `restart_policy: any`, its containers
labeled `io.bohux.app`, `io.bohux.environment`, `io.bohux.release` and
`io.bohux.spec-hash`, with bounded `json-file` logs. Images are local (built on the
node, ADR-0028, or pulled): `--resolve-image never` keeps Swarm from asking a registry,
and nothing is ever pushed.

## Declared capabilities

Every verb.

| Verb | How |
|---|---|
| Plan | Compares the rendered files and the running tasks; reads only |
| Apply | Writes the files, pulls a private image first (see below), `docker stack deploy --prune`; the same spec not running gets `docker service update --force` |
| Status | Running tasks (`docker ps` by service label), their spec-hash labels, and the health check of each |
| Logs | `docker service logs`; instances are `<stack>_app.<n>`; stderr is marked |
| Exec | `docker exec` into the task's container (all tasks are on this node) |
| Scale | Rewrites `deploy.replicas` in `stack.yaml` and deploys again |
| Destroy | `docker stack rm`, then waits until the stack's tasks and networks are gone |

**Health checks** run inside each task's network namespace: the agent enters it on a
locked thread and sends the App's HTTP check to `127.0.0.1:<port>`. No route to the
overlay, no tool in the image and no `HEALTHCHECK` are needed; an image that has a
`HEALTHCHECK` still gets Swarm's own restarts.

**Ingress endpoints** are `<stack>:<port>`: the service joins `bohux-ingress` with the
stack name as an alias, and Swarm's DNS answers with every task, as Docker's does for
the Docker provider. Compose containers (the ingress, Docker-provider workloads) on the
same overlay reach them.

**Private images** (ADR-0041) are pulled with the throwaway credential before the
deploy, so the credential never enters the swarm's state (`--with-registry-auth` would
store it in the service spec).

## Known gaps

- **One node only.** A multi-node swarm needs a registry, remote health checks and
  remote exec (ADR-0045, revisit).
- **Host ports use `host` mode** (Swarm refuses the routing mesh with `dnsrr`), so, as
  with Docker, replicas above 1 cannot publish a host port, and a deploy of an App with
  a host port has the same brief downtime.
- **The resource view (ADR-0040) does not sample Swarm workloads yet**; the Docker
  provider's sampler finds containers by Compose project.
- Secret values cannot contain newlines (env-file format).

## `Spec.Raw`

As for Docker (ADR-0032), owner only, JSON:

```json
{
  "networks": ["shared-overlay"],
  "volumes": [{"source": "app-data", "target": "/var/lib/app"}]
}
```

- `networks`: existing **swarm-scoped** networks the service joins, besides its own.
- `volumes`: existing named volumes, mounted at `target` (a clean absolute path); host
  paths are refused.
- `publish_address` is refused: Swarm's port syntax has no host address.

## Testing

```sh
docker swarm init --advertise-addr 127.0.0.1   # on a daemon without live-restore
go test ./providers/_conformance -provider=swarm -target=<DOCKER_HOST, empty for local> -v
```

The daemon needs `busybox:1.36`. CI runs this in the `conformance-swarm` job.
