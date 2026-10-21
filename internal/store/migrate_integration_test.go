//go:build integration

package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"net/url"
	"os"
	"strings"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// BOHUX_TEST_POSTGRES_URL is a DSN for a role that can create roles and databases.
// Each test gets its own database, owned by a fresh unprivileged role.
const adminEnv = "BOHUX_TEST_POSTGRES_URL"

func adminDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv(adminEnv)
	if dsn == "" {
		t.Fatalf("%s is not set; integration tests need a Postgres to create databases in", adminEnv)
	}
	return dsn
}

func open(t *testing.T, dsn string) *sql.DB {
	t.Helper()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// newAppDB creates a database owned by a new non-superuser role and returns a
// connection as that role, as Bohux runs in production.
func newAppDB(t *testing.T) *sql.DB {
	t.Helper()
	ctx := context.Background()
	admin := open(t, adminDSN(t))
	name := "bohux_test_" + randomHex(t, 6)
	pass := randomHex(t, 12)
	for _, q := range []string{
		`CREATE ROLE ` + name + ` LOGIN PASSWORD '` + pass + `'`,
		`CREATE DATABASE ` + name + ` OWNER ` + name,
	} {
		if _, err := admin.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	t.Cleanup(func() {
		_, _ = admin.ExecContext(ctx, `DROP DATABASE IF EXISTS `+name+` WITH (FORCE)`)
		_, _ = admin.ExecContext(ctx, `DROP ROLE IF EXISTS `+name)
	})
	u, err := url.Parse(adminDSN(t))
	if err != nil {
		t.Fatalf("parse %s: %v", adminEnv, err)
	}
	u.User = url.UserPassword(name, pass)
	u.Path = "/" + name
	return open(t, u.String())
}

func randomHex(t *testing.T, n int) string {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("random: %v", err)
	}
	return hex.EncodeToString(b)
}

func TestMigrateRefusesPrivilegedRole(t *testing.T) {
	err := Migrate(context.Background(), open(t, adminDSN(t)))
	if !errors.Is(err, ErrPrivilegedRole) {
		t.Fatalf("Migrate as superuser: got %v, want ErrPrivilegedRole", err)
	}
}

func TestMigrateUpTwiceAndDownUp(t *testing.T) {
	ctx := context.Background()
	db := newAppDB(t)
	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("first Migrate: %v", err)
	}
	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("second Migrate must be a no-op: %v", err)
	}
	// Down sections are review documentation, never run by Bohux (ADR-0021), but they
	// must be correct: down to zero, then up again.
	p, err := newProvider(db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.DownTo(ctx, 0); err != nil {
		t.Fatalf("down to 0: %v", err)
	}
	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("Migrate after full down: %v", err)
	}
}

const (
	tenantA = "0192f0a0-0000-7000-8000-00000000000a"
	tenantB = "0192f0a0-0000-7000-8000-00000000000b"
	actor   = "0192f0a0-0000-7000-8000-0000000000ff"
)

// inTenant runs fn in a transaction scoped to tenant (empty = no tenant set).
func inTenant(ctx context.Context, db *sql.DB, tenant string, fn func(*sql.Tx) error) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if tenant != "" {
		if _, err := tx.ExecContext(ctx,
			`SELECT set_config('bohux.tenant_id', $1, true)`, tenant); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

func createTenant(ctx context.Context, db *sql.DB, id, slug string) error {
	rev := "1" + id[1:] // distinct from every fixed id in these tests
	return inTenant(ctx, db, id, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO tenants_heads
			(id, tenant_id, revision_id, created_by, slug) VALUES ($1, $1, $2, $3, $4)`,
			id, rev, actor, slug); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO tenants_revisions
			(id, tenant_id, resource_id, actor_id, display_name) VALUES ($1, $2, $2, $3, $4)`,
			rev, id, actor, slug)
		return err
	})
}

func migrated(t *testing.T) *sql.DB {
	t.Helper()
	db := newAppDB(t)
	if err := Migrate(context.Background(), db); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return db
}

