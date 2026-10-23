package slo

import "testing"

func counts(req, errs uint64, latency ...uint64) Counts {
	l := make([]uint64, len(LatencyBoundsMS)+1)
	copy(l, latency)
	return Counts{Requests: req, ServerErrors: errs, Latency: l}
}

func TestJudge(t *testing.T) {
	avail := Objective{Availability: 0.99} // 1% budget, burn > 10 means > 10% errors
	lat := Objective{Availability: 0.9, LatencyThresholdMS: 250, LatencyTarget: 0.9}
	slow := func(fast, slower uint64) Counts {
		c := counts(fast+slower, 0)
		c.Latency[Bucket(100)] = fast
		c.Latency[Bucket(1000)] = slower
		return c
	}
	for _, tc := range []struct {
		name string
		o    Objective
		w    Counts
		want string
	}{
		{"no traffic", avail, counts(0, 0), ""},
		{"too few requests to fail", avail, counts(49, 49), ""},
		{"within budget", avail, counts(1000, 50), ""},
		{"burning ten times over", avail, counts(1000, 101), "availability"},
		{"a custom burn rate", Objective{Availability: 0.99, MaxBurnRate: 2}, counts(1000, 21), "availability"},
		{"latency within", lat, slow(950, 50), ""},
		{"latency burning", lat, slow(0, 100), "latency"},
		{"latency ignored without an objective", avail, slow(0, 100), ""},
	} {
		if got := Judge(tc.o, tc.w); got.Violated != tc.want {
			t.Errorf("%s: %v violated %q, want %q", tc.name, got, got.Violated, tc.want)
		}
	}
	if v := Judge(avail, counts(1000, 50)); v.AvailabilityBurn < 4.99 || v.AvailabilityBurn > 5.01 {
		t.Errorf("burn = %v, want 5 (5%% errors on a 1%% budget)", v.AvailabilityBurn)
	}
}

func TestWindow(t *testing.T) {
	start, now := counts(100, 5, 100), counts(250, 8, 250)
	w, reset := Window(start, now)
	if reset || w.Requests != 150 || w.ServerErrors != 3 || w.Latency[0] != 150 {
		t.Errorf("window = %+v reset %v", w, reset)
	}
	if w, reset := Window(now, counts(20, 1, 20)); !reset || w.Requests != 20 {
		t.Errorf("after an agent restart = %+v reset %v; want the new counters", w, reset)
	}
}

func TestBucket(t *testing.T) {
	for ms, want := range map[float64]int{0.4: 0, 5: 0, 5.1: 1, 300: 6, 10000: 10, 10001: 11} {
		if got := Bucket(ms); got != want {
			t.Errorf("Bucket(%v) = %d, want %d", ms, got, want)
		}
	}
}
