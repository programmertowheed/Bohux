# ADR-0040: Resource view — current usage sampled by agents, carried in status reports

- **Status:** proposed
- **Date:** 2026-10-21
- **Deciders:** Towheed

## Context
v0.2 promises a "basic server and app resource view (CPU/memory/disk) from node agents —
current values only; history goes to the user's OTLP backend (non-goal: metrics
storage)". Choices that shape the agent protocol and the provider boundary:

1. **Who measures**: the provider (it knows its containers) or the agent (it knows the
   host).
2. **How the numbers travel** to the API without a second channel (ADR-0003, ADR-0009).
3. **How often**, given `docker stats` takes about two seconds to sample CPU, and the
   reconcile pass must stay fast (rule 1: it is what keeps Apps running).

## Decision
1. **Node usage is the agent's**: CPU busy share from `/proc/stat` deltas, memory from
   `/proc/meminfo` (total, available), disk from `statfs` of the state directory's and
   the provider's data root's filesystems (the same paths as the disk-pressure
   monitor, ADR-0020 item 4).
2. **Workload usage is the provider's**, through an optional interface
   `provider.UsageReader` (`Usage(ctx) → per ResourceRef: CPU millicores, memory bytes,
   memory limit`), like `ImageStore`. A provider without it reports no workload usage;
   nothing else depends on it. Docker reads one `docker stats --no-stream` for all its
   containers and sums replicas per workload (and per slot, ADR-0035, under the App).
3. **Sampled off the reconcile loop**: a sampler goroutine refreshes every 30 s; each
   status report carries the latest sample. A slow or failing sample never delays a
   reconcile pass; a sample older than 2 minutes is not reported.
4. **Carried in `StatusReport`** (agent.proto: `NodeUsage usage`, and `Usage usage` per
   `WorkloadStatus`), stored with the report the control plane already keeps per node
   (migration 00015), and exposed read-only: `Node.usage` and a new
   `WorkloadService.GetAppUsage(app_id)` (viewer). No table, no history: the API shows
   the latest sample and its time.
5. **Shown** by `bohux node list` and `bohux app show`, and in the UI.

## Alternatives considered
- **cAdvisor or node-exporter on each node.** Complete metrics, but another process on
  the 2 GB box and a second agent to install. Lost on footprint (ADR-0020 item 1).
- **Sample inside the reconcile pass.** Simple, but adds `docker stats`' two seconds to
  every pass. Lost on rule 1.
- **A metrics table in Postgres.** History, but the vision rules out metrics storage:
  the user's OTLP backend keeps history. Lost.

## Consequences
- Easy: numbers appear where people already look, with no new process or table.
- Hard: agent.proto and the provider package grow (optional interface); usage is up to
  30 s old and gone when a node is offline (shown as such).

## Revisit when
Users ask for history in Bohux itself (then: OTLP export from the agent, not storage),
or a provider cannot report usage cheaply.
