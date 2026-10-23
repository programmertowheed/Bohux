package api

import (
	"context"
	"errors"
	"fmt"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	bohuxv1 "github.com/bohumix/bohux/gen/bohux/v1"
	"github.com/bohumix/bohux/gen/bohux/v1/bohuxv1connect"
	"github.com/bohumix/bohux/internal/auth"
	"github.com/bohumix/bohux/internal/store"
)

const (
	apiTokenType = "bohux.v1.ApiToken"

	defaultPageSize = 50
	maxPageSize     = 200
)

type identityService struct{ s *Server }

func (i *identityService) WhoAmI(ctx context.Context, _ *connect.Request[bohuxv1.WhoAmIRequest]) (*connect.Response[bohuxv1.WhoAmIResponse], error) {
	p, _ := auth.FromContext(ctx)
	var name string
	err := i.s.Store.InTenant(ctx, p.Tenant, func(tx *store.Tx) error {
		tok, err := getAPIToken(ctx, tx, p.ID)
		if err != nil {
			return err
		}
		name = tok.GetName()
		return nil
	})
	if err != nil {
		return nil, toConnect(ctx, err)
	}
	return connect.NewResponse(&bohuxv1.WhoAmIResponse{
		Principal: &bohuxv1.Principal{
			Id: p.ID.String(), Kind: bohuxv1.PrincipalKind_PRINCIPAL_KIND_API_TOKEN, DisplayName: name,
		},
		TenantId: p.Tenant.String(),
	}), nil
}

func (i *identityService) CreateApiToken(ctx context.Context, req *connect.Request[bohuxv1.CreateApiTokenRequest]) (*connect.Response[bohuxv1.CreateApiTokenResponse], error) {
	msg := req.Msg
	p, _ := auth.FromContext(ctx)
	if msg.GetName() == "" {
		return nil, invalidArgument("name", "REQUIRED", "name is required")
	}
	role := roleFromProto(msg.GetRole())
	if role == auth.RoleNone {
		return nil, invalidArgument("role", "REQUIRED", "role is required")
	}
	var project uuid.UUID
	if msg.GetProjectId() != "" {
		var err error
		if project, err = parseID("project_id", msg.GetProjectId()); err != nil {
			return nil, err
		}
	}
	expires := timePtr(msg.GetExpiresAt())
	if expires != nil && !expires.After(i.s.now()) {
		return nil, invalidArgument("expires_at", "IN_PAST", "expires_at must be in the future")
	}
	if !p.CanGrant(project, role) {
		return nil, toConnect(ctx, auth.ErrPermissionDenied)
	}
	idem, err := newIdempotency(ctx, bohuxv1connect.IdentityServiceCreateApiTokenProcedure,
		msg.GetIdempotencyKey(), msg)
	if err != nil {
		return nil, toConnect(ctx, err)
	}

	resp := &bohuxv1.CreateApiTokenResponse{}
	err = i.s.Store.InTenant(ctx, p.Tenant, func(tx *store.Tx) error {
		prior, err := idem.lookup(ctx, tx)
		if err != nil {
			return err
		}
		if prior != nil {
			// The secret is shown once only; a replay returns the metadata (ADR-0022).
			resp.ApiToken, err = getAPIToken(ctx, tx, prior.ResourceID)
			return err
		}
		if project != uuid.Nil {
			if err := projectExists(ctx, tx, project); err != nil {
				return err
			}
		}
		raw, id, rev, err := auth.CreateAPIToken(ctx, tx, project, role, msg.GetName(), expires,
			store.Audit{Actor: p.Actor(), Action: bohuxv1connect.IdentityServiceCreateApiTokenProcedure,
				ResourceType: apiTokenType, ProjectID: project})
		if err != nil {
			return err
		}
		if err := idem.record(ctx, tx, apiTokenType, id, rev); err != nil {
			return err
		}
		resp.Secret = raw
		resp.ApiToken, err = getAPIToken(ctx, tx, id)
		return err
	})
	if err != nil {
		return nil, toConnect(ctx, err)
	}
	return connect.NewResponse(resp), nil
}

