package auth

import (
	"testing"

	"github.com/google/uuid"
)

func TestScopeChecks(t *testing.T) {
	p1 := uuid.MustParse("0192f0a0-0000-7000-8000-000000000001")
	p2 := uuid.MustParse("0192f0a0-0000-7000-8000-000000000002")
	projectDeployer := Principal{ProjectID: p1, Role: RoleDeployer}
	tenantOwner := Principal{Role: RoleOwner}

	access := []struct {
		name    string
		p       Principal
		project uuid.UUID
		min     Role
		want    bool
	}{
		{"own project, enough role", projectDeployer, p1, RoleDeployer, true},
		{"own project, role too low", projectDeployer, p1, RoleOwner, false},
		{"other project", projectDeployer, p2, RoleViewer, false},
		{"tenant-wide reaches any project", tenantOwner, p2, RoleOwner, true},
		{"no project is never a match", tenantOwner, uuid.Nil, RoleViewer, false},
	}
	for _, tt := range access {
		t.Run("access/"+tt.name, func(t *testing.T) {
			if got := tt.p.CanAccessProject(tt.project, tt.min); got != tt.want {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}

	grant := []struct {
		name    string
		p       Principal
		project uuid.UUID
		role    Role
		want    bool
	}{
		{"same scope, lower role", projectDeployer, p1, RoleViewer, true},
		{"same scope, same role", projectDeployer, p1, RoleDeployer, true},
		{"higher role", projectDeployer, p1, RoleOwner, false},
		{"other project", projectDeployer, p2, RoleViewer, false},
		{"widen to tenant", projectDeployer, uuid.Nil, RoleViewer, false},
		{"tenant owner grants tenant-wide", tenantOwner, uuid.Nil, RoleOwner, true},
		{"tenant owner grants a project", tenantOwner, p2, RoleDeployer, true},
		{"no role", tenantOwner, p1, RoleNone, false},
	}
	for _, tt := range grant {
		t.Run("grant/"+tt.name, func(t *testing.T) {
			if got := tt.p.CanGrant(tt.project, tt.role); got != tt.want {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}
