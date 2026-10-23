package swarm

import (
	"context"
	"errors"
	"fmt"

	"github.com/bohumix/bohux/internal/provider"
	"github.com/bohumix/bohux/providers/docker"
)

// Exec runs a command in a running task's container with `docker exec`; every task is
// on this node (ADR-0045 item 7). With a TTY the CLI runs on a pseudo-terminal the
// agent holds, so Docker allocates one in the container too.
func (p *Provider) Exec(ctx context.Context, req provider.ExecRequest) (provider.Session, error) {
	if len(req.Command) == 0 {
		return nil, errors.New("exec: command is required")
	}
	cs, err := p.containers(ctx, req.Ref)
	if err != nil {
		return nil, fmt.Errorf("exec: %w", err)
	}
	var target string
	for _, c := range running(cs) {
		if req.Instance == "" || c.instance() == req.Instance {
			target = c.ID
			break
		}
	}
	if target == "" {
		if req.Instance != "" {
			return nil, fmt.Errorf("exec: %q is not a running instance of this workload", req.Instance)
		}
		return nil, errors.New("exec: no instance is running")
	}
	args := []string{"exec", "-i"}
	if req.TTY {
		args = append(args, "-t")
	}
	args = append(append(args, target), req.Command...)
	return docker.StartSession(p.cmd(ctx, "", args...), req.TTY, req.Rows, req.Cols)
}
