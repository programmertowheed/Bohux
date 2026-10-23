# ADR-0035: Zero-downtime deploys — two slots per routed App, switched by the agent

- **Status:** proposed
- **Date:** 2026-10-21
- **Deciders:** Towheed

## Context
v0.1 deploys stop the old containers before the new ones start (ADR-0020 item 5), and
the CLI prints the downtime. With the node's ingress (ADR-0034) an App served on a
route does not need a host port, so the old and the new Release can run side by side
and the ingress can switch between them. What must be decided, and is costly to change
once nodes run it:

1. **Who orchestrates the switch.** The control plane's deploy workflow (ADR-0026) or
   the agent. Rule 1: a switch half done when the control plane goes away must still
   end in a serving state.
2. **How the provider runs two Releases of one App** without a new verb (rule 4) and
   without the provider knowing about the ingress.
3. **What happens to a Release that never turns healthy.**

## Decision
1. **The agent switches, per workload, in its reconcile loop.** A workload qualifies
   when it has routes and publishes no host port (a host port cannot be held twice).
   Others keep the v0.1 in-place apply.
2. **Two slots, `blue` and `green`, as a field of the provider's `ResourceRef`**
   (`Slot`). A slot is an independent workload to the provider: its own artifact
   directory, project, containers (Docker: project name suffixed `-blue`/`-green`). An
   empty slot is the v0.1 name, so existing workloads keep theirs; the first switch
   moves an App from the empty slot into one.
3. **The switch** when a qualifying workload's spec hash differs from the live slot's:
   apply it to the other slot; while it is not running and healthy, the ingress keeps
   the live slot's endpoints and the report shows the live Release; once it is, the
   agent loads the ingress with the new slot's endpoints, records the new slot as live
   (`slots.json`, written before the old one goes, so a crash in between re-destroys
   rather than serving a destroyed slot), then destroys the old slot. One load is the
   cut-over: Caddy switches atomically.
4. **A Release that never turns healthy never takes traffic.** The deploy's health gate
   times out, the workflow compensates by assigning the live Release again (ADR-0026),
   the agent sees the live slot already matches and destroys the pending slot. The
   agent's own fallback (ADR-0031) does the same without the control plane.
5. **Status during a switch** reports the pending slot's Release as `APPLYING` (or its
   failure) until the cut-over, so the gate waits; `ingress_loaded` is true only after
   the cut-over.
6. **Any change to what runs switches**, replicas and rotated secrets included: the
   switch compares spec hashes and nothing else, and it costs only time. A change to
   the routes alone is not one (ADR-0034): it reloads the ingress in place. A crashed
   live slot restarts in place: the agent records the live slot's hash, since a
   provider reports none for a workload that is not running.

## Alternatives considered
- **The workflow orchestrates** (assign the new slot, wait, switch, remove the old).
  Every step is a round trip through the stream, and a control-plane outage mid-switch
  leaves two slots and a decision nobody makes until it returns. Lost on rule 1.
- **The provider does it inside `Apply`** (start new containers next to the old,
  remove the old). The provider would have to know when the ingress has switched,
  which ADR-0034 keeps out of providers. Lost.
- **Compose scale-up then scale-down within one project.** Compose recreates or keeps
  containers by config hash; it cannot hold two images for one service. Lost.
- **Health checks at the proxy (Caddy active health checks) instead of a cut-over.**
  Caddy would send traffic to whichever upstream answers, mixing Releases for the
  length of the deploy. Lost: a cut-over is one atomic step, and v0.3 canary will add
  deliberate mixing with weights.

## Consequences
- Easy: Apps on routes deploy without downtime, and a broken Release never serves; a
  control-plane outage mid-switch ends in a consistent state on the agent alone.
- Hard: during a switch the node runs twice the App's containers (memory on the 2 GB
  box: a deploy needs room for both); `ResourceRef` gains a field every provider must
  keep in its names; the agent keeps per-App slot state.
- The CLI's downtime note applies only to Apps with host ports.

## Revisit when
Canary arrives (v0.3: weighted upstreams across the two slots), or a node cannot fit
two copies of an App during a deploy.
