# ADR-0031: Agent-side fallback when the control plane cannot compensate

- **Status:** proposed
- **Date:** 2026-10-21
- **Deciders:** Towheed

## Context
Compensation is the deploy workflow's job (ADR-0026): when a new Release does not turn
healthy, the workflow assigns the last healthy one. That needs a running worker, which
lives in the control plane. Two cases break it:

1. **The control plane deploying itself** (ADR-0030): if the new version cannot start,
   nothing runs the workflow, and the node keeps restarting the broken version.
2. **The control plane is down** during any deploy's health gate (crash, host trouble):
   the node keeps running a Release that never turned healthy until someone fixes the
   control plane.

Rule 1 already says the agent keeps reconciling its last known good state without the
control plane. Today, "last known good" is "the latest assignment", even when that
assignment is what is broken.

## Decision
1. **A workload may carry a fallback**: `Workload.fallback` (the previous workload of
   the same App, as it ran) and `Workload.fallback_after` (a deadline). The deploy
   workflow sets both whenever it assigns a new Release over a running one: the
   fallback is the workload being replaced, the deadline is the health timeout plus a
   margin (default 2 minutes) during which the control plane is expected to compensate
   itself. Proto first.
2. **The agent falls back only when all hold**: the deadline has passed; the workload has
   not been healthy since it was applied; and no newer generation has arrived. It then
   applies the fallback, records that in its state directory (so a restart keeps it),
   and reports the workload with the fallback's Release and `FALLBACK_APPLIED`.
3. **A newer assignment always wins.** The control plane's compensation (assign the last
   healthy Release) normally arrives before the deadline and makes the fallback moot; if
   it arrives after, it assigns the same Release the agent already fell back to, which
   applies without churn (same spec hash).
4. Once a workload has been healthy, its fallback is dropped: a later failure is a
   runtime problem, not a bad deploy, and is not the agent's to second-guess.

## Alternatives considered
- **Agent-local rollback without a deadline** (fall back as soon as the new version is
  unhealthy). Faster, but it races the control plane's own health gate and turns a
  slow start into a rollback. Lost on predictability.
- **Keep the control plane's own previous version running beside the new one.** Needs
  two copies of a stateful service sharing ports; that is blue/green, which needs v0.2
  ingress.
- **A watchdog outside Bohux (systemd `OnFailure=`) that restores the previous control
  plane image.** Works only for the control plane, duplicates the deploy model in shell,
  and would disagree with Bohux's revision history. Lost on "rollback is a deploy"
  (ADR-0010).
- **Do nothing; document a manual recovery.** Lost on the exit criterion ("rolls itself
  back").

## Consequences
- Easy: a node recovers from a bad deploy even with no control plane, for every App,
  not only Bohux.
- Hard: there are now two places that can roll back. They agree by construction (both
  go to the workload that ran before), and the control plane's history records the
  outcome when it returns; the agent's fallback is visible in its report. The
  conformance of this path needs its own tests (rule 5).
- The assignment grows by one previous workload per App in a deploy window.

## Revisit when
The agent's fallback fires in a deploy where the control plane was healthy (a sign the
margin is too short), or users ask for agent-side health policy beyond deploys.
