package api

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/bohumix/bohux/internal/auth"
	"github.com/bohumix/bohux/internal/store"
)

// Bootstrap creates a tenant and its first credential: a tenant-wide owner API token
// (ADR-0023). It is a CLI step, not an RPC (api-contract.md). The token is returned
// once and is not stored anywhere but as a hash.
func Bootstrap(ctx context.Context, st *store.Store, slug, displayName string) (tenant uuid.UUID, token string, err error) {
	if slug == "" {
		return uuid.Nil, "", fmt.Errorf("bootstrap: slug is required")
	}
	if tenant, err = store.NewID(); err != nil {
		return uuid.Nil, "", err
	}
	system := store.Actor{ID: store.SystemActorID, Kind: store.ActorSystem}
	err = st.InTenant(ctx, tenant, func(tx *store.Tx) error {
		if _, _, err := tx.Create(ctx, "tenants",
			[]store.Col{{Name: "slug", Value: slug}},
			[]store.Col{{Name: "display_name", Value: displayName}},
			store.Audit{Actor: system, Action: "bohux.admin/Bootstrap", ResourceType: "bohux.v1.Tenant"}); err != nil {
			return err
		}
		var err error
		token, _, _, err = auth.CreateAPIToken(ctx, tx, uuid.Nil, auth.RoleOwner, "bootstrap", nil,
			store.Audit{Actor: system, Action: "bohux.admin/Bootstrap", ResourceType: apiTokenType})
		return err
	})
	if err != nil {
		return uuid.Nil, "", fmt.Errorf("bootstrap tenant %q: %w", slug, err)
	}
	return tenant, token, nil
}
