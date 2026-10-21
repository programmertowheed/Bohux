package api

import (
	"context"
	"errors"
	"fmt"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	bohuxv1 "github.com/bohumix/bohux/gen/bohux/v1"
	"github.com/bohumix/bohux/gen/bohux/v1/bohuxv1connect"
	"github.com/bohumix/bohux/internal/auth"
	"github.com/bohumix/bohux/internal/store"
)

const projectType = "bohux.v1.Project"

// errRoleBindingsLater: RoleBindings bind user principals, which arrive with user
// accounts in v1.0. Until then a token carries its own role (ADR-0023), so stored
// bindings would have no effect; the RPCs say so instead of pretending.
var errRoleBindingsLater = errors.New("role bindings arrive with user accounts (v1.0); " +
	"use project-scoped API tokens (ADR-0023)")

type projectService struct {
	s *Server
}

func (ps *projectService) CreateProject(ctx context.Context, req *connect.Request[bohuxv1.CreateProjectRequest]) (*connect.Response[bohuxv1.CreateProjectResponse], error) {
	msg := req.Msg
	p, _ := auth.FromContext(ctx)
	if err := validSlug("slug", msg.GetSlug()); err != nil {
		return nil, err
	}
	idem, err := newIdempotency(ctx, bohuxv1connect.ProjectServiceCreateProjectProcedure, msg.GetIdempotencyKey(), msg)
	if err != nil {
		return nil, toConnect(ctx, err)
	}
	resp := &bohuxv1.CreateProjectResponse{}
	err = ps.s.Store.InTenant(ctx, p.Tenant, func(tx *store.Tx) error {
		prior, err := idem.lookup(ctx, tx)
		if err != nil {
			return err
		}
		if prior != nil {
			resp.Project, err = getProject(ctx, tx, prior.ResourceID, "")
			return err
		}
		id, err := store.NewID()
		if err != nil {
			return err
		}
		rev, err := tx.CreateWithID(ctx, "projects", id,
			[]store.Col{{Name: "slug", Value: msg.GetSlug()}},
			[]store.Col{{Name: "display_name", Value: msg.GetDisplayName()}},
			store.Audit{Actor: p.Actor(), Action: bohuxv1connect.ProjectServiceCreateProjectProcedure,
				ResourceType: projectType, ProjectID: id})
		if err != nil {
			return err
		}
		if err := idem.record(ctx, tx, projectType, id, rev); err != nil {
			return err
		}
		resp.Project, err = getProject(ctx, tx, id, "")
		return err
	})
	if err != nil {
		return nil, toConnect(ctx, err)
	}
	return connect.NewResponse(resp), nil
}

func (ps *projectService) GetProject(ctx context.Context, req *connect.Request[bohuxv1.GetProjectRequest]) (*connect.Response[bohuxv1.GetProjectResponse], error) {
	p, _ := auth.FromContext(ctx)
	var (
		id   uuid.UUID
		slug string
		err  error
	)
	switch l := req.Msg.GetLookup().(type) {
	case *bohuxv1.GetProjectRequest_Id:
		if id, err = parseID("id", l.Id); err != nil {
			return nil, err
		}
	case *bohuxv1.GetProjectRequest_Slug:
		slug = l.Slug
	default:
		return nil, invalidArgument("lookup", "REQUIRED", "id or slug is required")
	}
	resp := &bohuxv1.GetProjectResponse{}
	err = ps.s.Store.InTenant(ctx, p.Tenant, func(tx *store.Tx) error {
		proj, err := getProject(ctx, tx, id, slug)
		if err != nil {
			return err
		}
		pid, err := uuid.Parse(proj.GetMeta().GetId())
		if err != nil {
			return fmt.Errorf("stored project id: %w", err)
		}
		if err := authorizeProject(ctx, pid, auth.RoleViewer); err != nil {
			return err
		}
		resp.Project = proj
		return nil
	})
	if err != nil {
		return nil, toConnect(ctx, err)
	}
	return connect.NewResponse(resp), nil
}

