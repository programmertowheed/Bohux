//go:build integration

package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	agentv1 "github.com/bohumix/bohux/gen/bohux/agent/v1"
	bohuxv1 "github.com/bohumix/bohux/gen/bohux/v1"
	"github.com/bohumix/bohux/gen/bohux/v1/bohuxv1connect"
	"github.com/bohumix/bohux/internal/agentapi"
	"github.com/bohumix/bohux/internal/auth"
	"github.com/bohumix/bohux/internal/store"
)

// memBackend is a SecretBackend in memory: versions per path.
type memBackend struct {
	mu   sync.Mutex
	vals map[string][]string
	down bool
}

func (m *memBackend) Put(_ context.Context, path, value string) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.down {
		return 0, fmt.Errorf("backend down")
	}
	m.vals[path] = append(m.vals[path], value)
	return len(m.vals[path]), nil
}

func (m *memBackend) Destroy(_ context.Context, path string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.vals, path)
	return nil
}

func TestSecrets(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	bao := &memBackend{vals: map[string][]string{}}
	mux := http.NewServeMux()
	(&Server{Store: h.st, Authn: &auth.Authenticator{Store: h.st}, PKI: h.pki, Secrets: bao}).Mount(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	secrets := bohuxv1connect.NewSecretServiceClient(srv.Client(), srv.URL)
	envs := bohuxv1connect.NewEnvironmentServiceClient(srv.Client(), srv.URL)
	apps := bohuxv1connect.NewAppServiceClient(srv.Client(), srv.URL)
	p1, p2 := h.project("one"), h.project("two")
	tok := func(project string, role bohuxv1.Role, key string) string {
		r, err := h.createToken(h.root, mustUUID(t, project), role, key)
		if err != nil {
			t.Fatal(err)
		}
		return r.GetSecret()
	}
	deployer := tok(p1.String(), bohuxv1.Role_ROLE_DEPLOYER, "d1")
	viewer := tok(p1.String(), bohuxv1.Role_ROLE_VIEWER, "v1")
	outsider := tok(p2.String(), bohuxv1.Role_ROLE_DEPLOYER, "d2")
	e, err := envs.CreateEnvironment(ctx, req(h.root, &bohuxv1.CreateEnvironmentRequest{
		ProjectId: p1.String(), Slug: "prod", ProviderBinding: dockerOn(h.node("vps-1")), IdempotencyKey: "e"}))
	if err != nil {
		t.Fatal(err)
	}
	env := e.Msg.GetEnvironment().GetMeta().GetId()
	const v1, v2 = "postgres://u:first-hunter2@db/app", "postgres://u:second-hunter2@db/app"
	set := func(token, key, value, expected, idem string) (*bohuxv1.Secret, error) {
		r, err := secrets.SetSecret(ctx, req(token, &bohuxv1.SetSecretRequest{EnvironmentId: env, Key: key,
			Value: value, ExpectedRevisionId: expected, IdempotencyKey: idem}))
		if err != nil {
			return nil, err
		}
		return r.Msg.GetSecret(), nil
	}

	created, err := set(deployer, "DATABASE_URL", v1, "", "s1")
	if err != nil {
		t.Fatal(err)
	}
	path := strings.TrimPrefix(created.GetHandle(), "openbao:")
	if !strings.HasPrefix(path, "bohux/"+h.tenant.String()+"/") || bao.vals[path][0] != v1 {
		t.Fatalf("handle %q, stored %v", created.GetHandle(), bao.vals)
	}
	t.Run("replay returns the same secret without writing", func(t *testing.T) {
		again, err := set(deployer, "DATABASE_URL", v1, "", "s1")
		if err != nil || again.GetMeta().GetRevisionId() != created.GetMeta().GetRevisionId() || len(bao.vals[path]) != 1 {
			t.Errorf("replay = %v, %v; %d versions", again, err, len(bao.vals[path]))
		}
	})
	t.Run("overwrite needs the expected revision", func(t *testing.T) {
		_, err := set(deployer, "DATABASE_URL", v2, "", "s2")
		wantCode(t, err, connect.CodeAlreadyExists)
		_, err = set(deployer, "DATABASE_URL", v2, mustUUID(t, created.GetMeta().GetId()).String(), "s3")
		wantCode(t, err, connect.CodeAborted)
		updated, err := set(deployer, "DATABASE_URL", v2, created.GetMeta().GetRevisionId(), "s4")
		if err != nil {
			t.Fatal(err)
		}
		if updated.GetHandle() != created.GetHandle() || len(bao.vals[path]) != 2 {
			t.Errorf("update changed the handle or did not write: %v", updated)
		}
		var version int
		if err := h.st.InTenant(ctx, h.tenant, func(tx *store.Tx) error {
			return tx.QueryRow(ctx, `SELECT r.value_version FROM secret_refs_heads s
				JOIN secret_refs_revisions r ON r.id = s.revision_id WHERE s.id = $1`, mustUUID(t, updated.GetMeta().GetId())).Scan(&version)
		}); err != nil || version != 2 {
			t.Errorf("recorded version %d, %v; want 2", version, err)
		}
		created = updated
	})
	t.Run("refusals", func(t *testing.T) {
		_, err := set(viewer, "OTHER", v1, "", "x1")
		wantCode(t, err, connect.CodePermissionDenied)
		_, err = set(outsider, "OTHER", v1, "", "x2")
		wantCode(t, err, connect.CodeNotFound)
		_, err = set(deployer, "lower-case", v1, "", "x3")
		wantCode(t, err, connect.CodeInvalidArgument)
		_, err = set(deployer, "EMPTY", "", "", "x4")
		wantCode(t, err, connect.CodeInvalidArgument)
		bao.down = true
		_, err = set(deployer, "DOWN", v1, "", "x5")
		bao.down = false
		wantCode(t, err, connect.CodeUnavailable)
	})
	t.Run("list shows keys and handles", func(t *testing.T) {
		r, err := secrets.ListSecretRefs(ctx, req(viewer, &bohuxv1.ListSecretRefsRequest{EnvironmentId: env}))
		if err != nil || len(r.Msg.GetSecrets()) != 1 || r.Msg.GetSecrets()[0].GetKey() != "DATABASE_URL" {
			t.Errorf("list = %v, %v", r, err)
		}
	})
	t.Run("a referenced secret cannot be deleted", func(t *testing.T) {
		app, err := apps.CreateApp(ctx, req(deployer, &bohuxv1.CreateAppRequest{EnvironmentId: env, Slug: "api",
			IdempotencyKey: "a", Runtime: &bohuxv1.Runtime{Replicas: 1},
			Source: &bohuxv1.Source{Kind: &bohuxv1.Source_Image{Image: &bohuxv1.ImageSource{
				Repository: "ghcr.io/acme/api", Digest: "sha256:" + strings.Repeat("ab", 32)}}},
			Secrets: []*bohuxv1.SecretRef{{Key: "DATABASE_URL", Handle: created.GetHandle()}}}))
		if err != nil {
			t.Fatal(err)
		}
		_, err = secrets.DeleteSecret(ctx, req(deployer, &bohuxv1.DeleteSecretRequest{Id: created.GetMeta().GetId(),
			ExpectedRevisionId: created.GetMeta().GetRevisionId(), IdempotencyKey: "del1"}))
		wantCode(t, err, connect.CodeFailedPrecondition)
		a := app.Msg.GetApp()
		if _, err := apps.DeleteApp(ctx, req(h.root, &bohuxv1.DeleteAppRequest{Id: a.GetMeta().GetId(),
			ExpectedRevisionId: a.GetMeta().GetRevisionId(), IdempotencyKey: "da"})); err != nil {
			t.Fatal(err)
		}
		if _, err := secrets.DeleteSecret(ctx, req(deployer, &bohuxv1.DeleteSecretRequest{Id: created.GetMeta().GetId(),
			ExpectedRevisionId: created.GetMeta().GetRevisionId(), IdempotencyKey: "del2"})); err != nil {
			t.Fatal(err)
		}
		if _, ok := bao.vals[path]; ok {
			t.Error("the value outlived its secret")
		}
	})
	t.Run("values never reach Postgres", func(t *testing.T) {
		err := h.st.InTenant(ctx, h.tenant, func(tx *store.Tx) error {
			rows, err := tx.Query(ctx, `SELECT table_name FROM information_schema.tables
				WHERE table_schema = 'public' AND table_type = 'BASE TABLE'`)
			if err != nil {
				return err
			}
			var tables []string
			for rows.Next() {
				var n string
				if err := rows.Scan(&n); err != nil {
					return err
				}
				tables = append(tables, n)
			}
			rows.Close()
			for _, table := range tables {
				var n int
				if err := tx.QueryRow(ctx, `SELECT count(*) FROM `+table+` x
					WHERE x::text LIKE '%hunter2%'`).Scan(&n); err != nil {
					return fmt.Errorf("%s: %w", table, err)
				}
				if n > 0 {
					t.Errorf("table %s holds a secret value", table)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	})
}

// TestSecretRotationAssigns checks a new value writes a new generation for the node
// running a workload that uses it, in SetSecret's transaction.
func TestSecretRotationAssigns(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	agents := &agentapi.Server{Store: h.st}
	mux := http.NewServeMux()
	(&Server{Store: h.st, Authn: &auth.Authenticator{Store: h.st}, PKI: h.pki,
		Secrets: &memBackend{vals: map[string][]string{}}, Agents: agents}).Mount(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	secrets := bohuxv1connect.NewSecretServiceClient(srv.Client(), srv.URL)
	envs := bohuxv1connect.NewEnvironmentServiceClient(srv.Client(), srv.URL)
	node := h.node("vps-1")
	e, err := envs.CreateEnvironment(ctx, req(h.root, &bohuxv1.CreateEnvironmentRequest{
		ProjectId: h.project("one").String(), Slug: "prod", ProviderBinding: dockerOn(node), IdempotencyKey: "e"}))
	if err != nil {
		t.Fatal(err)
	}
	env := e.Msg.GetEnvironment().GetMeta().GetId()
	s1, err := secrets.SetSecret(ctx, req(h.root, &bohuxv1.SetSecretRequest{EnvironmentId: env, Key: "TOKEN",
		Value: "one", IdempotencyKey: "s1"}))
	if err != nil {
		t.Fatal(err)
	}
	sec := s1.Msg.GetSecret()
	appID := fixtureApp(t, h, env)
	if _, err := agents.AssignApp(ctx, h.tenant, node, appID, &agentv1.Workload{AppId: appID.String(),
		ReleaseId: "r1", DeploymentId: "d1", Image: "img", Runtime: &bohuxv1.Runtime{Replicas: 1},
		Secrets: []*bohuxv1.SecretRef{{Key: "TOKEN", Handle: sec.GetHandle()}}}, time.Time{},
		store.Actor{ID: store.SystemActorID, Kind: store.ActorSystem}); err != nil {
		t.Fatal(err)
	}
	if _, err := secrets.SetSecret(ctx, req(h.root, &bohuxv1.SetSecretRequest{EnvironmentId: env, Key: "TOKEN",
		Value: "two", ExpectedRevisionId: sec.GetMeta().GetRevisionId(), IdempotencyKey: "s2"})); err != nil {
		t.Fatal(err)
	}
	var gen int64
	if err := h.st.InTenant(ctx, h.tenant, func(tx *store.Tx) error {
		return tx.QueryRow(ctx, `SELECT max(generation) FROM assignments WHERE node_id = $1`, node).Scan(&gen)
	}); err != nil || gen != 2 {
		t.Errorf("after rotation the node is at generation %d, %v; want 2", gen, err)
	}
}

// fixtureApp creates an App row in env directly and returns its ID.
func fixtureApp(t *testing.T, h *harness, env string) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	envID := mustUUID(t, env)
	var app uuid.UUID
	err := h.st.InTenant(ctx, h.tenant, func(tx *store.Tx) error {
		var err error
		app, _, err = tx.Create(ctx, "apps",
			[]store.Col{{Name: "environment_id", Value: envID}, {Name: "slug", Value: "api"}},
			[]store.Col{{Name: "source", Value: "{}"}, {Name: "runtime", Value: "{}"}},
			store.Audit{Actor: store.Actor{ID: store.SystemActorID, Kind: store.ActorSystem}, Action: "test", ResourceType: appType})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return app
}
