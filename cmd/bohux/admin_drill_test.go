package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/bohumix/bohux/internal/drill"
)

// A drill is refused before it touches anything when it is not told what to prove or
// where to prove it. The DSNs in these cases go nowhere, so reaching Postgres is the
// bug the test would catch.
func TestAdminDrillRequiredFlags(t *testing.T) {
	const (
		archive = "--in=/var/lib/bohux-backups/bohux-20261023t031700z.backup"
		key     = "--identity=/etc/bohux/drill.key"
		scratch = "--drill-database-url=postgres://bohux_drill@127.0.0.1:1/postgres"
		admin   = "--drill-admin-database-url=postgres://bohux_drill_admin@127.0.0.1:1/postgres"
	)
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "nothing", args: nil, want: "are required"},
		{name: "no archive", args: []string{key, scratch, admin}, want: "are required"},
		{name: "no identity", args: []string{archive, scratch, admin}, want: "are required"},
		{name: "no scratch role", args: []string{archive, key, admin}, want: "are required"},
		{name: "no admin role", args: []string{archive, key, scratch}, want: "are required"},
		{
			name: "a mount is required once OpenBao is configured",
			args: []string{archive, key, scratch, admin, "--openbao-address=http://127.0.0.1:8200"},
			want: "--drill-kv-mount is required",
		},
		{
			name: "an identity file that is not there",
			args: []string{archive, key, scratch, admin},
			want: "no such file",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			err := adminDrill(context.Background(), tc.args, &out, func(string) string { return "" })
			if err == nil {
				t.Fatalf("adminDrill() = nil, want %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("adminDrill() = %q, want it to mention %q", err, tc.want)
			}
			if out.Len() > 0 {
				t.Errorf("a refused drill printed %q", out.String())
			}
		})
	}
}

// The identity and the mount can come from the environment, so a cron-like caller and
// the schedule read the same configuration (ADR-0051 item 2).
func TestAdminDrillReadsTheEnvironment(t *testing.T) {
	env := map[string]string{
		envDrillIdentity:   "/etc/bohux/drill.key",
		envDrillDBURL:      "postgres://bohux_drill@127.0.0.1:1/postgres",
		envDrillAdminDBURL: "postgres://bohux_drill_admin@127.0.0.1:1/postgres",
	}
	var out bytes.Buffer
	err := adminDrill(context.Background(), []string{"--in=/tmp/a.backup"}, &out,
		func(k string) string { return env[k] })
	// It got past the required-flag check and failed on the identity file itself.
	if err == nil || !strings.Contains(err.Error(), "drill.key") {
		t.Fatalf("adminDrill() = %v, want it to have reached the identity file from the environment", err)
	}
}

func verdictFixture() *drill.Verdict {
	started := time.Date(2026, 10, 25, 4, 47, 0, 0, time.UTC)
	v := &drill.Verdict{
		Archive:  "/var/lib/bohux-backups/bohux-20261025t031700z.backup",
		Database: "bohux_drill_20261025t044700z", StartedAt: started,
		FinishedAt: started.Add(42 * time.Second), CreatedAt: started.Add(-90 * time.Minute),
		BohuxVersion: "0.1.0", Migration: 25, Tables: 29, Rows: 1841,
		Tenants: 1, OwnerTokens: 2, Secrets: 7, CAPin: "d78e3a7b", CAExpires: started.AddDate(10, 0, 0),
	}
	v.PerTable = map[string]int64{"tenants_heads": 1}
	return v
}

func TestPrintVerdict(t *testing.T) {
	t.Run("passing", func(t *testing.T) {
		v := verdictFixture()
		v.Checks = []drill.Check{{Name: "restore", OK: true, Detail: "29 tables"}}
		var out bytes.Buffer
		if err := printVerdict(&out, v, false); err != nil {
			t.Fatal(err)
		}
		got := out.String()
		for _, want := range []string{"PASSED", "[ok  ] restore", "29 tables", "1841 rows", "7 secret(s)", "42s"} {
			if !strings.Contains(got, want) {
				t.Errorf("printVerdict() missing %q:\n%s", want, got)
			}
		}
	})

	t.Run("failing names the check", func(t *testing.T) {
		v := verdictFixture()
		v.Checks = []drill.Check{
			{Name: "restore", OK: true, Detail: "29 tables"},
			{Name: "owner-token", Detail: "no live owner token"},
		}
		var out bytes.Buffer
		if err := printVerdict(&out, v, false); err != nil {
			t.Fatal(err)
		}
		got := out.String()
		if !strings.Contains(got, "FAILED") || !strings.Contains(got, "[FAIL] owner-token") {
			t.Errorf("printVerdict() does not show the failed check:\n%s", got)
		}
	})

	t.Run("JSON is the verdict the schedule records", func(t *testing.T) {
		v := verdictFixture()
		v.Checks = []drill.Check{{Name: "restore", OK: true, Detail: "29 tables"}}
		var out bytes.Buffer
		if err := printVerdict(&out, v, true); err != nil {
			t.Fatal(err)
		}
		var back drill.Verdict
		if err := json.Unmarshal(out.Bytes(), &back); err != nil {
			t.Fatalf("the verdict does not round-trip: %v\n%s", err, out.String())
		}
		if back.Migration != v.Migration || back.CAPin != v.CAPin || len(back.Checks) != 1 {
			t.Errorf("round-tripped verdict = %+v", back)
		}
	})
}
