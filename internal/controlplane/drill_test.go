package controlplane

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"filippo.io/age"
)

// identityFile writes an age identity file and returns its path and public half.
func identityFile(t *testing.T) (path, recipient string) {
	t.Helper()
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	path = filepath.Join(t.TempDir(), "drill.key")
	if err := os.WriteFile(path, []byte(id.String()+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path, id.Recipient().String()
}

// A drill wipes the mount it is given, so a configuration naming the live mount must
// be refused at start rather than at 04:47 on Sunday.
func TestDrillConfigValidate(t *testing.T) {
	const live = "secret"
	key, _ := identityFile(t)
	full := DrillConfig{IdentityFile: key, DatabaseURL: "postgres://bohux_drill@h/postgres",
		AdminDatabaseURL: "postgres://bohux_drill_admin@h/postgres", KVMount: "bohux-drill"}
	tests := []struct {
		name    string
		mutate  func(*DrillConfig)
		wantErr string
	}{
		{name: "ok", mutate: func(*DrillConfig) {}},
		{name: "off", mutate: func(c *DrillConfig) { *c = DrillConfig{} }},
		{
			name:   "off, and the rest is not read",
			mutate: func(c *DrillConfig) { c.IdentityFile, c.DatabaseURL, c.KVMount = "", "", live },
		},
		{name: "no scratch role", mutate: func(c *DrillConfig) { c.DatabaseURL = "" }, wantErr: "both drill database URLs"},
		{name: "no admin role", mutate: func(c *DrillConfig) { c.AdminDatabaseURL = "" }, wantErr: "both drill database URLs"},
		{name: "the live mount", mutate: func(c *DrillConfig) { c.KVMount = live }, wantErr: "is the live one"},
		{name: "no mount at all", mutate: func(c *DrillConfig) { c.KVMount = "" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := full
			tt.mutate(&cfg)
			err := cfg.validate(live)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("validate() = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("validate() = %v", err)
			}
		})
	}
}

// The drill's public half is what rides on every archive (ADR-0051 item 2), so it has
// to come back from the identity file the operator points at.
func TestDrillConfigIdentity(t *testing.T) {
	key, recipient := identityFile(t)
	ids, got, err := DrillConfig{IdentityFile: key}.identity()
	if err != nil {
		t.Fatalf("identity() = %v", err)
	}
	if len(ids) != 1 {
		t.Errorf("identity() returned %d identities, want 1", len(ids))
	}
	if got != recipient {
		t.Errorf("recipient = %q, want %q", got, recipient)
	}
}

func TestDrillConfigIdentityFailures(t *testing.T) {
	dir := t.TempDir()
	empty := filepath.Join(dir, "empty.key")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	junk := filepath.Join(dir, "junk.key")
	if err := os.WriteFile(junk, []byte("not a key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name, file, wantErr string
	}{
		{name: "missing", file: filepath.Join(dir, "nope.key"), wantErr: "read the drill identity"},
		{name: "empty", file: empty, wantErr: "parse the drill identity"},
		{name: "not a key", file: junk, wantErr: "parse the drill identity"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := DrillConfig{IdentityFile: tt.file}.identity()
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("identity() = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

// The control plane runs as an App of itself, so it gets its configuration as env
// vars: the identity has to work as a value, not only as a path (ADR-0032), as the
// OpenBao token does.
func TestDrillConfigIdentityFromValue(t *testing.T) {
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	_, recipient, err := DrillConfig{Identity: id.String()}.identity()
	if err != nil || recipient != id.Recipient().String() {
		t.Fatalf("identity() = %q, %v; want %q", recipient, err, id.Recipient())
	}

	// A file beats a value, as it does for the OpenBao token.
	file, fromFile := identityFile(t)
	_, recipient, err = DrillConfig{IdentityFile: file, Identity: id.String()}.identity()
	if err != nil || recipient != fromFile {
		t.Fatalf("identity() = %q, %v; want the file's %q", recipient, err, fromFile)
	}
}

func TestDrillConfigEnabled(t *testing.T) {
	if (DrillConfig{}).enabled() {
		t.Error("drills are on with no identity at all")
	}
	if !(DrillConfig{IdentityFile: "/etc/bohux/drill.key"}).enabled() {
		t.Error("drills are off with an identity file")
	}
	if !(DrillConfig{Identity: "AGE-SECRET-KEY-1"}).enabled() {
		t.Error("drills are off with an identity value")
	}
}
