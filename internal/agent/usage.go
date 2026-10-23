package agent

import (
	"bufio"
	"context"
	"log/slog"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	bohuxv1 "github.com/bohumix/bohux/gen/bohux/v1"
	"github.com/bohumix/bohux/internal/provider"
)

const (
	defaultSampleInterval = 30 * time.Second
	// maxSampleAge is how old a sample may be and still be reported (ADR-0040).
	maxSampleAge = 2 * time.Minute
)

// Sampler measures the node and, through the provider's optional UsageReader, its
// workloads, off the reconcile loop so a slow sample never delays a pass (ADR-0040).
type Sampler struct {
	// Paths are the filesystems to report, as the disk-pressure monitor's.
	Paths    []string
	Provider provider.Provider
	// Interval between samples; zero means 30 seconds.
	Interval time.Duration
	// ProcRoot replaces /proc in tests.
	ProcRoot string

	mu        sync.Mutex
	node      *bohuxv1.NodeUsage
	workloads map[provider.ResourceRef]provider.Usage
	at        time.Time
	prevBusy  uint64
	prevTotal uint64
}

// Run samples until ctx ends.
func (s *Sampler) Run(ctx context.Context) error {
	interval := s.Interval
	if interval <= 0 {
		interval = defaultSampleInterval
	}
	for {
		s.Sample(ctx)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(interval):
		}
	}
}

// Sample takes one sample now.
func (s *Sampler) Sample(ctx context.Context) {
	now := time.Now()
	node := &bohuxv1.NodeUsage{CpuCores: int32(runtime.NumCPU()), SampledAt: timestamppb.New(now)}
	if busy, total, err := s.cpuTimes(); err == nil {
		s.mu.Lock()
		if s.prevTotal != 0 && total > s.prevTotal {
			node.CpuBusy = float64(busy-s.prevBusy) / float64(total-s.prevTotal)
		}
		s.prevBusy, s.prevTotal = busy, total
		s.mu.Unlock()
	}
	if total, avail, err := s.memory(); err == nil {
		node.MemoryTotalBytes, node.MemoryAvailableBytes = total, avail
	}
	for _, p := range s.Paths {
		var st syscall.Statfs_t
		if err := syscall.Statfs(p, &st); err != nil {
			continue
		}
		node.Disks = append(node.Disks, &bohuxv1.DiskUsage{Path: p,
			// Bsize is int64 on Linux, uint32 on macOS.
			TotalBytes: int64(st.Blocks) * int64(st.Bsize), FreeBytes: int64(st.Bavail) * int64(st.Bsize)}) //nolint:unconvert
	}
	var workloads map[provider.ResourceRef]provider.Usage
	if ur, ok := s.Provider.(provider.UsageReader); ok {
		w, err := ur.Usage(ctx)
		if err != nil {
			slog.WarnContext(ctx, "sample workload usage", "err", err)
		} else {
			workloads = w
		}
	}
	s.mu.Lock()
	s.node, s.workloads, s.at = node, workloads, now
	s.mu.Unlock()
}

// Latest returns the newest sample, or nothing when it is older than maxSampleAge.
func (s *Sampler) Latest() (*bohuxv1.NodeUsage, map[provider.ResourceRef]provider.Usage, time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.node == nil || time.Since(s.at) > maxSampleAge {
		return nil, nil, time.Time{}
	}
	return s.node, s.workloads, s.at
}

func (s *Sampler) proc(name string) string {
	root := s.ProcRoot
	if root == "" {
		root = "/proc"
	}
	return root + "/" + name
}

// cpuTimes reads the total and busy jiffies from /proc/stat's "cpu" line; idle and
// iowait are not busy.
func (s *Sampler) cpuTimes() (busy, total uint64, err error) {
	b, err := os.ReadFile(s.proc("stat"))
	if err != nil {
		return 0, 0, err
	}
	line, _, _ := strings.Cut(string(b), "\n")
	f := strings.Fields(line)
	if len(f) < 5 || f[0] != "cpu" {
		return 0, 0, os.ErrInvalid
	}
	for i, v := range f[1:] {
		n, err := strconv.ParseUint(v, 10, 64)
		if err != nil {
			return 0, 0, err
		}
		total += n
		if i != 3 && i != 4 { // idle, iowait
			busy += n
		}
	}
	return busy, total, nil
}

// memory reads MemTotal and MemAvailable from /proc/meminfo, in bytes.
func (s *Sampler) memory() (total, available int64, err error) {
	f, err := os.Open(s.proc("meminfo"))
	if err != nil {
		return 0, 0, err
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue
		}
		kb, err := strconv.ParseInt(strings.TrimSuffix(strings.TrimSpace(v), " kB"), 10, 64)
		if err != nil {
			continue
		}
		switch k {
		case "MemTotal":
			total = kb << 10
		case "MemAvailable":
			available = kb << 10
		}
	}
	return total, available, sc.Err()
}
