package api

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/timestamppb"

	bohuxv1 "github.com/bohumix/bohux/gen/bohux/v1"
	"github.com/bohumix/bohux/internal/auth"
	"github.com/bohumix/bohux/internal/store"
)

// history reads one resource type's past revisions: its current-state select, joined
// on a chosen revision instead of the head's (ADR-0017: full snapshots).
type history struct {
	table string
	sel   string
	scan  func(pgx.Row) (proto.Message, error)
}

func scanAs[M proto.Message](f func(pgx.Row) (M, error)) func(pgx.Row) (proto.Message, error) {
	return func(row pgx.Row) (proto.Message, error) { return f(row) }
}

var histories = map[string]history{
	"bohux.v1.App":                 {"apps", appSelect, scanAs(scanApp)},
	"bohux.v1.Deployment":          {"deployments", deploymentSelect, scanAs(scanDeployment)},
	"bohux.v1.Environment":         {"environments", environmentSelect, scanAs(scanEnvironment)},
	"bohux.v1.ApiToken":            {"api_tokens", apiTokenSelect, scanAs(scanAPIToken)},
	"bohux.v1.Node":                {"nodes", nodeSelect, scanAs(scanNode)},
	"bohux.v1.NotificationChannel": {"notification_channels", channelSelect, scanAs(scanChannel)},
	"bohux.v1.Project":             {"projects", projectSelect, scanAs(scanProject)},
	"bohux.v1.Release":             {"releases", releaseSelect, scanAs(scanRelease)},
	"bohux.v1.Secret":              {"secret_refs", secretSelect, scanAs(scanSecret)},
	"bohux.v1.Webhook":             {"webhooks", webhookSelect, scanAs(scanWebhook)},
}

// at turns a current-state select into one that reads revision $1 of its resource,
// deleted or not, with the revision's own ID in the snapshot's meta.
func (h history) at() string {
	q := strings.Replace(h.sel, "r.id = h.revision_id", "r.resource_id = h.id", 1)
	q = strings.Replace(q, "h.revision_id,", "r.id,", 1)
	return q + ` WHERE r.id = $1`
}

type auditReader struct {
	s *Server
}

// revisionRow is a revision's record, without its snapshot.
type revisionRow struct {
	id, resource uuid.UUID
	parent       *uuid.UUID
	at           time.Time
	actor        uuid.UUID
	deleted      bool
}

// authorizeResource lets a viewer of the resource's project read its history; a
// resource outside any project (tokens, nodes) needs a tenant-wide token. The project
// comes from the resource's own audit events, written with every revision.
func authorizeResource(ctx context.Context, tx *store.Tx, typ string, id uuid.UUID) error {
	var project *uuid.UUID
	err := tx.QueryRow(ctx, `SELECT project_id FROM audit_events WHERE resource_type = $1 AND resource_id = $2
		ORDER BY id LIMIT 1`, typ, id).Scan(&project)
	if errors.Is(err, pgx.ErrNoRows) {
		return store.ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("look up resource project: %w", err)
	}
	if project != nil {
		return authorizeProject(ctx, *project, auth.RoleViewer)
	}
	if p, _ := auth.FromContext(ctx); !p.TenantWide() {
		return store.ErrNotFound
	}
	return nil
}

func (h history) revision(ctx context.Context, tx *store.Tx, typ string, r revisionRow) (*bohuxv1.Revision, error) {
	snap, err := h.scan(tx.QueryRow(ctx, h.at(), r.id))
	if err != nil {
		return nil, fmt.Errorf("read %s revision: %w", typ, err)
	}
	a, err := anypb.New(snap)
	if err != nil {
		return nil, fmt.Errorf("pack %s revision: %w", typ, err)
	}
	var kind string
	_ = tx.QueryRow(ctx, `SELECT actor_kind FROM audit_events WHERE revision_id = $1 LIMIT 1`, r.id).Scan(&kind)
	out := &bohuxv1.Revision{Id: r.id.String(), Resource: &bohuxv1.ResourceRef{Type: typ, Id: r.resource.String()},
		CreatedAt: timestamppb.New(r.at), Deleted: r.deleted, Snapshot: a,
		Actor: &bohuxv1.Principal{Id: r.actor.String(), Kind: actorKinds[kind]}}
	if r.parent != nil {
		out.ParentRevisionId = r.parent.String()
	}
	return out, nil
}

