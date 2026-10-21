package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/url"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	bohuxv1 "github.com/bohumix/bohux/gen/bohux/v1"
	"github.com/bohumix/bohux/gen/bohux/v1/bohuxv1connect"
	"github.com/bohumix/bohux/internal/auth"
	"github.com/bohumix/bohux/internal/notify"
	"github.com/bohumix/bohux/internal/secrets/openbao"
	"github.com/bohumix/bohux/internal/store"
)

const channelType = "bohux.v1.NotificationChannel"

type notificationService struct {
	s *Server
}

var (
	channelKinds = map[bohuxv1.NotificationKind]string{
		bohuxv1.NotificationKind_NOTIFICATION_KIND_SLACK:   "slack",
		bohuxv1.NotificationKind_NOTIFICATION_KIND_DISCORD: "discord",
		bohuxv1.NotificationKind_NOTIFICATION_KIND_WEBHOOK: "webhook",
		bohuxv1.NotificationKind_NOTIFICATION_KIND_EMAIL:   "email",
	}
	terminalStates = map[bohuxv1.DeploymentState]string{
		bohuxv1.DeploymentState_DEPLOYMENT_STATE_SUCCEEDED:   "succeeded",
		bohuxv1.DeploymentState_DEPLOYMENT_STATE_FAILED:      "failed",
		bohuxv1.DeploymentState_DEPLOYMENT_STATE_ROLLED_BACK: "rolled_back",
		bohuxv1.DeploymentState_DEPLOYMENT_STATE_CANCELLED:   "cancelled",
	}
	defaultStates = []string{"failed", "rolled_back", "cancelled"}
)

// channelURL checks the URL without ever putting it in an error (rule 3): https only,
// so the control plane never posts to a plain-HTTP service beside it; for email, a
// mailto: list whose domains are shown (ADR-0043).
func channelURL(kind, raw string) (host, canonical string, err error) {
	if kind == "email" {
		to, rerr := notify.Recipients(raw)
		if rerr != nil {
			return "", "", invalidArgument("url", "INVALID", "url must be one to ten plain addresses (mailto: optional): "+rerr.Error())
		}
		return notify.Domains(to), notify.MailtoURL(to), nil
	}
	u, perr := url.Parse(raw)
	if perr != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil {
		return "", "", invalidArgument("url", "INVALID", "url must be an https:// URL without credentials")
	}
	return u.Hostname(), raw, nil
}

