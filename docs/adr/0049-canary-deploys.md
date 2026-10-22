# ADR-0049: Canary deploys — one weighted step through Caddy, promoted after a bake

- **Status:** proposed (promotion, steps and scope chosen by the owner 2026-10-22; the
  rest built on)
- **Date:** 2026-10-22
- **Deciders:** Towheed
- **Extends:** ADR-0035 (two slots per routed App), ADR-0034 (the agent programs Caddy),
  ADR-0026 (deploy workflow), ADR-0019 (Caddy)

## Context
v0.3 lists "blue/green and canary via Caddy weighted upstreams". Blue/green exists: a
routed App without host ports deploys into its other slot and the agent switches the
ingress at once when the new slot is healthy (ADR-0035). A canary lets the new Release
take part of the traffic first, so a bad Release reaches a few requests, not all of
them. The owner chose: **one step** (a weight, then everything), **promoted by itself
after a bake time** if it stays healthy, with an early promote and an abort, **chosen
per deployment**. Caddy 2.8, the ingress Bohux ships, has a `weighted_round_robin`
selection policy. Costly to change once used:

1. How a canary is asked for, and what the Deployment records.
2. How the weight reaches the node, and how the agent serves two slots at once.
3. Who judges the canary during the bake, and what ends it.
4. How a person promotes early or aborts.

## Decision
1. **`DeploymentStrategy.CANARY` with `canary { weight_percent, bake }`** on
   `CreateDeployment`: weight 1–99, bake from one minute to 24 hours. Both are recorded
   on the Deployment (immutable; migration 00024). Only an App that switches slots (it
   has routes and publishes no host port, ADR-0035) can have one; others are refused
   with `FAILED_PRECONDITION`. Rollbacks and webhook deploys keep the instant switch.
2. **A new state, `CANARY`**: the new Release serves its share and the bake runs.
   `PENDING → APPLYING → HEALTH_GATE → CANARY → SUCCEEDED`, or `COMPENSATING →
   ROLLED_BACK` as for any failed deploy.
3. **The weight travels on the workload** (`Workload.canary_weight`). It is left out of
   the spec hash, like routes: it changes what the ingress does, not what runs. The
   agent brings the new Release up in the other slot as for blue/green; once it is
   running and healthy, instead of cutting over it loads Caddy with both slots'
   endpoints, weighted `100 - w` and `w`, and reports the new Release healthy with
   `ingress_loaded`. If the new slot stops being healthy, the agent serves the live slot
   alone again on its next pass, without waiting for the control plane.
4. **The workflow judges the bake by health**, as the health gate does: an activity
   watches the node's reports until the bake ends and fails when the new Release has
   been unhealthy (not running, not enough ready replicas, a permanent failure, or not
   served) for longer than 30 seconds, so one restart is not a verdict. A failure
   compensates exactly as a failed health gate does: the live Release is assigned again
   and the agent drops the new slot (ADR-0035 item 4). Judging by error rates and
   latency is the SLO gate's job (a later v0.3 item); it will plug in here.
5. **Promotion is assigning the same Release with weight 0**, in the same run: the
   agent cuts over as for blue/green and destroys the old slot; the gate waits for that,
   then the Deployment succeeds. `PromoteDeployment` (deployer) signals the run to end
   the bake early; it is refused unless the Deployment is in `CANARY`. **Abort is
   `CancelDeployment`**, which already compensates. The CLI gains `bohux deploy
   --canary N --bake D` and `bohux deployment promote|cancel <id>`; both actions are
   audited.
6. **Without the control plane the canary holds**: the agent keeps serving the split
   (rule 1) and protects itself only by dropping an unhealthy new slot; the agent's own
   fallback (ADR-0031) is unchanged and is already cancelled once the new Release was
   seen healthy.

## Alternatives considered
- **Several steps (10% → 50% → 100%).** More control, but a list of steps with bakes in
  the API, the record and the workflow. Lost (owner); a later ADR can turn the single
  weight into a list.
- **Manual promotion only.** No automatic step, but an unattended canary would serve a
  split forever. Lost (owner); manual promote remains.
- **A canary policy stored on the App**, applied to every deploy including webhooks.
  Lost (owner) for now; per deployment first.
- **Weights through the provider** (two services behind one alias with skewed replica
  counts). Ties the split to replica counts and to the provider. Lost to the ingress,
  which every routed App already has.
- **Header- or cookie-based canaries** (sticky users). Needs a policy per App and
  request matching. Lost for v0.3; weighted round robin is what the roadmap names.

## Consequences
- Easy: `bohux deploy api -e prod --canary 10 --bake 15m`; a bad Release reaches 10% of
  requests for at most the health gate plus 30 seconds of failure.
- Hard: a third state in which two Releases serve at once; the CLI's follow shows it and
  waits. The proto gains a strategy, a message, a state and an RPC; migration 00024.
- Requests are split per request, not per user: one user can see both Releases.
- Health is the only judge until the SLO gate exists; a Release that is healthy but
  wrong (bad responses with 200) is promoted after the bake.

## Revisit when
The SLO gate lands (judge the bake by it), users ask for several steps or sticky
canaries, or the ingress changes from Caddy.
