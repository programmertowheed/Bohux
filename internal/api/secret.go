package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	bohuxv1 "github.com/bohumix/bohux/gen/bohux/v1"
	"github.com/bohumix/bohux/gen/bohux/v1/bohuxv1connect"
	"github.com/bohumix/bohux/internal/auth"
	"github.com/bohumix/bohux/internal/secrets/openbao"
	"github.com/bohumix/bohux/internal/store"
)

const secretType = "bohux.v1.Secret"

var errNoSecrets = connect.NewError(connect.CodeUnavailable, errors.New("the secrets backend is unavailable"))

// SecretBackend stores secret values; openbao.Client implements it. Values go there
// and nowhere else (rule 3, ADR-0008).
type SecretBackend interface {
	Put(ctx context.Context, path, value string) (int, error)
	Destroy(ctx context.Context, path string) error
}

type secretService struct {
	s *Server
}

// SetSecret creates a secret or, with expected_revision_id, gives it a new value. The
// value goes to the backend first; the revision records only its version, and the
// resolver reads exactly that version, so a value whose revision never commits is
// never used. The handle stays the same across values.
func (ss *secretService) SetSecret(ctx context.Context, req *connect.Request[bohuxv1.SetSecretRequest]) (*connect.Response[bohuxv1.SetSecretResponse], error) {
	msg := req.Msg
	p, _ := auth.FromContext(ctx)
	env, err := parseID("environment_id", msg.GetEnvironmentId())
	if err != nil {
		return nil, err
	}
	if !envVarName.MatchString(msg.GetKey()) {
		return nil, invalidArgument("key", "INVALID", "key must be an environment variable name")
	}
	if msg.GetValue() == "" {
		return nil, invalidArgument("value", "REQUIRED", "value is required")
	}
	var expected *uuid.UUID
	if msg.GetExpectedRevisionId() != "" {
		id, err := parseID("expected_revision_id", msg.GetExpectedRevisionId())
		if err != nil {
			return nil, err
		}
		expected = &id
	}
	idem, err := newIdempotency(ctx, bohuxv1connect.SecretServiceSetSecretProcedure, msg.GetIdempotencyKey(), msg)
	if err != nil {
		return nil, toConnect(ctx, err)
	}
	if ss.s.Secrets == nil {
		return nil, errNoSecrets
	}
	var (
		out     *bohuxv1.Secret
		id      uuid.UUID
		project uuid.UUID
		exists  bool
	)
	err = ss.s.Store.InTenant(ctx, p.Tenant, func(tx *store.Tx) error {
		prior, err := idem.lookup(ctx, tx)
		if err != nil {
			return err
		}
		if prior != nil {
			out, err = getSecret(ctx, tx, prior.ResourceID)
			return err
		}
		if project, err = environmentProject(ctx, tx, env); err != nil {
			return err
		}
		if err := authorizeProject(ctx, project, auth.RoleDeployer); err != nil {
			return err
		}
		var rev uuid.UUID
		err = tx.QueryRow(ctx, `SELECT id, revision_id FROM secret_refs_heads
			WHERE environment_id = $1 AND key = $2 AND NOT deleted`, env, msg.GetKey()).Scan(&id, &rev)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			if expected != nil {
				return store.ErrNotFound
			}
			id, err = store.NewID()
			return err
		case err != nil:
			return fmt.Errorf("look up secret: %w", err)
		case expected == nil:
			return connect.NewError(connect.CodeAlreadyExists,
				errors.New("secret exists; pass expected_revision_id to set a new value"))
		case *expected != rev:
			return store.ErrConflict
		}
		exists = true
		return nil
	})
	if err != nil || out != nil {
		return connectResp(&bohuxv1.SetSecretResponse{Secret: out}, toConnect(ctx, err))
	}
	version, err := ss.s.Secrets.Put(ctx, openbao.Path(p.Tenant, id), msg.GetValue())
	if err != nil {
		// Backend errors carry paths and statuses, never values.
		slog.ErrorContext(ctx, "store secret value", "secret", id.String(), "err", err)
		return nil, errNoSecrets
	}
	err = ss.s.Store.InTenant(ctx, p.Tenant, func(tx *store.Tx) error {
		audit := store.Audit{Actor: p.Actor(), Action: bohuxv1connect.SecretServiceSetSecretProcedure,
			ResourceType: secretType, ProjectID: project}
		rev := []store.Col{{Name: "value_version", Value: version}}
		var (
			revID uuid.UUID
			err   error
		)
		if exists {
			revID, err = tx.Update(ctx, "secret_refs", id, *expected, rev, audit)
		} else {
			revID, err = tx.CreateWithID(ctx, "secret_refs", id, []store.Col{
				{Name: "environment_id", Value: env}, {Name: "key", Value: msg.GetKey()},
				{Name: "handle", Value: openbao.Handle(p.Tenant, id)},
			}, rev, audit)
		}
		if err != nil {
			return err
		}
		if err := idem.record(ctx, tx, secretType, id, revID); err != nil {
			return err
		}
		out, err = getSecret(ctx, tx, id)
		return err
	})
	if err != nil {
		return nil, toConnect(ctx, err)
	}
	return connect.NewResponse(&bohuxv1.SetSecretResponse{Secret: out}), nil
}

