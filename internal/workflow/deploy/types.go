// Package deploy is the deploy workflow (ADR-0002, ADR-0026): assign a Release to its
// environment's node, wait for the agent to report it healthy, and compensate by
// reassigning the last healthy Release when it does not (ADR-0010). Rollback is the
// same workflow with an earlier Release.
package deploy

import "time"

// TaskQueue is the task queue the control plane's worker polls.
const TaskQueue = "bohux-deploy"

// WorkflowID is the ID of an App's deploy workflow. One per App: Temporal refuses a
// second deploy of the App while one runs (ADR-0026).
func WorkflowID(appID string) string { return "deploy/" + appID }

// DefaultHealthTimeout is how long a Release has to turn healthy.
const DefaultHealthTimeout = 5 * time.Minute

// State is a Deployment state as stored in deployments_revisions.state.
type State string

// Deployment states the workflow writes.
const (
	StatePending      State = "pending"
	StateApplying     State = "applying"
	StateHealthGate   State = "health_gate"
	StateSucceeded    State = "succeeded"
	StateFailed       State = "failed"
	StateRolledBack   State = "rolled_back"
	StateCompensating State = "compensating"
	StateCancelled    State = "cancelled"
)

// Terminal reports whether no further transition follows s.
func (s State) Terminal() bool {
	switch s {
	case StateSucceeded, StateFailed, StateRolledBack, StateCancelled:
		return true
	}
	return false
}

// Failure reasons recorded on a Deployment (bohux.v1.ErrorDetail.reason). Stable
// across releases.
const (
	ReasonInvalidRelease     = "INVALID_RELEASE"
	ReasonAssignFailed       = "ASSIGN_FAILED"
	ReasonWorkloadFailed     = "WORKLOAD_FAILED"
	ReasonHealthTimeout      = "HEALTH_TIMEOUT"
	ReasonCancelled          = "CANCELLED"
	ReasonCompensationFailed = "COMPENSATION_FAILED"
)

// Error types of non-retryable activity errors, so the workflow can tell them apart.
const (
	ErrTypeInvalid        = "Invalid"
	ErrTypeWorkloadFailed = "WorkloadFailed"
	ErrTypeHealthTimeout  = "HealthTimeout"
)

// Actor is who started the Deployment (rule 10). Kind is a store.ActorKind.
type Actor struct {
	ID   string
	Kind string
}

// Input starts a deploy. Everything in it lands in workflow history, so it carries IDs
// and never a secret value (rule 3).
type Input struct {
	TenantID      string
	DeploymentID  string
	AppID         string
	EnvironmentID string
	ReleaseID     string
	// RollbackOf is the Deployment this one reverts, if it is a rollback.
	RollbackOf string
	Reason     string
	Actor      Actor
	// HealthTimeout overrides DefaultHealthTimeout when positive.
	HealthTimeout time.Duration
}

// Failure is what went wrong, stored as the Deployment's ErrorDetail.
type Failure struct {
	Reason string
}

// Result is how the workflow ended. A deploy that failed or rolled back is a
// successful workflow: its outcome is recorded, nothing is left to retry.
type Result struct {
	State   State
	Failure *Failure
}

// StateChange moves a Deployment to State, writing one revision and audit event.
type StateChange struct {
	TenantID     string
	DeploymentID string
	State        State
	Failure      *Failure
	Actor        Actor
}

// AssignInput sets an App's workload on its environment's node to ReleaseID, or
// removes the workload when ReleaseID is empty.
type AssignInput struct {
	TenantID     string
	AppID        string
	ReleaseID    string
	DeploymentID string
	Actor        Actor
}

// Assigned names the node generation that carries the change.
type Assigned struct {
	NodeID     string
	Generation uint64
	// Replicas the Release runs with; zero for a removal.
	Replicas int32
}

// WaitInput waits for Generation on NodeID to run AppID at ReleaseID, healthy.
type WaitInput struct {
	TenantID   string
	NodeID     string
	Generation uint64
	AppID      string
	ReleaseID  string
	// Replicas that must be ready.
	Replicas int32
	// Deadline is when the health gate fails with ErrTypeHealthTimeout. Set by the
	// workflow so activity retries do not extend it.
	Deadline time.Time
}

// LastHealthyInput asks for the Release of the App's newest succeeded Deployment
// other than DeploymentID.
type LastHealthyInput struct {
	TenantID     string
	AppID        string
	DeploymentID string
}
