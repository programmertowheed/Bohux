package auth

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/bohumix/bohux/internal/store"
)

// lastUsedGranularity bounds how often a token's last_used_at is written.
const lastUsedGranularity = time.Minute

// Authenticator verifies credentials against the store.
type Authenticator struct {
	Store *store.Store
	// Now is the clock; nil means time.Now.
	Now func() time.Time
}

func (a *Authenticator) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

// AuthenticateAPIToken resolves an API token to its principal. Every failure is
// ErrUnauthenticated; infrastructure errors are returned wrapped so they are not
// mistaken for a bad token.
func (a *Authenticator) AuthenticateAPIToken(ctx context.Context, raw string) (Principal, error) {
	tok, err := Parse(raw)
	if err != nil || tok.Kind != KindAPI {
		return Principal{}, ErrUnauthenticated
	}
	var p Principal
	err = a.Store.InTenant(ctx, tok.Tenant, func(tx *store.Tx) error {
		var (
			hash      []byte
			project   uuid.UUID
			role      string
			expiresAt *time.Time
			revokedAt *time.Time
			lastUsed  *time.Time
		)
		err := tx.QueryRow(ctx, `
			SELECT h.token_hash, h.project_id, h.role, h.expires_at, r.revoked_at, h.last_used_at
			FROM api_tokens_heads h
			JOIN api_tokens_revisions r ON r.id = h.revision_id
			WHERE h.id = $1 AND NOT h.deleted`, tok.ID,
		).Scan(&hash, &project, &role, &expiresAt, &revokedAt, &lastUsed)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrUnauthenticated
		}
		if err != nil {
			return fmt.Errorf("look up api token: %w", err)
		}
		now := a.now()
		if !tok.Matches(hash) || revokedAt != nil || (expiresAt != nil && !now.Before(*expiresAt)) {
			return ErrUnauthenticated
		}
		// An observation, not state: updated in place, at most once a minute.
		if lastUsed == nil || now.Sub(*lastUsed) >= lastUsedGranularity {
			if err := tx.Exec(ctx,
				`UPDATE api_tokens_heads SET last_used_at = $1 WHERE id = $2`, now, tok.ID); err != nil {
				return fmt.Errorf("record api token use: %w", err)
			}
		}
		p = Principal{
			ID: tok.ID, Kind: store.ActorAPIToken, Tenant: tok.Tenant,
			ProjectID: project, Role: ParseRole(role),
		}
		return nil
	})
	if errors.Is(err, ErrUnauthenticated) {
		return Principal{}, ErrUnauthenticated
	}
	if err != nil {
		return Principal{}, fmt.Errorf("authenticate: %w", err)
	}
	return p, nil
}

// CreateAPIToken creates an API token in tx and returns the credential to show once.
// expiresAt may be nil for no expiry.
func CreateAPIToken(ctx context.Context, tx *store.Tx, project uuid.UUID, role Role, name string, expiresAt *time.Time, a store.Audit) (raw string, id, rev uuid.UUID, err error) {
	if role == RoleNone {
		return "", uuid.Nil, uuid.Nil, errors.New("create api token: role is required")
	}
	if id, err = store.NewID(); err != nil {
		return "", uuid.Nil, uuid.Nil, err
	}
	raw, hash, err := Issue(KindAPI, tx.Tenant(), id)
	if err != nil {
		return "", uuid.Nil, uuid.Nil, err
	}
	rev, err = tx.CreateWithID(ctx, "api_tokens", id,
		[]store.Col{
			{Name: "project_id", Value: project}, {Name: "role", Value: role.String()},
			{Name: "token_hash", Value: hash}, {Name: "expires_at", Value: expiresAt},
		},
		[]store.Col{{Name: "name", Value: name}}, a)
	if err != nil {
		return "", uuid.Nil, uuid.Nil, fmt.Errorf("create api token: %w", err)
	}
	return raw, id, rev, nil
}
