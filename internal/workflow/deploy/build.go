package deploy

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	agentv1 "github.com/bohumix/bohux/gen/bohux/agent/v1"
	bohuxv1 "github.com/bohumix/bohux/gen/bohux/v1"
	"github.com/bohumix/bohux/internal/store"
)

const (
	releaseType = "bohux.v1.Release"
	buildAction = "bohux.workflow/Build"
	// buildTimeout bounds one build, retries included.
	buildTimeout = 2 * time.Hour
)

// BuildWorkflowID is the ID of a Release's build run (ADR-0028).
func BuildWorkflowID(releaseID string) string { return "build/" + releaseID }

// BuildInput starts a build. The ref is the one asked for; the App revision the
// Release captured supplies the repository and subpath.
type BuildInput struct {
	TenantID  string
	ReleaseID string
	Ref       string
	Actor     Actor
}

// BuildOutcome is how a build ended: an image and commit, or a failure reason.
type BuildOutcome struct {
	ImageID string
	Commit  string
	Failure string
}

// ReleaseRef names a Release.
type ReleaseRef struct {
	TenantID  string
	ReleaseID string
}

// Dispatcher sends a build to a node and waits for it; agentapi.Server implements it.
type Dispatcher interface {
	Build(ctx context.Context, tenant, node uuid.UUID, req *agentv1.BuildRequest) (*agentv1.BuildResult, error)
}

// BuildWorkflow builds a Release on its App's node and records the outcome on the
// Release (ADR-0028). A failed build is a successful run: its outcome is recorded.
func BuildWorkflow(ctx workflow.Context, in BuildInput) (BuildOutcome, error) {
	var a *Activities
	opts := storeOptions
	opts.StartToCloseTimeout = time.Hour
	opts.ScheduleToCloseTimeout = buildTimeout
	opts.HeartbeatTimeout = 2 * healthHeartbeat
	var out BuildOutcome
	err := workflow.ExecuteActivity(workflow.WithActivityOptions(ctx, opts), a.RunBuild, in).Get(ctx, &out)
	switch {
	case isType(err, ErrTypeInvalid):
		out = BuildOutcome{Failure: agentBuildInvalid}
	case temporal.IsTimeoutError(err):
		out = BuildOutcome{Failure: "BUILD_TIMEOUT"}
	case err != nil:
		return BuildOutcome{}, err
	}
	if err := workflow.ExecuteActivity(storeCtx(ctx), a.FinishBuild, FinishInput{
		TenantID: in.TenantID, ReleaseID: in.ReleaseID, Actor: in.Actor, Outcome: out,
	}).Get(ctx, nil); err != nil {
		return BuildOutcome{}, err
	}
	return out, nil
}

// agentBuildInvalid matches agent.BuildInvalid without importing the agent.
const agentBuildInvalid = "BUILD_REQUEST_INVALID"

// FinishInput records a build's outcome on its Release.
type FinishInput struct {
	TenantID  string
	ReleaseID string
	Actor     Actor
	Outcome   BuildOutcome
}

// RunBuild sends the Release's build to the node of its App's environment and waits
// for the result, heartbeating. A node that is not connected is a retryable error;
// a build that ran and failed is an outcome, not an error.
func (a *Activities) RunBuild(ctx context.Context, in BuildInput) (BuildOutcome, error) {
	ids, err := parseIDs(in.TenantID, in.ReleaseID)
	if err != nil {
		return BuildOutcome{}, err
	}
	var (
		app, node uuid.UUID
		git       *bohuxv1.GitSource
		sshKey    *bohuxv1.SecretRef
	)
	err = a.Store.InTenant(ctx, ids[0], func(tx *store.Tx) error {
		var (
			env uuid.UUID
			src []byte
		)
		err := tx.QueryRow(ctx, `SELECT h.app_id, ap.environment_id, ar.source FROM releases_heads h
			JOIN apps_revisions ar ON ar.id = h.config_revision_id
			JOIN apps_heads ap ON ap.id = h.app_id
			WHERE h.id = $1 AND NOT h.deleted`, ids[1]).Scan(&app, &env, &src)
		if errors.Is(err, pgx.ErrNoRows) {
			// The API starts this run before it writes the Release (ADR-0026 item 3);
			// retry until the row exists or the build times out.
			return fmt.Errorf("release %s: %w", ids[1], store.ErrNotFound)
		}
		if err != nil {
			return fmt.Errorf("read release: %w", err)
		}
		source := &bohuxv1.Source{}
		if err := loadJSON.Unmarshal(src, source); err != nil {
			return fmt.Errorf("decode app source: %w", err)
		}
		if git = source.GetGit(); git == nil {
			return invalid("the release's app has no git source")
		}
		if key := git.GetSshKeySecret(); key != "" {
			// The key travels by handle; the relay seals its value for the node (ADR-0041).
			var handle string
			err := tx.QueryRow(ctx, `SELECT handle FROM secret_refs_heads
				WHERE environment_id = $1 AND key = $2 AND NOT deleted`, env, key).Scan(&handle)
			if errors.Is(err, pgx.ErrNoRows) {
				return invalid("the app's deploy key secret " + key + " no longer exists")
			}
			if err != nil {
				return fmt.Errorf("read deploy key secret: %w", err)
			}
			sshKey = &bohuxv1.SecretRef{Key: key, Handle: handle}
		}
		node, err = appNode(ctx, tx, app)
		return err
	})
	if app := applicationError(err); app != nil {
		return BuildOutcome{}, app
	}
	if err != nil {
		return BuildOutcome{}, err
	}
	ref := in.Ref
	if ref == "" {
		ref = git.GetRef()
	}

	beat, stop := context.WithCancel(ctx)
	defer stop()
	go func() {
		t := time.NewTicker(healthHeartbeat / 3)
		defer t.Stop()
		for {
			select {
			case <-beat.Done():
				return
			case <-t.C:
				activity.RecordHeartbeat(ctx)
			}
		}
	}()
	res, err := a.Builds.Build(ctx, ids[0], node, &agentv1.BuildRequest{
		ReleaseId: in.ReleaseID, AppId: app.String(), RepoUrl: git.GetRepoUrl(), Ref: ref, Subpath: git.GetSubpath(),
		SshKey: sshKey,
	})
	if err != nil {
		// Including agentapi.ErrNodeOffline: Temporal retries until the node is back.
		return BuildOutcome{}, fmt.Errorf("build release %s: %w", in.ReleaseID, err)
	}
	if f := res.GetFailure(); f != nil {
		return BuildOutcome{Failure: f.GetReason()}, nil
	}
	return BuildOutcome{ImageID: res.GetImageId(), Commit: res.GetCommit()}, nil
}

