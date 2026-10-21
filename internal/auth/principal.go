package auth

import (
	"context"

	"github.com/google/uuid"

	"github.com/bohumix/bohux/internal/store"
)

// Role is one of the fixed per-project roles (ADR-0011). Each includes the ones before
// it: owner can do everything deployer can, deployer everything viewer can.
type Role int

const (
	RoleNone Role = iota
	RoleViewer
	RoleDeployer
	RoleOwner
)

// ParseRole maps the stored form ('viewer', …) to a Role.
func ParseRole(s string) Role {
	switch s {
	case "viewer":
		return RoleViewer
	case "deployer":
		return RoleDeployer
	case "owner":
		return RoleOwner
	default:
		return RoleNone
	}
}

// String is the stored form.
func (r Role) String() string {
	switch r {
	case RoleViewer:
		return "viewer"
	case RoleDeployer:
		return "deployer"
	case RoleOwner:
		return "owner"
	default:
		return "none"
	}
}

// Principal is who is calling, derived only from authentication (rule 10).
type Principal struct {
	ID     uuid.UUID
	Kind   store.ActorKind
	Tenant uuid.UUID
	// The project the credential is scoped to, and its role there.
	ProjectID uuid.UUID
	Role      Role
}

// Actor is the principal as the store records it.
func (p Principal) Actor() store.Actor { return store.Actor{ID: p.ID, Kind: p.Kind} }

type principalKey struct{}

// WithPrincipal returns ctx carrying p.
func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

// FromContext returns the authenticated principal, if any.
func FromContext(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(Principal)
	return p, ok
}
