package docker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"

	"github.com/bohumix/bohux/internal/provider"
)

// errTTY: an interactive terminal needs a pseudo-terminal on the agent side, which v0.1
// does not have yet. Non-interactive exec works.
var errTTY = errors.New("exec with a TTY is not supported yet; run without one")

// Exec runs a command in a running instance with `docker compose exec`.
func (p *Provider) Exec(ctx context.Context, req provider.ExecRequest) (provider.Session, error) {
	if req.TTY {
		return nil, errTTY
	}
	if len(req.Command) == 0 {
		return nil, errors.New("exec: command is required")
	}
	args := []string{"compose", "-p", project(req.Ref), "exec", "-T"}
	if req.Instance != "" {
		i := strings.LastIndex(req.Instance, "-")
		if i < 0 || !strings.HasPrefix(req.Instance, project(req.Ref)) {
			return nil, fmt.Errorf("exec: %q is not an instance of this workload", req.Instance)
		}
		args = append(args, "--index", req.Instance[i+1:])
	}
	args = append(append(args, service), req.Command...)
	c := p.cmd(ctx, "", args...)
	stdin, err := c.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("exec: %w", err)
	}
	stdout, err := c.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("exec: %w", err)
	}
	stderr, err := c.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("exec: %w", err)
	}
	if err := c.Start(); err != nil {
		return nil, fmt.Errorf("exec: %w", err)
	}
	return &session{cmd: c, stdin: stdin, stdout: stdout, stderr: stderr}, nil
}

type session struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.Reader
	stderr io.Reader
}

func (s *session) Stdin() io.WriteCloser { return s.stdin }
func (s *session) Stdout() io.Reader     { return s.stdout }
func (s *session) Stderr() io.Reader     { return s.stderr }

func (s *session) Resize(uint16, uint16) error { return errTTY }

// Wait returns the command's exit code. Read Stdout and Stderr to the end first.
func (s *session) Wait() (int, error) {
	err := s.cmd.Wait()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode(), nil
	}
	if err != nil {
		return 0, fmt.Errorf("exec: %w", err)
	}
	return 0, nil
}

func (s *session) Close() error {
	_ = s.stdin.Close()
	if s.cmd.ProcessState == nil && s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
	}
	return nil
}
