package api

import (
	"context"
	"errors"
	"fmt"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5"

	bohuxv1 "github.com/bohumix/bohux/gen/bohux/v1"
	"github.com/bohumix/bohux/internal/auth"
	"github.com/bohumix/bohux/internal/store"
)

type tenantService struct{ s *Server }

func (t *tenantService) GetTenant(ctx context.Context, _ *connect.Request[bohuxv1.GetTenantRequest]) (*connect.Response[bohuxv1.GetTenantResponse], error) {
	p, _ := auth.FromContext(ctx)
	var (
		m          metaRow
		slug, name string
	)
	err := t.s.Store.InTenant(ctx, p.Tenant, func(tx *store.Tx) error {
		err := tx.QueryRow(ctx, `SELECT `+metaColumns+`, h.slug, r.display_name
			FROM tenants_heads h JOIN tenants_revisions r ON r.id = h.revision_id
			WHERE h.id = $1 AND NOT h.deleted`, p.Tenant).Scan(append(m.dest(), &slug, &name)...)
		if errors.Is(err, pgx.ErrNoRows) {
			return store.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("get tenant: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, toConnect(ctx, err)
	}
	return connect.NewResponse(&bohuxv1.GetTenantResponse{
		Tenant: &bohuxv1.Tenant{Meta: m.proto(), Slug: slug, DisplayName: name},
	}), nil
}
