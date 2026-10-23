package agent

import (
	"slices"
	"testing"

	"github.com/bohumix/bohux/internal/provider"
)

func TestPrunable(t *testing.T) {
	// Release IDs sort by creation time, like UUIDv7.
	img := func(id, app, rel string) provider.ReleaseImage {
		return provider.ReleaseImage{ID: id, App: app, Release: rel}
	}
	many := func(app string, n int) []provider.ReleaseImage {
		var out []provider.ReleaseImage
		for i := 1; i <= n; i++ {
			r := string(rune('a' + i - 1)) // a (oldest) ... newest
			out = append(out, img("sha256:"+app+r, app, "r-"+r))
		}
		return out
	}
	tests := []struct {
		name    string
		images  []provider.ReleaseImage
		current map[string]string
		inUse   map[string]bool
		keep    int
		want    []string
	}{
		{
			name:    "within the window nothing goes",
			images:  many("x", 6),
			current: map[string]string{"x": "r-f"},
			keep:    5,
		},
		{
			name:    "current newest, oldest two beyond the window go",
			images:  many("x", 8),
			current: map[string]string{"x": "r-h"},
			keep:    5,
			want:    []string{"sha256:xa", "sha256:xb"},
		},
		{
			name:    "an old current release (after a rollback) is kept on top of the window",
			images:  many("x", 8),
			current: map[string]string{"x": "r-a"},
			keep:    5,
			want:    []string{"sha256:xb", "sha256:xc"},
		},
		{
			name:    "an image a container uses is never removed",
			images:  many("x", 8),
			current: map[string]string{"x": "r-h"},
			inUse:   map[string]bool{"sha256:xa": true},
			keep:    5,
			want:    []string{"sha256:xb"},
		},
		{
			name:    "apps are counted separately",
			images:  append(many("x", 7), many("y", 3)...),
			current: map[string]string{"x": "r-g", "y": "r-c"},
			keep:    5,
			want:    []string{"sha256:xa"},
		},
		{
			name:    "an app no longer assigned keeps its newest releases",
			images:  many("gone", 7),
			current: map[string]string{},
			keep:    5,
			want:    []string{"sha256:gonea", "sha256:goneb"},
		},
		{
			name: "an image shared with a kept release stays",
			images: append(many("x", 7),
				img("sha256:xa", "x", "r-z")), // same image, newest release
			current: map[string]string{"x": "r-z"},
			keep:    5,
			want:    []string{"sha256:xb"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := prunable(tt.images, tt.current, tt.inUse, tt.keep)
			if !slices.Equal(got, tt.want) {
				t.Errorf("prunable() = %v, want %v", got, tt.want)
			}
		})
	}
}
