// Package store is Bohux's Postgres storage (ADR-0004, ADR-0017, ADR-0021).
package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"

	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" database/sql driver for goose
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"
)

//go:embed migrations/*.sql
var embedded embed.FS

// Migrations returns the embedded migration files, named NNNNN_name.sql.
func Migrations() fs.FS {
	sub, err := fs.Sub(embedded, "migrations")
	if err != nil {
		// The directory is embedded at compile time; this cannot fail at run time.
		return embedded
	}
	return sub
}

// ErrPrivilegedRole is returned when the database role would bypass row-level
// security, which would silently disable tenant isolation (CLAUDE.md rule 10).
var ErrPrivilegedRole = errors.New("database role is a superuser or has BYPASSRLS; " +
	"run Bohux as a role that owns its schema without either")

// Migrate applies every pending migration. Concurrent callers serialize on a Postgres
// advisory lock. It refuses to run as a role that bypasses row-level security, because
// the same role owns the tables and serves requests.
func Migrate(ctx context.Context, db *sql.DB) error {
	if err := checkRole(ctx, db); err != nil {
		return err
	}
	p, err := newProvider(db)
	if err != nil {
		return err
	}
	results, err := p.Up(ctx)
	if err != nil {
		return fmt.Errorf("apply migrations: %w", err)
	}
	for _, r := range results {
		slog.InfoContext(ctx, "migration applied",
			"version", r.Source.Version, "file", r.Source.Path, "duration", r.Duration)
	}
	return nil
}

// MigrateDSN opens a short-lived connection to dsn and runs Migrate on it.
func MigrateDSN(ctx context.Context, dsn string) (err error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return fmt.Errorf("open postgres: %w", err)
	}
	defer func() {
		if cerr := db.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("close postgres: %w", cerr)
		}
	}()
	return Migrate(ctx, db)
}

func newProvider(db *sql.DB) (*goose.Provider, error) {
	locker, err := lock.NewPostgresSessionLocker()
	if err != nil {
		return nil, fmt.Errorf("create migration lock: %w", err)
	}
	p, err := goose.NewProvider(goose.DialectPostgres, db, Migrations(),
		goose.WithSessionLocker(locker))
	if err != nil {
		return nil, fmt.Errorf("load migrations: %w", err)
	}
	return p, nil
}

func checkRole(ctx context.Context, db *sql.DB) error {
	var super, bypass bool
	err := db.QueryRowContext(ctx,
		`SELECT rolsuper, rolbypassrls FROM pg_roles WHERE rolname = current_user`,
	).Scan(&super, &bypass)
	if err != nil {
		return fmt.Errorf("check database role: %w", err)
	}
	if super || bypass {
		return ErrPrivilegedRole
	}
	return nil
}
