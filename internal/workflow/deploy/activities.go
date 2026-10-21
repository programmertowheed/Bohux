package deploy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"
	"google.golang.org/protobuf/encoding/protojson"

	agentv1 "github.com/bohumix/bohux/gen/bohux/agent/v1"
	bohuxv1 "github.com/bohumix/bohux/gen/bohux/v1"
	"github.com/bohumix/bohux/internal/agentapi"
	"github.com/bohumix/bohux/internal/store"
)

const (
	deploymentType = "bohux.v1.Deployment"
	// createAction is recorded by whichever side inserts the row (ADR-0026 item 3).
	createAction = "bohux.v1.DeploymentService/CreateDeployment"
	// stateAction is recorded for every transition the workflow writes.
	stateAction = "bohux.workflow/Deploy"

	defaultPollInterval = 2 * time.Second
)

var (
	storeJSON = protojson.MarshalOptions{UseProtoNames: true}
	loadJSON  = protojson.UnmarshalOptions{DiscardUnknown: true}
)

// Assigner changes one App's workload on a node; agentapi.Server implements it.
type Assigner interface {
	AssignApp(ctx context.Context, tenant, node, app uuid.UUID, w *agentv1.Workload, actor store.Actor) (uint64, error)
}

// Activities are the deploy workflow's I/O: Postgres and the agent API. Every method is
// idempotent, because Temporal retries it (new-workflow skill).
type Activities struct {
	Store  *store.Store
	Agents Assigner
	// PollInterval is how often WaitHealthy reads the node's report; 2 s when zero.
	PollInterval time.Duration
}

// RecordStart writes the Deployment row if it is absent (ADR-0026 item 3), records the
// run, and validates the Release. An invalid Release is a non-retryable
// ErrTypeInvalid error, returned after the row exists.
func (a *Activities) RecordStart(ctx context.Context, in Input) error {
	ids, err := parseIDs(in.TenantID, in.DeploymentID, in.AppID, in.ReleaseID, in.EnvironmentID)
	if err != nil {
		return err
	}
	tenant, deployment, app, release, env := ids[0], ids[1], ids[2], ids[3], ids[4]
	actor, err := in.Actor.store()
	if err != nil {
		return err
	}
	var rollbackOf *uuid.UUID
	if in.RollbackOf != "" {
		id, err := uuid.Parse(in.RollbackOf)
		if err != nil {
			return invalid("rollback_of is not an ID")
		}
		rollbackOf = &id
	}
	run := activity.GetInfo(ctx).WorkflowExecution
	var problem error
	err = a.Store.InTenant(ctx, tenant, func(tx *store.Tx) error {
		row := DeploymentRow{
			ID: deployment, ReleaseID: release, EnvironmentID: env, RollbackOf: rollbackOf,
			Reason: in.Reason, WorkflowID: run.ID, RunID: run.RunID,
		}
		if err := InsertDeployment(ctx, tx, row, actor); err != nil {
			return err
		}
		problem = ValidateRelease(ctx, tx, app, release, env)
		return nil
	})
	if errors.Is(err, store.ErrNotFound) {
		return invalid("release does not exist")
	}
	if err != nil {
		return fmt.Errorf("record deployment %s: %w", deployment, err)
	}
	if problem != nil {
		return invalid(problem.Error())
	}
	return nil
}

// DeploymentRow is what creating a Deployment writes.
type DeploymentRow struct {
	ID            uuid.UUID
	ReleaseID     uuid.UUID
	EnvironmentID uuid.UUID
	RollbackOf    *uuid.UUID
	Reason        string
	WorkflowID    string
	RunID         string
}

