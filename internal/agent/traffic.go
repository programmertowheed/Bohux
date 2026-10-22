package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/bohumix/bohux/internal/slo"
)

// Traffic follows the ingress's access log (ADR-0050) and counts each upstream's
// requests, server errors, and latencies in memory, since the agent started. It stores
// nothing; a line holds only an upstream, a status, and a duration (ingress.Caddy).
type Traffic struct {
	// Path is the access log Caddy writes.
	Path string
	// Interval between reads; zero means two seconds.
	Interval time.Duration

	mu     sync.Mutex
	counts map[string]*slo.Counts

	f       *os.File
	r       *bufio.Reader
	partial []byte
	opened  bool
}

// Run reads new lines every interval until ctx ends.
func (t *Traffic) Run(ctx context.Context) error {
	interval := t.Interval
	if interval <= 0 {
		interval = 2 * time.Second
	}
	tick := time.NewTicker(interval)
	defer tick.Stop()
	defer func() {
		if t.f != nil {
			_ = t.f.Close()
		}
	}()
	for {
		t.poll()
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
		}
	}
}

// poll reads what was appended since the last call. A file that exists when the agent
// starts is read from its end (counts are since the agent started); after Caddy rolls the file the rest of the old
// one is read before the new one, from its start.
func (t *Traffic) poll() {
	if t.f == nil {
		f, err := os.Open(t.Path)
		if errors.Is(err, fs.ErrNotExist) {
			// Caddy has written nothing yet: read the file from its start once it exists.
			t.opened = true
			return
		}
		if err != nil {
			slog.Warn("open ingress access log", "err", err)
			return
		}
		if !t.opened {
			if _, err := f.Seek(0, io.SeekEnd); err != nil {
				_ = f.Close()
				return
			}
		}
		t.f, t.r, t.opened = f, bufio.NewReader(f), true
	}
	t.readLines()
	cur, err1 := t.f.Stat()
	next, err2 := os.Stat(t.Path)
	switch {
	case err1 != nil:
		t.reopen()
	case err2 == nil && !os.SameFile(cur, next):
		// Rolled: the old file is read to its end above; continue with the new one.
		t.reopen()
		t.poll()
	case err2 == nil:
		if off, err := t.f.Seek(0, io.SeekCurrent); err == nil && next.Size() < off-int64(t.r.Buffered()) {
			// Truncated in place.
			_, _ = t.f.Seek(0, io.SeekStart)
			t.r.Reset(t.f)
			t.partial = nil
		}
	}
}

func (t *Traffic) reopen() {
	_ = t.f.Close()
	t.f, t.r, t.partial = nil, nil, nil
}

func (t *Traffic) readLines() {
	for {
		b, err := t.r.ReadBytes('\n')
		if len(b) > 0 {
			if err != nil {
				// No newline yet: the rest of the line comes later.
				t.partial = append(t.partial, b...)
				return
			}
			line := b
			if len(t.partial) > 0 {
				line = append(t.partial, b...)
				t.partial = nil
			}
			t.record(line)
		}
		if err != nil {
			return
		}
	}
}

// accessLine is what the agent reads from a log line.
type accessLine struct {
	Upstream string  `json:"upstream"`
	Status   int     `json:"status"`
	Duration float64 `json:"duration"`
}

func (t *Traffic) record(line []byte) {
	var l accessLine
	if json.Unmarshal(line, &l) != nil || l.Upstream == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.counts == nil {
		t.counts = map[string]*slo.Counts{}
	}
	c := t.counts[l.Upstream]
	if c == nil {
		c = &slo.Counts{Latency: make([]uint64, len(slo.LatencyBoundsMS)+1)}
		t.counts[l.Upstream] = c
	}
	c.Requests++
	if l.Status >= 500 {
		c.ServerErrors++
	}
	c.Latency[slo.Bucket(l.Duration*1000)]++
}

// Counts returns the counters of the upstreams dialled as endpoints, summed; zeros for
// those that served nothing yet.
func (t *Traffic) Counts(endpoints ...string) slo.Counts {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := slo.Counts{Latency: make([]uint64, len(slo.LatencyBoundsMS)+1)}
	for _, ep := range endpoints {
		c := t.counts[ep]
		if c == nil {
			continue
		}
		out.Requests += c.Requests
		out.ServerErrors += c.ServerErrors
		for i, n := range c.Latency {
			out.Latency[i] += n
		}
	}
	return out
}
