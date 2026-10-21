package docker

import (
	"context"
	"fmt"
	"strings"

	"github.com/bohumix/bohux/internal/provider"
)

// The agent's image retention runs over the images this provider's workloads use
// (ADR-0020 item 3, ADR-0025). It is not a provider verb.
var _ provider.ImageStore = (*Provider)(nil)

// ReleaseImages lists images labeled io.bohux.app and io.bohux.release. Bohux puts
// these labels on the images it builds (S7); images it did not build are never touched.
func (p *Provider) ReleaseImages(ctx context.Context) ([]provider.ReleaseImage, error) {
	ids, err := p.lines(ctx, "image", "ls", "--quiet", "--no-trunc",
		"--filter", "label="+labelApp, "--filter", "label="+labelRelease)
	if err != nil || len(ids) == 0 {
		return nil, err
	}
	format := fmt.Sprintf(`{{.Id}}|{{index .Config.Labels %q}}|{{index .Config.Labels %q}}`, labelApp, labelRelease)
	rows, err := p.lines(ctx, append([]string{"image", "inspect", "--format", format}, unique(ids)...)...)
	if err != nil {
		return nil, err
	}
	out := make([]provider.ReleaseImage, 0, len(rows))
	for _, row := range rows {
		f := strings.Split(row, "|")
		if len(f) != 3 || f[1] == "" || f[2] == "" {
			continue
		}
		out = append(out, provider.ReleaseImage{ID: f[0], App: f[1], Release: f[2]})
	}
	return out, nil
}

// ImagesInUse returns the image IDs of every container on the daemon, running or not.
// Counting stopped containers too keeps an image whose container is only briefly down.
func (p *Provider) ImagesInUse(ctx context.Context) (map[string]bool, error) {
	ids, err := p.lines(ctx, "container", "ls", "--all", "--quiet", "--no-trunc")
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	if len(ids) == 0 {
		return out, nil
	}
	images, err := p.lines(ctx, append([]string{"container", "inspect", "--format", "{{.Image}}"}, ids...)...)
	if err != nil {
		return nil, err
	}
	for _, id := range images {
		out[id] = true
	}
	return out, nil
}

// RemoveImage deletes an image without force: Docker itself refuses while a container
// uses it or while it has more than one tag.
func (p *Provider) RemoveImage(ctx context.Context, id string) error {
	if _, err := p.run(ctx, "", "image", "rm", id); err != nil {
		return err
	}
	return nil
}

// DataRoot is the daemon's storage directory, for the agent's disk-pressure check.
func (p *Provider) DataRoot(ctx context.Context) (string, error) {
	out, err := p.run(ctx, "", "info", "--format", "{{.DockerRootDir}}")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func (p *Provider) lines(ctx context.Context, args ...string) ([]string, error) {
	out, err := p.run(ctx, "", args...)
	if err != nil {
		return nil, err
	}
	var lines []string
	for _, l := range strings.Split(string(out), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
	}
	return lines, nil
}

func unique(ss []string) []string {
	seen := map[string]bool{}
	out := ss[:0:0]
	for _, s := range ss {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