// InsertDeployment inserts the Deployment in state pending unless a row with its ID
// exists, in which case it does nothing. The API and RecordStart both call it
// (ADR-0026 item 3); the App's head is locked first so the two serialize.
func InsertDeployment(ctx context.Context, tx *store.Tx, d DeploymentRow, actor store.Actor) error {
	var project uuid.UUID
	err := tx.QueryRow(ctx, `SELECT e.project_id FROM releases_heads r
		JOIN apps_heads a ON a.id = r.app_id
		JOIN environments_heads e ON e.id = a.environment_id
		WHERE r.id = $1 FOR UPDATE OF a`, d.ReleaseID).Scan(&project)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("release %s: %w", d.ReleaseID, store.ErrNotFound)
	}
	if err != nil {
		return fmt.Errorf("lock app: %w", err)
	}
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM deployments_heads WHERE id = $1)`,
		d.ID).Scan(&exists); err != nil {
		return fmt.Errorf("check deployment: %w", err)
	}
	if exists {
		return nil
	}
	_, err = tx.CreateWithID(ctx, "deployments", d.ID,
		[]store.Col{
			{Name: "release_id", Value: d.ReleaseID}, {Name: "environment_id", Value: d.EnvironmentID},
			{Name: "strategy", Value: "recreate"}, {Name: "rollback_of", Value: d.RollbackOf},
			{Name: "reason", Value: d.Reason},
		},
		[]store.Col{
			{Name: "state", Value: string(StatePending)}, {Name: "workflow_id", Value: d.WorkflowID},
			{Name: "workflow_run_id", Value: d.RunID}, {Name: "started_at", Value: time.Now()},
		},
		store.Audit{Actor: actor, Action: createAction, ResourceType: deploymentType, ProjectID: project})
	if err != nil {
		return fmt.Errorf("insert deployment: %w", err)
	}
	return nil
}

// ValidateRelease returns why release cannot be deployed to env as app, or nil.
func ValidateRelease(ctx context.Context, tx *store.Tx, app, release, env uuid.UUID) error {
	var (
		releaseApp, appEnv uuid.UUID
		state              string
		available          bool
	)
	err := tx.QueryRow(ctx, `SELECT h.app_id, r.state, r.artifact_available, a.environment_id
		FROM releases_heads h
		JOIN releases_revisions r ON r.id = h.revision_id
		JOIN apps_heads a ON a.id = h.app_id AND NOT a.deleted
		WHERE h.id = $1 AND NOT h.deleted`, release).Scan(&releaseApp, &state, &available, &appEnv)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return errors.New("release or its app does not exist")
	case err != nil:
		return fmt.Errorf("read release: %w", err)
	case releaseApp != app:
		return errors.New("release belongs to another app")
	case appEnv != env:
		return errors.New("release's app is not in this environment")
	case state != "ready":
		return fmt.Errorf("release is %s, not ready", state)
	case !available:
		return errors.New("release artifact is no longer available")
	}
	return nil
}

// SetState moves the Deployment to a new state. A repeat of the current state, or any
// change to a Deployment already terminal, is a no-op.
func (a *Activities) SetState(ctx context.Context, c StateChange) error {
	ids, err := parseIDs(c.TenantID, c.DeploymentID)
	if err != nil {
		return err
	}
	actor, err := c.Actor.store()
	if err != nil {
		return err
	}
	var failure []byte
	if c.Failure != nil {
		if failure, err = storeJSON.Marshal(&bohuxv1.ErrorDetail{Reason: c.Failure.Reason}); err != nil {
			return fmt.Errorf("encode failure: %w", err)
		}
	}
	err = a.Store.InTenant(ctx, ids[0], func(tx *store.Tx) error {
		var (
			revision, env, project uuid.UUID
		)
		// The head is locked alone, then its revision read (see agentapi's Assign).
		err := tx.QueryRow(ctx, `SELECT revision_id, environment_id FROM deployments_heads
			WHERE id = $1 AND NOT deleted FOR UPDATE`, ids[1]).Scan(&revision, &env)
		if errors.Is(err, pgx.ErrNoRows) {
			// Only RecordStart creates the row, and it refused: nothing to update.
			return invalid("deployment does not exist")
		}
		if err != nil {
			return fmt.Errorf("lock deployment: %w", err)
		}
		var (
			state, workflowID, runID string
			started                  *time.Time
		)
		if err := tx.QueryRow(ctx, `SELECT r.state, r.workflow_id, r.workflow_run_id, r.started_at,
			e.project_id FROM deployments_revisions r, environments_heads e
			WHERE r.id = $1 AND e.id = $2`, revision, env).Scan(&state, &workflowID, &runID, &started, &project); err != nil {
			return fmt.Errorf("read deployment: %w", err)
		}
		if State(state) == c.State || State(state).Terminal() {
			return nil
		}
		var finished *time.Time
		if c.State.Terminal() {
			now := time.Now()
			finished = &now
		}
		_, err = tx.Update(ctx, "deployments", ids[1], revision, []store.Col{
			{Name: "state", Value: string(c.State)}, {Name: "workflow_id", Value: workflowID},
			{Name: "workflow_run_id", Value: runID}, {Name: "started_at", Value: started},
			{Name: "finished_at", Value: finished}, {Name: "failure", Value: failure},
		}, store.Audit{Actor: actor, Action: stateAction, ResourceType: deploymentType, ProjectID: project})
		return err
	})
	if app := applicationError(err); app != nil {
		return app
	}
	if err != nil {
		return fmt.Errorf("set deployment %s to %s: %w", c.DeploymentID, c.State, err)
	}
	return nil
}

// LastHealthyRelease returns the Release of the App's newest succeeded Deployment
// other than in.DeploymentID, or "" if there is none.
func (a *Activities) LastHealthyRelease(ctx context.Context, in LastHealthyInput) (string, error) {
	ids, err := parseIDs(in.TenantID, in.AppID, in.DeploymentID)
	if err != nil {
		return "", err
	}
	var release uuid.UUID
	err = a.Store.InTenant(ctx, ids[0], func(tx *store.Tx) error {
		return tx.QueryRow(ctx, `SELECT d.release_id FROM deployments_heads d
			JOIN deployments_revisions r ON r.id = d.revision_id
			JOIN releases_heads rh ON rh.id = d.release_id
			WHERE rh.app_id = $1 AND d.id <> $2 AND NOT d.deleted AND r.state = 'succeeded'
			ORDER BY r.finished_at DESC NULLS LAST, d.created_at DESC LIMIT 1`, ids[1], ids[2]).Scan(&release)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("find last healthy release of app %s: %w", in.AppID, err)
	}
	return release.String(), nil
}

// AssignRelease sets the App's workload on its environment's node to the Release, or
// removes it when in.ReleaseID is empty (ADR-0026 item 5). The workload is built from
// the Release's artifact and the App revision it captured; secrets stay handles.
func (a *Activities) AssignRelease(ctx context.Context, in AssignInput) (Assigned, error) {
	ids, err := parseIDs(in.TenantID, in.AppID)
	if err != nil {
		return Assigned{}, err
	}
	tenant, app := ids[0], ids[1]
	actor, err := in.Actor.store()
	if err != nil {
		return Assigned{}, err
	}
	var (
		node uuid.UUID
		w    *agentv1.Workload
	)
	err = a.Store.InTenant(ctx, tenant, func(tx *store.Tx) error {
		var err error
		node, err = appNode(ctx, tx, app)
		if err != nil || in.ReleaseID == "" {
			return err
		}
		w, err = buildWorkload(ctx, tx, app, in)
		return err
	})
	if app := applicationError(err); app != nil {
		return Assigned{}, app
	}
	if err != nil {
		return Assigned{}, fmt.Errorf("build workload for app %s: %w", in.AppID, err)
	}
	gen, err := a.Agents.AssignApp(ctx, tenant, node, app, w, actor)
	switch {
	case errors.Is(err, agentapi.ErrRevokedNode), errors.Is(err, agentapi.ErrUnknownApp),
		errors.Is(err, agentapi.ErrWrongEnv), errors.Is(err, store.ErrNotFound):
		return Assigned{}, invalid(err.Error())
	case err != nil:
		return Assigned{}, fmt.Errorf("assign app %s: %w", in.AppID, err)
	}
	out := Assigned{NodeID: node.String(), Generation: gen}
	if w != nil {
		out.Replicas = w.GetRuntime().GetReplicas()
	}
	return out, nil
}

// appNode returns the node of the App's environment.
func appNode(ctx context.Context, tx *store.Tx, app uuid.UUID) (uuid.UUID, error) {
	var node *uuid.UUID
	err := tx.QueryRow(ctx, `SELECT er.node_id FROM apps_heads a
		JOIN environments_heads e ON e.id = a.environment_id
		JOIN environments_revisions er ON er.id = e.revision_id
		WHERE a.id = $1 AND NOT a.deleted`, app).Scan(&node)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, invalid("app does not exist")
	}
	if err != nil {
		return uuid.Nil, fmt.Errorf("find app node: %w", err)
	}
	if node == nil {
		return uuid.Nil, invalid("app's environment has no node")
	}
	return *node, nil
}

// buildWorkload reads the Release, the App revision it captured, the App's current
// replicas, and the environment's provider, and returns the workload to assign.
func buildWorkload(ctx context.Context, tx *store.Tx, app uuid.UUID, in AssignInput) (*agentv1.Workload, error) {
	release, err := uuid.Parse(in.ReleaseID)
	if err != nil {
		return nil, invalid("release is not an ID")
	}
	var (
		env                     uuid.UUID
		provider, digest, state string
		sourceRef               string
		available               bool
		rt, secs, currentRT     []byte
	)
	err = tx.QueryRow(ctx, `SELECT a.environment_id, er.provider, r.artifact_digest, r.state,
		r.artifact_available, r.source_ref, ar.runtime, ar.secrets, cur.runtime
		FROM releases_heads h
		JOIN releases_revisions r ON r.id = h.revision_id
		JOIN apps_revisions ar ON ar.id = h.config_revision_id
		JOIN apps_heads a ON a.id = h.app_id
		JOIN apps_revisions cur ON cur.id = a.revision_id
		JOIN environments_heads e ON e.id = a.environment_id
		JOIN environments_revisions er ON er.id = e.revision_id
		WHERE h.id = $1 AND h.app_id = $2 AND NOT h.deleted`, release, app).Scan(
		&env, &provider, &digest, &state, &available, &sourceRef, &rt, &secs, &currentRT)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, invalid("release does not exist for this app")
	}
	if err != nil {
		return nil, fmt.Errorf("read release: %w", err)
	}
	if state != "ready" || !available {
		return nil, invalid("release is not ready or its artifact is gone")
	}
	runtime, current := &bohuxv1.Runtime{}, &bohuxv1.Runtime{}
	if err := loadJSON.Unmarshal(rt, runtime); err != nil {
		return nil, fmt.Errorf("decode app runtime: %w", err)
	}
	if err := loadJSON.Unmarshal(currentRT, current); err != nil {
		return nil, fmt.Errorf("decode current app runtime: %w", err)
	}
	// Replicas are operational, not part of the Release (ADR-0027).
	runtime.Replicas = current.GetReplicas()
	secrets, err := decodeSecretRefs(secs)
	if err != nil {
		return nil, err
	}
	return &agentv1.Workload{
		AppId: app.String(), EnvironmentId: env.String(), ReleaseId: in.ReleaseID,
		DeploymentId: in.DeploymentID, Provider: provider, Image: imageRef(sourceRef, digest),
		Runtime: runtime, Secrets: secrets,
	}, nil
}

// imageRef is what the provider runs: the pinned reference an imported Release
// records as its source_ref, or the local image ID a build produced (ADR-0020 item 2),
// whose source_ref is a commit.
func imageRef(sourceRef, digest string) string {
	if strings.Contains(sourceRef, "@sha256:") {
		return sourceRef
	}
	return digest
}

func decodeSecretRefs(raw []byte) ([]*bohuxv1.SecretRef, error) {
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, fmt.Errorf("decode app secret refs: %w", err)
	}
	refs := make([]*bohuxv1.SecretRef, 0, len(items))
	for _, it := range items {
		ref := &bohuxv1.SecretRef{}
		if err := loadJSON.Unmarshal(it, ref); err != nil {
			return nil, fmt.Errorf("decode app secret ref: %w", err)
		}
		refs = append(refs, ref)
	}
	return refs, nil
}

// WaitHealthy polls the node's stored StatusReport until the assignment runs the
// Release healthy (ADR-0026 item 6). A FAILED workload fails it with
// ErrTypeWorkloadFailed; passing in.Deadline fails it with ErrTypeHealthTimeout.
func (a *Activities) WaitHealthy(ctx context.Context, in WaitInput) error {
	ids, err := parseIDs(in.TenantID, in.NodeID)
	if err != nil {
		return err
	}
	interval := a.PollInterval
	if interval <= 0 {
		interval = defaultPollInterval
	}
	for {
		done, err := a.healthy(ctx, ids[0], ids[1], in)
		if err != nil || done {
			return err
		}
		if !time.Now().Before(in.Deadline) {
			return temporal.NewNonRetryableApplicationError(
				"release did not turn healthy before the deadline", ErrTypeHealthTimeout, nil)
		}
		activity.RecordHeartbeat(ctx)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
		}
	}
}

// healthy reads the node's report once.
func (a *Activities) healthy(ctx context.Context, tenant, node uuid.UUID, in WaitInput) (bool, error) {
	var raw []byte
	err := a.Store.InTenant(ctx, tenant, func(tx *store.Tx) error {
		return tx.QueryRow(ctx, `SELECT report FROM nodes_heads WHERE id = $1`, node).Scan(&raw)
	})
	if err != nil {
		return false, fmt.Errorf("read node report: %w", err)
	}
	if raw == nil {
		return false, nil
	}
	report := &agentv1.StatusReport{}
	if err := loadJSON.Unmarshal(raw, report); err != nil {
		return false, fmt.Errorf("decode node report: %w", err)
	}
	if report.GetGeneration() < in.Generation {
		return false, nil
	}
	for _, w := range report.GetWorkloads() {
		if w.GetAppId() != in.AppID || w.GetReleaseId() != in.ReleaseID {
			continue
		}
		switch w.GetState() {
		case agentv1.WorkloadState_WORKLOAD_STATE_FAILED:
			return false, temporal.NewNonRetryableApplicationError(
				"workload failed: "+w.GetFailure().GetReason(), ErrTypeWorkloadFailed, nil)
		case agentv1.WorkloadState_WORKLOAD_STATE_HEALTHY:
			return w.GetReadyReplicas() >= in.Replicas, nil
		}
	}
	return false, nil
}

// store converts the workflow's Actor back to the store's.
func (a Actor) store() (store.Actor, error) {
	id, err := uuid.Parse(a.ID)
	if err != nil || a.Kind == "" {
		return store.Actor{}, invalid("deployment has no valid actor")
	}
	return store.Actor{ID: id, Kind: store.ActorKind(a.Kind)}, nil
}

// parseIDs parses workflow-supplied IDs; a malformed one can never succeed.
func parseIDs(raw ...string) ([]uuid.UUID, error) {
	out := make([]uuid.UUID, len(raw))
	for i, r := range raw {
		id, err := uuid.Parse(r)
		if err != nil {
			return nil, invalid(fmt.Sprintf("%q is not an ID", r))
		}
		out[i] = id
	}
	return out, nil
}

// applicationError returns the Temporal application error inside err, or nil. Temporal
// reads retryability from the returned error itself, so a wrapped non-retryable error
// must be returned bare.
func applicationError(err error) error {
	var app *temporal.ApplicationError
	if errors.As(err, &app) {
		return app
	}
	return nil
}

func invalid(msg string) error {
	return temporal.NewNonRetryableApplicationError(msg, ErrTypeInvalid, nil)
}
