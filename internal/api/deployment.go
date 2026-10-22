package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"

	bohuxv1 "github.com/bohumix/bohux/gen/bohux/v1"
	"github.com/bohumix/bohux/gen/bohux/v1/bohuxv1connect"
	"github.com/bohumix/bohux/internal/auth"
	"github.com/bohumix/bohux/internal/slo"
	"github.com/bohumix/bohux/internal/store"
	"github.com/bohumix/bohux/internal/workflow/deploy"
)

const deploymentType = "bohux.v1.Deployment"

// watchInterval is how often WatchDeployment re-reads the row. It only reads; the
// workflow drives the state (rule 6).
var watchInterval = time.Second

var (
	errDeployRunning = connect.NewError(connect.CodeFailedPrecondition, deploy.ErrRunning)
	errNoDeploys     = connect.NewError(connect.CodeUnavailable, deploy.ErrUnavailable)
)

type deploymentService struct {
	s *Server
}

// deployRequest is what CreateDeployment and Rollback both start.
type deployRequest struct {
	rpc        string
	key        string
	msg        proto.Message
	release    uuid.UUID
	env        uuid.UUID
	rollbackOf *uuid.UUID
	reason     string
	canary     *deploy.Canary
}

func (ds *deploymentService) CreateDeployment(ctx context.Context, req *connect.Request[bohuxv1.CreateDeploymentRequest]) (*connect.Response[bohuxv1.CreateDeploymentResponse], error) {
	msg := req.Msg
	release, err := parseID("release_id", msg.GetReleaseId())
	if err != nil {
		return nil, err
	}
	env, err := parseID("environment_id", msg.GetEnvironmentId())
	if err != nil {
		return nil, err
	}
	var canary *deploy.Canary
	switch msg.GetStrategy() {
	case bohuxv1.DeploymentStrategy_DEPLOYMENT_STRATEGY_UNSPECIFIED, bohuxv1.DeploymentStrategy_DEPLOYMENT_STRATEGY_RECREATE:
		if msg.GetCanary() != nil {
			return nil, invalidArgument("canary", "UNEXPECTED", "canary needs strategy CANARY")
		}
	case bohuxv1.DeploymentStrategy_DEPLOYMENT_STRATEGY_CANARY:
		if canary, err = canaryOf(msg.GetCanary()); err != nil {
			return nil, err
		}
	default:
		return nil, invalidArgument("strategy", "UNSUPPORTED", "strategy must be RECREATE or CANARY")
	}
	var rollbackOf *uuid.UUID
	if msg.GetRollbackOf() != "" {
		id, err := parseID("rollback_of", msg.GetRollbackOf())
		if err != nil {
			return nil, err
		}
		rollbackOf = &id
	}
	d, err := ds.start(ctx, deployRequest{rpc: bohuxv1connect.DeploymentServiceCreateDeploymentProcedure,
		key: msg.GetIdempotencyKey(), msg: msg, release: release, env: env, rollbackOf: rollbackOf,
		reason: msg.GetReason(), canary: canary})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&bohuxv1.CreateDeploymentResponse{Deployment: d}), nil
}

// Bounds of a canary (ADR-0049).
const (
	minCanaryBake = time.Minute
	maxCanaryBake = 24 * time.Hour
)

// canaryOf validates a CANARY deployment's step.
func canaryOf(c *bohuxv1.Canary) (*deploy.Canary, error) {
	if c == nil {
		return nil, invalidArgument("canary", "REQUIRED", "strategy CANARY needs canary")
	}
	if w := c.GetWeightPercent(); w < 1 || w > 99 {
		return nil, invalidArgument("canary.weight_percent", "OUT_OF_RANGE", "weight_percent must be 1 to 99")
	}
	bake := c.GetBake().AsDuration()
	if !c.GetBake().IsValid() || bake < minCanaryBake || bake > maxCanaryBake || bake%time.Second != 0 {
		return nil, invalidArgument("canary.bake", "OUT_OF_RANGE", "bake must be whole seconds from 1m to 24h")
	}
	return &deploy.Canary{Weight: c.GetWeightPercent(), Bake: bake}, nil
}

