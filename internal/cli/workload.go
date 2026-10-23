package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"sync"
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

// exec runs a command in one of the App's instances and prints its output. With -i
// it forwards standard input; with -t it also gives the command a terminal, puts the
// local one in raw mode, and follows its size, like `ssh -t`.
func (c *command) exec(ctx context.Context, args []string) error {
	i := slices.Index(args, "--")
	if i < 0 || i == len(args)-1 {
		return fmt.Errorf("exec: put the command after --, e.g. bohux exec api -e prod -- ls: %w", ErrUsage)
	}
	command := args[i+1:]
	fs, t := c.flags("exec")
	instance := fs.String("instance", "", "instance to run in (default: any)")
	interactive := fs.Bool("i", false, "forward standard input")
	tty := fs.Bool("t", false, "allocate a terminal (implies -i); standard input must be one")
	slug, err := parse(fs, args[:i])
	if err != nil {
		return err
	}
	var term *os.File
	if *tty {
		f, ok := c.in.(*os.File)
		if !ok || !isTerminal(f) {
			return fmt.Errorf("exec -t: standard input is not a terminal; use -i: %w", ErrUsage)
		}
		term, *interactive = f, true
	}
	if err := c.connect(); err != nil {
		return err
	}
	app, err := c.app(ctx, t, slug)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stream := c.workloads.Exec(ctx)
	stream.RequestHeader().Set("Authorization", "Bearer "+c.token)
	start := &bohuxv1.ExecStart{AppId: app.GetMeta().GetId(), Instance: *instance, Command: command, Tty: *tty}
	if term != nil {
		rows, cols := termSize(term)
		start.Size = &bohuxv1.TerminalSize{Rows: rows, Cols: cols}
	}
	// Sends come from the input and resize goroutines; a stream takes one at a time.
	var sendMu sync.Mutex
	send := func(m *bohuxv1.ExecRequest) error {
		sendMu.Lock()
		defer sendMu.Unlock()
		return stream.Send(m)
	}
	if err := send(&bohuxv1.ExecRequest{Msg: &bohuxv1.ExecRequest_Start{Start: start}}); err != nil {
		return fmt.Errorf("exec: %w", err)
	}
	if term != nil {
		restore, err := makeRaw(term)
		if err != nil {
			return fmt.Errorf("exec -t: %w", err)
		}
		defer restore()
		go func() {
			for range resizes(ctx) {
				rows, cols := termSize(term)
				_ = send(&bohuxv1.ExecRequest{Msg: &bohuxv1.ExecRequest_Resize{
					Resize: &bohuxv1.TerminalSize{Rows: rows, Cols: cols}}})
			}
		}()
	}
	if *interactive && c.in != nil {
		go func() {
			buf := make([]byte, 32<<10)
			for {
				n, err := c.in.Read(buf)
				if n > 0 {
					if send(&bohuxv1.ExecRequest{Msg: &bohuxv1.ExecRequest_Stdin{Stdin: append([]byte(nil), buf[:n]...)}}) != nil {
						return
					}
				}
				if err != nil {
					// End of input: the process reads end of file.
					sendMu.Lock()
					_ = stream.CloseRequest()
					sendMu.Unlock()
					return
				}
			}
		}()
	} else if err := stream.CloseRequest(); err != nil {
		// No stdin: the process reads end of file at once.
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
