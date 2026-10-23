package api

import (
	"context"
	"fmt"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"

	bohuxv1 "github.com/bohumix/bohux/gen/bohux/v1"
	"github.com/bohumix/bohux/internal/auth"
	"github.com/bohumix/bohux/internal/store"
)

// auditService reads the audit trail and revision history written with every change
// (rule 10, ADR-0004, ADR-0017).
type auditService struct {
	s *Server
	auditReader
}

var actorKinds = map[string]bohuxv1.PrincipalKind{
	string(store.ActorUser):     bohuxv1.PrincipalKind_PRINCIPAL_KIND_USER,
	string(store.ActorAPIToken): bohuxv1.PrincipalKind_PRINCIPAL_KIND_API_TOKEN,
	string(store.ActorNode):     bohuxv1.PrincipalKind_PRINCIPAL_KIND_NODE,
	string(store.ActorSystem):   bohuxv1.PrincipalKind_PRINCIPAL_KIND_SYSTEM,
	string(store.ActorWebhook):  bohuxv1.PrincipalKind_PRINCIPAL_KIND_WEBHOOK,
}

// ListAuditEvents lists events newest first. With a project, a viewer of it may read
// them; without one, the whole tenant's trail needs a tenant-wide token (ADR-0023),
// since it includes tenant-level actions such as token creation.
func (as *auditService) ListAuditEvents(ctx context.Context, req *connect.Request[bohuxv1.ListAuditEventsRequest]) (*connect.Response[bohuxv1.ListAuditEventsResponse], error) {
	msg := req.Msg
	p, _ := auth.FromContext(ctx)
	var project *uuid.UUID
	if msg.GetProjectId() != "" {
		id, err := parseID("project_id", msg.GetProjectId())
		if err != nil {
			return nil, err
		}
		project = &id
	}
	var resType string
	var resID *uuid.UUID
	if r := msg.GetResource(); r != nil {
		if r.GetType() == "" {
			return nil, invalidArgument("resource.type", "REQUIRED", "resource.type is required with a resource")
		}
		id, err := parseID("resource.id", r.GetId())
		if err != nil {
			return nil, err
		}
		resType, resID = r.GetType(), &id
	}
	size, after, err := page(msg.GetPageSize(), msg.GetPageToken())
	if err != nil {
		return nil, err
	}
	resp := &bohuxv1.ListAuditEventsResponse{}
	err = as.s.Store.InTenant(ctx, p.Tenant, func(tx *store.Tx) error {
		if project != nil {
			if err := ensureProject(ctx, tx, *project); err != nil {
				return err
			}
			if err := authorizeProject(ctx, *project, auth.RoleViewer); err != nil {
				return err
			}
		} else if !p.TenantWide() {
			return connect.NewError(connect.CodePermissionDenied,
				fmt.Errorf("a project-scoped token reads its project's events only: pass project_id"))
		}
		rows, err := tx.Query(ctx, `SELECT id, occurred_at, actor_id, actor_kind, action, resource_type,
				resource_id, revision_id, project_id
			FROM audit_events
			WHERE ($1::uuid IS NULL OR project_id = $1)
			  AND ($2 = '' OR (resource_type = $2 AND resource_id = $3))
			  AND ($4::uuid IS NULL OR id < $4)
			ORDER BY id DESC LIMIT $5`, project, resType, resID, after, size+1)
		if err != nil {
			return fmt.Errorf("list audit events: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var (
				id, actor, resource uuid.UUID
				at                  time.Time
				kind, action, typ   string
				rev, proj           *uuid.UUID
			)
			if err := rows.Scan(&id, &at, &actor, &kind, &action, &typ, &resource, &rev, &proj); err != nil {
				return fmt.Errorf("read audit event: %w", err)
			}
			e := &bohuxv1.AuditEvent{Id: id.String(), OccurredAt: timestamppb.New(at),
				Actor:    &bohuxv1.Principal{Id: actor.String(), Kind: actorKinds[kind]},
				Action:   strings.TrimPrefix(action, "/"),
				Resource: &bohuxv1.ResourceRef{Type: typ, Id: resource.String()}}
			if rev != nil {
				e.RevisionId = rev.String()
			}
			if proj != nil {
				e.ProjectId = proj.String()
			}
			resp.AuditEvents = append(resp.AuditEvents, e)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, toConnect(ctx, err)
	}
	if len(resp.AuditEvents) > size {
		resp.AuditEvents = resp.AuditEvents[:size]
		resp.NextPageToken = resp.AuditEvents[size-1].GetId()
	}
	return connect.NewResponse(resp), nil
}
