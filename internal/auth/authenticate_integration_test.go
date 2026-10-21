//go:build integration

package auth

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bohumix/bohux/internal/store"
	"github.com/bohumix/bohux/internal/store/storetest"
)

func newStore(t *testing.T) *store.Store {
	t.Helper()
	ctx := context.Background()
	dsn := storetest.NewAppDSN(t)
	if err := store.MigrateDSN(ctx, dsn); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	s, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(s.Close)
	return s
}

var system = store.Actor{ID: uuid.MustParse("0192f0a0-0000-7000-8000-0000000000ff"), Kind: store.ActorSystem}

func audit(action, typ string) store.Audit {
	return store.Audit{Actor: system, Action: action, ResourceType: typ}
}

// seed creates a tenant, a project, and API tokens in it.
func seed(t *testing.T, s *store.Store, tenant uuid.UUID, tokens map[string]*time.Time) (project uuid.UUID, raw map[string]string, ids map[string]uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	raw, ids = map[string]string{}, map[string]uuid.UUID{}
	err := s.InTenant(ctx, tenant, func(tx *store.Tx) error {
		if _, _, err := tx.Create(ctx, "tenants", []store.Col{{Name: "slug", Value: "t" + tenant.String()[24:]}}, nil,
			audit("bootstrap", "bohux.v1.Tenant")); err != nil {
			return err
		}
		var err error
		project, _, err = tx.Create(ctx, "projects", []store.Col{{Name: "slug", Value: "web"}}, nil,
			audit("bohux.v1.ProjectService/CreateProject", "bohux.v1.Project"))
		if err != nil {
			return err
		}
		for name, exp := range tokens {
			r, id, _, err := CreateAPIToken(ctx, tx, project, RoleDeployer, name, exp,
				audit("bohux.v1.IdentityService/CreateApiToken", "bohux.v1.ApiToken"))
			if err != nil {
				return err
			}
			raw[name], ids[name] = r, id
		}
		return nil
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	return project, raw, ids
}

func TestAuthenticateAPIToken(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	now := time.Now()
	past, future := now.Add(-time.Hour), now.Add(time.Hour)
	tenantA := uuid.MustParse("0192f0a0-0000-7000-8000-00000000000a")
	tenantB := uuid.MustParse("0192f0a0-0000-7000-8000-00000000000b")
	project, raw, ids := seed(t, s, tenantA, map[string]*time.Time{
		"ok": nil, "unexpired": &future, "expired": &past, "revoked": nil,
	})
	_, rawB, _ := seed(t, s, tenantB, map[string]*time.Time{"other": nil})

	// Revoke one token with an ordinary revision.
	err := s.InTenant(ctx, tenantA, func(tx *store.Tx) error {
		var rev uuid.UUID
		if err := tx.QueryRow(ctx, `SELECT revision_id FROM api_tokens_heads WHERE id = $1`,
			ids["revoked"]).Scan(&rev); err != nil {
			return err
		}
		_, err := tx.Update(ctx, "api_tokens", ids["revoked"], rev,
			[]store.Col{{Name: "name", Value: "revoked"}, {Name: "revoked_at", Value: now}},
			audit("bohux.v1.IdentityService/RevokeApiToken", "bohux.v1.ApiToken"))
		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	a := &Authenticator{Store: s, Now: func() time.Time { return now }}

	t.Run("valid tokens", func(t *testing.T) {
		for _, name := range []string{"ok", "unexpired"} {
			p, err := a.AuthenticateAPIToken(ctx, raw[name])
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			want := Principal{ID: ids[name], Kind: store.ActorAPIToken, Tenant: tenantA,
				ProjectID: project, Role: RoleDeployer}
			if p != want {
				t.Errorf("%s: got %+v, want %+v", name, p, want)
			}
		}
	})

	// Swap the tenant part of tenant B's token for tenant A's.
	tampered := strings.Replace(rawB["other"],
		strings.ReplaceAll(tenantB.String(), "-", ""), strings.ReplaceAll(tenantA.String(), "-", ""), 1)
	// Keep A's tenant and ID, but use another token's secret.
	wrongSecret := strings.Join(strings.SplitN(raw["ok"], "_", 4)[:3], "_") + "_" +
		strings.SplitN(raw["unexpired"], "_", 4)[3]
	bootstrap, _, err := Issue(KindBootstrap, tenantA, ids["ok"])
	if err != nil {
		t.Fatal(err)
	}
	unknown, _, err := Issue(KindAPI, tenantA, uuid.Must(uuid.NewV7()))
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct{ name, raw string }{
		{"garbage", "not-a-token"},
		{"expired", raw["expired"]},
		{"revoked", raw["revoked"]},
		{"wrong secret", wrongSecret},
		{"tenant swapped", tampered},
		{"bootstrap kind", bootstrap},
		{"unknown id", unknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := a.AuthenticateAPIToken(ctx, tt.raw)
			if !errors.Is(err, ErrUnauthenticated) {
				t.Errorf("got %v, want ErrUnauthenticated", err)
			}
		})
	}

	t.Run("records last use", func(t *testing.T) {
		var used *time.Time
		err := s.InTenant(ctx, tenantA, func(tx *store.Tx) error {
			return tx.QueryRow(ctx, `SELECT last_used_at FROM api_tokens_heads WHERE id = $1`,
				ids["ok"]).Scan(&used)
		})
		if err != nil {
			t.Fatal(err)
		}
		if used == nil || used.Sub(now).Abs() > time.Millisecond {
			t.Errorf("last_used_at = %v, want %v", used, now)
		}
	})

	t.Run("the stored hash is not the secret", func(t *testing.T) {
		var hash []byte
		err := s.InTenant(ctx, tenantA, func(tx *store.Tx) error {
			return tx.QueryRow(ctx, `SELECT token_hash FROM api_tokens_heads WHERE id = $1`,
				ids["ok"]).Scan(&hash)
		})
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(raw["ok"], string(hash)) || len(hash) != 32 {
			t.Errorf("stored token_hash looks wrong: %d bytes", len(hash))
		}
	})
}
