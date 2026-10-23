package swarm

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/bohumix/bohux/internal/provider"
)

// destroyWait bounds how long Destroy waits for Swarm to remove the stack's tasks and
// networks, which `docker stack rm` does in the background.
const destroyWait = 2 * time.Minute

// Destroy removes the stack, waits until its containers and networks are gone (a
// deploy of the same stack would otherwise race their removal), then removes its
// directory. Running it again is a no-op.
func (p *Provider) Destroy(ctx context.Context, ref provider.ResourceRef) error {
	if _, err := p.run(ctx, "", "stack", "rm", stack(ref)); err != nil {
		return err
	}
	deadline := time.Now().Add(destroyWait)
	for {
		left, err := p.stackObjects(ctx, ref, true)
		if err != nil {
			return err
		}
		if len(left) == 0 {
			break
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("stack %s still has %v after %v", stack(ref), left, destroyWait)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
	if err := os.RemoveAll(p.dir(ref)); err != nil {
		return fmt.Errorf("remove %s: %w", p.dir(ref), err)
	}
	return nil
}
