package openbao

import (
	"errors"
	"testing"

	"github.com/google/uuid"
)

func TestPathOf(t *testing.T) {
	tenant, other, id := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	if p, err := pathOf(tenant, Handle(tenant, id)); err != nil || p != Path(tenant, id) {
		t.Errorf("own handle = %q, %v", p, err)
	}
	for _, h := range []string{Handle(other, id), "openbao:bohux/" + tenant.String() + "x/" + id.String(), "vault:x", ""} {
		if _, err := pathOf(tenant, h); !errors.Is(err, ErrForeignHandle) {
			t.Errorf("pathOf(%q) = %v, want ErrForeignHandle", h, err)
		}
	}
}
