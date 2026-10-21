// Package auth issues and verifies Bohux credentials and holds the authenticated
// principal (ADR-0022, ADR-0011).
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/google/uuid"
)

// Kind is a credential's prefix (ADR-0022).
type Kind string

const (
	// KindAPI is an API token: bxa_…
	KindAPI Kind = "bxa"
	// KindBootstrap is a one-time node bootstrap token: bxb_…
	KindBootstrap Kind = "bxb"
)

const secretBytes = 32

// ErrUnauthenticated is the only error authentication returns to callers: a bad
// format, an unknown tenant or ID, a wrong secret, and an expired or revoked token are
// indistinguishable on purpose (ADR-0022).
var ErrUnauthenticated = errors.New("unauthenticated")

// Token is a parsed credential. It never prints its secret (rule 3).
type Token struct {
	Kind   Kind
	Tenant uuid.UUID
	ID     uuid.UUID
	secret []byte
}

// Issue creates a credential for a row with this tenant and ID. It returns the string
// to hand to the user once, and the hash to store.
func Issue(kind Kind, tenant, id uuid.UUID) (raw string, hash []byte, err error) {
	if kind != KindAPI && kind != KindBootstrap {
		return "", nil, fmt.Errorf("issue token: unknown kind %q", kind)
	}
	secret := make([]byte, secretBytes)
	if _, err := rand.Read(secret); err != nil {
		return "", nil, fmt.Errorf("issue token: %w", err)
	}
	t := Token{Kind: kind, Tenant: tenant, ID: id, secret: secret}
	raw = string(kind) + "_" + hex.EncodeToString(tenant[:]) + "_" +
		hex.EncodeToString(id[:]) + "_" + base64.RawURLEncoding.EncodeToString(secret)
	return raw, t.Hash(), nil
}

// Parse splits a credential string. Any malformed input is ErrUnauthenticated.
func Parse(raw string) (Token, error) {
	// The secret is base64url and may itself contain "_", so split at most 4 ways.
	parts := strings.SplitN(raw, "_", 4)
	if len(parts) != 4 {
		return Token{}, ErrUnauthenticated
	}
	kind := Kind(parts[0])
	if kind != KindAPI && kind != KindBootstrap {
		return Token{}, ErrUnauthenticated
	}
	tenant, err := parseHexUUID(parts[1])
	if err != nil {
		return Token{}, ErrUnauthenticated
	}
	id, err := parseHexUUID(parts[2])
	if err != nil {
		return Token{}, ErrUnauthenticated
	}
	secret, err := base64.RawURLEncoding.DecodeString(parts[3])
	if err != nil || len(secret) != secretBytes {
		return Token{}, ErrUnauthenticated
	}
	return Token{Kind: kind, Tenant: tenant, ID: id, secret: secret}, nil
}

func parseHexUUID(s string) (uuid.UUID, error) {
	if len(s) != 32 {
		return uuid.Nil, errors.New("bad length")
	}
	var id uuid.UUID
	if _, err := hex.Decode(id[:], []byte(s)); err != nil {
		return uuid.Nil, fmt.Errorf("decode: %w", err)
	}
	if id == uuid.Nil {
		return uuid.Nil, errors.New("nil uuid")
	}
	return id, nil
}

// Hash is SHA-256 of the secret, the only form stored (ADR-0022).
func (t Token) Hash() []byte {
	h := sha256.Sum256(t.secret)
	return h[:]
}

// Matches reports, in constant time, whether stored is this token's hash.
func (t Token) Matches(stored []byte) bool {
	return subtle.ConstantTimeCompare(t.Hash(), stored) == 1
}

// String omits the secret.
func (t Token) String() string {
	return fmt.Sprintf("%s token %s/%s", t.Kind, t.Tenant, t.ID)
}

// GoString omits the secret, for %#v.
func (t Token) GoString() string { return t.String() }

// LogValue omits the secret, for slog.
func (t Token) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("kind", string(t.Kind)),
		slog.String("tenant", t.Tenant.String()),
		slog.String("id", t.ID.String()),
	)
}
