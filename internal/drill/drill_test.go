package drill

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"filippo.io/age"

	"github.com/bohumix/bohux/internal/secrets/openbao"
)

// identity is one age identity for configuration that has to carry a private key.
func identity(t *testing.T) age.Identity {
	t.Helper()
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// valid is a configuration that passes validation, for tests that break one field.
func valid(t *testing.T) Config {
	t.Helper()
	return Config{
		Archive:    "/var/lib/bohux-backups/bohux-20261023T031700Z.backup",
		Identities: []age.Identity{identity(t)},
		ScratchDSN: "postgres://bohux_drill:pw@127.0.0.1:5432/postgres?sslmode=disable",
		AdminDSN:   "postgres://bohux_drill_admin:pw@127.0.0.1:5432/postgres?sslmode=disable",
		Bao:        &openbao.Client{Addr: "http://127.0.0.1:8200", Token: "t", Mount: "bohux-drill"},
		LiveMount:  "secret",
	}
}

// A drill that is misconfigured must refuse before it creates anything, and above all
// must never be pointed at the live KV mount: it wipes the mount it is given.
func TestConfigValidate(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Config)
		want   string
	}{
		{"ok", func(*Config) {}, ""},
		{"no archive", func(c *Config) { c.Archive = "" }, "archive to restore is required"},
		{"no identity", func(c *Config) { c.Identities = nil }, "age identity is required"},
		{"no scratch role", func(c *Config) { c.ScratchDSN = "" }, "scratch role's DSN is required"},
		{"no admin role", func(c *Config) { c.AdminDSN = "" }, "admin role's DSN is required"},
		{"the live mount", func(c *Config) { c.Bao.Mount = c.LiveMount }, "is the live one"},
		{"no secrets configured at all", func(c *Config) { c.Bao = nil }, ""},
		{"no live mount to compare with", func(c *Config) { c.LiveMount = "" }, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := valid(t)
			tc.mutate(&cfg)
			err := cfg.validate()
			switch {
			case tc.want == "" && err != nil:
				t.Fatalf("validate() = %v, want nil", err)
			case tc.want == "":
			case err == nil:
				t.Fatalf("validate() = nil, want %q", tc.want)
			case !strings.Contains(err.Error(), tc.want):
				t.Fatalf("validate() = %q, want it to mention %q", err, tc.want)
			}
		})
	}
}

// Run must reject a configuration pointed at the live mount before it touches
// anything: no database created, no mount listed, nothing wiped. The DSNs here go
// nowhere, so reaching Postgres at all would be the bug.
func TestRunRefusesTheLiveMount(t *testing.T) {
	cfg := valid(t)
	cfg.Bao.Mount = cfg.LiveMount
	v, err := Run(context.Background(), cfg)
	if v != nil {
		t.Errorf("Run returned a verdict for a refused configuration: %+v", v)
	}
	if err == nil || !strings.Contains(err.Error(), "is the live one") {
		t.Fatalf("Run() = %v, want a refusal naming the live mount", err)
	}
}