const revisionColumns = `id, resource_id, parent_revision_id, created_at, actor_id, deleted`

func scanRevisionRow(row pgx.Row) (revisionRow, error) {
	var r revisionRow
	err := row.Scan(&r.id, &r.resource, &r.parent, &r.at, &r.actor, &r.deleted)
	return r, err
}

func historyOf(ref *bohuxv1.ResourceRef) (history, uuid.UUID, error) {
	h, ok := histories[ref.GetType()]
	if !ok {
		return history{}, uuid.Nil, invalidArgument("resource.type", "UNSUPPORTED", "this resource type keeps no readable history")
	}
	id, err := parseID("resource.id", ref.GetId())
	return h, id, err
}

// ListRevisions lists a resource's revisions, newest first, each with its snapshot.
func (ar *auditReader) ListRevisions(ctx context.Context, req *connect.Request[bohuxv1.ListRevisionsRequest]) (*connect.Response[bohuxv1.ListRevisionsResponse], error) {
	p, _ := auth.FromContext(ctx)
	h, id, err := historyOf(req.Msg.GetResource())
	if err != nil {
		return nil, err
	}
	typ := req.Msg.GetResource().GetType()
	size, after, err := page(req.Msg.GetPageSize(), req.Msg.GetPageToken())
	if err != nil {
		return nil, err
	}
	resp := &bohuxv1.ListRevisionsResponse{}
	err = ar.s.Store.InTenant(ctx, p.Tenant, func(tx *store.Tx) error {
		if err := authorizeResource(ctx, tx, typ, id); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT `+revisionColumns+` FROM `+h.table+`_revisions
			WHERE resource_id = $1 AND ($2::uuid IS NULL OR id < $2) ORDER BY id DESC LIMIT $3`, id, after, size+1)
		if err != nil {
			return fmt.Errorf("list revisions: %w", err)
		}
		var list []revisionRow
		for rows.Next() {
			r, err := scanRevisionRow(rows)
			if err != nil {
				rows.Close()
				return fmt.Errorf("read revision: %w", err)
			}
			list = append(list, r)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return fmt.Errorf("list revisions: %w", err)
		}
		for _, r := range list {
			rev, err := h.revision(ctx, tx, typ, r)
			if err != nil {
				return err
			}
			resp.Revisions = append(resp.Revisions, rev)
		}
		return nil
	})
	if err != nil {
		return nil, toConnect(ctx, err)
	}
	if len(resp.Revisions) > size {
		resp.Revisions = resp.Revisions[:size]
		resp.NextPageToken = resp.Revisions[size-1].GetId()
	}
	return connect.NewResponse(resp), nil
}

// GetRevision reads one revision by ID; its type comes from the audit event that
// recorded it.
func (ar *auditReader) GetRevision(ctx context.Context, req *connect.Request[bohuxv1.GetRevisionRequest]) (*connect.Response[bohuxv1.GetRevisionResponse], error) {
	p, _ := auth.FromContext(ctx)
	rev, err := parseID("id", req.Msg.GetId())
	if err != nil {
		return nil, err
	}
	resp := &bohuxv1.GetRevisionResponse{}
	err = ar.s.Store.InTenant(ctx, p.Tenant, func(tx *store.Tx) error {
		var typ string
		var res uuid.UUID
		err := tx.QueryRow(ctx, `SELECT resource_type, resource_id FROM audit_events WHERE revision_id = $1 LIMIT 1`,
			rev).Scan(&typ, &res)
		if errors.Is(err, pgx.ErrNoRows) {
			return store.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("look up revision: %w", err)
		}
		h, ok := histories[typ]
		if !ok {
			return store.ErrNotFound
		}
		if err := authorizeResource(ctx, tx, typ, res); err != nil {
			return err
		}
		r, err := scanRevisionRow(tx.QueryRow(ctx, `SELECT `+revisionColumns+` FROM `+h.table+`_revisions WHERE id = $1`, rev))
		if errors.Is(err, pgx.ErrNoRows) {
			return store.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("read revision: %w", err)
		}
		resp.Revision, err = h.revision(ctx, tx, typ, r)
		return err
	})
	if err != nil {
		return nil, toConnect(ctx, err)
	}
	return connect.NewResponse(resp), nil
}