func connectResp[T any](msg *T, err error) (*connect.Response[T], error) {
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(msg), nil
}

// ListSecretRefs lists an environment's secrets: keys and handles, never values.
func (ss *secretService) ListSecretRefs(ctx context.Context, req *connect.Request[bohuxv1.ListSecretRefsRequest]) (*connect.Response[bohuxv1.ListSecretRefsResponse], error) {
	p, _ := auth.FromContext(ctx)
	env, err := parseID("environment_id", req.Msg.GetEnvironmentId())
	if err != nil {
		return nil, err
	}
	size, after, err := page(req.Msg.GetPageSize(), req.Msg.GetPageToken())
	if err != nil {
		return nil, err
	}
	resp := &bohuxv1.ListSecretRefsResponse{}
	err = ss.s.Store.InTenant(ctx, p.Tenant, func(tx *store.Tx) error {
		project, err := environmentProject(ctx, tx, env)
		if err != nil {
			return err
		}
		if err := authorizeProject(ctx, project, auth.RoleViewer); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, secretSelect+`
			WHERE NOT h.deleted AND h.environment_id = $1 AND ($2::uuid IS NULL OR h.id > $2)
			ORDER BY h.id LIMIT $3`, env, after, size+1)
		if err != nil {
			return fmt.Errorf("list secrets: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			sec, err := scanSecret(rows)
			if err != nil {
				return err
			}
			resp.Secrets = append(resp.Secrets, sec)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, toConnect(ctx, err)
	}
	if len(resp.Secrets) > size {
		resp.Secrets = resp.Secrets[:size]
		resp.NextPageToken = resp.Secrets[size-1].GetMeta().GetId()
	}
	return connect.NewResponse(resp), nil
}

// DeleteSecret deletes a secret no live App references, then destroys its value.
func (ss *secretService) DeleteSecret(ctx context.Context, req *connect.Request[bohuxv1.DeleteSecretRequest]) (*connect.Response[bohuxv1.DeleteSecretResponse], error) {
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
	idem, err := newIdempotency(ctx, bohuxv1connect.SecretServiceDeleteSecretProcedure, msg.GetIdempotencyKey(), msg)
	if err != nil {
		return nil, toConnect(ctx, err)
	}
	resp := &bohuxv1.DeleteSecretResponse{}
	deleted := false
	err = ss.s.Store.InTenant(ctx, p.Tenant, func(tx *store.Tx) error {
		prior, err := idem.lookup(ctx, tx)
		if err != nil {
			return err
		}
		if prior != nil {
			resp.RevisionId = prior.RevisionID.String()
			return nil
		}
		sec, err := getSecret(ctx, tx, id)
		if err != nil {
			return err
		}
		env, err := uuid.Parse(sec.GetEnvironmentId())
		if err != nil {
			return fmt.Errorf("stored environment id: %w", err)
		}
		project, err := environmentProject(ctx, tx, env)
		if err != nil {
			return err
		}
		if err := authorizeProject(ctx, project, auth.RoleDeployer); err != nil {
			return err
		}
		var used bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM apps_heads a
			JOIN apps_revisions r ON r.id = a.revision_id
			WHERE NOT a.deleted AND a.environment_id = $1
			  AND r.secrets @> jsonb_build_array(jsonb_build_object('handle', $2::text)))`,
			env, sec.GetHandle()).Scan(&used); err != nil {
			return fmt.Errorf("check secret use: %w", err)
		}
		if used {
			return connect.NewError(connect.CodeFailedPrecondition, errors.New("an app still references this secret"))
		}
		rev, err := tx.Delete(ctx, "secret_refs", id, expected, store.Audit{Actor: p.Actor(),
			Action: bohuxv1connect.SecretServiceDeleteSecretProcedure, ResourceType: secretType, ProjectID: project})
		if err != nil {
			return err
		}
		resp.RevisionId, deleted = rev.String(), true
		return idem.record(ctx, tx, secretType, id, rev)
	})
	if err != nil {
		return nil, toConnect(ctx, err)
	}
	if deleted && ss.s.Secrets != nil {
		// Best effort: the reference is gone, so nothing resolves the value any more.
		if err := ss.s.Secrets.Destroy(ctx, openbao.Path(p.Tenant, id)); err != nil {
			slog.WarnContext(ctx, "destroy deleted secret's value", "secret", id.String(), "err", err)
		}
	}
	return connect.NewResponse(resp), nil
}

const secretSelect = `SELECT ` + metaColumns + `, h.environment_id, h.key, h.handle
	FROM secret_refs_heads h JOIN secret_refs_revisions r ON r.id = h.revision_id`

func getSecret(ctx context.Context, tx *store.Tx, id uuid.UUID) (*bohuxv1.Secret, error) {
	sec, err := scanSecret(tx.QueryRow(ctx, secretSelect+` WHERE h.id = $1 AND NOT h.deleted`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	return sec, err
}

func scanSecret(row pgx.Row) (*bohuxv1.Secret, error) {
	var (
		m   metaRow
		env uuid.UUID
		sec bohuxv1.Secret
	)
	if err := row.Scan(append(m.dest(), &env, &sec.Key, &sec.Handle)...); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
		return nil, fmt.Errorf("read secret: %w", err)
	}
	sec.Meta, sec.EnvironmentId = m.proto(), env.String()
	return &sec, nil
}
