package docker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/bohumix/bohux/internal/provider"
)

// errNoTTY: Resize on a session that has no terminal.
var errNoTTY = errors.New("this exec session has no TTY")

// Exec runs a command in a running instance with `docker compose exec`. With a TTY,
// the CLI runs on a pseudo-terminal the agent holds, so compose allocates one in the
// container too; output then all arrives on Stdout.
func (p *Provider) Exec(ctx context.Context, req provider.ExecRequest) (provider.Session, error) {
	if len(req.Command) == 0 {
		return nil, errors.New("exec: command is required")
	}
	args := []string{"compose", "-p", project(req.Ref), "exec"}
	if !req.TTY {
		args = append(args, "-T")
	}
	if req.Instance != "" {
		i := strings.LastIndex(req.Instance, "-")
		if i < 0 || !strings.HasPrefix(req.Instance, project(req.Ref)) {
			return nil, fmt.Errorf("exec: %q is not an instance of this workload", req.Instance)
		}
		args = append(args, "--index", req.Instance[i+1:])
	}
	args = append(append(args, service), req.Command...)
	c := p.cmd(ctx, "", args...)
	if req.TTY {
		return startTTY(c, req.Rows, req.Cols)
	}
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

func (s *session) Resize(uint16, uint16) error { return errNoTTY }

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

// Close ends the session. It may run while another goroutine waits, so it never
// reads cmd.ProcessState: Kill on a reaped process is a harmless ErrProcessDone.
func (s *session) Close() error {
	_ = s.stdin.Close()
	if s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
	}
	return nil
}

// startTTY starts c as the session leader of a new pseudo-terminal.
func startTTY(c *exec.Cmd, rows, cols uint16) (provider.Session, error) {
	controller, terminal, err := openPTY()
	if err != nil {
		return nil, fmt.Errorf("exec: %w", err)
	}
	if err := setSize(controller, rows, cols); err != nil {
		_, _ = controller.Close(), terminal.Close()
		return nil, fmt.Errorf("exec: set terminal size: %w", err)
	}
	c.Stdin, c.Stdout, c.Stderr = terminal, terminal, terminal
	c.SysProcAttr = ttyProcAttr()
	err = c.Start()
	// The child holds the terminal now; the controller sees end of output once the
	// child and everything it started have closed it.
	_ = terminal.Close()
	if err != nil {
		_ = controller.Close()
		return nil, fmt.Errorf("exec: %w", err)
	}
	return &ttySession{cmd: c, pty: controller}, nil
}

type ttySession struct {
	cmd *exec.Cmd
	pty *os.File
}

func (s *ttySession) Stdin() io.WriteCloser { return ttyInput{s.pty} }
func (s *ttySession) Stdout() io.Reader     { return s.pty }
func (s *ttySession) Stderr() io.Reader     { return nil }

func (s *ttySession) Resize(rows, cols uint16) error { return setSize(s.pty, rows, cols) }

func (s *ttySession) Wait() (int, error) {
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

// Close ends the session; as session.Close, it is safe beside Wait.
func (s *ttySession) Close() error {
	if s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
	}
	return s.pty.Close()
}

// ttyInput writes to the terminal; closing it types end-of-file (^D), as at a
// keyboard, instead of closing the terminal under the running command.
type ttyInput struct{ pty *os.File }

func (t ttyInput) Write(b []byte) (int, error) { return t.pty.Write(b) }
func (t ttyInput) Close() error {
	_, err := t.pty.Write([]byte{4})
	return err
}
