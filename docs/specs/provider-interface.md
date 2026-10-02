# Provider interface

The extension boundary. Small on purpose — see ADR-0005.

## Contract

```go
// Provider translates a resolved Spec onto a concrete compute target.
// Implementations MUST be idempotent: Apply with the same Spec and the same
// observed state is a no-op.
type Provider interface {
    // Identity and declared capabilities. Called at registration.
    Describe(ctx context.Context) (Descriptor, error)

    // Plan computes the diff between desired and observed state.
    // MUST NOT mutate anything. Output is shown to the user for approval.
    Plan(ctx context.Context, req PlanRequest) (Plan, error)

    // Apply reconciles toward desired state. MUST be resumable: called again
    // after a crash mid-apply, it finishes the job rather than duplicating it.
    Apply(ctx context.Context, req ApplyRequest) (ApplyResult, error)

    // Status reports observed state. Called continuously by the agent.
    Status(ctx context.Context, ref ResourceRef) (Status, error)

    // Logs streams until ctx is cancelled.
    Logs(ctx context.Context, req LogRequest) (iter.Seq2[LogLine, error], error)

    // Exec attaches to a running instance. Session is audited by the caller.
    Exec(ctx context.Context, req ExecRequest) (Session, error)

    // Scale adjusts replica count or resource allocation.
    Scale(ctx context.Context, req ScaleRequest) error

    // Rollback returns to a previous Release. Held to the same standard as
    // Apply — see CLAUDE.md rule 5.
    Rollback(ctx context.Context, req RollbackRequest) error

    // Destroy removes everything Apply created. MUST be complete; orphaned
    // resources are a conformance failure.
    Destroy(ctx context.Context, ref ResourceRef) error
}
```

## Rules

1. **All nine methods or no merge.** Partial providers are rejected (CLAUDE.md rule 4).
   A target that genuinely cannot do something returns `ErrUnsupported` and declares the
   gap in `Descriptor.Capabilities` so the API can reject the request up front — it does
   not silently no-op.
2. **Apply writes readable artifacts to disk** (Compose file, manifest, unit file) before
   acting on them. A user who deletes Kiln keeps a working system (CLAUDE.md rule 2).
3. **Never log secret values.** Specs arrive with secrets as handles; resolve them as late
   as possible and never into an error string.
4. **No direct database access.** Providers receive a resolved Spec and return results.
   They do not know Postgres exists.
5. **Target-specific features go in `Spec.Raw`,** a passthrough blob the provider
   interprets. They never become new interface methods.

## Conformance

`providers/_conformance` is the gate. It runs the full lifecycle against a real target:

- apply from empty → status healthy
- apply again, unchanged → no-op, no churn
- apply a changed spec → converges
- kill mid-apply, apply again → converges, no duplicates
- rollback → previous release live
- destroy → nothing orphaned (checked by enumerating the target)
- logs and exec stream and terminate cleanly
- secrets never appear in any emitted artifact or log line

```
go test ./providers/_conformance -provider=docker -target=$TEST_HOST
```

Green conformance is the done criterion for a new provider. Nothing else is.

## Adding one

Use `.claude/skills/new-provider/SKILL.md`.