func (ns *notificationService) CreateNotificationChannel(ctx context.Context, req *connect.Request[bohuxv1.CreateNotificationChannelRequest]) (*connect.Response[bohuxv1.CreateNotificationChannelResponse], error) {
	msg := req.Msg
	p, _ := auth.FromContext(ctx)
	if ns.s.Secrets == nil {
		return nil, errNoSecrets
	}
	project, err := parseID("project_id", msg.GetProjectId())
	if err != nil {
		return nil, err
	}
	kind, ok := channelKinds[msg.GetKind()]
	if !ok {
		return nil, invalidArgument("kind", "REQUIRED", "kind must be slack, discord, webhook, or email")
	}
	if kind == "email" && !ns.s.MailRelay {
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			errors.New("this install has no SMTP relay; its operator sets --smtp-addr (ADR-0043)"))
	}
	host, target, err := channelURL(kind, msg.GetUrl())
	if err != nil {
		return nil, err
	}
	states := defaultStates
	if len(msg.GetStates()) > 0 {
		states = nil
		seen := map[string]bool{}
		for i, st := range msg.GetStates() {
			name, ok := terminalStates[st]
			if !ok {
				return nil, invalidArgument(fmt.Sprintf("states[%d]", i), "INVALID", "only terminal deployment states can be posted")
			}
			if !seen[name] {
				seen[name] = true
				states = append(states, name)
			}
		}
	}
	idem, err := newIdempotency(ctx, bohuxv1connect.NotificationServiceCreateNotificationChannelProcedure, msg.GetIdempotencyKey(), msg)
	if err != nil {
		return nil, toConnect(ctx, err)
	}
	resp := &bohuxv1.CreateNotificationChannelResponse{}
	err = ns.s.Store.InTenant(ctx, p.Tenant, func(tx *store.Tx) error {
		prior, err := idem.lookup(ctx, tx)
		if err != nil {
			return err
		}
		if prior != nil {
			resp.Channel, err = getChannel(ctx, tx, prior.ResourceID)
			return err
		}
		if err := ensureProject(ctx, tx, project); err != nil {
			return err
		}
		return authorizeProject(ctx, project, auth.RoleOwner)
	})
	if err != nil || resp.Channel != nil {
		return connectResp(resp, toConnect(ctx, err))
	}
	id, err := store.NewID()
	if err != nil {
		return nil, toConnect(ctx, err)
	}
	if _, err := ns.s.Secrets.Put(ctx, notify.URLPath(p.Tenant, id), target); err != nil {
		slog.ErrorContext(ctx, "store notification url", "channel", id.String(), "err", err)
		return nil, errNoSecrets
	}
	var signing *string
	if kind == "webhook" {
		raw := make([]byte, 32)
		if _, err := rand.Read(raw); err != nil {
			return nil, toConnect(ctx, fmt.Errorf("generate signing secret: %w", err))
		}
		resp.SigningSecret = hex.EncodeToString(raw)
		if _, err := ns.s.Secrets.Put(ctx, notify.SigningPath(p.Tenant, id), resp.SigningSecret); err != nil {
			slog.ErrorContext(ctx, "store notification signing secret", "channel", id.String(), "err", err)
			ns.destroy(ctx, p.Tenant, id)
			return nil, errNoSecrets
		}
		h := openbao.HandlePrefix + notify.SigningPath(p.Tenant, id)
		signing = &h
	}
	err = ns.s.Store.InTenant(ctx, p.Tenant, func(tx *store.Tx) error {
		rev, err := tx.CreateWithID(ctx, "notification_channels", id, []store.Col{
			{Name: "project_id", Value: project}, {Name: "kind", Value: kind}, {Name: "states", Value: states},
			{Name: "url_handle", Value: openbao.HandlePrefix + notify.URLPath(p.Tenant, id)},
			{Name: "url_host", Value: host}, {Name: "signing_handle", Value: signing},
		}, nil, store.Audit{Actor: p.Actor(), Action: bohuxv1connect.NotificationServiceCreateNotificationChannelProcedure,
			ResourceType: channelType, ProjectID: project})
		if err != nil {
			return err
		}
		if err := idem.record(ctx, tx, channelType, id, rev); err != nil {
			return err
		}
		resp.Channel, err = getChannel(ctx, tx, id)
		return err
	})
	if err != nil {
		ns.destroy(ctx, p.Tenant, id)
		return nil, toConnect(ctx, err)
	}
	return connect.NewResponse(resp), nil
}

// destroy removes a channel's credentials from OpenBao, best effort.
func (ns *notificationService) destroy(ctx context.Context, tenant, id uuid.UUID) {
	for _, path := range []string{notify.URLPath(tenant, id), notify.SigningPath(tenant, id)} {
		if err := ns.s.Secrets.Destroy(context.WithoutCancel(ctx), path); err != nil {
			slog.WarnContext(ctx, "destroy notification credential", "channel", id.String(), "err", err)
		}
	}
}

