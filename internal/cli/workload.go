package cli

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	bohuxv1 "github.com/bohumix/bohux/gen/bohux/v1"
)

// ExitError carries the exit code of a command run with `bohux exec`, so the binary
// can exit with it.
type ExitError struct{ Code int }

func (e *ExitError) Error() string { return fmt.Sprintf("command exited with status %d", e.Code) }

// logs prints the App's log lines, following them with -f.
func (c *command) logs(ctx context.Context, args []string) error {
	fs, t := c.flags("logs")
	follow := fs.Bool("f", false, "keep printing new lines")
	tail := fs.Int("tail", 100, "lines of history to print first")
	since := fs.Duration("since", 0, "only lines newer than this, e.g. 10m")
	instance := fs.String("instance", "", "one instance instead of all")
	slug, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := c.connect(); err != nil {
		return err
	}
	app, err := c.app(ctx, t, slug)
	if err != nil {
		return err
	}
	req := &bohuxv1.StreamLogsRequest{AppId: app.GetMeta().GetId(), Follow: *follow, TailLines: int32(*tail), Instance: *instance}
	if *since > 0 {
		req.Since = timestamppb.New(time.Now().Add(-*since))
	}
	stream, err := c.workloads.StreamLogs(ctx, call(c.token, req))
	if err != nil {
		return fmt.Errorf("logs: %w", err)
	}
	defer func() { _ = stream.Close() }()
	for stream.Receive() {
		for _, l := range stream.Msg().GetLines() {
			c.printf("%s %s %s\n", l.GetTime().AsTime().Local().Format(time.RFC3339), l.GetInstance(), l.GetMessage())
		}
	}
	if err := stream.Err(); err != nil && !errors.Is(err, context.Canceled) {
		return fmt.Errorf("logs: %w", err)
	}
	return nil
}

// exec runs a command in one of the App's instances and prints its output. Standard
// input is not forwarded in v0.1, and there is no TTY.
func (c *command) exec(ctx context.Context, args []string) error {
	i := slices.Index(args, "--")
	if i < 0 || i == len(args)-1 {
		return fmt.Errorf("exec: put the command after --, e.g. bohux exec api -e prod -- ls: %w", ErrUsage)
	}
	command := args[i+1:]
	fs, t := c.flags("exec")
	instance := fs.String("instance", "", "instance to run in (default: any)")
	slug, err := parse(fs, args[:i])
	if err != nil {
		return err
	}
	if err := c.connect(); err != nil {
		return err
	}
	app, err := c.app(ctx, t, slug)
	if err != nil {
		return err
	}
	stream := c.workloads.Exec(ctx)
	stream.RequestHeader().Set("Authorization", "Bearer "+c.token)
	if err := stream.Send(&bohuxv1.ExecRequest{Msg: &bohuxv1.ExecRequest_Start{Start: &bohuxv1.ExecStart{
		AppId: app.GetMeta().GetId(), Instance: *instance, Command: command}}}); err != nil {
		return fmt.Errorf("exec: %w", err)
	}
	// No stdin: the process reads end of file at once.
	if err := stream.CloseRequest(); err != nil {
		return fmt.Errorf("exec: %w", err)
	}
	for {
		msg, err := stream.Receive()
		if err != nil {
			return fmt.Errorf("exec: %w", err)
		}
		switch m := msg.GetMsg().(type) {
		case *bohuxv1.ExecResponse_Stdout:
			_, _ = c.out.Write(m.Stdout)
		case *bohuxv1.ExecResponse_Stderr:
			_, _ = c.out.Write(m.Stderr)
		case *bohuxv1.ExecResponse_Exit:
			_ = stream.CloseResponse()
			if code := m.Exit.GetExitCode(); code != 0 {
				return &ExitError{Code: int(code)}
			}
			return nil
		}
	}
}
