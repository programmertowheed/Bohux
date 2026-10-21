//go:build integration

// Package storetest gives integration tests a fresh Postgres database owned by an
// unprivileged role, as Bohux runs in production.
package storetest

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"net/url"
	"os"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" database/sql driver
)

// AdminEnv names a DSN for a role that can create roles and databases. Each test gets
// its own database.
const AdminEnv = "BOHUX_TEST_POSTGRES_URL"

// AdminDSN returns the admin DSN, failing the test if it is unset.
func AdminDSN(t testing.TB) string {
	t.Helper()
	dsn := os.Getenv(AdminEnv)
	if dsn == "" {
		t.Fatalf("%s is not set; integration tests need a Postgres to create databases in", AdminEnv)
	}
	return dsn
}

// Open opens dsn with database/sql and closes it when the test ends.
func Open(t testing.TB, dsn string) *sql.DB {
	t.Helper()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// NewAppDSN creates a database owned by a new non-superuser role, dropped when the
// test ends, and returns its DSN.
func NewAppDSN(t testing.TB) string {
	t.Helper()
	ctx := context.Background()
	admin := Open(t, AdminDSN(t))
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
	u, err := url.Parse(AdminDSN(t))
	if err != nil {
		t.Fatalf("parse %s: %v", AdminEnv, err)
	}
	u.User = url.UserPassword(name, pass)
	u.Path = "/" + name
	return u.String()
}

func randomHex(t testing.TB, n int) string {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("random: %v", err)
	}
	return hex.EncodeToString(b)
}
