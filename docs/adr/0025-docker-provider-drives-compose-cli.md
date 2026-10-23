# ADR-0025: The Docker provider drives the Compose CLI against its own artifact

- **Status:** proposed (owner authorized building on it before acceptance, 2026-10-21)
- **Date:** 2026-10-21
- **Deciders:** Towheed (pending)

## Context
S5 builds the first provider. ADR-0008 and rule 2 require it to write a readable
`compose.yaml` and a 0600 `secrets.env` per workload, such that a user who deletes
Bohux can still run `docker compose up`. The provider must implement all eight methods
(rule 4), pass the conformance suite, record the desired-state hash so `Status` can
report it back (provider-interface.md, *Types*), keep the images rollback needs
(ADR-0020 item 3), rotate container logs (item 3), and accept that v0.1 host-port
deploys have downtime (item 5).

Two questions decide most of the code:

1. **How the provider talks to Docker.** Shell out to the `docker` / `docker compose`
   CLI, or use the Engine API through a Go client.
2. **How health is checked.** `Runtime.health` is an HTTP path and port. A Compose
   `healthcheck` runs a command *inside* the container, which needs `curl` or `wget`
   in the image; many minimal images have neither.

Constraints: Go 1.23; the agent runs as root or in the docker group on the node; the
Docker daemon in the cloud container is available for tests (S5a check); no new
dependency without an ADR.

## Decision
**The provider writes the artifact, then runs the Compose CLI on it.** `Apply` renders
`compose.yaml` and `secrets.env` into the workload's directory, then runs
`docker compose -p <project> -f compose.yaml up -d --remove-orphans`. `Destroy` runs
`down --volumes` and removes the directory. `Status` reads
`docker compose ps --format json` and container labels. `Logs` and `Exec` use
`docker compose logs` and `docker compose exec`; `Scale` re-renders `replicas` and
runs `up -d` (it must not drift from the artifact). No Go Docker client is added.
Minimum versions: Docker Engine 25, Compose v2.24 (checked by `Describe`).

**Layout and names.** Root `<agent state dir>/workloads/`, one directory per workload,
named `<project>`, where the Compose project name is
`bohux-<app slug>-<environment slug>-<first 8 hex of the app ID>` (readable, unique on
the node). `Workload` gains `app_slug` and `environment_slug` (additive proto change)
so names are readable. Every container carries labels `io.bohux.app`,
`io.bohux.environment`, `io.bohux.release`, and `io.bohux.spec-hash`.

**Secrets.** `secrets.env` (0600) is referenced with `env_file`; values never appear in
`compose.yaml`, in command lines, or in errors (rule 3).

**Health is checked by the agent, from the host.** The provider resolves each
container's address on the Compose network and sends `GET <path>` to the health port;
2xx/3xx within the timeout is healthy. No `HEALTHCHECK` is written into the artifact.

**ADR-0020 duties.** Logging uses the `json-file` driver with `max-size: 10m`,
`max-file: "3"`, written into the artifact. Image retention (current plus the last 5
Releases per App) is a separate agent task over the images the provider labeled, not
part of `Apply`. Deploys are stop-old/start-new (Compose's default recreate), which is
the v0.1 downtime ADR-0020 accepts.

## Alternatives considered
- **Engine API through `github.com/docker/docker/client`.** Typed, no process spawn,
  structured errors. Lost because Bohux would then implement Compose's semantics
  itself (networks, recreate rules, scaling, `env_file`), so the artifact on disk and
  what actually runs could differ, breaking rule 2's promise that the file is the
  system. It is also a very large dependency tree for v0.1.
- **`compose-go` to load the file plus the Engine API.** Keeps the file authoritative
  in parsing but still reimplements `up`. Lost on the same divergence risk plus two
  large dependencies.
- **Plain `docker run` per replica, no Compose.** Fewest moving parts. Lost on rule 2:
  a user deleting Bohux would get no readable file to bring the workload back.
- **Compose `healthcheck` with `curl`.** Native and shown by `docker ps`. Lost because
  it silently fails on images without `curl`/`wget`, which would mark healthy apps
  unhealthy and block deploys.
- **Health from inside the agent via published host ports.** Simpler addressing. Lost
  because apps without published ports (internal services) could not be checked.

## Consequences
- The node needs the Docker CLI and Compose plugin, not only the daemon; `Describe`
  fails clearly when they are missing or too old.
- CLI output formats become a compatibility surface; `--format json` and labels keep
  parsing narrow, and conformance runs against a pinned Docker in CI.
- Spawning a process per verb costs milliseconds; irrelevant at reconcile intervals.
- Health checks need the agent to reach the container network, true for a host agent
  on Linux; a future containerized agent must join the network.
- One additive proto change (`Workload.app_slug`, `Workload.environment_slug`).

## Revisit when
A conformance failure or a user report traces to Compose CLI behavior Bohux cannot
control (an output format change, a recreate rule), twice; or the agent must run where
spawning the CLI is impossible.