// defaultSLOWatch is how long a deploy is verified when the SLO names no watch.
const defaultSLOWatch = 10 * time.Minute

// appSLO returns the App's current SLO as the deploy judges it, nil when it has none
// (ADR-0050). The deploy takes it when it starts; a later change applies to the next.
func appSLO(ctx context.Context, tx *store.Tx, app uuid.UUID) (*deploy.SLO, error) {
	var raw []byte
	if err := tx.QueryRow(ctx, `SELECT r.slo FROM apps_heads a
		JOIN apps_revisions r ON r.id = a.revision_id WHERE a.id = $1`, app).Scan(&raw); err != nil {
		return nil, fmt.Errorf("read app slo: %w", err)
	}
	if raw == nil {
		return nil, nil
	}
	s := &bohuxv1.SLO{}
	if err := loadJSON.Unmarshal(raw, s); err != nil {
		return nil, fmt.Errorf("decode app slo: %w", err)
	}
	out := &deploy.SLO{Watch: defaultSLOWatch, Objective: slo.Objective{
		Availability: s.GetAvailabilityTarget(), MaxBurnRate: s.GetMaxBurnRate(),
		LatencyThresholdMS: s.GetLatency().GetThresholdMs(), LatencyTarget: s.GetLatency().GetTarget()}}
	if s.GetWatch() != nil {
		out.Watch = s.GetWatch().AsDuration()
	}
	return out, nil
}

// switchesSlots reports whether the App's current revision deploys into its other
// slot (ADR-0035): it has routes and publishes no host port. Only such an App can have
// a canary (ADR-0049).
func switchesSlots(ctx context.Context, tx *store.Tx, app uuid.UUID) (bool, error) {
	var routes, rt []byte
	if err := tx.QueryRow(ctx, `SELECT r.routes, r.runtime FROM apps_heads a
		JOIN apps_revisions r ON r.id = a.revision_id WHERE a.id = $1`, app).Scan(&routes, &rt); err != nil {
		return false, fmt.Errorf("read app: %w", err)
	}
	var rs []json.RawMessage
	if err := json.Unmarshal(routes, &rs); err != nil {
		return false, fmt.Errorf("decode app routes: %w", err)
	}
	runtime := &bohuxv1.Runtime{}
	if err := loadJSON.Unmarshal(rt, runtime); err != nil {
		return false, fmt.Errorf("decode app runtime: %w", err)
	}
	for _, p := range runtime.GetPorts() {
		if p.GetHostPort() != 0 {
			return false, nil
		}
	}
	return len(rs) > 0, nil
}

// Rollback deploys an earlier Release of the App (ADR-0010). With no target it picks
// the last Release that deployed healthy before the current one.
func (ds *deploymentService) Rollback(ctx context.Context, req *connect.Request[bohuxv1.RollbackRequest]) (*connect.Response[bohuxv1.RollbackResponse], error) {
	msg := req.Msg
	p, _ := auth.FromContext(ctx)
	app, err := parseID("app_id", msg.GetAppId())
	if err != nil {
		return nil, err
	}
	var target uuid.UUID
	if msg.GetToReleaseId() != "" {
		if target, err = parseID("to_release_id", msg.GetToReleaseId()); err != nil {
			return nil, err
		}
	}
	var (
		env     uuid.UUID
		current *uuid.UUID
	)
	err = ds.s.Store.InTenant(ctx, p.Tenant, func(tx *store.Tx) error {
		project, appEnv, err := appProject(ctx, tx, app)
		if err != nil {
			return err
		}
		if err := authorizeProject(ctx, project, auth.RoleDeployer); err != nil {
			return err
		}
		env = appEnv
		cur, prev, err := healthyHistory(ctx, tx, app)
		if err != nil {
			return err
		}
		current = cur
		if target == uuid.Nil {
			if prev == uuid.Nil {
				return connect.NewError(connect.CodeFailedPrecondition,
					errors.New("no earlier healthy release to roll back to"))
			}
			target = prev
		}
		return nil
	})
	if err != nil {
		return nil, toConnect(ctx, err)
	}
	d, err := ds.start(ctx, deployRequest{rpc: bohuxv1connect.DeploymentServiceRollbackProcedure,
		key: msg.GetIdempotencyKey(), msg: msg, release: target, env: env, rollbackOf: current,
		reason: msg.GetReason()})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&bohuxv1.RollbackResponse{Deployment: d}), nil
}

