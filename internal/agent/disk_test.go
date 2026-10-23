package agent

import (
	"context"
	"errors"
	"testing"
)

func TestDiskMonitor(t *testing.T) {
	type fs struct {
		free, total uint64
		err         error
	}
	tests := []struct {
		name       string
		disks      map[string]fs
		wantReason string // empty: not degraded
	}{
		{name: "plenty free", disks: map[string]fs{"/a": {free: 50, total: 100}, "/b": {free: 11, total: 100}}},
		{name: "exactly at the threshold is fine", disks: map[string]fs{"/a": {free: 10, total: 100}}},
		{name: "one filesystem low", disks: map[string]fs{"/a": {free: 50, total: 100}, "/b": {free: 9, total: 100}},
			wantReason: reasonDiskPressure},
		{name: "empty filesystem size", disks: map[string]fs{"/a": {free: 0, total: 0}}, wantReason: reasonDiskPressure},
		{name: "check fails", disks: map[string]fs{"/a": {err: errors.New("no such path")}},
			wantReason: reasonDiskCheckFailing},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := &DiskMonitor{Statfs: func(p string) (uint64, uint64, error) {
				d := tt.disks[p]
				return d.free, d.total, d.err
			}}
			for p := range tt.disks {
				m.Paths = append(m.Paths, p)
			}
			m.Check(context.Background())
			degraded, detail := m.Degraded()
			if degraded != (tt.wantReason != "") || detail.GetReason() != tt.wantReason {
				t.Fatalf("Degraded() = %v, %q; want reason %q", degraded, detail.GetReason(), tt.wantReason)
			}
		})
	}
}

func TestDiskMonitorSignalsOnlyFlips(t *testing.T) {
	free := uint64(50)
	m := &DiskMonitor{Paths: []string{"/a"}, Statfs: func(string) (uint64, uint64, error) { return free, 100, nil }}
	ctx := context.Background()
	changed := m.Changed()
	m.Check(ctx)
	select {
	case <-changed:
		t.Fatal("signaled without a change")
	default:
	}
	free = 5
	m.Check(ctx)
	m.Check(ctx)
	select {
	case <-changed:
	default:
		t.Fatal("no signal when the node became degraded")
	}
	select {
	case <-changed:
		t.Fatal("signaled twice for one flip")
	default:
	}
	free = 50
	m.Check(ctx)
	if d, _ := m.Degraded(); d {
		t.Fatal("still degraded after space was freed")
	}
	select {
	case <-changed:
	default:
		t.Fatal("no signal when pressure cleared")
	}
}

func TestReportCarriesDiskState(t *testing.T) {
	m := &DiskMonitor{Paths: []string{"/a"}, Statfs: func(string) (uint64, uint64, error) { return 1, 100, nil }}
	m.Check(context.Background())
	o := RunOptions{Cache: &Cache{}, Disk: m}
	rep := o.report()
	if !rep.GetDegraded() || rep.GetDegradedDetail().GetReason() != reasonDiskPressure {
		t.Fatalf("report = %v, want degraded with %s", rep, reasonDiskPressure)
	}
}