func TestRowLevelSecurity(t *testing.T) {
	ctx := context.Background()
	db := migrated(t)
	for _, tc := range []struct{ id, slug string }{{tenantA, "a"}, {tenantB, "b"}} {
		if err := createTenant(ctx, db, tc.id, tc.slug); err != nil {
			t.Fatalf("create tenant %s: %v", tc.slug, err)
		}
	}

	tests := []struct {
		name   string
		tenant string
		want   []string
	}{
		{name: "tenant A sees only A", tenant: tenantA, want: []string{"a"}},
		{name: "tenant B sees only B", tenant: tenantB, want: []string{"b"}},
		{name: "no tenant sees nothing", tenant: "", want: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got []string
			err := inTenant(ctx, db, tt.tenant, func(tx *sql.Tx) error {
				rows, err := tx.QueryContext(ctx, `SELECT slug FROM tenants_heads ORDER BY slug`)
				if err != nil {
					return err
				}
				defer func() { _ = rows.Close() }()
				for rows.Next() {
					var s string
					if err := rows.Scan(&s); err != nil {
						return err
					}
					got = append(got, s)
				}
				return rows.Err()
			})
			if err != nil {
				t.Fatal(err)
			}
			if strings.Join(got, ",") != strings.Join(tt.want, ",") {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}

	t.Run("writing another tenant's row is rejected", func(t *testing.T) {
		err := inTenant(ctx, db, tenantB, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, `INSERT INTO audit_events
				(id, tenant_id, actor_id, actor_kind, action, resource_type, resource_id)
				VALUES (gen_random_uuid(), $1, $2, 'system', 'test', 'bohux.v1.Tenant', $1)`,
				tenantA, actor)
			return err
		})
		if err == nil || !strings.Contains(err.Error(), "row-level security") {
			t.Fatalf("got %v, want a row-level security violation", err)
		}
	})
}

func TestRevisionInvariants(t *testing.T) {
	ctx := context.Background()
	db := migrated(t)
	if err := createTenant(ctx, db, tenantA, "a"); err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	const (
		project = "0192f0a0-0000-7000-8000-000000000001"
		rev1    = "0192f0a0-0000-7000-8000-000000000011"
		rev2    = "0192f0a0-0000-7000-8000-000000000012"
		other   = "0192f0a0-0000-7000-8000-000000000002"
	)
	insertProject := func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO projects_heads
			(id, tenant_id, revision_id, created_by, slug) VALUES ($1, $2, $3, $4, 'web')`,
			project, tenantA, rev1, actor); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO projects_revisions
			(id, tenant_id, resource_id, actor_id) VALUES ($1, $2, $3, $4)`,
			rev1, tenantA, project, actor)
		return err
	}
	if err := inTenant(ctx, db, tenantA, insertProject); err != nil {
		t.Fatalf("head + revision in one transaction: %v", err)
	}

	tests := []struct {
		name string
		fn   func(*sql.Tx) error
		want string
	}{
		{
			name: "revisions cannot be updated",
			fn: func(tx *sql.Tx) error {
				_, err := tx.ExecContext(ctx,
					`UPDATE projects_revisions SET display_name = 'x' WHERE id = $1`, rev1)
				return err
			},
			want: "append-only",
		},
		{
			name: "revisions cannot be deleted",
			fn: func(tx *sql.Tx) error {
				_, err := tx.ExecContext(ctx, `DELETE FROM projects_revisions WHERE id = $1`, rev1)
				return err
			},
			want: "append-only",
		},
		{
			name: "a second root revision is rejected",
			fn: func(tx *sql.Tx) error {
				_, err := tx.ExecContext(ctx, `INSERT INTO projects_revisions
					(id, tenant_id, resource_id, actor_id) VALUES ($1, $2, $3, $4)`,
					rev2, tenantA, project, actor)
				return err
			},
			want: "projects_revisions_root",
		},
		{
			name: "a head cannot point at a missing revision",
			fn: func(tx *sql.Tx) error {
				_, err := tx.ExecContext(ctx, `INSERT INTO projects_heads
					(id, tenant_id, revision_id, created_by, slug) VALUES ($1, $2, $3, $4, 'api')`,
					other, tenantA, rev2, actor)
				return err
			},
			want: "projects_heads_revision_fk",
		},
		{
			name: "a live slug is unique within the tenant",
			fn: func(tx *sql.Tx) error {
				_, err := tx.ExecContext(ctx, `INSERT INTO projects_heads
					(id, tenant_id, revision_id, created_by, slug) VALUES ($1, $2, $3, $4, 'web')`,
					other, tenantA, rev2, actor)
				return err
			},
			want: "projects_heads_slug",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := inTenant(ctx, db, tenantA, tt.fn)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("got %v, want an error mentioning %q", err, tt.want)
			}
		})
	}

	t.Run("a child revision extends the history", func(t *testing.T) {
		err := inTenant(ctx, db, tenantA, func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, `INSERT INTO projects_revisions
				(id, tenant_id, resource_id, parent_revision_id, actor_id, display_name)
				VALUES ($1, $2, $3, $4, $5, 'Web')`, rev2, tenantA, project, rev1, actor); err != nil {
				return err
			}
			_, err := tx.ExecContext(ctx,
				`UPDATE projects_heads SET revision_id = $1 WHERE id = $2 AND revision_id = $3`,
				rev2, project, rev1)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	})
}