func TestWithDatabase(t *testing.T) {
	tests := []struct {
		name, dsn, database, want, wantErr string
	}{
		{
			name: "replaces the database", dsn: "postgres://u:p@h:5432/postgres?sslmode=disable",
			database: "bohux_drill_x", want: "postgres://u:p@h:5432/bohux_drill_x?sslmode=disable",
		},
		{
			name: "keeps every parameter", dsn: "postgres://u@h/live?sslmode=verify-full&connect_timeout=5",
			database: "d", want: "postgres://u@h/d?sslmode=verify-full&connect_timeout=5",
		},
		{name: "not a URL", dsn: "host=h user=u dbname=d", database: "d", wantErr: "not a postgres:// URL"},
		{name: "no host", dsn: "postgres:///live", database: "d", wantErr: "not a postgres:// URL"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := withDatabase(tc.dsn, tc.database)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("withDatabase() error = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("withDatabase() = %v", err)
			}
			if got != tc.want {
				t.Errorf("withDatabase() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRoleOf(t *testing.T) {
	tests := []struct {
		name, dsn, want, wantErr string
	}{
		{name: "with a password", dsn: "postgres://bohux_drill_admin:p@h/d", want: "bohux_drill_admin"},
		{name: "without one", dsn: "postgres://bohux_drill_admin@h/d", want: "bohux_drill_admin"},
		{name: "no role", dsn: "postgres://h/d", wantErr: "names no role"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := roleOf(tc.dsn)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("roleOf() error = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Errorf("roleOf() = %q, %v, want %q", got, err, tc.want)
			}
		})
	}
}

// A database or role name is quoted, not trusted, even though both come from
// configuration and a timestamp rather than from a request.
func TestIdentifierQuotes(t *testing.T) {
	tests := []struct{ in, want string }{
		{"bohux_drill_20261023t044700z", `"bohux_drill_20261023t044700z"`},
		{`we"ird`, `"we""ird"`},
	}
	for _, tc := range tests {
		if got := identifier(tc.in); got != tc.want {
			t.Errorf("identifier(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestMissing(t *testing.T) {
	tests := []struct {
		name       string
		want, got  []string
		wantResult []string
	}{
		{name: "all there", want: []string{"a", "b"}, got: []string{"a", "b", "c"}},
		{name: "one gone", want: []string{"a", "b"}, got: []string{"a"}, wantResult: []string{"b"}},
		{name: "all gone", want: []string{"a", "b"}, got: nil, wantResult: []string{"a", "b"}},
		{name: "nothing wanted", want: nil, got: []string{"a"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := missing(tc.want, tc.got)
			if strings.Join(got, ",") != strings.Join(tc.wantResult, ",") {
				t.Errorf("missing() = %v, want %v", got, tc.wantResult)
			}
		})
	}
}

func TestVerdictChecks(t *testing.T) {
	var v Verdict
	if !v.OK() || v.Failed() != nil {
		t.Fatal("a verdict with no checks should be OK")
	}
	v.add("restore", true, "%d tables", 25)
	if !v.OK() {
		t.Error("OK() = false after a passing check")
	}
	v.add("owner-token", false, "none")
	v.add("secrets", false, "%d of %d", 1, 2)
	if v.OK() {
		t.Error("OK() = true with failed checks")
	}
	if got := strings.Join(v.Failed(), ","); got != "owner-token,secrets" {
		t.Errorf("Failed() = %q, want the failed names in order", got)
	}
	if v.Checks[0].Detail != "25 tables" {
		t.Errorf("Checks[0].Detail = %q, want the formatted detail", v.Checks[0].Detail)
	}
}

// A nil client is a drill configured without secrets; wiping it is a no-op rather
// than a nil dereference on the teardown path.
func TestWipeMountWithoutSecrets(t *testing.T) {
	if err := wipeMount(context.Background(), nil); err != nil {
		t.Errorf("wipeMount(nil) = %v", err)
	}
}

// teardown reports every problem it could not fix, not just the first, so a leaked
// scratch database is never hidden by a mount that also failed to wipe.
func TestTeardownJoinsFailures(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cfg := valid(t)
	cfg.ScratchDSN = "postgres://u@127.0.0.1:1/postgres?sslmode=disable&connect_timeout=1"
	cfg.Bao = &openbao.Client{Addr: "http://127.0.0.1:1", Token: "t", Mount: "bohux-drill"}
	err := teardown(ctx, cfg, "bohux_drill_nope", "/nonexistent/also-nope")
	if err == nil {
		t.Fatal("teardown() = nil, want the failures it could not fix")
	}
	var joined interface{ Unwrap() []error }
	if !errors.As(err, &joined) || len(joined.Unwrap()) != 2 {
		t.Errorf("teardown() = %v, want a join of the database and mount failures", err)
	}
}

func TestDefaultScheduleIsWeeklyAfterTheNightly(t *testing.T) {
	// 47 4 * * 0: Sunday 04:47 UTC, after ADR-0033's 03:17 nightly, off the hour.
	if DefaultSchedule != "47 4 * * 0" {
		t.Errorf("DefaultSchedule = %q", DefaultSchedule)
	}
}

func TestConfigNowDefaults(t *testing.T) {
	var c Config
	if got := c.now(); time.Since(got) > time.Minute {
		t.Errorf("now() = %v, want time.Now by default", got)
	}
	fixed := time.Date(2026, 10, 23, 4, 47, 0, 0, time.UTC)
	c.Now = func() time.Time { return fixed }
	if got := c.now(); !got.Equal(fixed) {
		t.Errorf("now() = %v, want %v", got, fixed)
	}
}
