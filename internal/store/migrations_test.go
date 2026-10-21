package store

import (
	"fmt"
	"io/fs"
	"regexp"
	"strings"
	"testing"
)

var migrationName = regexp.MustCompile(`^(\d{5})_[a-z0-9_]+\.sql$`)

// CLAUDE.md: forward-only, one file per change, each reversible or explicitly marked
// irreversible with a reason.
func TestMigrationFiles(t *testing.T) {
	entries, err := fs.ReadDir(Migrations(), ".")
	if err != nil {
		t.Fatalf("read migrations: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("no migrations embedded")
	}
	for i, e := range entries {
		t.Run(e.Name(), func(t *testing.T) {
			m := migrationName.FindStringSubmatch(e.Name())
			if m == nil {
				t.Fatalf("name must match %s", migrationName)
			}
			if want := fmt.Sprintf("%05d", i+1); m[1] != want {
				t.Errorf("version %s, want %s: numbering must have no gaps", m[1], want)
			}
			b, err := fs.ReadFile(Migrations(), e.Name())
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			up, down, ok := strings.Cut(string(b), "-- +goose Down\n")
			if !strings.HasPrefix(up, "-- +goose Up\n") || !ok {
				t.Fatal("must have a -- +goose Up section followed by a -- +goose Down section")
			}
			if !hasSQL(down) && !strings.Contains(down, "-- irreversible:") {
				t.Error("Down must reverse Up, or say '-- irreversible: <why>'")
			}
		})
	}
}

func hasSQL(s string) bool {
	for _, line := range strings.Split(s, "\n") {
		if l := strings.TrimSpace(line); l != "" && !strings.HasPrefix(l, "--") {
			return true
		}
	}
	return false
}
