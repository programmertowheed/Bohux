package auth

import (
	"context"
	"errors"

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
	// The project the credential is scoped to; uuid.Nil means tenant-wide (ADR-0023).
	ProjectID uuid.UUID
	// The role, in ProjectID or, if tenant-wide, in every project.
	Role Role
}

// TenantWide reports whether the principal's role applies to the whole tenant.
func (p Principal) TenantWide() bool { return p.ProjectID == uuid.Nil }

// ErrPermissionDenied: authenticated, but not allowed.
var ErrPermissionDenied = errors.New("permission denied")

// CanAccessProject reports whether p may act on project with at least role min. Every
// handler calls it (through RequireProject) before touching a project's resources.
func (p Principal) CanAccessProject(project uuid.UUID, min Role) bool {
	if p.Role < min || project == uuid.Nil {
		return false
	}
	return p.TenantWide() || p.ProjectID == project
}

// RequireProject is the handler-side check (ADR-0023, step two): the caller in ctx
// must hold at least min in project.
func RequireProject(ctx context.Context, project uuid.UUID, min Role) error {
	p, ok := FromContext(ctx)
	if !ok {
		return ErrUnauthenticated
	}
	if !p.CanAccessProject(project, min) {
		return ErrPermissionDenied
	}
	return nil
}

// CanGrant reports whether p may create a credential with this scope and role: never
// wider and never higher than its own (ADR-0023).
func (p Principal) CanGrant(project uuid.UUID, role Role) bool {
	if role > p.Role || role == RoleNone {
		return false
	}
	if project == uuid.Nil {
		return p.TenantWide()
	}
	return p.TenantWide() || p.ProjectID == project
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
