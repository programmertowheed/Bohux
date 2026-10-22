package swarm

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"iter"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bohumix/bohux/internal/provider"
)

// Logs runs `docker service logs` and yields its lines; it reaches every task. With
// Follow it streams until ctx ends, which stops the CLI.
func (p *Provider) Logs(ctx context.Context, req provider.LogRequest) (iter.Seq2[provider.LogLine, error], error) {
	args := []string{"service", "logs", "--timestamps", "--no-task-ids", "--no-trunc"}
	if req.Follow {
		args = append(args, "--follow")
	}
	if !req.Since.IsZero() {
		args = append(args, "--since", req.Since.UTC().Format(time.RFC3339Nano))
	}
	if req.TailLines > 0 {
		args = append(args, "--tail", strconv.Itoa(req.TailLines))
	}
	args = append(args, serviceName(req.Ref))
	cctx, cancel := context.WithCancel(ctx)
	c := p.cmd(cctx, "", args...)
	stdout, err := c.StdoutPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("logs: %w", err)
	}
	stderr, err := c.StderrPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("logs: %w", err)
	}
	if err := c.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("logs: %w", err)
	}
	// The CLI writes the tasks' stdout and stderr to its own.
	lines := make(chan provider.LogLine)
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	read := func(r io.Reader, isStderr bool) {
		defer wg.Done()
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for sc.Scan() {
			line, ok := parseLogLine(sc.Text())
			if !ok || (req.Instance != "" && line.Instance != req.Instance) {
				continue
			}
			line.Stderr = isStderr
			select {
			case lines <- line:
			case <-cctx.Done():
				return
			}
		}
		if err := sc.Err(); err != nil && cctx.Err() == nil {
			errs <- fmt.Errorf("read logs: %w", err)
		}
	}
	wg.Add(2)
	go read(stdout, false)
	go read(stderr, true)
	go func() {
		wg.Wait()
		close(lines)
	}()
	return func(yield func(provider.LogLine, error) bool) {
		defer func() {
			cancel()
			for range lines {
			}
			_ = c.Wait()
		}()
		for line := range lines {
			if !yield(line, nil) {
				return
			}
		}
		select {
		case err := <-errs:
			yield(provider.LogLine{}, err)
		default:
		}
	}, nil
}

// parseLogLine reads "<RFC3339 timestamp> <task>@<node>    | <message>".
func parseLogLine(s string) (provider.LogLine, bool) {
	ts, rest, ok := strings.Cut(s, " ")
	if !ok {
		return provider.LogLine{}, false
	}
	t, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		return provider.LogLine{}, false
	}
	name, msg, ok := strings.Cut(rest, "|")
	if !ok {
		return provider.LogLine{}, false
	}
	name = strings.TrimSpace(name)
	if i := strings.LastIndex(name, "@"); i > 0 {
		name = name[:i]
	}
	return provider.LogLine{Time: t, Instance: name, Message: strings.TrimPrefix(msg, " ")}, true
}
