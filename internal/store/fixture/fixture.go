// Package fixture creates resources directly in the store for tests in other
// packages, without going through the API. Never imported by non-test code.
package fixture

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/bohumix/bohux/internal/store"
)

var system = store.Audit{Actor: store.Actor{ID: store.SystemActorID, Kind: store.ActorSystem}, Action: "test"}

// App creates a project, an environment with the given slug, and an App in it with the
// given slug, and returns the App and environment IDs.
func App(t testing.TB, st *store.Store, tenant uuid.UUID, appSlug, envSlug string) (app, env uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	err := st.InTenant(ctx, tenant, func(tx *store.Tx) error {
		a := system
		a.ResourceType = "bohux.v1.Project"
		project, _, err := tx.Create(ctx, "projects",
			[]store.Col{{Name: "slug", Value: "p-" + uuid.NewString()[:8]}}, nil, a)
		if err != nil {
			return err
		}
		a.ResourceType = "bohux.v1.Environment"
		if env, _, err = tx.Create(ctx, "environments",
			[]store.Col{{Name: "project_id", Value: project}, {Name: "slug", Value: envSlug}},
			[]store.Col{{Name: "provider", Value: "docker"}, {Name: "target_ref", Value: "local"}}, a); err != nil {
			return err
		}
		a.ResourceType = "bohux.v1.App"
		app, _, err = tx.Create(ctx, "apps",
			[]store.Col{{Name: "environment_id", Value: env}, {Name: "slug", Value: appSlug}},
			[]store.Col{{Name: "source", Value: "{}"}, {Name: "runtime", Value: "{}"}}, a)
		return err
	})
	if err != nil {
		t.Fatalf("create app fixture: %v", err)
	}
	return app, env
}

// Node creates an online node row with the given slug, as Register would, and returns
// its ID.
func Node(t testing.TB, st *store.Store, tenant uuid.UUID, slug string) uuid.UUID {
	t.Helper()
	var node uuid.UUID
	err := st.InTenant(context.Background(), tenant, func(tx *store.Tx) error {
		a := system
		a.ResourceType = "bohux.v1.Node"
		var err error
		node, _, err = tx.Create(context.Background(), "nodes",
			[]store.Col{{Name: "slug", Value: slug}, {Name: "sealing_public_key", Value: make([]byte, 32)}},
			[]store.Col{{Name: "agent_version", Value: "0.3.0"}, {Name: "protocol_version", Value: 1},
				{Name: "state", Value: "online"}}, a)
		return err
	})
	if err != nil {
		t.Fatalf("create node fixture: %v", err)
	}
	return node
}

// DeployableApp creates a project, an environment on node with the docker provider,
// and an App in it with an image source, the given replicas, an HTTP health check, and
// one secret ref. It returns the App and environment IDs.
func DeployableApp(t testing.TB, st *store.Store, tenant, node uuid.UUID, appSlug string, replicas int) (app, env uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	err := st.InTenant(ctx, tenant, func(tx *store.Tx) error {
		a := system
		a.ResourceType = "bohux.v1.Project"
		project, _, err := tx.Create(ctx, "projects",
			[]store.Col{{Name: "slug", Value: "p-" + uuid.NewString()[:8]}}, nil, a)
		if err != nil {
			return err
		}
		a.ResourceType = "bohux.v1.Environment"
		if env, _, err = tx.Create(ctx, "environments",
			[]store.Col{{Name: "project_id", Value: project}, {Name: "slug", Value: "e-" + uuid.NewString()[:8]}},
			[]store.Col{{Name: "provider", Value: "docker"}, {Name: "node_id", Value: node}}, a); err != nil {
			return err
		}
		a.ResourceType = "bohux.v1.App"
		app, _, err = tx.Create(ctx, "apps",
			[]store.Col{{Name: "environment_id", Value: env}, {Name: "slug", Value: appSlug}},
			[]store.Col{
				{Name: "source", Value: `{"image": {"repository": "ghcr.io/acme/` + appSlug +
					`", "digest": "sha256:` + strings.Repeat("a", 64) + `"}}`},
				{Name: "runtime", Value: `{"replicas": ` + strconv.Itoa(replicas) +
					`, "ports": [{"container_port": 8080}], "health": {"path": "/healthz", "port": 8080}}`},
				{Name: "secrets", Value: `[{"key": "DATABASE_URL", "handle": "openbao:db"}]`},
			}, a)
		return err
	})
	if err != nil {
		t.Fatalf("create deployable app fixture: %v", err)
	}
	return app, env
}

// Release creates a Release of app capturing its current revision, in the given state
// ("building", "ready", "failed") with the artifact available or not, and returns its
// ID.
func Release(t testing.TB, st *store.Store, tenant, app uuid.UUID, state string, available bool) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	var release uuid.UUID
	err := st.InTenant(ctx, tenant, func(tx *store.Tx) error {
		var config uuid.UUID
		if err := tx.QueryRow(ctx, `SELECT revision_id FROM apps_heads WHERE id = $1`, app).Scan(&config); err != nil {
			return err
		}
		a := system
		a.ResourceType = "bohux.v1.Release"
		var err error
		release, _, err = tx.Create(ctx, "releases",
			[]store.Col{{Name: "app_id", Value: app}, {Name: "config_revision_id", Value: config}},
			[]store.Col{{Name: "state", Value: state}, {Name: "artifact_digest", Value: "sha256:" + strings.Repeat("a", 64)},
				{Name: "artifact_available", Value: available}}, a)
		return err
	})
	if err != nil {
		t.Fatalf("create release fixture: %v", err)
	}
	return release
}
