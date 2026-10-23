package docker

import (
	"context"
	"fmt"
	"os"

	"github.com/bohumix/bohux/internal/provider"
)

// Destroy removes the project's containers, networks, and volumes, then its directory.
// Running it again is a no-op.
func (p *Provider) Destroy(ctx context.Context, ref provider.ResourceRef) error {
	if _, err := p.run(ctx, "", "compose", "-p", project(ref), "down",
		"--volumes", "--remove-orphans", "--timeout", "10"); err != nil {
		return err
	}
	if err := os.RemoveAll(p.dir(ref)); err != nil {
		return fmt.Errorf("remove %s: %w", p.dir(ref), err)
	}
	return nil
}
