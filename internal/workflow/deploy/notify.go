package deploy

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	"github.com/bohumix/bohux/internal/notify"
	"github.com/bohumix/bohux/internal/store"
)

// SecretReader reads a channel's credentials from OpenBao (ADR-0038).
type SecretReader interface {
	Get(ctx context.Context, path string, version int) (string, error)
}

// NotifyInput names a finished Deployment. IDs only: the channels' URLs are read in
// the activity, never put in workflow history (rule 3).
type NotifyInput struct {
	TenantID     string
	DeploymentID string
	ChannelID    string
	State        State
	Failure      *Failure
	StartedBy    string
}

// notifyEnd posts the run's outcome to its project's channels: one activity lists
// them, one per channel posts, each retried on its own. A notification never changes
// the outcome, so every error ends here (ADR-0038 item 3).
func (d *run) notifyEnd(ctx workflow.Context, res Result) {
	if workflow.GetVersion(ctx, "notify-channels", workflow.DefaultVersion, 1) != 1 {
		return
	}
	dctx, _ := workflow.NewDisconnectedContext(ctx)
	log := workflow.GetLogger(dctx)
	var a *Activities
	in := NotifyInput{TenantID: d.in.TenantID, DeploymentID: d.in.DeploymentID, State: res.State,
		Failure: res.Failure, StartedBy: d.in.Actor.Kind}
	var channels []string
	if err := workflow.ExecuteActivity(storeCtx(dctx), a.NotificationChannels, in).Get(dctx, &channels); err != nil {
		log.Warn("list notification channels", "error", err)
		return
	}
	send := workflow.WithActivityOptions(dctx, workflow.ActivityOptions{
		StartToCloseTimeout: 30 * time.Second,
		RetryPolicy: &temporal.RetryPolicy{InitialInterval: 5 * time.Second, BackoffCoefficient: 2,
			MaximumAttempts: 5},
	})
	futures := make([]workflow.Future, len(channels))
	for i, ch := range channels {
		c := in
		c.ChannelID = ch
		futures[i] = workflow.ExecuteActivity(send, a.NotifyChannel, c)
	}
	for i, f := range futures {
		if err := f.Get(dctx, nil); err != nil {
			log.Warn("notification not delivered", "channel", channels[i], "error", err)
		}
	}
}

// NotificationChannels lists the channels of the Deployment's project that want its
// state.
func (a *Activities) NotificationChannels(ctx context.Context, in NotifyInput) ([]string, error) {
	ids, err := parseIDs(in.TenantID, in.DeploymentID)
	if err != nil {
		return nil, err
	}
	var out []string
	err = a.Store.InTenant(ctx, ids[0], func(tx *store.Tx) error {
		rows, err := tx.Query(ctx, `SELECT c.id::text FROM deployments_heads d
			JOIN environments_heads e ON e.id = d.environment_id
			JOIN notification_channels_heads c ON c.project_id = e.project_id AND NOT c.deleted
			WHERE d.id = $1 AND $2 = ANY (c.states) ORDER BY c.id`, ids[1], string(in.State))
		if err != nil {
			return fmt.Errorf("list notification channels: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return fmt.Errorf("list notification channels: %w", err)
			}
			out = append(out, id)
		}
		return rows.Err()
	})
	return out, err
}

// NotifyChannel posts the outcome to one channel. A channel deleted meanwhile is
// skipped.
func (a *Activities) NotifyChannel(ctx context.Context, in NotifyInput) error {
	ids, err := parseIDs(in.TenantID, in.DeploymentID, in.ChannelID)
	if err != nil {
		return err
	}
	if a.Secrets == nil {
		return temporal.NewNonRetryableApplicationError("no secrets backend for notification URLs", ErrTypeInvalid, nil)
	}
	o := notify.Outcome{DeploymentID: in.DeploymentID, State: string(in.State), StartedBy: in.StartedBy}
	if in.Failure != nil {
		o.Reason = in.Failure.Reason
	}
	var kind string
	var signed bool
	err = a.Store.InTenant(ctx, ids[0], func(tx *store.Tx) error {
		err := tx.QueryRow(ctx, `SELECT c.kind, c.signing_handle IS NOT NULL FROM notification_channels_heads c
			WHERE c.id = $1 AND NOT c.deleted`, ids[2]).Scan(&kind, &signed)
		if err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT p.slug, e.slug, a.slug, d.release_id::text
			FROM deployments_heads d
			JOIN environments_heads e ON e.id = d.environment_id
			JOIN projects_heads p ON p.id = e.project_id
			JOIN releases_heads r ON r.id = d.release_id
			JOIN apps_heads a ON a.id = r.app_id
			WHERE d.id = $1`, ids[1]).Scan(&o.Project, &o.Environment, &o.App, &o.ReleaseID)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read notification: %w", err)
	}
	ch := notify.Channel{Kind: kind}
	if ch.URL, err = a.Secrets.Get(ctx, notify.URLPath(ids[0], ids[2]), 0); err != nil {
		return fmt.Errorf("read channel url: %w", err)
	}
	if signed {
		if ch.Signing, err = a.Secrets.Get(ctx, notify.SigningPath(ids[0], ids[2]), 0); err != nil {
			return fmt.Errorf("read channel signing secret: %w", err)
		}
	}
	if kind == "email" {
		to, err := notify.Recipients(ch.URL)
		if err != nil {
			return temporal.NewNonRetryableApplicationError("the channel's recipients are invalid", ErrTypeInvalid, nil)
		}
		if !a.Mail.Enabled() {
			// The relay was removed after the channel was made; retrying cannot help.
			return temporal.NewNonRetryableApplicationError("this install has no SMTP relay", ErrTypeInvalid, nil)
		}
		if err := notify.SendEmail(ctx, a.Mail, to, o); err != nil {
			if errors.Is(err, notify.ErrPermanent) {
				return temporal.NewNonRetryableApplicationError(err.Error(), ErrTypeInvalid, nil)
			}
			return err
		}
		return nil
	}
	return notify.Send(ctx, a.HTTP, ch, o)
}
