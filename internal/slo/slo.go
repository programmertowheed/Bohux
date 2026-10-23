// Package slo judges a Release against its App's SLO from the ingress's counters of
// its requests (ADR-0050). It stores nothing: callers hand it two snapshots of
// cumulative counters and get a verdict on the traffic between them.
package slo

import "fmt"

// LatencyBoundsMS are the upper bounds of the ingress's latency histogram, in
// milliseconds. Counts has one bucket per bound plus one for everything above the last.
var LatencyBoundsMS = []uint32{5, 10, 25, 50, 100, 250, 500, 1000, 2500, 5000, 10000}

// DefaultMaxBurnRate applies when an SLO sets none.
const DefaultMaxBurnRate = 10.0

// MinRequests is how many requests a window needs before it can fail a Release; with
// fewer there is no verdict (ADR-0050 item 4).
const MinRequests = 50

// Counts are cumulative counters of one slot's requests.
type Counts struct {
	Requests     uint64
	ServerErrors uint64
	// Latency has len(LatencyBoundsMS)+1 buckets, not cumulative.
	Latency []uint64
}

// Objective is an App's SLO in the units the judge works in.
type Objective struct {
	// Availability is the share of requests that must not be 5xx, e.g. 0.999.
	Availability float64
	// LatencyThresholdMS and LatencyTarget are zero when the SLO has no latency part.
	LatencyThresholdMS uint32
	LatencyTarget      float64
	// MaxBurnRate zero means DefaultMaxBurnRate.
	MaxBurnRate float64
}

// Verdict is the result of judging one window.
type Verdict struct {
	// Requests in the window.
	Requests uint64
	// Burn rates, the share of bad requests over the share the target allows.
	AvailabilityBurn float64
	LatencyBurn      float64
	// Violated names the objective broken: "", "availability", or "latency".
	Violated string
}

func (v Verdict) String() string {
	return fmt.Sprintf("%d requests, availability burn %.2f, latency burn %.2f", v.Requests, v.AvailabilityBurn, v.LatencyBurn)
}

// Window is the traffic between a start snapshot and now. A counter lower than at the
// start means the agent restarted and counted from zero; the window then restarts at
// now's values, which is what Reset reports.
func Window(start, now Counts) (w Counts, reset bool) {
	if now.Requests < start.Requests || now.ServerErrors < start.ServerErrors || len(now.Latency) != len(start.Latency) {
		return now, true
	}
	w = Counts{Requests: now.Requests - start.Requests, ServerErrors: now.ServerErrors - start.ServerErrors,
		Latency: make([]uint64, len(now.Latency))}
	for i := range now.Latency {
		if now.Latency[i] < start.Latency[i] {
			return now, true
		}
		w.Latency[i] = now.Latency[i] - start.Latency[i]
	}
	return w, false
}

// Judge decides whether the window's traffic violates o. Below MinRequests nothing is
// violated.
func Judge(o Objective, w Counts) Verdict {
	v := Verdict{Requests: w.Requests}
	if w.Requests == 0 {
		return v
	}
	max := o.MaxBurnRate
	if max <= 0 {
		max = DefaultMaxBurnRate
	}
	total := float64(w.Requests)
	if budget := 1 - o.Availability; budget > 0 {
		v.AvailabilityBurn = float64(w.ServerErrors) / total / budget
	}
	if o.LatencyThresholdMS > 0 && o.LatencyTarget > 0 && len(w.Latency) == len(LatencyBoundsMS)+1 {
		var slow uint64
		for i, n := range w.Latency {
			if i == len(LatencyBoundsMS) || LatencyBoundsMS[i] > o.LatencyThresholdMS {
				slow += n
			}
		}
		if budget := 1 - o.LatencyTarget; budget > 0 {
			v.LatencyBurn = float64(slow) / total / budget
		}
	}
	if w.Requests < MinRequests {
		return v
	}
	switch {
	case v.AvailabilityBurn > max:
		v.Violated = "availability"
	case v.LatencyBurn > max:
		v.Violated = "latency"
	}
	return v
}

// Bucket returns the latency bucket for a duration in milliseconds.
func Bucket(ms float64) int {
	for i, b := range LatencyBoundsMS {
		if ms <= float64(b) {
			return i
		}
	}
	return len(LatencyBoundsMS)
}