// FinishBuild writes the Release ready, with its image and commit, or failed. A
// Release that is no longer building is left as it is.
func (a *Activities) FinishBuild(ctx context.Context, in FinishInput) error {
	ids, err := parseIDs(in.TenantID, in.ReleaseID)
	if err != nil {
		return err
	}
	actor, err := in.Actor.store()
	if err != nil {
		return err
	}
	err = a.Store.InTenant(ctx, ids[0], func(tx *store.Tx) error {
		var revision uuid.UUID
		err := tx.QueryRow(ctx, `SELECT revision_id FROM releases_heads WHERE id = $1 AND NOT deleted
			FOR UPDATE`, ids[1]).Scan(&revision)
		if errors.Is(err, pgx.ErrNoRows) {
			return invalid("release does not exist")
		}
		if err != nil {
			return fmt.Errorf("lock release: %w", err)
		}
		var state, sourceRef, workflowID string
		var project uuid.UUID
		if err := tx.QueryRow(ctx, `SELECT r.state, r.source_ref, r.build_workflow_id, e.project_id
			FROM releases_revisions r, releases_heads h
			JOIN apps_heads ap ON ap.id = h.app_id
			JOIN environments_heads e ON e.id = ap.environment_id
			WHERE r.id = $1 AND h.id = $2`, revision, ids[1]).Scan(&state, &sourceRef, &workflowID, &project); err != nil {
			return fmt.Errorf("read release: %w", err)
		}
		if state != "building" {
			return nil
		}
		cols := []store.Col{{Name: "build_workflow_id", Value: workflowID}}
		if in.Outcome.Failure != "" {
			failure, err := storeJSON.Marshal(&bohuxv1.ErrorDetail{Reason: in.Outcome.Failure})
			if err != nil {
				return fmt.Errorf("encode failure: %w", err)
			}
			cols = append(cols, store.Col{Name: "state", Value: "failed"},
				store.Col{Name: "source_ref", Value: sourceRef}, store.Col{Name: "failure", Value: failure})
		} else {
			cols = append(cols, store.Col{Name: "state", Value: "ready"},
				store.Col{Name: "artifact_digest", Value: in.Outcome.ImageID},
				store.Col{Name: "source_ref", Value: in.Outcome.Commit},
				store.Col{Name: "artifact_available", Value: true})
		}
		_, err = tx.Update(ctx, "releases", ids[1], revision, cols,
			store.Audit{Actor: actor, Action: buildAction, ResourceType: releaseType, ProjectID: project})
		return err
	})
	if app := applicationError(err); app != nil {
		return app
	}
	if err != nil {
		return fmt.Errorf("finish build of release %s: %w", in.ReleaseID, err)
	}
	return nil
}

// WaitReleaseReady polls the Release until it is ready. A failed build fails it with
// ErrTypeBuildFailed.
func (a *Activities) WaitReleaseReady(ctx context.Context, in ReleaseRef) error {
	ids, err := parseIDs(in.TenantID, in.ReleaseID)
	if err != nil {
		return err
	}
	interval := a.PollInterval
	if interval <= 0 {
		interval = defaultPollInterval
	}
	for {
		var state string
		err := a.Store.InTenant(ctx, ids[0], func(tx *store.Tx) error {
			return tx.QueryRow(ctx, `SELECT r.state FROM releases_heads h
				JOIN releases_revisions r ON r.id = h.revision_id WHERE h.id = $1`, ids[1]).Scan(&state)
		})
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			return invalid("release does not exist")
		case err != nil:
			return fmt.Errorf("read release state: %w", err)
		case state == "ready":
			return nil
		case state == "failed":
			return temporal.NewNonRetryableApplicationError("the release's build failed", ErrTypeBuildFailed, nil)
		}
		activity.RecordHeartbeat(ctx)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
		}
	}
}