func (ps *projectService) ListProjects(ctx context.Context, req *connect.Request[bohuxv1.ListProjectsRequest]) (*connect.Response[bohuxv1.ListProjectsResponse], error) {
	p, _ := auth.FromContext(ctx)
	size, after, err := page(req.Msg.GetPageSize(), req.Msg.GetPageToken())
	if err != nil {
		return nil, err
	}
	// A project-scoped token sees only its own project (ADR-0023).
	var scope *uuid.UUID
	if !p.TenantWide() {
		scope = &p.ProjectID
	}
	resp := &bohuxv1.ListProjectsResponse{}
	err = ps.s.Store.InTenant(ctx, p.Tenant, func(tx *store.Tx) error {
		rows, err := tx.Query(ctx, projectSelect+`
			WHERE NOT h.deleted AND ($1::uuid IS NULL OR h.id = $1)
			  AND ($2::uuid IS NULL OR h.id > $2)
			ORDER BY h.id LIMIT $3`, scope, after, size+1)
		if err != nil {
			return fmt.Errorf("list projects: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			proj, err := scanProject(rows)
			if err != nil {
				return err
			}
			resp.Projects = append(resp.Projects, proj)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, toConnect(ctx, err)
	}
	if len(resp.Projects) > size {
		resp.Projects = resp.Projects[:size]
		resp.NextPageToken = resp.Projects[size-1].GetMeta().GetId()
	}
	return connect.NewResponse(resp), nil
}

func (ps *projectService) UpdateProject(ctx context.Context, req *connect.Request[bohuxv1.UpdateProjectRequest]) (*connect.Response[bohuxv1.UpdateProjectResponse], error) {
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
	idem, err := newIdempotency(ctx, bohuxv1connect.ProjectServiceUpdateProjectProcedure, msg.GetIdempotencyKey(), msg)
	if err != nil {
		return nil, toConnect(ctx, err)
	}
	resp := &bohuxv1.UpdateProjectResponse{}
	err = ps.s.Store.InTenant(ctx, p.Tenant, func(tx *store.Tx) error {
		prior, err := idem.lookup(ctx, tx)
		if err != nil {
			return err
		}
		if prior == nil {
			if err := authorizeProject(ctx, id, auth.RoleOwner); err != nil {
				return err
			}
			rev, err := tx.Update(ctx, "projects", id, expected,
				[]store.Col{{Name: "display_name", Value: msg.GetDisplayName()}},
				store.Audit{Actor: p.Actor(), Action: bohuxv1connect.ProjectServiceUpdateProjectProcedure,
					ResourceType: projectType, ProjectID: id})
			if err != nil {
				return err
			}
			if err := idem.record(ctx, tx, projectType, id, rev); err != nil {
				return err
			}
		}
		resp.Project, err = getProject(ctx, tx, id, "")
		return err
	})
	if err != nil {
		return nil, toConnect(ctx, err)
	}
	return connect.NewResponse(resp), nil
}

func (ps *projectService) DeleteProject(ctx context.Context, req *connect.Request[bohuxv1.DeleteProjectRequest]) (*connect.Response[bohuxv1.DeleteProjectResponse], error) {
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
	idem, err := newIdempotency(ctx, bohuxv1connect.ProjectServiceDeleteProjectProcedure, msg.GetIdempotencyKey(), msg)
	if err != nil {
		return nil, toConnect(ctx, err)
	}
	resp := &bohuxv1.DeleteProjectResponse{}
	err = ps.s.Store.InTenant(ctx, p.Tenant, func(tx *store.Tx) error {
		prior, err := idem.lookup(ctx, tx)
		if err != nil {
			return err
		}
		if prior != nil {
			resp.RevisionId = prior.RevisionID.String()
			return nil
		}
		if err := authorizeProject(ctx, id, auth.RoleOwner); err != nil {
			return err
		}
		if err := noLiveChildren(ctx, tx, "environments_heads", "project_id", id); err != nil {
			return err
		}
		rev, err := tx.Delete(ctx, "projects", id, expected,
			store.Audit{Actor: p.Actor(), Action: bohuxv1connect.ProjectServiceDeleteProjectProcedure,
				ResourceType: projectType, ProjectID: id})
		if err != nil {
			return err
		}
		resp.RevisionId = rev.String()
		return idem.record(ctx, tx, projectType, id, rev)
	})
	if err != nil {
		return nil, toConnect(ctx, err)
	}
	return connect.NewResponse(resp), nil
}

func (ps *projectService) SetRoleBinding(context.Context, *connect.Request[bohuxv1.SetRoleBindingRequest]) (*connect.Response[bohuxv1.SetRoleBindingResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, errRoleBindingsLater)
}

func (ps *projectService) DeleteRoleBinding(context.Context, *connect.Request[bohuxv1.DeleteRoleBindingRequest]) (*connect.Response[bohuxv1.DeleteRoleBindingResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, errRoleBindingsLater)
}

func (ps *projectService) ListRoleBindings(context.Context, *connect.Request[bohuxv1.ListRoleBindingsRequest]) (*connect.Response[bohuxv1.ListRoleBindingsResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, errRoleBindingsLater)
}

const projectSelect = `SELECT ` + metaColumns + `, h.slug, r.display_name
	FROM projects_heads h JOIN projects_revisions r ON r.id = h.revision_id`

// getProject reads a live project by id, or by slug if id is nil.
func getProject(ctx context.Context, tx *store.Tx, id uuid.UUID, slug string) (*bohuxv1.Project, error) {
	var row pgx.Row
	if id != uuid.Nil {
		row = tx.QueryRow(ctx, projectSelect+` WHERE h.id = $1 AND NOT h.deleted`, id)
	} else {
		row = tx.QueryRow(ctx, projectSelect+` WHERE h.slug = $1 AND NOT h.deleted`, slug)
	}
	proj, err := scanProject(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	return proj, err
}

func scanProject(row pgx.Row) (*bohuxv1.Project, error) {
	var (
		m          metaRow
		slug, name string
	)
	if err := row.Scan(append(m.dest(), &slug, &name)...); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
		return nil, fmt.Errorf("read project: %w", err)
	}
	return &bohuxv1.Project{Meta: m.proto(), Slug: slug, DisplayName: name}, nil
}

// noLiveChildren fails with FailedPrecondition if table has a live row whose column
// is parent. table and column are code constants.
func noLiveChildren(ctx context.Context, tx *store.Tx, table, column string, parent uuid.UUID) error {
	var n int
	err := tx.QueryRow(ctx, `SELECT count(*) FROM `+table+` WHERE `+column+` = $1 AND NOT deleted`,
		parent).Scan(&n)
	if err != nil {
		return fmt.Errorf("count children: %w", err)
	}
	if n > 0 {
		return connect.NewError(connect.CodeFailedPrecondition, errHasChildren)
	}
	return nil
}
