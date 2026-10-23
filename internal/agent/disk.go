package agent

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"golang.org/x/sys/unix"

	bohuxv1 "github.com/bohumix/bohux/gen/bohux/v1"
)

const (
	// DefaultMinFreeDisk is the free-space fraction below which the node is degraded
	// (ADR-0020 item 4).
	DefaultMinFreeDisk     = 0.10
	defaultDiskInterval    = 30 * time.Second
	reasonDiskPressure     = "DISK_PRESSURE"
	reasonDiskCheckFailing = "DISK_CHECK_FAILED"
)

// DiskMonitor watches free space on the filesystems the node writes to (the Docker
// data root, the agent state dir). Below MinFree on any of them, the node is degraded:
// it says so in every StatusReport and refuses new builds (ADR-0020 item 4). Running
// workloads are never touched because of it (rule 1).
type DiskMonitor struct {
	// Paths are checked on every pass; each is any path on the filesystem to watch.
	Paths []string
	// MinFree is a fraction in (0, 1); zero means DefaultMinFreeDisk.
	MinFree float64
	// Interval between checks; zero means 30 seconds.
	Interval time.Duration
	// Statfs returns free (available to unprivileged users) and total bytes for path;
	// nil means the real filesystem.
	Statfs func(path string) (free, total uint64, err error)

	mu       sync.Mutex
	degraded *bohuxv1.ErrorDetail
	changed  chan struct{}
}

// Degraded reports whether the node is under disk pressure, with the reason. Builds
// must check it and refuse while it is set.
func (m *DiskMonitor) Degraded() (bool, *bohuxv1.ErrorDetail) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.degraded != nil, m.degraded
}

// Changed is signaled whenever the degraded state flips.
func (m *DiskMonitor) Changed() <-chan struct{} {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.changed == nil {
		m.changed = make(chan struct{}, 1)
	}
	return m.changed
}

// Run checks until ctx ends.
func (m *DiskMonitor) Run(ctx context.Context) error {
	interval := m.Interval
	if interval <= 0 {
		interval = defaultDiskInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		m.Check(ctx)
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

// Check runs one pass and updates the degraded state. A path that cannot be checked
// counts as degraded: refusing a build is cheap, filling the disk is not.
func (m *DiskMonitor) Check(ctx context.Context) {
	minFree := m.MinFree
	if minFree <= 0 || minFree >= 1 {
		minFree = DefaultMinFreeDisk
	}
	statfs := m.Statfs
	if statfs == nil {
		statfs = realStatfs
	}
	var detail *bohuxv1.ErrorDetail
	for _, p := range m.Paths {
		free, total, err := statfs(p)
		if err != nil {
			slog.ErrorContext(ctx, "check free disk space", "path", p, "err", err)
			detail = &bohuxv1.ErrorDetail{Reason: reasonDiskCheckFailing}
			break
		}
		if total == 0 || float64(free)/float64(total) < minFree {
			slog.WarnContext(ctx, "disk pressure: node degraded, new builds refused",
				"path", p, "free_bytes", free, "total_bytes", total, "min_free", minFree)
			detail = &bohuxv1.ErrorDetail{Reason: reasonDiskPressure}
			break
		}
	}
	m.mu.Lock()
	flipped := (detail == nil) != (m.degraded == nil)
	m.degraded = detail
	if m.changed == nil {
		m.changed = make(chan struct{}, 1)
	}
	ch := m.changed
	m.mu.Unlock()
	if !flipped {
		return
	}
	if detail == nil {
		slog.InfoContext(ctx, "disk pressure cleared")
	}
	select {
	case ch <- struct{}{}:
	default:
	}
}

func realStatfs(path string) (uint64, uint64, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return 0, 0, fmt.Errorf("statfs %s: %w", path, err)
	}
	bsize := uint64(st.Bsize) //nolint:gosec // block size is positive
	return st.Bavail * bsize, st.Blocks * bsize, nil
}
