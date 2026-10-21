// Package fixture creates resources directly in the store for tests in other
// packages, without going through the API. Never imported by non-test code.
package fixture

import (
	"context"
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
