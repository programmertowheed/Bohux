package auth

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/google/uuid"
)

var (
	tenant = uuid.MustParse("0192f0a0-0000-7000-8000-00000000000a")
	id     = uuid.MustParse("0192f0a0-0000-7000-8000-000000000001")
)

func TestIssueParseRoundTrip(t *testing.T) {
	for _, kind := range []Kind{KindAPI, KindBootstrap} {
		t.Run(string(kind), func(t *testing.T) {
			raw, hash, err := Issue(kind, tenant, id)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(raw, string(kind)+"_") {
				t.Errorf("token %q lacks prefix %s_", raw, kind)
			}
			tok, err := Parse(raw)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if tok.Kind != kind || tok.Tenant != tenant || tok.ID != id {
				t.Errorf("parsed %v, want %s %s/%s", tok, kind, tenant, id)
			}
			if !tok.Matches(hash) {
				t.Error("token does not match its own hash")
			}
		})
	}
}

func TestParseRejects(t *testing.T) {
	raw, _, err := Issue(KindAPI, tenant, id)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.SplitN(raw, "_", 4)
	short := base64.RawURLEncoding.EncodeToString([]byte("short"))
	tests := []struct{ name, raw string }{
		{"empty", ""},
		{"unknown prefix", "bxz_" + strings.Join(parts[1:], "_")},
		{"too few parts", parts[0] + "_" + parts[1]},
		{"bad tenant hex", parts[0] + "_zz" + parts[1][2:] + "_" + parts[2] + "_" + parts[3]},
		{"nil tenant", parts[0] + "_" + strings.Repeat("0", 32) + "_" + parts[2] + "_" + parts[3]},
		{"short id", parts[0] + "_" + parts[1] + "_abc_" + parts[3]},
		{"short secret", parts[0] + "_" + parts[1] + "_" + parts[2] + "_" + short},
		{"secret not base64url", parts[0] + "_" + parts[1] + "_" + parts[2] + "_" + strings.Repeat("!", 43)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := Parse(tt.raw); !errors.Is(err, ErrUnauthenticated) {
				t.Errorf("Parse(%q) = %v, want ErrUnauthenticated", tt.raw, err)
			}
		})
	}
}

func TestMatchesRejectsOtherSecret(t *testing.T) {
	raw, _, err := Issue(KindAPI, tenant, id)
	if err != nil {
		t.Fatal(err)
	}
	_, otherHash, err := Issue(KindAPI, tenant, id)
	if err != nil {
		t.Fatal(err)
	}
	tok, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if tok.Matches(otherHash) {
		t.Error("token matched another token's hash")
	}
}

// Rule 3: a token's secret never reaches logs or formatted strings.
func TestTokenNeverPrintsSecret(t *testing.T) {
	raw, _, err := Issue(KindAPI, tenant, id)
	if err != nil {
		t.Fatal(err)
	}
	tok, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	secret := strings.SplitN(raw, "_", 4)[3]

	var logs bytes.Buffer
	slog.New(slog.NewJSONHandler(&logs, nil)).Info("auth", "token", tok)
	slog.New(slog.NewTextHandler(&logs, nil)).Info("auth", "token", tok)
	outputs := map[string]string{
		"slog":   logs.String(),
		"%v":     fmt.Sprintf("%v", tok),
		"%+v":    fmt.Sprintf("%+v", tok),
		"%#v":    fmt.Sprintf("%#v", tok),
		"String": tok.String(),
	}
	for name, out := range outputs {
		if strings.Contains(out, secret) {
			t.Errorf("%s output contains the secret: %s", name, out)
		}
	}
}
