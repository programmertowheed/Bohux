# Provider interface

The extension boundary. Small on purpose — see ADR-0005.

Providers execute only inside an agent, never in the control plane (ADR-0007). The agent
calls `Status` and `Apply` in one reconcile loop against its cached assignment.

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

    // Destroy removes everything Apply created. MUST be complete; orphaned
    // resources are a conformance failure.
    Destroy(ctx context.Context, ref ResourceRef) error
}
```

## Types

The Go definitions live in `providers/provider.go` (written in S4e; the owner reviews
them as O10). The choices the contract above left open:

- **`ResourceRef`** is `{Environment, App}` (IDs). One workload per App per Environment.
- **`Spec`** carries the pinned `Image`, the `bohux.v1.Runtime`, a lazy
  `SecretSource` (rule 3 below: providers call `Open` only to write the secret file),
  `Raw` (rule 5), and a `Hash` of the desired state.
- **`Hash` is how drift is detected.** `Apply` records `Spec.Hash` on the target (for
  Docker, a label) and `Status` reports it back. The agent applies when the target is
  missing, reports another hash, or is not running. Providers never compare specs
  themselves. Secret values enter the hash only through an HMAC keyed by a node-local
  key, so a rotated secret re-applies and the hash reveals nothing.
- **There is no list verb.** The agent records every workload it applied
  (`applied.json` in its state directory, written before `Apply`) and destroys those
  that leave the assignment. An agent with no assignment yet changes nothing.
- **`Status`** reports `Exists`, `Hash`, `Running`, `Healthy`, `ReadyReplicas`, and a
  `Message` without secret values.
- **`Logs`** yields `LogLine{Time, Instance, Stderr, Message}`; **`Exec`** returns a
  `Session` (stdin, stdout, stderr unless TTY, resize, wait for the exit code).

## Rules

1. **All eight methods (`Describe` + seven verbs) or no merge.** Partial providers are rejected (CLAUDE.md rule 4).
   A target that genuinely cannot do something returns `ErrUnsupported` and declares the
   gap in `Descriptor.Capabilities` so the API can reject the request up front — it does
   not silently no-op.
2. **Apply writes readable artifacts to disk** (Compose file, manifest, unit file) before
   acting on them. A user who deletes Bohux keeps a working system (CLAUDE.md rule 2).
3. **Never log secret values.** Specs arrive with secrets as handles; resolve them as late
   as possible and never into an error string. Values are written only to the target's
   declared secret mechanism (Docker: a 0600 `secrets.env` via `env_file`; K8s: `Secret`;
   systemd: `LoadCredential=`), never into the readable artifact (ADR-0008).
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
- rollback = apply the previous release again → previous release live, nothing orphaned.
  There is no `Rollback` verb; going back is a deploy of a prior release (ADR-0010)
- destroy → nothing orphaned (checked by enumerating the target)
- logs and exec stream and terminate cleanly
- secrets never appear in any emitted artifact or log line; on disk they appear only in
  the declared secret file/object and the agent's sealed cache (ADR-0008)
- secret rotation re-renders the secret file and restarts affected workloads

```
go test ./providers/_conformance -provider=docker -target=$TEST_HOST
```

Green conformance is the done criterion for a new provider. Nothing else is.

## Adding one

Use `.claude/skills/new-provider/SKILL.md`.
