package docker

import (
	"bufio"
	"context"
	"fmt"
	"iter"
	"strconv"
	"strings"
	"time"

	"github.com/bohumix/bohux/internal/provider"
)

// Logs runs `docker compose logs` and yields its lines. With Follow it streams until
// ctx ends, which stops the CLI.
func (p *Provider) Logs(ctx context.Context, req provider.LogRequest) (iter.Seq2[provider.LogLine, error], error) {
	args := []string{"compose", "-p", project(req.Ref), "logs", "--no-color", "--timestamps"}
	if req.Follow {
		args = append(args, "--follow")
	}
	if !req.Since.IsZero() {
		args = append(args, "--since", req.Since.UTC().Format(time.RFC3339Nano))
	}
	if req.TailLines > 0 {
		args = append(args, "--tail", strconv.Itoa(req.TailLines))
	}
	args = append(args, service)
	cctx, cancel := context.WithCancel(ctx)
	c := p.cmd(cctx, "", args...)
	stdout, err := c.StdoutPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("logs: %w", err)
	}
	if err := c.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("logs: %w", err)
	}
	return func(yield func(provider.LogLine, error) bool) {
		defer func() {
			cancel()
			_ = c.Wait()
		}()
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for sc.Scan() {
			line, ok := parseLogLine(sc.Text())
			if !ok || (req.Instance != "" && line.Instance != req.Instance) {
				continue
			}
			if !yield(line, nil) {
				return
			}
		}
		if err := sc.Err(); err != nil && ctx.Err() == nil {
			yield(provider.LogLine{}, fmt.Errorf("read logs: %w", err))
		}
	}, nil
}

// parseLogLine reads "<container>  | <RFC3339 timestamp> <message>".
func parseLogLine(s string) (provider.LogLine, bool) {
	name, rest, ok := strings.Cut(s, "|")
	if !ok {
		return provider.LogLine{}, false
	}
	rest = strings.TrimPrefix(rest, " ")
	ts, msg, _ := strings.Cut(rest, " ")
	t, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		return provider.LogLine{Instance: strings.TrimSpace(name), Message: rest}, true
	}
	return provider.LogLine{Time: t, Instance: strings.TrimSpace(name), Message: msg}, true
}