// healthyHistory returns the App's newest succeeded Deployment and the Release of the
// newest succeeded one before it that ran a different Release. Either may be nil.
func healthyHistory(ctx context.Context, tx *store.Tx, app uuid.UUID) (*uuid.UUID, uuid.UUID, error) {
	rows, err := tx.Query(ctx, `SELECT d.id, d.release_id FROM deployments_heads d
		JOIN deployments_revisions r ON r.id = d.revision_id
		JOIN releases_heads rh ON rh.id = d.release_id
		WHERE rh.app_id = $1 AND NOT d.deleted AND r.state = 'succeeded'
		ORDER BY r.finished_at DESC NULLS LAST, d.created_at DESC`, app)
	if err != nil {
		return nil, uuid.Nil, fmt.Errorf("read deploy history: %w", err)
	}
	defer rows.Close()
	var (
		current        *uuid.UUID
		currentRelease uuid.UUID
	)
	for rows.Next() {
		var d, rel uuid.UUID
		if err := rows.Scan(&d, &rel); err != nil {
			return nil, uuid.Nil, fmt.Errorf("read deploy history: %w", err)
		}
		if current == nil {
			current, currentRelease = &d, rel
			continue
		}
		if rel != currentRelease {
			return current, rel, nil
		}
	}
	return current, uuid.Nil, rows.Err()
}

// start validates r, starts the App's deploy run, then writes the Deployment row
// (ADR-0026 item 3) and returns it. A replayed idempotency key returns the first
// Deployment without starting anything.
func (ds *deploymentService) start(ctx context.Context, r deployRequest) (*bohuxv1.Deployment, error) {
	p, _ := auth.FromContext(ctx)
	idem, err := newIdempotency(ctx, r.rpc, r.key, r.msg)
	if err != nil {
		return nil, toConnect(ctx, err)
	}
	var (
		out       *bohuxv1.Deployment
		app       uuid.UUID
		objective *deploy.SLO
	)
	err = ds.s.Store.InTenant(ctx, p.Tenant, func(tx *store.Tx) error {
		prior, err := idem.lookup(ctx, tx)
		if err != nil {
			return err
		}
		if prior != nil {
			out, err = getDeployment(ctx, tx, prior.ResourceID)
			return err
		}
		project, err := environmentProject(ctx, tx, r.env)
		if err != nil {
			return err
		}
		if err := authorizeProject(ctx, project, auth.RoleDeployer); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT app_id FROM releases_heads WHERE id = $1 AND NOT deleted`,
			r.release).Scan(&app); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return invalidArgument("release_id", "UNKNOWN_RELEASE", "no such release")
			}
			return fmt.Errorf("read release: %w", err)
		}
		if _, problem := deploy.ValidateRelease(ctx, tx, app, r.release, r.env); problem != nil {
			return connect.NewError(connect.CodeFailedPrecondition, problem)
		}
		if objective, err = appSLO(ctx, tx, app); err != nil {
			return err
		}
		if r.canary != nil {
			ok, err := switchesSlots(ctx, tx, app)
			if err != nil {
				return err
			}
			if !ok {
				return connect.NewError(connect.CodeFailedPrecondition, errors.New(
					"a canary needs an app with routes and no host port, which deploys into its other slot"))
			}
		}
		if r.rollbackOf != nil {
			var ok bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM deployments_heads d
				JOIN releases_heads rh ON rh.id = d.release_id
				WHERE d.id = $1 AND rh.app_id = $2)`, *r.rollbackOf, app).Scan(&ok); err != nil {
				return fmt.Errorf("check rollback_of: %w", err)
			}
			if !ok {
				return invalidArgument("rollback_of", "UNKNOWN_DEPLOYMENT", "rollback_of must be a deployment of the same app")
			}
		}
		return nil
	})
	if err != nil || out != nil {
		return out, toConnect(ctx, err)
	}
	if ds.s.Deploys == nil {
		return nil, errNoDeploys
	}
	id, err := store.NewID()
	if err != nil {
		return nil, toConnect(ctx, err)
	}
	actor := p.Actor()
	in := deploy.Input{
		TenantID: p.Tenant.String(), DeploymentID: id.String(), AppID: app.String(),
		EnvironmentID: r.env.String(), ReleaseID: r.release.String(), Reason: r.reason,
		Actor:  deploy.Actor{ID: actor.ID.String(), Kind: string(actor.Kind)},
		Canary: r.canary,
		SLO:    objective,
	}
	if r.rollbackOf != nil {
		in.RollbackOf = r.rollbackOf.String()
	}
	workflowID, runID, err := ds.startRun(ctx, p.Tenant, app, in)
	switch {
	case errors.Is(err, deploy.ErrRunning):
		return nil, errDeployRunning
	case errors.Is(err, deploy.ErrUnavailable):
		return nil, errNoDeploys
	case err != nil:
		return nil, toConnect(ctx, err)
	}
	// The run also writes this row; whichever is second does nothing.
	err = ds.s.Store.InTenant(ctx, p.Tenant, func(tx *store.Tx) error {
		if err := deploy.InsertDeployment(ctx, tx, deploy.DeploymentRow{
			ID: id, ReleaseID: r.release, EnvironmentID: r.env, RollbackOf: r.rollbackOf,
			Reason: r.reason, WorkflowID: workflowID, RunID: runID, Canary: r.canary,
		}, actor); err != nil {
			return err
		}
		head, err := deploymentRevision(ctx, tx, id)
		if err != nil {
			return err
		}
		if err := idem.record(ctx, tx, deploymentType, id, head); err != nil {
			return err
		}
		out, err = getDeployment(ctx, tx, id)
		return err
	})
	if err != nil {
		return nil, toConnect(ctx, err)
	}
	return out, nil
}

