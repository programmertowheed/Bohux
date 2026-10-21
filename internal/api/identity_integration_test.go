//go:build integration

package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	bohuxv1 "github.com/bohumix/bohux/gen/bohux/v1"
	"github.com/bohumix/bohux/gen/bohux/v1/bohuxv1connect"
	"github.com/bohumix/bohux/internal/auth"
	"github.com/bohumix/bohux/internal/store"
	"github.com/bohumix/bohux/internal/store/storetest"
)

type harness struct {
	t        *testing.T
	st       *store.Store
	tenant   uuid.UUID
	root     string // tenant-wide owner token from bootstrap
	identity bohuxv1connect.IdentityServiceClient
	tenants  bohuxv1connect.TenantServiceClient
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	ctx := context.Background()
	dsn := storetest.NewAppDSN(t)
	if err := store.MigrateDSN(ctx, dsn); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	tenant, root, err := Bootstrap(ctx, st, "acme", "Acme")
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	mux := http.NewServeMux()
	(&Server{Store: st, Authn: &auth.Authenticator{Store: st}}).Mount(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &harness{
		t: t, st: st, tenant: tenant, root: root,
		identity: bohuxv1connect.NewIdentityServiceClient(srv.Client(), srv.URL),
		tenants:  bohuxv1connect.NewTenantServiceClient(srv.Client(), srv.URL),
	}
}

func req[T any](token string, msg *T) *connect.Request[T] {
	r := connect.NewRequest(msg)
	r.Header().Set("Authorization", "Bearer "+token)
	return r
}

// project creates a project directly in the store (ProjectService arrives in S3e).
func (h *harness) project(slug string) uuid.UUID {
	h.t.Helper()
	var id uuid.UUID
	err := h.st.InTenant(context.Background(), h.tenant, func(tx *store.Tx) error {
		var err error
		id, _, err = tx.Create(context.Background(), "projects",
			[]store.Col{{Name: "slug", Value: slug}}, nil,
			store.Audit{Actor: store.Actor{ID: store.SystemActorID, Kind: store.ActorSystem},
				Action: "test", ResourceType: "bohux.v1.Project"})
		return err
	})
	if err != nil {
		h.t.Fatal(err)
	}
	return id
}

func (h *harness) createToken(caller string, project uuid.UUID, role bohuxv1.Role, key string) (*bohuxv1.CreateApiTokenResponse, error) {
	msg := &bohuxv1.CreateApiTokenRequest{Name: key, Role: role, IdempotencyKey: key}
	if project != uuid.Nil {
		msg.ProjectId = project.String()
	}
	resp, err := h.identity.CreateApiToken(context.Background(), req(caller, msg))
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}

func wantCode(t *testing.T, err error, want connect.Code) {
	t.Helper()
	if got := connect.CodeOf(err); err == nil || got != want {
		t.Fatalf("got %v (%v), want %v", got, err, want)
	}
}

func TestIdentityAndTenant(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	p1, p2 := h.project("one"), h.project("two")

	t.Run("bootstrap token: WhoAmI and GetTenant", func(t *testing.T) {
		who, err := h.identity.WhoAmI(ctx, req(h.root, &bohuxv1.WhoAmIRequest{}))
		if err != nil {
			t.Fatal(err)
		}
		if who.Msg.GetTenantId() != h.tenant.String() || who.Msg.GetPrincipal().GetDisplayName() != "bootstrap" {
			t.Errorf("WhoAmI = %v", who.Msg)
		}
		ten, err := h.tenants.GetTenant(ctx, req(h.root, &bohuxv1.GetTenantRequest{}))
		if err != nil {
			t.Fatal(err)
		}
		if ten.Msg.GetTenant().GetSlug() != "acme" || ten.Msg.GetTenant().GetDisplayName() != "Acme" {
			t.Errorf("GetTenant = %v", ten.Msg)
		}
	})

	owner1, err := h.createToken(h.root, p1, bohuxv1.Role_ROLE_OWNER, "owner-1")
	if err != nil {
		t.Fatal(err)
	}
	owner2, err := h.createToken(h.root, p2, bohuxv1.Role_ROLE_OWNER, "owner-2")
	if err != nil {
		t.Fatal(err)
	}

	t.Run("a created token works and carries its scope", func(t *testing.T) {
		who, err := h.identity.WhoAmI(ctx, req(owner1.GetSecret(), &bohuxv1.WhoAmIRequest{}))
		if err != nil {
			t.Fatal(err)
		}
		if who.Msg.GetPrincipal().GetId() != owner1.GetApiToken().GetMeta().GetId() {
			t.Errorf("WhoAmI id = %s", who.Msg.GetPrincipal().GetId())
		}
		if owner1.GetApiToken().GetProjectId() != p1.String() {
			t.Errorf("project_id = %s, want %s", owner1.GetApiToken().GetProjectId(), p1)
		}
	})

	t.Run("idempotent retry returns the same token without the secret", func(t *testing.T) {
		again, err := h.createToken(h.root, p1, bohuxv1.Role_ROLE_OWNER, "owner-1")
		if err != nil {
			t.Fatal(err)
		}
		if again.GetApiToken().GetMeta().GetId() != owner1.GetApiToken().GetMeta().GetId() {
			t.Error("retry created a second token")
		}
		if again.GetSecret() != "" {
			t.Error("retry returned the secret again")
		}
	})

	t.Run("same key, different request", func(t *testing.T) {
		_, err := h.createToken(h.root, p1, bohuxv1.Role_ROLE_VIEWER, "owner-1")
		wantCode(t, err, connect.CodeFailedPrecondition)
	})

	t.Run("missing idempotency key", func(t *testing.T) {
		_, err := h.identity.CreateApiToken(ctx, req(h.root,
			&bohuxv1.CreateApiTokenRequest{Name: "x", Role: bohuxv1.Role_ROLE_VIEWER}))
		wantCode(t, err, connect.CodeInvalidArgument)
	})

	t.Run("no escalation", func(t *testing.T) {
		_, err := h.createToken(owner1.GetSecret(), uuid.Nil, bohuxv1.Role_ROLE_VIEWER, "widen")
		wantCode(t, err, connect.CodePermissionDenied)
		_, err = h.createToken(owner1.GetSecret(), p2, bohuxv1.Role_ROLE_VIEWER, "other-project")
		wantCode(t, err, connect.CodePermissionDenied)
		dep, err := h.createToken(owner1.GetSecret(), p1, bohuxv1.Role_ROLE_DEPLOYER, "deployer-1")
		if err != nil {
			t.Fatal(err)
		}
		_, err = h.createToken(dep.GetSecret(), p1, bohuxv1.Role_ROLE_VIEWER, "from-deployer")
		wantCode(t, err, connect.CodePermissionDenied)
	})

	t.Run("list is scoped", func(t *testing.T) {
		list := func(token string) map[string]bool {
			resp, err := h.identity.ListApiTokens(ctx, req(token, &bohuxv1.ListApiTokensRequest{}))
			if err != nil {
				t.Fatal(err)
			}
			ids := map[string]bool{}
			for _, tok := range resp.Msg.GetApiTokens() {
				ids[tok.GetMeta().GetId()] = true
			}
			return ids
		}
		all, mine := list(h.root), list(owner1.GetSecret())
		if !all[owner2.GetApiToken().GetMeta().GetId()] || !all[owner1.GetApiToken().GetMeta().GetId()] {
			t.Error("tenant owner does not see every token")
		}
		if mine[owner2.GetApiToken().GetMeta().GetId()] {
			t.Error("project owner sees another project's token")
		}
		if !mine[owner1.GetApiToken().GetMeta().GetId()] {
			t.Error("project owner does not see its own project's token")
		}
	})

	t.Run("paging", func(t *testing.T) {
		seen := map[string]bool{}
		token := ""
		for pages := 0; pages < 10; pages++ {
			resp, err := h.identity.ListApiTokens(ctx, req(h.root,
				&bohuxv1.ListApiTokensRequest{PageSize: 2, PageToken: token}))
			if err != nil {
				t.Fatal(err)
			}
			for _, tok := range resp.Msg.GetApiTokens() {
				if seen[tok.GetMeta().GetId()] {
					t.Fatalf("token %s on two pages", tok.GetMeta().GetId())
				}
				seen[tok.GetMeta().GetId()] = true
			}
			if token = resp.Msg.GetNextPageToken(); token == "" {
				break
			}
		}
		if len(seen) < 4 {
			t.Errorf("paged through %d tokens, want at least 4", len(seen))
		}
	})

	t.Run("revoke", func(t *testing.T) {
		id := owner2.GetApiToken().GetMeta().GetId()
		_, err := h.identity.RevokeApiToken(ctx, req(owner1.GetSecret(),
			&bohuxv1.RevokeApiTokenRequest{Id: id, IdempotencyKey: "r0"}))
		wantCode(t, err, connect.CodeNotFound)

		stale := owner1.GetApiToken().GetMeta().GetRevisionId()
		_, err = h.identity.RevokeApiToken(ctx, req(h.root, &bohuxv1.RevokeApiTokenRequest{
			Id: id, ExpectedRevisionId: stale, IdempotencyKey: "r1"}))
		wantCode(t, err, connect.CodeAborted)

		resp, err := h.identity.RevokeApiToken(ctx, req(h.root, &bohuxv1.RevokeApiTokenRequest{
			Id: id, ExpectedRevisionId: owner2.GetApiToken().GetMeta().GetRevisionId(), IdempotencyKey: "r2"}))
		if err != nil {
			t.Fatal(err)
		}
		if resp.Msg.GetApiToken().GetRevokedAt() == nil {
			t.Error("revoked_at not set")
		}
		_, err = h.identity.WhoAmI(ctx, req(owner2.GetSecret(), &bohuxv1.WhoAmIRequest{}))
		wantCode(t, err, connect.CodeUnauthenticated)
	})

	t.Run("mutations are audited with the caller as actor", func(t *testing.T) {
		var n int
		err := h.st.InTenant(ctx, h.tenant, func(tx *store.Tx) error {
			return tx.QueryRow(ctx, `SELECT count(*) FROM audit_events
				WHERE resource_id = $1 AND actor_kind = 'api_token'
				  AND action = $2`, owner1.GetApiToken().GetMeta().GetId(),
				bohuxv1connect.IdentityServiceCreateApiTokenProcedure).Scan(&n)
		})
		if err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Errorf("audit events for the create = %d, want 1 (the retry must not add one)", n)
		}
	})
}

func mustUUID(t *testing.T, s string) uuid.UUID {
	t.Helper()
	id, err := uuid.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return id
}
