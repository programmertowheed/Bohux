package openbao

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/bohumix/bohux/internal/store"
)

// HandlePrefix starts every OpenBao handle; the rest is the KV path.
const HandlePrefix = "openbao:"

// ErrForeignHandle means a handle does not belong to the tenant asking for it.
var ErrForeignHandle = errors.New("openbao: handle belongs to another tenant")

// Path is where a secret's value lives in the KV mount: one path per secret, under its
// tenant, stable across value changes.
func Path(tenant, secret uuid.UUID) string {
	return "bohux/" + tenant.String() + "/" + secret.String()
}

// Handle is the opaque reference stored in Postgres and assignments (rule 3).
func Handle(tenant, secret uuid.UUID) string { return HandlePrefix + Path(tenant, secret) }

// pathOf returns the KV path a handle names, refusing one outside tenant.
func pathOf(tenant uuid.UUID, handle string) (string, error) {
	path, ok := strings.CutPrefix(handle, HandlePrefix)
	if !ok || !strings.HasPrefix(path, "bohux/"+tenant.String()+"/") {
		return "", ErrForeignHandle
	}
	return path, nil
}

// Resolver turns handles into values for the agent API (agentapi.SecretResolver). It
// reads the version Postgres recorded for each handle, so a value written to OpenBao
// whose revision never committed is never sent.
type Resolver struct {
	Store *store.Store
	Bao   *Client
}

// Resolve returns the value of each handle. A handle that is unknown, deleted, or
// another tenant's fails the whole call.
func (r *Resolver) Resolve(ctx context.Context, tenant uuid.UUID, handles []string) (map[string]string, error) {
	versions := make(map[string]int, len(handles))
	err := r.Store.InTenant(ctx, tenant, func(tx *store.Tx) error {
		for _, h := range handles {
			var v int
			err := tx.QueryRow(ctx, `SELECT r.value_version FROM secret_refs_heads s
				JOIN secret_refs_revisions r ON r.id = s.revision_id
				WHERE s.handle = $1 AND NOT s.deleted`, h).Scan(&v)
			if errors.Is(err, pgx.ErrNoRows) {
				return fmt.Errorf("secret handle %q: %w", h, store.ErrNotFound)
			}
			if err != nil {
				return fmt.Errorf("read secret version: %w", err)
			}
			versions[h] = v
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(handles))
	for h, v := range versions {
		path, err := pathOf(tenant, h)
		if err != nil {
			return nil, err
		}
		value, err := r.Bao.Get(ctx, path, v)
		if err != nil {
			return nil, err
		}
		out[h] = value
	}
	return out, nil
}