// closingWait bounds how long startRun waits for an App's finished run to close.
const closingWait = 5 * time.Second

// startRun starts the run. A run whose Deployment is already terminal is still open
// for a moment after its last state write; when every Deployment of the App is
// terminal, a refusal means that run is closing, so startRun retries briefly instead
// of refusing a deploy nothing blocks.
func (ds *deploymentService) startRun(ctx context.Context, tenant, app uuid.UUID, in deploy.Input) (string, string, error) {
	deadline := time.Now().Add(closingWait)
	for {
		workflowID, runID, err := ds.s.Deploys.Start(ctx, in)
		if !errors.Is(err, deploy.ErrRunning) || time.Now().After(deadline) {
			return workflowID, runID, err
		}
		var open bool
		if qerr := ds.s.Store.InTenant(ctx, tenant, func(tx *store.Tx) error {
			return tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM deployments_heads d
				JOIN deployments_revisions r ON r.id = d.revision_id
				JOIN releases_heads rh ON rh.id = d.release_id
				WHERE rh.app_id = $1 AND NOT d.deleted
				  AND r.state NOT IN ('succeeded', 'failed', 'rolled_back', 'cancelled'))`, app).Scan(&open)
		}); qerr != nil || open {
			return workflowID, runID, err
		}
		select {
		case <-ctx.Done():
			return "", "", ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func deploymentRevision(ctx context.Context, tx *store.Tx, id uuid.UUID) (uuid.UUID, error) {
	var rev uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT revision_id FROM deployments_heads WHERE id = $1`, id).Scan(&rev); err != nil {
		return uuid.Nil, fmt.Errorf("read deployment revision: %w", err)
	}
	return rev, nil
}

func (ds *deploymentService) GetDeployment(ctx context.Context, req *connect.Request[bohuxv1.GetDeploymentRequest]) (*connect.Response[bohuxv1.GetDeploymentResponse], error) {
	id, err := parseID("id", req.Msg.GetId())
	if err != nil {
		return nil, err
	}
	d, err := ds.read(ctx, id, auth.RoleViewer)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&bohuxv1.GetDeploymentResponse{Deployment: d}), nil
}