func (ns *notificationService) ListNotificationChannels(ctx context.Context, req *connect.Request[bohuxv1.ListNotificationChannelsRequest]) (*connect.Response[bohuxv1.ListNotificationChannelsResponse], error) {
	p, _ := auth.FromContext(ctx)
	project, err := parseID("project_id", req.Msg.GetProjectId())
	if err != nil {
		return nil, err
	}
	size, after, err := page(req.Msg.GetPageSize(), req.Msg.GetPageToken())
	if err != nil {
		return nil, err
	}
	resp := &bohuxv1.ListNotificationChannelsResponse{}
	err = ns.s.Store.InTenant(ctx, p.Tenant, func(tx *store.Tx) error {
		if err := ensureProject(ctx, tx, project); err != nil {
			return err
		}
		if err := authorizeProject(ctx, project, auth.RoleViewer); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, channelSelect+` WHERE NOT h.deleted AND h.project_id = $1
			AND ($2::uuid IS NULL OR h.id > $2) ORDER BY h.id LIMIT $3`, project, after, size+1)
		if err != nil {
			return fmt.Errorf("list notification channels: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			c, err := scanChannel(rows)
			if err != nil {
				return err
			}
			resp.Channels = append(resp.Channels, c)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, toConnect(ctx, err)
	}
	if len(resp.Channels) > size {
		resp.Channels = resp.Channels[:size]
		resp.NextPageToken = resp.Channels[size-1].GetMeta().GetId()
	}
	return connect.NewResponse(resp), nil
}

func (ns *notificationService) DeleteNotificationChannel(ctx context.Context, req *connect.Request[bohuxv1.DeleteNotificationChannelRequest]) (*connect.Response[bohuxv1.DeleteNotificationChannelResponse], error) {
	msg := req.Msg
	p, _ := auth.FromContext(ctx)
	id, err := parseID("id", msg.GetId())
	if err != nil {
		return nil, err
	}
	expected, err := requiredRevision(msg.GetExpectedRevisionId())
	if err != nil {
		return nil, err
	}
	idem, err := newIdempotency(ctx, bohuxv1connect.NotificationServiceDeleteNotificationChannelProcedure, msg.GetIdempotencyKey(), msg)
	if err != nil {
		return nil, toConnect(ctx, err)
	}
	resp := &bohuxv1.DeleteNotificationChannelResponse{}
	deleted := false
	err = ns.s.Store.InTenant(ctx, p.Tenant, func(tx *store.Tx) error {
		prior, err := idem.lookup(ctx, tx)
		if err != nil {
			return err
		}
		if prior != nil {
			resp.RevisionId = prior.RevisionID.String()
			return nil
		}
		c, err := getChannel(ctx, tx, id)
		if err != nil {
			return err
		}
		project, err := uuid.Parse(c.GetProjectId())
		if err != nil {
			return fmt.Errorf("stored project id: %w", err)
		}
		if err := authorizeProject(ctx, project, auth.RoleOwner); err != nil {
			return err
		}
		rev, err := tx.Delete(ctx, "notification_channels", id, expected, store.Audit{Actor: p.Actor(),
			Action: bohuxv1connect.NotificationServiceDeleteNotificationChannelProcedure, ResourceType: channelType, ProjectID: project})
		if err != nil {
			return err
		}
		resp.RevisionId, deleted = rev.String(), true
		return idem.record(ctx, tx, channelType, id, rev)
	})
	if err != nil {
		return nil, toConnect(ctx, err)
	}
	if deleted && ns.s.Secrets != nil {
		ns.destroy(ctx, p.Tenant, id)
	}
	return connect.NewResponse(resp), nil
}

const channelSelect = `SELECT ` + metaColumns + `, h.project_id, h.kind, h.states, h.url_host
	FROM notification_channels_heads h JOIN notification_channels_revisions r ON r.id = h.revision_id`

func getChannel(ctx context.Context, tx *store.Tx, id uuid.UUID) (*bohuxv1.NotificationChannel, error) {
	c, err := scanChannel(tx.QueryRow(ctx, channelSelect+` WHERE h.id = $1 AND NOT h.deleted`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	return c, err
}

func scanChannel(row pgx.Row) (*bohuxv1.NotificationChannel, error) {
	var (
		m       metaRow
		project uuid.UUID
		kind    string
		states  []string
		c       bohuxv1.NotificationChannel
	)
	if err := row.Scan(append(m.dest(), &project, &kind, &states, &c.UrlHost)...); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
		return nil, fmt.Errorf("read notification channel: %w", err)
	}
	c.Meta, c.ProjectId = m.proto(), project.String()
	for k, name := range channelKinds {
		if name == kind {
			c.Kind = k
		}
	}
	for _, s := range states {
		for st, name := range terminalStates {
			if name == s {
				c.States = append(c.States, st)
			}
		}
	}
	return &c, nil
}

// ensureProject reports ErrNotFound for a project the tenant does not have.
func ensureProject(ctx context.Context, tx *store.Tx, project uuid.UUID) error {
	var ok bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM projects_heads WHERE id = $1 AND NOT deleted)`,
		project).Scan(&ok); err != nil {
		return fmt.Errorf("look up project: %w", err)
	}
	if !ok {
		return store.ErrNotFound
	}
	return nil
}
