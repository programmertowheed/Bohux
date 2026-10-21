# ADR-0028: Builds run in the agent, on a resource-limited BuildKit managed by buildx

- **Status:** proposed
- **Date:** 2026-10-21
- **Deciders:** Towheed

## Context
ADR-0011 item 7 makes v0.1 build Dockerfiles with BuildKit. ADR-0020 item 2 says the
build exports straight into the node's local image store (no registry), item 3 caps
the build cache (default 10 GB), and item 4 refuses builds under disk pressure. The
architecture says a single-node install builds locally under CPU and memory limits.
Two things are still open: **where the build runs** and **how the image reaches the
Docker daemon that will run it**.

Constraints from accepted ADRs:
- Nodes dial out; nothing dials into a node (ADR-0003).
- Everything that touches a node's runtime runs in the agent (ADR-0007).
- Multi-step, failure-prone work is a Temporal workflow (ADR-0002, rule 6).
- Measured in this repo's cloud container: `docker buildx` with the `docker-container`
  driver runs BuildKit (`moby/buildkit`) in a container with memory and CPU limits,
  honours a `gckeepstorage` cache cap from its config, and `--load`s the result into
  dockerd, printing the image ID.

## Decision
1. **The agent builds.** A build is requested over the agent's existing stream: the
   control plane sends `BuildRequest` (Release ID, repository URL, ref, subpath) and the
   agent answers with `BuildResult` (image ID and resolved commit, or a failure reason).
   Proto first (rule 9).
2. **BuildKit runs as a buildx builder named `bohux`**, `docker-container` driver,
   created by the agent on first use with a memory limit, a CPU quota, and a
   `gckeepstorage` of 10 GB. If a builder named `bohux` already exists, the agent uses
   it as it is: that is how an operator sets proxies, registry mirrors, CAs, or other
   limits without new Bohux settings.
3. **The image goes straight into dockerd** with `--load`, labelled `io.bohux.app` and
   `io.bohux.release` so retention (ADR-0020 item 3) sees it. The Release records the
   image ID as `artifact_digest` and the resolved commit as `source_ref`.
4. **The agent fetches the source with `git`**: `init`, `fetch --depth 1 <ref>`,
   checkout, so a branch, a tag, or a commit SHA all work. v0.1 builds public HTTPS
   repositories only; credentials for private ones need a secret-handle design of
   their own.
5. **A build workflow per Release** (`build/<release id>`) on the deploy task queue:
   an activity sends the request to the node of the App's environment and waits,
   heartbeating, for the result; a node that is offline or reconnects is retried by
   Temporal, and the agent may simply build again (BuildKit's cache makes a repeat
   cheap). The workflow writes the Release `READY` or `FAILED`. A deploy of a
   `BUILDING` Release waits in state `BUILDING` until it is ready (ADR-0026 item 9).
6. **Disk pressure fails closed**: an agent reporting `DISK_PRESSURE` answers every
   `BuildRequest` with that failure, without building.
7. The agent checks `docker buildx version` at registration, as it checks
   `live-restore`, and refuses to register without it.

## Alternatives considered
- **dockerd's built-in BuildKit (`docker build`).** No extra container and it reuses
  dockerd's images, but it has no per-build memory or CPU limits, and its cache cap
  lives in `daemon.json`, which belongs to the operator, not Bohux. Lost on ADR-0011's
  "builds under limits" and ADR-0020 item 3.
- **A standalone `buildkitd` service in the install bundle** (the dev stack runs one).
  Always on, and exporting to dockerd needs `type=docker` output piped into
  `docker load`. buildx does both, starts BuildKit on demand, and needs nothing in the
  bundle. Lost on moving parts.
- **The control plane drives the node's BuildKit over TCP.** Needs an inbound port on
  the node (ADR-0003) and puts runtime work in the control plane (ADR-0007).
- **Build on the control-plane host.** The image would then have to reach the node,
  which needs the registry ADR-0020 item 2 rules out for v0.1.

## Consequences
- Easy: no registry, no new service, limits and cache cap in one place, and operators
  customise the builder with plain `docker buildx create`.
- Harder: the agent needs the buildx plugin (shipped with Docker Engine packages) and
  pulls `moby/buildkit` on first build. Build logs are not streamed to the API in v0.1;
  the agent logs them and the Release records a failure reason.
- The agent protocol gains `BuildRequest` / `BuildResult` (additive, before `proto/`
  freezes).
- Private repositories and build-time secrets are out of v0.1 and need their own design
  (secret handles resolved only inside the build activity, never in history, rule 3).

## Revisit when
A second node joins (builds move to dedicated builder nodes and a registry, per the
architecture), or the BuildKit container's idle memory makes the 2 GB footprint gate
(ADR-0020 item 1) fail.