func (i *identityService) ListApiTokens(ctx context.Context, req *connect.Request[bohuxv1.ListApiTokensRequest]) (*connect.Response[bohuxv1.ListApiTokensResponse], error) {
	p, _ := auth.FromContext(ctx)
	size, after, err := page(req.Msg.GetPageSize(), req.Msg.GetPageToken())
	if err != nil {
		return nil, err
	}
	// A project-scoped caller sees only its project's tokens (ADR-0023).
	var scope *uuid.UUID
	if !p.TenantWide() {
		scope = &p.ProjectID
	}
	resp := &bohuxv1.ListApiTokensResponse{}
	err = i.s.Store.InTenant(ctx, p.Tenant, func(tx *store.Tx) error {
		rows, err := tx.Query(ctx, apiTokenSelect+`
			WHERE NOT h.deleted AND ($1::uuid IS NULL OR h.project_id = $1)
			  AND ($2::uuid IS NULL OR h.id > $2)
			ORDER BY h.id LIMIT $3`, scope, after, size+1)
		if err != nil {
			return fmt.Errorf("list api tokens: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			tok, err := scanAPIToken(rows)
			if err != nil {
				return err
			}
			resp.ApiTokens = append(resp.ApiTokens, tok)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, toConnect(ctx, err)
	}
	if len(resp.ApiTokens) > size {
		resp.ApiTokens = resp.ApiTokens[:size]
		resp.NextPageToken = resp.ApiTokens[size-1].GetMeta().GetId()
	}
	return connect.NewResponse(resp), nil
}

func (i *identityService) RevokeApiToken(ctx context.Context, req *connect.Request[bohuxv1.RevokeApiTokenRequest]) (*connect.Response[bohuxv1.RevokeApiTokenResponse], error) {
	msg := req.Msg
	p, _ := auth.FromContext(ctx)
	id, err := parseID("id", msg.GetId())
	if err != nil {
		return nil, err
	}
	var expected uuid.UUID
	if msg.GetExpectedRevisionId() != "" {
		if expected, err = parseID("expected_revision_id", msg.GetExpectedRevisionId()); err != nil {
			return nil, err
		}
	}
	idem, err := newIdempotency(ctx, bohuxv1connect.IdentityServiceRevokeApiTokenProcedure,
		msg.GetIdempotencyKey(), msg)
	if err != nil {
		return nil, toConnect(ctx, err)
	}
	resp := &bohuxv1.RevokeApiTokenResponse{}
	err = i.s.Store.InTenant(ctx, p.Tenant, func(tx *store.Tx) error {
		prior, err := idem.lookup(ctx, tx)
		if err != nil {
			return err
		}
		if prior != nil {
			resp.ApiToken, err = getAPIToken(ctx, tx, prior.ResourceID)
			return err
		}
		tok, err := getAPIToken(ctx, tx, id)
		if err != nil {
			return err
		}
		// Revoking a token needs owner over its scope; a tenant-wide token, a
		// tenant-wide owner. Out-of-scope tokens look absent.
		project, current, err := tokenScope(tok)
		if err != nil {
			return err
		}
		if !p.CanGrant(project, auth.RoleOwner) {
			return store.ErrNotFound
		}
		if expected == uuid.Nil {
			expected = current
		}
		if tok.GetRevokedAt() != nil {
			// Already revoked: nothing to change; record the key against the current state.
			return idem.record(ctx, tx, apiTokenType, id, expected)
		}
		rev, err := tx.Update(ctx, "api_tokens", id, expected,
			[]store.Col{{Name: "name", Value: tok.GetName()}, {Name: "revoked_at", Value: i.s.now()}},
			store.Audit{Actor: p.Actor(), Action: bohuxv1connect.IdentityServiceRevokeApiTokenProcedure,
				ResourceType: apiTokenType, ProjectID: project})
		if err != nil {
			return err
		}
		if err := idem.record(ctx, tx, apiTokenType, id, rev); err != nil {
			return err
		}
		resp.ApiToken, err = getAPIToken(ctx, tx, id)
		return err
	})
	if err != nil {
		return nil, toConnect(ctx, err)
	}
	return connect.NewResponse(resp), nil
}

const apiTokenSelect = `SELECT ` + metaColumns + `, r.name, h.project_id, h.role,
		h.expires_at, h.last_used_at, r.revoked_at
	FROM api_tokens_heads h JOIN api_tokens_revisions r ON r.id = h.revision_id`

func getAPIToken(ctx context.Context, tx *store.Tx, id uuid.UUID) (*bohuxv1.ApiToken, error) {
	tok, err := scanAPIToken(tx.QueryRow(ctx, apiTokenSelect+` WHERE h.id = $1 AND NOT h.deleted`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	return tok, err
}

func scanAPIToken(row pgx.Row) (*bohuxv1.ApiToken, error) {
	var (
		m                          metaRow
		name, role                 string
		project                    *uuid.UUID
		expires, lastUsed, revoked *time.Time
	)
	if err := row.Scan(append(m.dest(), &name, &project, &role, &expires, &lastUsed, &revoked)...); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
		return nil, fmt.Errorf("read api token: %w", err)
	}
	tok := &bohuxv1.ApiToken{
		Meta: m.proto(), Name: name, Role: roleToProto(auth.ParseRole(role)),
		ExpiresAt: tsOrNil(expires), LastUsedAt: tsOrNil(lastUsed), RevokedAt: tsOrNil(revoked),
	}
	if project != nil {
		tok.ProjectId = project.String()
	}
	return tok, nil
}

// tokenScope returns a stored token's project (uuid.Nil if tenant-wide) and current
// revision.
func tokenScope(tok *bohuxv1.ApiToken) (project, revision uuid.UUID, err error) {
	if tok.GetProjectId() != "" {
		if project, err = uuid.Parse(tok.GetProjectId()); err != nil {
			return uuid.Nil, uuid.Nil, fmt.Errorf("stored project id: %w", err)
		}
	}
	if revision, err = uuid.Parse(tok.GetMeta().GetRevisionId()); err != nil {
		return uuid.Nil, uuid.Nil, fmt.Errorf("stored revision id: %w", err)
	}
	return project, revision, nil
}

func projectExists(ctx context.Context, tx *store.Tx, id uuid.UUID) error {
	var ok bool
	err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM projects_heads
		WHERE id = $1 AND NOT deleted)`, id).Scan(&ok)
	if err != nil {
		return fmt.Errorf("check project: %w", err)
	}
	if !ok {
		return store.ErrNotFound
	}
	return nil
}

// page validates list paging: the page token is the last ID of the previous page.
func page(size int32, token string) (int, *uuid.UUID, error) {
	n := int(size)
	switch {
	case n < 0:
		return 0, nil, invalidArgument("page_size", "NEGATIVE", "page_size must not be negative")
	case n == 0:
		n = defaultPageSize
	case n > maxPageSize:
		n = maxPageSize
	}
	if token == "" {
		return n, nil, nil
	}
	after, err := uuid.Parse(token)
	if err != nil {
		return 0, nil, invalidArgument("page_token", "INVALID", "page_token is not valid")
	}
	return n, &after, nil
}

func roleFromProto(r bohuxv1.Role) auth.Role {
	switch r {
	case bohuxv1.Role_ROLE_VIEWER:
		return auth.RoleViewer
	case bohuxv1.Role_ROLE_DEPLOYER:
		return auth.RoleDeployer
	case bohuxv1.Role_ROLE_OWNER:
		return auth.RoleOwner
	default:
		return auth.RoleNone
	}
}

func roleToProto(r auth.Role) bohuxv1.Role {
	switch r {
	case auth.RoleViewer:
		return bohuxv1.Role_ROLE_VIEWER
	case auth.RoleDeployer:
		return bohuxv1.Role_ROLE_DEPLOYER
	case auth.RoleOwner:
		return bohuxv1.Role_ROLE_OWNER
	default:
		return bohuxv1.Role_ROLE_UNSPECIFIED
	}
}
