//go:build integration

package store

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
)

// newStore returns a Store on a freshly migrated database, as the unprivileged role.
func newStore(t *testing.T) *Store {
	t.Helper()
	ctx := context.Background()
	dsn := newAppDSN(t)
	if err := Migrate(ctx, open(t, dsn)); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	s, err := Open(ctx, dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(s.Close)
	return s
}

var testActor = Actor{ID: uuid.MustParse(actor), Kind: ActorUser}

func auditFor(action, typ string) Audit {
	return Audit{Actor: testActor, Action: action, ResourceType: typ}
}

// seedTenant creates a tenant and one project in it.
func seedTenant(t *testing.T, s *Store, tenant uuid.UUID, slug string) (project, rev uuid.UUID) {
	t.Helper()
	err := s.InTenant(context.Background(), tenant, func(tx *Tx) error {
		ctx := context.Background()
		if _, _, err := tx.Create(ctx, "tenants",
			[]Col{{"slug", slug}}, []Col{{"display_name", slug}},
			auditFor("bootstrap", "bohux.v1.Tenant")); err != nil {
			return err
		}
		var err error
		project, rev, err = tx.Create(ctx, "projects",
			[]Col{{"slug", "web"}}, []Col{{"display_name", "Web"}},
			auditFor("bohux.v1.ProjectService/CreateProject", "bohux.v1.Project"))
		return err
	})
	if err != nil {
		t.Fatalf("seed tenant %s: %v", slug, err)
	}
	return project, rev
}

// count returns a single integer from query, run in tenant.
func count(t *testing.T, s *Store, tenant uuid.UUID, query string, args ...any) int {
	t.Helper()
	var n int
	err := s.InTenant(context.Background(), tenant, func(tx *Tx) error {
		return tx.QueryRow(context.Background(), query, args...).Scan(&n)
	})
	if err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return n
}

func TestOpenRefusesPrivilegedRole(t *testing.T) {
	_, err := Open(context.Background(), adminDSN(t))
	if !errors.Is(err, ErrPrivilegedRole) {
		t.Fatalf("Open as superuser: got %v, want ErrPrivilegedRole", err)
	}
}

func TestWritePath(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	tenant := uuid.MustParse(tenantA)
	project, rev1 := seedTenant(t, s, tenant, "a")

	update := func(expected uuid.UUID, name string) (uuid.UUID, error) {
		var rev uuid.UUID
		err := s.InTenant(ctx, tenant, func(tx *Tx) error {
			var err error
			rev, err = tx.Update(ctx, "projects", project, expected,
				[]Col{{"display_name", name}},
				auditFor("bohux.v1.ProjectService/UpdateProject", "bohux.v1.Project"))
			return err
		})
		return rev, err
	}

	t.Run("create writes head, revision, and audit event together", func(t *testing.T) {
		if got := count(t, s, tenant,
			`SELECT count(*) FROM projects_heads WHERE id = $1 AND revision_id = $2`,
			project, rev1); got != 1 {
			t.Errorf("head at first revision: got %d rows", got)
		}
		if got := count(t, s, tenant,
			`SELECT count(*) FROM audit_events WHERE resource_id = $1 AND revision_id = $2
			 AND actor_id = $3 AND actor_kind = 'user'`, project, rev1, testActor.ID); got != 1 {
			t.Errorf("audit event for create: got %d rows", got)
		}
	})

	rev2, err := update(rev1, "Web 2")
	t.Run("update moves the head and links the revision to its parent", func(t *testing.T) {
		if err != nil {
			t.Fatal(err)
		}
		if got := count(t, s, tenant,
			`SELECT count(*) FROM projects_revisions r JOIN projects_heads h
			   ON h.revision_id = r.id
			 WHERE h.id = $1 AND r.id = $2 AND r.parent_revision_id = $3
			   AND r.display_name = 'Web 2'`, project, rev2, rev1); got != 1 {
			t.Errorf("head at new revision with parent: got %d rows", got)
		}
	})

	t.Run("stale expected revision is a conflict and writes nothing", func(t *testing.T) {
		revisions := count(t, s, tenant, `SELECT count(*) FROM projects_revisions`)
		events := count(t, s, tenant, `SELECT count(*) FROM audit_events`)
		if _, err := update(rev1, "lost update"); !errors.Is(err, ErrConflict) {
			t.Fatalf("got %v, want ErrConflict", err)
		}
		if got := count(t, s, tenant, `SELECT count(*) FROM projects_revisions`); got != revisions {
			t.Errorf("revisions: got %d, want %d", got, revisions)
		}
		if got := count(t, s, tenant, `SELECT count(*) FROM audit_events`); got != events {
			t.Errorf("audit events: got %d, want %d", got, events)
		}
	})

	t.Run("a failed transaction rolls back the audit event with the write", func(t *testing.T) {
		events := count(t, s, tenant, `SELECT count(*) FROM audit_events`)
		boom := errors.New("later step failed")
		err := s.InTenant(ctx, tenant, func(tx *Tx) error {
			if _, err := tx.Update(ctx, "projects", project, rev2, []Col{{"display_name", "x"}},
				auditFor("bohux.v1.ProjectService/UpdateProject", "bohux.v1.Project")); err != nil {
				return err
			}
			return boom
		})
		if !errors.Is(err, boom) {
			t.Fatalf("got %v, want the fn error", err)
		}
		if got := count(t, s, tenant, `SELECT count(*) FROM audit_events`); got != events {
			t.Errorf("audit events: got %d, want %d", got, events)
		}
		if got := count(t, s, tenant,
			`SELECT count(*) FROM projects_heads WHERE revision_id = $1`, rev2); got != 1 {
			t.Errorf("head moved by a rolled-back write")
		}
	})

	t.Run("a duplicate slug is ErrAlreadyExists", func(t *testing.T) {
		err := s.InTenant(ctx, tenant, func(tx *Tx) error {
			_, _, err := tx.Create(ctx, "projects", []Col{{"slug", "web"}}, nil,
				auditFor("bohux.v1.ProjectService/CreateProject", "bohux.v1.Project"))
			return err
		})
		if !errors.Is(err, ErrAlreadyExists) {
			t.Fatalf("got %v, want ErrAlreadyExists", err)
		}
	})

	t.Run("another tenant cannot see or change it", func(t *testing.T) {
		other := uuid.MustParse(tenantB)
		seedTenant(t, s, other, "b")
		err := s.InTenant(ctx, other, func(tx *Tx) error {
			_, err := tx.Update(ctx, "projects", project, rev2, []Col{{"display_name", "x"}},
				auditFor("bohux.v1.ProjectService/UpdateProject", "bohux.v1.Project"))
			return err
		})
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("got %v, want ErrNotFound", err)
		}
	})

	t.Run("delete keeps the snapshot and frees the slug", func(t *testing.T) {
		var del uuid.UUID
		err := s.InTenant(ctx, tenant, func(tx *Tx) error {
			var err error
			del, err = tx.Delete(ctx, "projects", project, rev2,
				auditFor("bohux.v1.ProjectService/DeleteProject", "bohux.v1.Project"))
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		if got := count(t, s, tenant,
			`SELECT count(*) FROM projects_revisions WHERE id = $1 AND deleted
			 AND parent_revision_id = $2 AND display_name = 'Web 2'`, del, rev2); got != 1 {
			t.Errorf("deletion revision with copied snapshot: got %d rows", got)
		}
		if _, err := update(del, "zombie"); !errors.Is(err, ErrNotFound) {
			t.Errorf("update after delete: got %v, want ErrNotFound", err)
		}
		err = s.InTenant(ctx, tenant, func(tx *Tx) error {
			_, _, err := tx.Create(ctx, "projects", []Col{{"slug", "web"}}, nil,
				auditFor("bohux.v1.ProjectService/CreateProject", "bohux.v1.Project"))
			return err
		})
		if err != nil {
			t.Errorf("reuse of a deleted slug: %v", err)
		}
	})

	t.Run("writes reject unknown resources and missing audit fields", func(t *testing.T) {
		tests := []struct {
			name     string
			resource string
			audit    Audit
		}{
			{name: "unknown resource", resource: "audit_events",
				audit: auditFor("x", "bohux.v1.Project")},
			{name: "no actor", resource: "projects",
				audit: Audit{Action: "x", ResourceType: "bohux.v1.Project"}},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				err := s.InTenant(ctx, tenant, func(tx *Tx) error {
					_, _, err := tx.Create(ctx, tt.resource, []Col{{"slug", "z"}}, nil, tt.audit)
					return err
				})
				if err == nil {
					t.Fatal("got nil error")
				}
			})
		}
	})
}
