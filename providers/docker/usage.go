package docker

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/bohumix/bohux/internal/provider"
)

var _ provider.UsageReader = (*Provider)(nil)

// Usage samples every Bohux container with one `docker stats --no-stream` (about two
// seconds, for CPU) and sums replicas per workload (ADR-0040). Memory limits come
// from the containers' configuration: stats shows the host's memory for an unlimited
// container.
func (p *Provider) Usage(ctx context.Context) (map[provider.ResourceRef]provider.Usage, error) {
	ps, err := p.run(ctx, "", "ps", "--filter", "label="+labelApp, "--format",
		`{{.ID}} {{.Label "`+labelApp+`"}} {{.Label "`+labelEnvironment+`"}} {{.Label "`+labelProject+`"}}`)
	if err != nil {
		return nil, err
	}
	refs := map[string]provider.ResourceRef{}
	var ids []string
	sc := bufio.NewScanner(bytes.NewReader(ps))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) != 4 {
			continue
		}
		refs[f[0]] = provider.ResourceRef{App: f[1], Environment: f[2], Slot: slotOf(f[3])}
		ids = append(ids, f[0])
	}
	out := map[provider.ResourceRef]provider.Usage{}
	if len(ids) == 0 {
		return out, nil
	}
	stats, err := p.run(ctx, "", append([]string{"stats", "--no-stream", "--format", "{{json .}}"}, ids...)...)
	if err != nil {
		return nil, err
	}
	limits, err := p.run(ctx, "", append([]string{"inspect", "--format", "{{.Id}} {{.HostConfig.Memory}}"}, ids...)...)
	if err != nil {
		return nil, err
	}
	limit := map[string]int64{}
	sc = bufio.NewScanner(bytes.NewReader(limits))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 2 && len(f[0]) >= 12 {
			n, _ := strconv.ParseInt(f[1], 10, 64)
			limit[f[0][:12]] = n
		}
	}
	sc = bufio.NewScanner(bytes.NewReader(stats))
	for sc.Scan() {
		var s struct {
			ID       string `json:"ID"`
			CPUPerc  string `json:"CPUPerc"`
			MemUsage string `json:"MemUsage"`
		}
		if json.Unmarshal(sc.Bytes(), &s) != nil {
			continue
		}
		ref, ok := refs[s.ID]
		if !ok {
			continue
		}
		u := out[ref]
		u.CPUMillicores += cpuMillicores(s.CPUPerc)
		used, _, _ := strings.Cut(s.MemUsage, "/")
		u.MemoryBytes += byteSize(strings.TrimSpace(used))
		u.MemoryLimitBytes += limit[s.ID]
		out[ref] = u
	}
	return out, sc.Err()
}

// slotOf reads a workload's slot from its Compose project name (ADR-0035).
func slotOf(project string) string {
	for _, s := range []string{"blue", "green"} {
		if strings.HasSuffix(project, "-"+s) {
			return s
		}
	}
	return ""
}

// cpuMillicores turns "12.34%" (of one core) into millicores.
func cpuMillicores(perc string) int64 {
	f, err := strconv.ParseFloat(strings.TrimSuffix(strings.TrimSpace(perc), "%"), 64)
	if err != nil {
		return 0
	}
	return int64(f*10 + 0.5)
}

// byteSize parses Docker's sizes: "524KiB", "1.5GiB", "12MB", "0B".
func byteSize(s string) int64 {
	units := []struct {
		suffix string
		mult   float64
	}{
		{"KiB", 1 << 10}, {"MiB", 1 << 20}, {"GiB", 1 << 30}, {"TiB", 1 << 40},
		{"kB", 1e3}, {"KB", 1e3}, {"MB", 1e6}, {"GB", 1e9}, {"TB", 1e12}, {"B", 1},
	}
	for _, u := range units {
		if n, ok := strings.CutSuffix(s, u.suffix); ok {
			f, err := strconv.ParseFloat(strings.TrimSpace(n), 64)
			if err != nil {
				return 0
			}
			return int64(f * u.mult)
		}
	}
	return 0
}
