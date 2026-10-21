package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	bohuxv1 "github.com/bohumix/bohux/gen/bohux/v1"
	"github.com/bohumix/bohux/internal/pki"
	"github.com/bohumix/bohux/internal/provider"
)

func TestSamplerNode(t *testing.T) {
	proc := t.TempDir()
	write := func(name, s string) {
		if err := os.WriteFile(filepath.Join(proc, name), []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("meminfo", "MemTotal:       2048000 kB\nMemFree:  100 kB\nMemAvailable:   1024000 kB\n")
	write("stat", "cpu  100 0 100 700 100 0 0 0 0 0\ncpu0 1 2 3\n")
	s := &Sampler{ProcRoot: proc, Paths: []string{t.TempDir()}, Provider: newFake()}
	s.Sample(context.Background())
	// 100 busy jiffies more out of 200: half busy.
	write("stat", "cpu  150 0 150 750 150 0 0 0 0 0\n")
	s.Sample(context.Background())
	node, workloads, _ := s.Latest()
	if node == nil || node.GetMemoryTotalBytes() != 2048000<<10 || node.GetMemoryAvailableBytes() != 1024000<<10 {
		t.Fatalf("node = %v", node)
	}
	if b := node.GetCpuBusy(); b < 0.49 || b > 0.51 {
		t.Errorf("cpu busy = %v, want 0.5", b)
	}
	if len(node.GetDisks()) != 1 || node.GetDisks()[0].GetTotalBytes() == 0 {
		t.Errorf("disks = %v", node.GetDisks())
	}
	if workloads != nil {
		t.Errorf("a provider without UsageReader reported %v", workloads)
	}
}

type fixedUsage struct {
	node      *bohuxv1.NodeUsage
	workloads map[provider.ResourceRef]provider.Usage
}

func (f fixedUsage) Latest() (*bohuxv1.NodeUsage, map[provider.ResourceRef]provider.Usage, time.Time) {
	return f.node, f.workloads, time.Now()
}

// TestReportCarriesUsage: the report holds the node's sample and each workload's,
// summed over its slots (ADR-0040, ADR-0035).
func TestReportCarriesUsage(t *testing.T) {
	ctx := context.Background()
	signer, err := pki.LoadOrCreate(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	st := testState(t, signer)
	c, err := OpenCache(st)
	if err != nil {
		t.Fatal(err)
	}
	usage := fixedUsage{node: &bohuxv1.NodeUsage{CpuCores: 4},
		workloads: map[provider.ResourceRef]provider.Usage{
			{Environment: "env", App: "api", Slot: "blue"}:  {CPUMillicores: 100, MemoryBytes: 10},
			{Environment: "env", App: "api", Slot: "green"}: {CPUMillicores: 50, MemoryBytes: 5},
			{Environment: "env", App: "other"}:              {CPUMillicores: 999},
		}}
	r := &Reconciler{State: st, Cache: c, Provider: newFake(), Usage: usage}
	cacheWorkloads(t, signer, st, c, 1, deployed("r1", "d1"))
	rep := r.Once(ctx)
	if rep.GetUsage().GetCpuCores() != 4 {
		t.Errorf("node usage = %v", rep.GetUsage())
	}
	if u := only(t, rep).GetUsage(); u.GetCpuMillicores() != 150 || u.GetMemoryBytes() != 15 {
		t.Errorf("workload usage = %v, want both slots summed", u)
	}
}