// read returns a Deployment the caller holds min on.
func (ds *deploymentService) read(ctx context.Context, id uuid.UUID, min auth.Role) (*bohuxv1.Deployment, error) {
	p, _ := auth.FromContext(ctx)
	var out *bohuxv1.Deployment
	err := ds.s.Store.InTenant(ctx, p.Tenant, func(tx *store.Tx) error {
		d, err := getDeployment(ctx, tx, id)
		if err != nil {
			return err
		}
		env, err := uuid.Parse(d.GetEnvironmentId())
		if err != nil {
			return fmt.Errorf("stored environment id: %w", err)
		}
		project, err := environmentProject(ctx, tx, env)
		if err != nil {
			return err
		}
		if err := authorizeProject(ctx, project, min); err != nil {
			return err
		}
		out = d
		return nil
	})
	if err != nil {
		return nil, toConnect(ctx, err)
	}
	return out, nil
}

// ListDeployments lists an environment's Deployments, newest first.
func (ds *deploymentService) ListDeployments(ctx context.Context, req *connect.Request[bohuxv1.ListDeploymentsRequest]) (*connect.Response[bohuxv1.ListDeploymentsResponse], error) {
	p, _ := auth.FromContext(ctx)
	env, err := parseID("environment_id", req.Msg.GetEnvironmentId())
	if err != nil {
		return nil, err
	}
	var app *uuid.UUID
	if req.Msg.GetAppId() != "" {
		id, err := parseID("app_id", req.Msg.GetAppId())
		if err != nil {
			return nil, err
		}
		app = &id
	}
	size, before, err := page(req.Msg.GetPageSize(), req.Msg.GetPageToken())
	if err != nil {
		return nil, err
	}
	resp := &bohuxv1.ListDeploymentsResponse{}
	err = ds.s.Store.InTenant(ctx, p.Tenant, func(tx *store.Tx) error {
		project, err := environmentProject(ctx, tx, env)
		if err != nil {
			return err
		}
		if err := authorizeProject(ctx, project, auth.RoleViewer); err != nil {
			return err
		}
		// IDs are UUIDv7, so descending ID order is newest first.
		rows, err := tx.Query(ctx, deploymentSelect+`
			JOIN releases_heads rh ON rh.id = h.release_id
			WHERE NOT h.deleted AND h.environment_id = $1 AND ($2::uuid IS NULL OR rh.app_id = $2)
			  AND ($3::uuid IS NULL OR h.id < $3)
			ORDER BY h.id DESC LIMIT $4`, env, app, before, size+1)
		if err != nil {
			return fmt.Errorf("list deployments: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			d, err := scanDeployment(rows)
			if err != nil {
				return err
			}
			resp.Deployments = append(resp.Deployments, d)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, toConnect(ctx, err)
	}
	if len(resp.Deployments) > size {
		resp.Deployments = resp.Deployments[:size]
		resp.NextPageToken = resp.Deployments[size-1].GetMeta().GetId()
	}
	return connect.NewResponse(resp), nil
}

// WatchDeployment sends the Deployment, then each new revision, until it is terminal.
func (ds *deploymentService) WatchDeployment(ctx context.Context, req *connect.Request[bohuxv1.WatchDeploymentRequest], stream *connect.ServerStream[bohuxv1.WatchDeploymentResponse]) error {
	id, err := parseID("id", req.Msg.GetId())
	if err != nil {
		return err
	}
	var sent string
	for {
		d, err := ds.read(ctx, id, auth.RoleViewer)
		if err != nil {
			return err
		}
		if rev := d.GetMeta().GetRevisionId(); rev != sent {
			if err := stream.Send(&bohuxv1.WatchDeploymentResponse{Deployment: d}); err != nil {
				return err
			}
			sent = rev
		}
		if terminal(d.GetState()) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(watchInterval):
		}
	}
}

// CancelDeployment asks the run to stop; the workflow compensates if it assigned
// anything (ADR-0026 item 8). A terminal Deployment is returned unchanged.
func (ds *deploymentService) CancelDeployment(ctx context.Context, req *connect.Request[bohuxv1.CancelDeploymentRequest]) (*connect.Response[bohuxv1.CancelDeploymentResponse], error) {
	p, _ := auth.FromContext(ctx)
	id, err := parseID("id", req.Msg.GetId())
	if err != nil {
		return nil, err
	}
	if req.Msg.GetIdempotencyKey() == "" {
		return nil, invalidArgument("idempotency_key", "REQUIRED", "idempotency_key is required")
	}
	d, err := ds.read(ctx, id, auth.RoleDeployer)
	if err != nil {
		return nil, err
	}
	if terminal(d.GetState()) {
		return connect.NewResponse(&bohuxv1.CancelDeploymentResponse{Deployment: d}), nil
	}
	if ds.s.Deploys == nil {
		return nil, errNoDeploys
	}
	var runID string
	err = ds.s.Store.InTenant(ctx, p.Tenant, func(tx *store.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT r.workflow_run_id FROM deployments_heads h
			JOIN deployments_revisions r ON r.id = h.revision_id WHERE h.id = $1`, id).Scan(&runID); err != nil {
			return fmt.Errorf("read run: %w", err)
		}
		env, err := uuid.Parse(d.GetEnvironmentId())
		if err != nil {
			return fmt.Errorf("stored environment id: %w", err)
		}
		project, err := environmentProject(ctx, tx, env)
		if err != nil {
			return err
		}
		// The request changes no row itself; the workflow writes the states that follow.
		return tx.AuditOnly(ctx, store.Audit{Actor: p.Actor(),
			Action:       bohuxv1connect.DeploymentServiceCancelDeploymentProcedure,
			ResourceType: deploymentType, ProjectID: project}, id)
	})
	if err != nil {
		return nil, toConnect(ctx, err)
	}
	if err := ds.s.Deploys.Cancel(ctx, d.GetWorkflowId(), runID); err != nil {
		if errors.Is(err, deploy.ErrUnavailable) {
			return nil, errNoDeploys
		}
		return nil, toConnect(ctx, err)
	}
	return connect.NewResponse(&bohuxv1.CancelDeploymentResponse{Deployment: d}), nil
}

// PromoteDeployment ends a canary's bake now (ADR-0049). The run assigns the Release
// with no weight and writes the states that follow.
func (ds *deploymentService) PromoteDeployment(ctx context.Context, req *connect.Request[bohuxv1.PromoteDeploymentRequest]) (*connect.Response[bohuxv1.PromoteDeploymentResponse], error) {
	p, _ := auth.FromContext(ctx)
	id, err := parseID("id", req.Msg.GetId())
	if err != nil {
		return nil, err
	}
	if req.Msg.GetIdempotencyKey() == "" {
		return nil, invalidArgument("idempotency_key", "REQUIRED", "idempotency_key is required")
	}
	d, err := ds.read(ctx, id, auth.RoleDeployer)
	if err != nil {
		return nil, err
	}
	if d.GetState() != bohuxv1.DeploymentState_DEPLOYMENT_STATE_CANARY {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("only a deployment in canary can be promoted"))
	}
	if ds.s.Deploys == nil {
		return nil, errNoDeploys
	}
	var runID string
	err = ds.s.Store.InTenant(ctx, p.Tenant, func(tx *store.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT r.workflow_run_id FROM deployments_heads h
			JOIN deployments_revisions r ON r.id = h.revision_id WHERE h.id = $1`, id).Scan(&runID); err != nil {
			return fmt.Errorf("read run: %w", err)
		}
		env, err := uuid.Parse(d.GetEnvironmentId())
		if err != nil {
			return fmt.Errorf("stored environment id: %w", err)
		}
		project, err := environmentProject(ctx, tx, env)
		if err != nil {
			return err
		}
		return tx.AuditOnly(ctx, store.Audit{Actor: p.Actor(),
			Action:       bohuxv1connect.DeploymentServicePromoteDeploymentProcedure,
			ResourceType: deploymentType, ProjectID: project}, id)
	})
	if err != nil {
		return nil, toConnect(ctx, err)
	}
	if err := ds.s.Deploys.Promote(ctx, d.GetWorkflowId(), runID); err != nil {
		if errors.Is(err, deploy.ErrUnavailable) {
			return nil, errNoDeploys
		}
		return nil, toConnect(ctx, err)
	}
	return connect.NewResponse(&bohuxv1.PromoteDeploymentResponse{Deployment: d}), nil
}

func terminal(s bohuxv1.DeploymentState) bool {
	switch s {
	case bohuxv1.DeploymentState_DEPLOYMENT_STATE_SUCCEEDED, bohuxv1.DeploymentState_DEPLOYMENT_STATE_FAILED,
		bohuxv1.DeploymentState_DEPLOYMENT_STATE_ROLLED_BACK, bohuxv1.DeploymentState_DEPLOYMENT_STATE_CANCELLED:
		return true
	}
	return false
}

const deploymentSelect = `SELECT ` + metaColumns + `, h.release_id, h.environment_id, h.strategy,
	h.rollback_of, h.reason, r.state, r.workflow_id, r.started_at, r.finished_at, r.failure,
	h.canary_weight, h.canary_bake_seconds
	FROM deployments_heads h JOIN deployments_revisions r ON r.id = h.revision_id`

var deploymentStates = map[string]bohuxv1.DeploymentState{
	"pending":           bohuxv1.DeploymentState_DEPLOYMENT_STATE_PENDING,
	"building":          bohuxv1.DeploymentState_DEPLOYMENT_STATE_BUILDING,
	"awaiting_approval": bohuxv1.DeploymentState_DEPLOYMENT_STATE_AWAITING_APPROVAL,
	"applying":          bohuxv1.DeploymentState_DEPLOYMENT_STATE_APPLYING,
	"health_gate":       bohuxv1.DeploymentState_DEPLOYMENT_STATE_HEALTH_GATE,
	"canary":            bohuxv1.DeploymentState_DEPLOYMENT_STATE_CANARY,
	"verifying":         bohuxv1.DeploymentState_DEPLOYMENT_STATE_VERIFYING,
	"succeeded":         bohuxv1.DeploymentState_DEPLOYMENT_STATE_SUCCEEDED,
	"failed":            bohuxv1.DeploymentState_DEPLOYMENT_STATE_FAILED,
	"rolled_back":       bohuxv1.DeploymentState_DEPLOYMENT_STATE_ROLLED_BACK,
	"compensating":      bohuxv1.DeploymentState_DEPLOYMENT_STATE_COMPENSATING,
	"cancelled":         bohuxv1.DeploymentState_DEPLOYMENT_STATE_CANCELLED,
}

func getDeployment(ctx context.Context, tx *store.Tx, id uuid.UUID) (*bohuxv1.Deployment, error) {
	d, err := scanDeployment(tx.QueryRow(ctx, deploymentSelect+` WHERE h.id = $1 AND NOT h.deleted`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	return d, err
}

func scanDeployment(row pgx.Row) (*bohuxv1.Deployment, error) {
	var (
		m                 metaRow
		release, env      uuid.UUID
		strategy, state   string
		rollbackOf        *uuid.UUID
		started, finished *time.Time
		failure           []byte
		weight, bake      *int32
		d                 bohuxv1.Deployment
	)
	if err := row.Scan(append(m.dest(), &release, &env, &strategy, &rollbackOf, &d.Reason, &state,
		&d.WorkflowId, &started, &finished, &failure, &weight, &bake)...); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
		return nil, fmt.Errorf("read deployment: %w", err)
	}
	d.Meta, d.ReleaseId, d.EnvironmentId = m.proto(), release.String(), env.String()
	d.Strategy = bohuxv1.DeploymentStrategy_DEPLOYMENT_STRATEGY_RECREATE
	if strategy == "canary" && weight != nil && bake != nil {
		d.Strategy = bohuxv1.DeploymentStrategy_DEPLOYMENT_STRATEGY_CANARY
		d.Canary = &bohuxv1.Canary{WeightPercent: uint32(*weight),
			Bake: durationpb.New(time.Duration(*bake) * time.Second)}
	}
	d.State, d.StartedAt, d.FinishedAt = deploymentStates[state], tsOrNil(started), tsOrNil(finished)
	if rollbackOf != nil {
		d.RollbackOf = rollbackOf.String()
	}
	if failure != nil {
		d.Failure = &bohuxv1.ErrorDetail{}
		if err := loadJSON.Unmarshal(failure, d.Failure); err != nil {
			return nil, fmt.Errorf("decode deployment failure: %w", err)
		}
	}
	return &d, nil
}
