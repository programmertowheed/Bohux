package agent

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"github.com/bohumix/bohux/internal/provider"
)

const (
	// DefaultKeepReleases is how many Releases per App are kept besides the current
	// one (ADR-0020 item 3).
	DefaultKeepReleases    = 5
	defaultRetentionPeriod = time.Hour
)

// Retention deletes release images beyond the rollback window: per App it keeps the
// current Release's images and those of the Keep most recent other Releases, and it
// never deletes an image a container uses (ADR-0020 item 3).
type Retention struct {
	Images provider.ImageStore
	Cache  *Cache
	// Keep is how many Releases besides the current one survive; zero means 5.
	Keep int
	// Interval between passes; zero means one hour.
	Interval time.Duration
}

// Run prunes until ctx ends.
func (r *Retention) Run(ctx context.Context) error {
	interval := r.Interval
	if interval <= 0 {
		interval = defaultRetentionPeriod
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if _, err := r.Once(ctx); err != nil {
			slog.ErrorContext(ctx, "image retention; will retry", "err", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

// Once runs one pass and returns the IDs it removed. Without a cached assignment it
// removes nothing: the current Releases are unknown, and an unknown current must
// never read as "nothing is current".
func (r *Retention) Once(ctx context.Context) ([]string, error) {
	a := r.Cache.Current()
	if a == nil {
		return nil, nil
	}
	current := map[string]string{}
	for _, w := range a.GetWorkloads() {
		current[w.GetAppId()] = w.GetReleaseId()
	}
	images, err := r.Images.ReleaseImages(ctx)
	if err != nil {
		return nil, fmt.Errorf("list release images: %w", err)
	}
	inUse, err := r.Images.ImagesInUse(ctx)
	if err != nil {
		return nil, fmt.Errorf("list images in use: %w", err)
	}
	keep := r.Keep
	if keep <= 0 {
		keep = DefaultKeepReleases
	}
	var removed []string
	for _, id := range prunable(images, current, inUse, keep) {
		if err := r.Images.RemoveImage(ctx, id); err != nil {
			slog.WarnContext(ctx, "remove old release image; will retry", "image", id, "err", err)
			continue
		}
		slog.InfoContext(ctx, "removed old release image", "image", id)
		removed = append(removed, id)
	}
	return removed, nil
}

// prunable picks the images to delete. Per App, the Releases are ordered newest first
// by ID (Release IDs are UUIDv7, so their text order is creation order). The current
// Release plus the keep newest others are kept; an image is deleted only if no kept
// Release uses it and no container does.
func prunable(images []provider.ReleaseImage, current map[string]string, inUse map[string]bool, keep int) []string {
	releases := map[string][]string{} // app -> distinct releases
	seen := map[[2]string]bool{}
	for _, im := range images {
		k := [2]string{im.App, im.Release}
		if !seen[k] {
			seen[k] = true
			releases[im.App] = append(releases[im.App], im.Release)
		}
	}
	kept := map[[2]string]bool{}
	for app, rs := range releases {
		sort.Sort(sort.Reverse(sort.StringSlice(rs)))
		others := 0
		for _, rel := range rs {
			switch {
			case rel == current[app]:
				kept[[2]string{app, rel}] = true
			case others < keep:
				kept[[2]string{app, rel}] = true
				others++
			}
		}
	}
	keepID := map[string]bool{}
	for _, im := range images {
		if kept[[2]string{im.App, im.Release}] || inUse[im.ID] {
			keepID[im.ID] = true
		}
	}
	var out []string
	done := map[string]bool{}
	for _, im := range images {
		if keepID[im.ID] || done[im.ID] {
			continue
		}
		done[im.ID] = true
		out = append(out, im.ID)
	}
	sort.Strings(out)
	return out
}
