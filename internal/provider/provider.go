// Package provider defines the extension boundary: the interface every deployment
// target implements, exactly as docs/specs/provider-interface.md states it (ADR-0005,
// ADR-0010). Providers run only inside the agent (ADR-0007). Every provider implements
// all eight methods or does not merge (CLAUDE.md rule 4); the conformance suite in
// providers/_conformance is the gate. Implementations live in providers/<name>/.
package provider

import (
	"context"
	"errors"
	"io"
	"iter"
	"time"

	bohuxv1 "github.com/bohumix/bohux/gen/bohux/v1"
)

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

// ErrUnsupported is returned by a verb the target genuinely cannot perform. The gap
// must also be absent from Descriptor.Capabilities so the API refuses up front.
var ErrUnsupported = errors.New("not supported by this provider")

// Verb names a provider operation in Descriptor.Capabilities.
type Verb string

// The seven verbs (ADR-0010: there is no Rollback verb).
const (
	VerbPlan    Verb = "plan"
	VerbApply   Verb = "apply"
	VerbStatus  Verb = "status"
	VerbLogs    Verb = "logs"
	VerbExec    Verb = "exec"
	VerbScale   Verb = "scale"
	VerbDestroy Verb = "destroy"
)

// AllVerbs lists every verb.
var AllVerbs = []Verb{VerbPlan, VerbApply, VerbStatus, VerbLogs, VerbExec, VerbScale, VerbDestroy}

// Descriptor is what Describe returns; it travels to the control plane as a
// bohux.v1.ProviderDescriptor.
type Descriptor struct {
	// Name as registered in the agent, e.g. "docker". Workloads name it.
	Name    string
	Version string
	// Verbs the provider supports.
	Capabilities []Verb
}

// ResourceRef names one workload on the target: an App in an Environment. The IDs
// identify it; the slugs (immutable, like the IDs) make the target's names readable
// (ADR-0025).
type ResourceRef struct {
	Environment string
	App         string

	EnvironmentSlug string `json:",omitempty"`
	AppSlug         string `json:",omitempty"`
	// Slot tells apart two copies of one App that run side by side during a
	// zero-downtime deploy ("blue", "green"; ADR-0035). Each slot is an independent
	// workload with its own names; empty is the App's only copy.
	Slot string `json:",omitempty"`
}

// SecretSource yields a workload's secret values when the provider writes its
// declared secret file. Providers call Open as late as possible and never log the
// result or put it in an artifact or error (rule 3, ADR-0008).
type SecretSource interface {
	Open() (map[string]string, error)
}

// Spec is the resolved desired state for one workload.
type Spec struct {
	Ref     ResourceRef
	Release string
	// Image to run, pinned (a digest reference, or a local image ID, ADR-0020).
	Image   string
	Runtime *bohuxv1.Runtime
	// Secrets is nil when the workload has none.
	Secrets SecretSource
	// Hash identifies this desired state, secrets included. Apply records it on the
	// target and Status reports it back, so the agent can tell whether the target
	// already matches without the provider comparing specs itself.
	Hash string
	// Raw is the target-specific passthrough (provider-interface.md rule 5).
	Raw []byte
	// Ingress is true when the node's ingress serves this workload (it has routes,
	// ADR-0034): the provider makes every container port reachable from the ingress
	// and reports how in Status.Endpoints.
	Ingress bool
}

// PlanRequest asks for the changes Apply would make.
type PlanRequest struct {
	Spec Spec
}

// Plan lists the changes Apply would make. No changes means Apply is a no-op.
type Plan struct {
	Changes []Change
}

// Change is one human-readable planned change.
type Change struct {
	// Action is "create", "update", "restart", or "delete".
	Action string
	Detail string
}

// ApplyRequest converges the target to Spec.
type ApplyRequest struct {
	Spec Spec
}

// ApplyResult reports what Apply did.
type ApplyResult struct {
	// Changed is false when the target already matched.
	Changed bool
}

// Status is the observed state of one workload.
type Status struct {
	// Exists is false when nothing Apply created is present.
	Exists bool
	// Hash is the Spec.Hash of the last successful Apply.
	Hash string
	// Running is true when every replica's process is up.
	Running bool
	// Healthy is true when the health check passes on every replica.
	Healthy       bool
	ReadyReplicas int32
	// Message explains a non-running or unhealthy state, without secret values.
	Message string
	// Endpoints maps each container port to an address the node's ingress dials to
	// reach the workload's replicas ("host:port"). Set only for a running workload
	// applied with Spec.Ingress (ADR-0034).
	Endpoints map[int32]string
}

// LogRequest selects log lines.
type LogRequest struct {
	Ref ResourceRef
	// Instance is a provider-defined instance name; empty means all.
	Instance  string
	Follow    bool
	Since     time.Time
	TailLines int
}

// LogLine is one line of workload output.
type LogLine struct {
	Time     time.Time
	Instance string
	Stderr   bool
	Message  string
}

// ExecRequest starts a process in a running instance.
type ExecRequest struct {
	Ref        ResourceRef
	Instance   string
	Command    []string
	TTY        bool
	Rows, Cols uint16
}

// Session is an attached exec process.
type Session interface {
	Stdin() io.WriteCloser
	Stdout() io.Reader
	// Stderr is nil for a TTY session, whose output is all on Stdout.
	Stderr() io.Reader
	Resize(rows, cols uint16) error
	// Wait blocks until the process exits and returns its exit code.
	Wait() (int, error)
	Close() error
}

// ScaleRequest changes the replica count.
type ScaleRequest struct {
	Ref      ResourceRef
	Replicas int32
}
