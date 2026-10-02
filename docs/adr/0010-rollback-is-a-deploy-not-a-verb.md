# ADR-0010: Rollback is a deploy of a prior release, not a provider verb

- **Status:** proposed
- **Date:** 2026-10-02
- **Deciders:** Towheed (pending review)

## Context
Two accepted ADRs disagree. ADR-0004 says rollback is "apply revision N-1, not a special
code path". ADR-0005 makes `Rollback` one of the provider verbs, which is a special code
path in every provider. With ADR-0007 (providers converge to an assignment), "go back" is
just "assign the previous release". A separate verb means each provider can roll back
differently from how it rolls forward, and the backward path is the one that gets less
testing — the opposite of CLAUDE.md rule 5.

## Decision
Remove `Rollback` from the provider interface. The provider interface is `Describe` plus
seven verbs: `Plan`, `Apply`, `Status`, `Logs`, `Exec`, `Scale`, `Destroy`.

User-facing rollback stays (API `DeploymentService.Rollback`, `kiln rollback`). It
starts a normal deploy workflow whose target is an existing, immutable Release. The
resulting Deployment records `rollback_of` for audit. Saga compensation uses the same
mechanism: assign the last healthy release.

Rule 5 stays and becomes concrete: the conformance case "apply release B over A, then
apply A again → A is live, nothing orphaned" must pass before a provider merges, and the
deploy workflow's compensation path has a test before its forward path merges.

## Alternatives considered
- **Keep the `Rollback` verb.** Lets a target use a native rollback (e.g. `kubectl
  rollout undo`). Lost because native rollback reverts to the target's idea of "previous",
  which can disagree with Kiln's revision history after a manual change — and Kiln's
  history is the audit truth (ADR-0004).
- **Keep the verb as optional with a default implementation.** Lost on ADR-0005's own
  argument: optional methods are how providers end up implementing 40% of the surface.
- **Rollback as config-only revert (re-apply old config, keep new image).** Useful but a
  different operation. Expressible later as "deploy release X with config revision Y";
  not a reason for a verb.

## Consequences
- One fewer verb for every provider author; rollback correctness comes from Apply
  correctness, which conformance already tests hardest.
- Rollback speed depends on the old artifact still being available. Retention of
  released images becomes a requirement: a release referenced by any of the last N
  deployments per environment must not be garbage-collected.
- Data rollbacks (schema migrations) are not covered by this ADR and stay a separate,
  gated workflow step (roadmap v0.3).
- ADR-0005's verb list is superseded on this point only.

## Revisit when
Three or more real providers show that "apply the previous release" is measurably slower
or less safe than a native rollback on their target (same threshold ADR-0005 sets for a
ninth verb).
