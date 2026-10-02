---
name: new-provider
description: Use when adding a new deployment target to Bohux (Swarm, Kubernetes, Nomad, ECS, systemd, static/CDN) or when modifying the provider interface itself. Covers the required verb set, the conformance suite, the artifact-on-disk rule, and registration.
---

# Adding a provider

Read `docs/specs/provider-interface.md` and ADR-0005 first. A provider is only done when
the conformance suite is green — not when it deploys something once by hand.

## Steps

1. **Check the interface has not drifted.** `internal/provider/provider.go` is
   authoritative; the spec doc may lag.
2. **Scaffold** `providers/<name>/` with `provider.go`, `plan.go`, `apply.go`,
   `status.go`, `logs.go`, `exec.go`, `scale.go`, `destroy.go`.
   One file per verb — keeps PRs reviewable.
3. **Implement `Describe` first.** Declare capabilities honestly. A capability you
   declare is a promise the API will hold you to.
4. **Implement `Plan` before `Apply`.** Plan must not mutate. If you find yourself
   needing to create something to compute a diff, the diff is wrong.
5. **Write the artifact to disk in `Apply` before acting on it** (CLAUDE.md rule 2). A
   user who deletes Bohux keeps a working system. For Swarm that is a stack file, for K8s
   a manifest set, for systemd a unit file.
6. **Make `Apply` resumable.** Kill the process at every step and re-run. It must
   converge, not duplicate.
7. **Implement `Destroy` early, not last.** It is what makes the conformance suite
   repeatable, and it is where orphaned resources hide.
8. **Register** in `internal/provider/registry.go`.
9. **Run conformance** against a real target:
   `go test ./providers/_conformance -provider=<name> -target=<host>`
10. **Document** in `docs/providers/<name>.md`: prerequisites, declared capabilities,
    known gaps, and what `Spec.Raw` accepts.

## Rules specific to this work

- `ErrUnsupported` plus a declared capability gap. Never a silent no-op.
- No direct database access from a provider. Resolved Spec in, result out.
- Resolve secret handles as late as possible; never into a log line or an error string.
- Target-specific features go in `Spec.Raw`, never as a new interface method.
- If you believe the interface needs a ninth verb, stop and write an ADR. Three real
  providers must need it (ADR-0005).

## Done means

- [ ] All eight methods implemented (`Describe` + seven verbs)
- [ ] Conformance suite green against a real target
- [ ] Re-applying the previous release converges and orphans nothing (CLAUDE.md rule 5, ADR-0010)
- [ ] Artifacts written to disk and verified readable without Bohux
- [ ] No secret appears in any artifact or log line
- [ ] Docs page written
