package drill

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/jackc/pgx/v5"
)

// withDatabase points a DSN at another database on the same server, leaving the role
// and every connection parameter alone.
func withDatabase(dsn, database string) (string, error) {
	u, err := url.Parse(dsn)
	if err != nil {
		return "", fmt.Errorf("parse: %w", err)
	}
	if u.Scheme == "" || u.Host == "" {
		return "", errors.New("not a postgres:// URL")
	}
	u.Path = "/" + database
	return u.String(), nil
}

// roleOf returns the role a DSN connects as, which is the role a GRANT names.
func roleOf(dsn string) (string, error) {
	u, err := url.Parse(dsn)
	if err != nil {
		return "", fmt.Errorf("parse: %w", err)
	}
	role := u.User.Username()
	if role == "" {
		return "", errors.New("names no role, so the drill cannot grant it the scratch schema")
	}
	return role, nil
}

// identifier quotes a name for use as an SQL identifier. Database and role names here
// come from configuration and a UTC timestamp, never from a request, but they are
// interpolated rather than bound — CREATE DATABASE takes no parameters — so they are
// quoted rather than trusted.
func identifier(name string) string { return pgx.Identifier{name}.Sanitize() }

// createDatabase creates the scratch database, owned by the role dsn connects as.
// CREATE DATABASE cannot run inside a transaction, so this is a bare Exec.
func createDatabase(ctx context.Context, dsn, database string) error {
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return fmt.Errorf("connect as the scratch role: %w", err)
	}
	defer func() { _ = conn.Close(context.WithoutCancel(ctx)) }()
	if _, err := conn.Exec(ctx, "CREATE DATABASE "+identifier(database)); err != nil {
		return fmt.Errorf("create the scratch database %s (the role needs CREATEDB): %w", database, err)
	}
	return nil
}

// dropDatabase removes the scratch database, disconnecting anything still on it. One
// that is already gone is not an error.
func dropDatabase(ctx context.Context, dsn, database string) error {
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return fmt.Errorf("connect to drop the scratch database %s: %w", database, err)
	}
	defer func() { _ = conn.Close(context.WithoutCancel(ctx)) }()
	if _, err := conn.Exec(ctx, "DROP DATABASE IF EXISTS "+identifier(database)+" WITH (FORCE)"); err != nil {
		return fmt.Errorf("drop the scratch database %s: %w", database, err)
	}
	return nil
}

// grantToAdmin lets the admin role read and write the tables the migrations are about
// to create. Restore migrates as the owner and then loads rows as the admin role, so
// the privileges have to be in place before the tables exist: a default privilege
// applies to every table the owner creates from here on (ADR-0051 item 3).
func grantToAdmin(ctx context.Context, appDSN, role string) error {
	conn, err := pgx.Connect(ctx, appDSN)
	if err != nil {
		return fmt.Errorf("connect to the scratch database: %w", err)
	}
	defer func() { _ = conn.Close(context.WithoutCancel(ctx)) }()
	r := identifier(role)
	for _, q := range []string{
		"GRANT USAGE, CREATE ON SCHEMA public TO " + r,
		"ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT ALL ON TABLES TO " + r,
		"ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT ALL ON SEQUENCES TO " + r,
	} {
		if _, err := conn.Exec(ctx, q); err != nil {
			return fmt.Errorf("grant the scratch schema to %s: %w", role, err)
		}
	}
	return nil
}

// tablesIn lists the tables restore filled, excluding goose's own bookkeeping, as
// backup does when it writes an archive.
func tablesIn(ctx context.Context, conn *pgx.Conn) ([]string, error) {
	rows, err := conn.Query(ctx, `SELECT tablename FROM pg_tables
		WHERE schemaname = 'public' AND tablename <> 'goose_db_version' ORDER BY tablename`)
	if err != nil {
		return nil, fmt.Errorf("list the restored tables: %w", err)
	}
	tables, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("list the restored tables: %w", err)
	}
	return tables, nil
}

// countRows counts every row of each table. The admin role bypasses row-level
// security, so these are the whole tables, not one tenant's slice.
func countRows(ctx context.Context, conn *pgx.Conn, tables []string) (map[string]int64, int64, error) {
	per := make(map[string]int64, len(tables))
	var total int64
	for _, t := range tables {
		var n int64
		if err := conn.QueryRow(ctx, "SELECT count(*) FROM public."+identifier(t)).Scan(&n); err != nil {
			return nil, 0, fmt.Errorf("count %s: %w", t, err)
		}
		per[t] = n
		total += n
	}
	return per, total, nil
}

// missing names the tables an archive's manifest promised that are not there.
func missing(want, got []string) []string {
	have := make(map[string]bool, len(got))
	for _, t := range got {
		have[t] = true
	}
	var out []string
	for _, t := range want {
		if !have[t] {
			out = append(out, t)
		}
	}
	return out
}

// quoteList renders names for a check's detail line.
func quoteList(names []string) string { return strings.Join(names, ", ") }
