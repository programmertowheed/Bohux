# ADR-0047: Builds without a Dockerfile — Railpack through the existing BuildKit builder

- **Status:** proposed (builder and trigger chosen by the owner 2026-10-22, O32; the rest
  built on)
- **Date:** 2026-10-22
- **Deciders:** Towheed
- **Extends:** ADR-0011 item 7 (Dockerfile only), ADR-0028

## Context
v0.2 lists "Buildpacks + Nixpacks auto-detection": a Git App without a Dockerfile should
still build. The owner first chose Nixpacks (O32). Its README now says it is in
maintenance mode and names **Railpack** (same team, MIT) as its replacement, so the
owner was asked again and chose Railpack, with "no Dockerfile means auto-detect" plus a
per-App override. What is costly to change once Apps rely on it:

1. **When** a build uses Railpack instead of the Dockerfile.
2. **How the node gets Railpack** and keeps the plan and the build on the same version.
3. **Where Railpack's planning runs**: it reads an untrusted repository and needs the
   network (it asks mise for package versions).
4. **What reaches the build**: tenants share the node's BuildKit, whose mount caches
   are keyed by directory.
5. **How a build that cannot be planned is reported.**

Railpack has two halves: `railpack prepare` reads the source and writes a JSON build
plan; a BuildKit frontend image turns that plan into LLB. Its docs recommend the
frontend for platforms. The frontend image `ghcr.io/railwayapp/railpack-frontend`
contains the whole `railpack` binary (its entrypoint is `/railpack frontend`).
Railpack needs Go 1.26, so it cannot be linked into Bohux (Go 1.23).

## Decision
1. **`GitSource.strategy` decides**, using the enum that already exists:
   - `BUILD_STRATEGY_UNSPECIFIED` (the default) is now **auto**: a `Dockerfile` (a
     regular file) at the build context's root is built as before; without one, Railpack
     builds. Until now it meant "Dockerfile", and an App without one failed to build,
     so no build that worked before changes.
   - `BUILD_STRATEGY_DOCKERFILE` always uses the Dockerfile (a missing one fails as
     before).
   - `BUILD_STRATEGY_RAILPACK` (new value) always uses Railpack, even when a
     Dockerfile exists.

   The build request carries the strategy to the agent, which decides after the fetch
   (only the checkout knows whether a Dockerfile exists) and reports the builder it used
   in `BuildResult.strategy`.
2. **One image, pinned by digest in the agent's code**, does both halves:
   `ghcr.io/railwayapp/railpack-frontend:v0.43.0@sha256:355ef61f…`. `prepare` runs its
   `/railpack` binary and the build names the same reference as `BUILDKIT_SYNTAX`, so
   the plan and the frontend can never be different versions. Upgrading Railpack is a
   reviewed one-line change, not something a node does on its own. Railpack's plans
   name its builder and runtime base images by tag (`mise-<date>`), not digest; that is
   Railpack's choice and is accepted, as a Dockerfile's `FROM` tags are.
3. **`prepare` runs in a throwaway container** from that image: the checkout mounted
   read-only at `/app`, an output directory as the only writable mount, all
   capabilities dropped, `no-new-privileges`, a memory and process limit, the default
   bridge network (it needs the internet to resolve versions), and the agent's proxy
   variables passed in when set. It runs as the agent's own user ID, so the plan it
   writes is the agent's to remove. Nothing from the repository runs on the host.
4. **The build runs on the existing `bohux` builder** (ADR-0028: limits, capped cache,
   `--load`, labels): `docker buildx build --build-arg BUILDKIT_SYNTAX=<image> -f
   <plan> --build-arg cache-key=<app id>`. The cache key prefixes Railpack's mount
   caches with the App ID, so one tenant's build never reads another App's `npm` or
   `apt` cache. No build secrets are passed, as for Dockerfile builds today; a deploy
   key is not offered to Railpack builds (its plans have no SSH mounts).
5. **A repository Railpack cannot plan fails the Release with `BUILD_PLAN_FAILED`**
   (`prepare` exited 1: no provider detected, no start command, a bad
   `railpack.json`). Railpack's error lines from its info file go to the agent's log.
   Its exit 75 (transient, e.g. a download failed) and anything else are
   `BUILD_FAILED`; the deploy workflow's own policy decides what happens next, as for
   any build failure (rule 6: no retry loop in the agent).
6. **CLI and UI**: `bohux app create --git … --build auto|dockerfile|railpack`
   (default auto); `bohux app show` and the UI's App page show the strategy. The UI
   creates image Apps only, so it gains no builder choice (rule 8 allows the UI less
   than the CLI, never more).

## Alternatives considered
- **Nixpacks.** The owner's first choice; in maintenance mode upstream since Railpack
  shipped, so new language versions and fixes stop. Lost (owner, 2026-10-22).
- **Cloud Native Buildpacks** (`pack`). A standard, but builder images near 1 GB per
  node and a second build engine beside BuildKit. Lost on the resource budget
  (ADR-0002) and on reuse of ADR-0028's builder.
- **Download the `railpack` binary from GitHub releases** (checksum pinned) and run
  `prepare` on the host. One more artifact to fetch, verify, and keep in step with
  the frontend image, and the planner would read untrusted repositories outside any
  sandbox. Lost to the one image.
- **Link Railpack's Go packages into the agent.** No container per build, but it
  needs Go 1.26 and pulls Railpack's dependency tree into Bohux. Lost.
- **Opt-in only (`railpack` must be chosen).** Fully explicit, but the roadmap item is
  auto-detection, and auto only changes Apps that could not build. Lost (owner).
- **A Railpack version chosen by the operator** (flag or config). Plans and frontend
  could then drift from what Bohux was tested with. Lost; an air-gapped install
  mirrors the pinned image instead (see Consequences).

## Consequences
- Easy: a Git App with a Node, Python, Go, Ruby, PHP, Java, Rust, Deno, Elixir, .NET,
  or static site and no Dockerfile builds and deploys; the image still carries Bohux's
  labels, so retention (ADR-0020) treats it like any other.
- Hard: the node pulls the frontend image (~30 MB) and BuildKit pulls Railpack's
  builder and runtime images (hundreds of MB) into its capped cache on the first
  Railpack build; the disk-pressure rule still applies. Builds need `ghcr.io` and the
  hosts mise downloads from; an air-gapped install must mirror them on its own
  builder (ADR-0028 already lets an operator pre-create `bohux` with mirrors).
- Users see only `BUILD_PLAN_FAILED`, not Railpack's explanation: Bohux has no build
  log for users yet (a known gap, larger than this ADR).
- A Railpack upgrade changes how existing Apps build on their next Release; it is a
  Bohux release note, never silent.

## Revisit when
Railpack reaches 1.0 or changes its plan format, a user needs build secrets or private
dependencies over SSH in a Railpack build, or build logs reach users (then show
Railpack's messages).
