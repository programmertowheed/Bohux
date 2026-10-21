package api

import (
	"context"
	"encoding/json"
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

const environmentType = "bohux.v1.Environment"

type environmentService struct {
	s *Server
}

// bindingCols validates a ProviderBinding and config and returns the revision columns.
func bindingCols(ctx context.Context, tx *store.Tx, b *bohuxv1.ProviderBinding, config map[string]string) ([]store.Col, error) {
	if b.GetProvider() == "" {
		return nil, invalidArgument("provider_binding.provider", "REQUIRED", "provider is required")
	}
	var nodeID, targetRef any
	switch t := b.GetTarget().(type) {
	case *bohuxv1.ProviderBinding_NodeId:
		id, err := parseID("provider_binding.node_id", t.NodeId)
		if err != nil {
			return nil, err
		}
		var ok bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM nodes_heads
			WHERE id = $1 AND NOT deleted)`, id).Scan(&ok); err != nil {
			return nil, fmt.Errorf("check node: %w", err)
		}
		if !ok {
			return nil, invalidArgument("provider_binding.node_id", "NOT_FOUND", "node not found")
		}
		nodeID = id
	case *bohuxv1.ProviderBinding_TargetRef:
		if t.TargetRef == "" {
			return nil, invalidArgument("provider_binding.target_ref", "REQUIRED", "target_ref is empty")
		}
		targetRef = t.TargetRef
	default:
		return nil, invalidArgument("provider_binding.target", "REQUIRED", "node_id or target_ref is required")
	}
	if config == nil {
		config = map[string]string{}
	}
	for k := range config {
		if k == "" {
			return nil, invalidArgument("config", "EMPTY_KEY", "config keys must not be empty")
		}
	}
	cfg, err := json.Marshal(config)
	if err != nil {
		return nil, fmt.Errorf("encode config: %w", err)
	}
	return []store.Col{
		{Name: "provider", Value: b.GetProvider()}, {Name: "node_id", Value: nodeID},
		{Name: "target_ref", Value: targetRef}, {Name: "config", Value: cfg},
	}, nil
}

func (es *environmentService) CreateEnvironment(ctx context.Context, req *connect.Request[bohuxv1.CreateEnvironmentRequest]) (*connect.Response[bohuxv1.CreateEnvironmentResponse], error) {
	msg := req.Msg
	p, _ := auth.FromContext(ctx)
	project, err := parseID("project_id", msg.GetProjectId())
	if err != nil {
		return nil, err
	}
	if err := validSlug("slug", msg.GetSlug()); err != nil {
		return nil, err
	}
	idem, err := newIdempotency(ctx, bohuxv1connect.EnvironmentServiceCreateEnvironmentProcedure, msg.GetIdempotencyKey(), msg)
	if err != nil {
		return nil, toConnect(ctx, err)
	}
	resp := &bohuxv1.CreateEnvironmentResponse{}
	err = es.s.Store.InTenant(ctx, p.Tenant, func(tx *store.Tx) error {
		prior, err := idem.lookup(ctx, tx)
		if err != nil {
			return err
		}
		if prior != nil {
			resp.Environment, err = getEnvironment(ctx, tx, prior.ResourceID, uuid.Nil, "")
			return err
		}
		if err := authorizeProject(ctx, project, auth.RoleOwner); err != nil {
			return err
		}
		if err := projectExists(ctx, tx, project); err != nil {
			return err
		}
		cols, err := bindingCols(ctx, tx, msg.GetProviderBinding(), msg.GetConfig())
		if err != nil {
			return err
		}
		id, rev, err := tx.Create(ctx, "environments",
			[]store.Col{{Name: "project_id", Value: project}, {Name: "slug", Value: msg.GetSlug()}},
			cols,
			store.Audit{Actor: p.Actor(), Action: bohuxv1connect.EnvironmentServiceCreateEnvironmentProcedure,
				ResourceType: environmentType, ProjectID: project})
		if err != nil {
			return err
		}
		if err := idem.record(ctx, tx, environmentType, id, rev); err != nil {
			return err
		}
		resp.Environment, err = getEnvironment(ctx, tx, id, uuid.Nil, "")
		return err
	})
	if err != nil {
		return nil, toConnect(ctx, err)
	}
	return connect.NewResponse(resp), nil
}

func (es *environmentService) GetEnvironment(ctx context.Context, req *connect.Request[bohuxv1.GetEnvironmentRequest]) (*connect.Response[bohuxv1.GetEnvironmentResponse], error) {
	p, _ := auth.FromContext(ctx)
	var (
		id, project uuid.UUID
		slug        string
		err         error
	)
	switch l := req.Msg.GetLookup().(type) {
	case *bohuxv1.GetEnvironmentRequest_Id:
		if id, err = parseID("id", l.Id); err != nil {
			return nil, err
		}
	case *bohuxv1.GetEnvironmentRequest_Slug:
		if project, err = parseID("slug.project_id", l.Slug.GetProjectId()); err != nil {
			return nil, err
		}
		slug = l.Slug.GetSlug()
	default:
		return nil, invalidArgument("lookup", "REQUIRED", "id or slug is required")
	}
	resp := &bohuxv1.GetEnvironmentResponse{}
	err = es.s.Store.InTenant(ctx, p.Tenant, func(tx *store.Tx) error {
		env, err := getEnvironment(ctx, tx, id, project, slug)
		if err != nil {
			return err
		}
		project, err := uuid.Parse(env.GetProjectId())
		if err != nil {
			return fmt.Errorf("stored project id: %w", err)
		}
		if err := authorizeProject(ctx, project, auth.RoleViewer); err != nil {
			return err
		}
		resp.Environment = env
		return nil
	})
	if err != nil {
		return nil, toConnect(ctx, err)
	}
	return connect.NewResponse(resp), nil
}

func (es *environmentService) ListEnvironments(ctx context.Context, req *connect.Request[bohuxv1.ListEnvironmentsRequest]) (*connect.Response[bohuxv1.ListEnvironmentsResponse], error) {
	p, _ := auth.FromContext(ctx)
	project, err := parseID("project_id", req.Msg.GetProjectId())
	if err != nil {
		return nil, err
	}
	size, after, err := page(req.Msg.GetPageSize(), req.Msg.GetPageToken())
	if err != nil {
		return nil, err
	}
	resp := &bohuxv1.ListEnvironmentsResponse{}
	err = es.s.Store.InTenant(ctx, p.Tenant, func(tx *store.Tx) error {
		if err := authorizeProject(ctx, project, auth.RoleViewer); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, environmentSelect+`
			WHERE NOT h.deleted AND h.project_id = $1 AND ($2::uuid IS NULL OR h.id > $2)
			ORDER BY h.id LIMIT $3`, project, after, size+1)
		if err != nil {
			return fmt.Errorf("list environments: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			env, err := scanEnvironment(rows)
			if err != nil {
				return err
			}
			resp.Environments = append(resp.Environments, env)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, toConnect(ctx, err)
	}
	if len(resp.Environments) > size {
		resp.Environments = resp.Environments[:size]
		resp.NextPageToken = resp.Environments[size-1].GetMeta().GetId()
	}
	return connect.NewResponse(resp), nil
}

func (es *environmentService) UpdateEnvironment(ctx context.Context, req *connect.Request[bohuxv1.UpdateEnvironmentRequest]) (*connect.Response[bohuxv1.UpdateEnvironmentResponse], error) {
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
	idem, err := newIdempotency(ctx, bohuxv1connect.EnvironmentServiceUpdateEnvironmentProcedure, msg.GetIdempotencyKey(), msg)
	if err != nil {
		return nil, toConnect(ctx, err)
	}
	resp := &bohuxv1.UpdateEnvironmentResponse{}
	err = es.s.Store.InTenant(ctx, p.Tenant, func(tx *store.Tx) error {
		prior, err := idem.lookup(ctx, tx)
		if err != nil {
			return err
		}
		if prior == nil {
			project, err := environmentProject(ctx, tx, id)
			if err != nil {
				return err
			}
			if err := authorizeProject(ctx, project, auth.RoleOwner); err != nil {
				return err
			}
			cols, err := bindingCols(ctx, tx, msg.GetProviderBinding(), msg.GetConfig())
			if err != nil {
				return err
			}
			rev, err := tx.Update(ctx, "environments", id, expected, cols,
				store.Audit{Actor: p.Actor(), Action: bohuxv1connect.EnvironmentServiceUpdateEnvironmentProcedure,
					ResourceType: environmentType, ProjectID: project})
			if err != nil {
				return err
			}
			if err := idem.record(ctx, tx, environmentType, id, rev); err != nil {
				return err
			}
		}
		resp.Environment, err = getEnvironment(ctx, tx, id, uuid.Nil, "")
		return err
	})
	if err != nil {
		return nil, toConnect(ctx, err)
	}
	return connect.NewResponse(resp), nil
}

func (es *environmentService) DeleteEnvironment(ctx context.Context, req *connect.Request[bohuxv1.DeleteEnvironmentRequest]) (*connect.Response[bohuxv1.DeleteEnvironmentResponse], error) {
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
	idem, err := newIdempotency(ctx, bohuxv1connect.EnvironmentServiceDeleteEnvironmentProcedure, msg.GetIdempotencyKey(), msg)
	if err != nil {
		return nil, toConnect(ctx, err)
	}
	resp := &bohuxv1.DeleteEnvironmentResponse{}
	err = es.s.Store.InTenant(ctx, p.Tenant, func(tx *store.Tx) error {
		prior, err := idem.lookup(ctx, tx)
		if err != nil {
			return err
		}
		if prior != nil {
			resp.RevisionId = prior.RevisionID.String()
			return nil
		}
		project, err := environmentProject(ctx, tx, id)
		if err != nil {
			return err
		}
		if err := authorizeProject(ctx, project, auth.RoleOwner); err != nil {
			return err
		}
		if err := noLiveChildren(ctx, tx, "apps_heads", "environment_id", id); err != nil {
			return err
		}
		rev, err := tx.Delete(ctx, "environments", id, expected,
			store.Audit{Actor: p.Actor(), Action: bohuxv1connect.EnvironmentServiceDeleteEnvironmentProcedure,
				ResourceType: environmentType, ProjectID: project})
		if err != nil {
			return err
		}
		resp.RevisionId = rev.String()
		return idem.record(ctx, tx, environmentType, id, rev)
	})
	if err != nil {
		return nil, toConnect(ctx, err)
	}
	return connect.NewResponse(resp), nil
}

const environmentSelect = `SELECT ` + metaColumns + `, h.project_id, h.slug,
		r.provider, r.node_id, r.target_ref, r.config
	FROM environments_heads h JOIN environments_revisions r ON r.id = h.revision_id`

// getEnvironment reads a live environment by id, or by (project, slug) if id is nil.
func getEnvironment(ctx context.Context, tx *store.Tx, id, project uuid.UUID, slug string) (*bohuxv1.Environment, error) {
	var row pgx.Row
	if id != uuid.Nil {
		row = tx.QueryRow(ctx, environmentSelect+` WHERE h.id = $1 AND NOT h.deleted`, id)
	} else {
		row = tx.QueryRow(ctx, environmentSelect+` WHERE h.project_id = $1 AND h.slug = $2
			AND NOT h.deleted`, project, slug)
	}
	env, err := scanEnvironment(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	return env, err
}

func scanEnvironment(row pgx.Row) (*bohuxv1.Environment, error) {
	var (
		m              metaRow
		project        uuid.UUID
		slug, provider string
		node           *uuid.UUID
		target         *string
		config         []byte
	)
	if err := row.Scan(append(m.dest(), &project, &slug, &provider, &node, &target, &config)...); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
		return nil, fmt.Errorf("read environment: %w", err)
	}
	env := &bohuxv1.Environment{
		Meta: m.proto(), ProjectId: project.String(), Slug: slug,
		ProviderBinding: &bohuxv1.ProviderBinding{Provider: provider},
	}
	switch {
	case node != nil:
		env.ProviderBinding.Target = &bohuxv1.ProviderBinding_NodeId{NodeId: node.String()}
	case target != nil:
		env.ProviderBinding.Target = &bohuxv1.ProviderBinding_TargetRef{TargetRef: *target}
	}
	if err := json.Unmarshal(config, &env.Config); err != nil {
		return nil, fmt.Errorf("decode environment config: %w", err)
	}
	return env, nil
}

// environmentProject returns the project of a live environment.
func environmentProject(ctx context.Context, tx *store.Tx, id uuid.UUID) (uuid.UUID, error) {
	var project uuid.UUID
	err := tx.QueryRow(ctx, `SELECT project_id FROM environments_heads WHERE id = $1 AND NOT deleted`,
		id).Scan(&project)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, store.ErrNotFound
	}
	if err != nil {
		return uuid.Nil, fmt.Errorf("look up environment: %w", err)
	}
	return project, nil
}
