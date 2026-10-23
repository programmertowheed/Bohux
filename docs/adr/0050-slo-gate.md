# ADR-0050: SLO gate — judge deploys by their traffic, roll back on a fast error-budget burn

- **Status:** proposed (metrics source, SLO shape and when to judge chosen by the owner
  2026-10-22; the rest built on)
- **Date:** 2026-10-22
- **Deciders:** Towheed
- **Extends:** ADR-0049 (canary), ADR-0034/0035 (ingress, slots), ADR-0026 (deploy
  workflow), 01-architecture.md *Observability*

## Context
v0.3 lists "SLO burn-rate health gate" and "automatic rollback on SLO violation". Today
a Release is judged by health checks only: a Release that answers its health check but
fails real requests is promoted. The architecture says Bohux stores no metrics (a
vision non-goal) and that "the health gate queries real SLO burn rate before
promoting". The owner chose:

- **Metrics from the node's ingress by default**, with an optional query against the
  user's own Prometheus-compatible backend (a later slice).
- **An SLO as a target with a burn-rate threshold**: availability (e.g. 99.9%) and,
  optionally, latency (e.g. 95% of requests under 300 ms).
- **Judged during a canary's bake and for a watch window after any deploy turns
  healthy**, with automatic rollback on violation.

Costly to change once Apps carry SLOs: where the numbers come from and how they reach
the judge without being stored, the shape of the SLO, how a verdict is reached, and the
deploy states.

## Decision
1. **Caddy logs each request's upstream, status and duration**, and nothing else. The
   ingress config adds a `log_append` field with the chosen upstream
   (`{http.reverse_proxy.upstream.hostport}`, which names the slot) and an access log
   whose filter encoder deletes the request (URI, headers, addresses) and response
   headers, so no URL, token or client address is ever written. The log is a file in
   the directory Caddy and the agent already share (beside the admin socket), rolled
   at 10 MB with one old file kept. A file, not a socket: an agent that stops reading
   can never block a request (rule 1).
2. **The agent tails it and keeps counters per upstream in memory**: requests, 5xx
   responses, and a latency histogram with fixed buckets (5, 10, 25, 50, 100, 250,
   500, 1000, 2500, 5000, 10000 ms, and above). Every status report carries, for each
   routed workload, the counters of the slot that runs its reported Release
   (`WorkloadStatus.traffic`), cumulative since the agent started. Nothing is stored
   anywhere: the report replaces the previous one, as today.
3. **An SLO is part of the App** (`App.slo`, changed with `UpdateApp` like routes, by a
   deployer): `availability_target` (0.9 to 0.99999), optional `latency {
   threshold_ms, target }` with the threshold one of the histogram's bucket bounds,
   `max_burn_rate` (default 10), and `watch` (1 minute to 1 hour, default 10 minutes).
   No SLO: deploys behave exactly as today.
4. **The verdict is the burn rate over the window so far.** The judge takes the
   Release's counters when the window starts and reads them again on every poll; the
   difference is the window's traffic (a counter that goes backwards means the agent
   restarted, and the window starts again from there). Burn rate = the share of bad
   requests ÷ the share the target allows (5xx for availability; requests above the
   threshold for latency). The SLO is violated when either burn rate exceeds
   `max_burn_rate` with at least 50 requests in the window; with fewer, there is no
   verdict and the deploy proceeds on health alone (a canary at 10% of a quiet App must
   not fail for lack of traffic).
5. **When it is judged**: during a canary's bake (ADR-0049), by the same activity that
   watches health; and for a non-canary deploy, in a new `VERIFYING` state that holds
   the run open for `watch` after the Release is healthy. A violation compensates as a
   failed health gate does (the last healthy Release is assigned again), with reason
   `SLO_AVAILABILITY` or `SLO_LATENCY`. A canary's bake replaces the watch: a promoted
   canary is not watched again. While a deploy verifies, a new deploy of the App is
   refused as one in progress; cancelling it rolls back.
6. **Only routed Apps produce ingress numbers.** An App with an SLO but no routes is
   judged on health alone until the PromQL source exists; the API accepts the SLO and
   `bohux app slo` says so when it sets one on such an App (a Deployment has no log of
   its own to say it in).

## Alternatives considered
- **Caddy's Prometheus metrics** (`/metrics` on the admin API). Per server and per
  host, not per upstream: the two slots of a canary share a host. Lost.
- **A second Caddy server per slot** (the public route proxies to a per-slot listener,
  whose metrics are labelled by server). Exact metrics, but a hop on every routed
  request and a config twice the size. Lost to the access log.
- **Caddy writing the log to a socket the agent reads.** No file, but an agent that
  stops reading would fill the socket and stall Caddy. Lost on rule 1.
- **The user's metrics backend only** (PromQL), with Bohux tagging workloads with
  `OTEL_RESOURCE_ATTRIBUTES`. The architecture's first idea, but nothing works without
  a backend and instrumentation. Kept as the optional second source (owner).
- **Plain thresholds** (max 5xx share, max p95). Easier to read, but not an SLO; the
  burn rate expresses the same check against the target users already state. Lost
  (owner).
- **Multi-window, multi-burn-rate alerting** (SRE book). Built for continuous alerting
  over hours; a deploy's window is minutes. One cumulative window with a minimum
  request count. Revisit if verdicts flap.

## Consequences
- Easy: `bohux app slo api --availability 99.9 --latency 300ms@95 --watch 10m`; a Release
  that turns every tenth request into a 500 is rolled back within minutes of taking
  traffic, canary or not.
- Hard: a third reason the deploy run stays open (verifying), and the ingress config
  grows a logging section; the agent tails a file. Proto: `App.slo`,
  `WorkloadStatus.traffic`, state `VERIFYING`; migration 00025.
- Requests are counted at the ingress: errors behind it that still return 2xx, and
  traffic that bypasses it (host ports), are invisible until the PromQL source.
- The fixed buckets limit latency thresholds to their bounds.

## Revisit when
The PromQL source lands (ADR to come), verdicts flap on real traffic, or Caddy gains
per-upstream metrics.
