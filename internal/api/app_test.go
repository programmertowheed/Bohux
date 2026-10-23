package api

import (
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/durationpb"

	bohuxv1 "github.com/bohumix/bohux/gen/bohux/v1"
)

func TestValidSourceStrategy(t *testing.T) {
	git := func(s bohuxv1.BuildStrategy) *bohuxv1.Source {
		return &bohuxv1.Source{Kind: &bohuxv1.Source_Git{Git: &bohuxv1.GitSource{
			RepoUrl: "https://example.com/r.git", Strategy: s}}}
	}
	for _, tc := range []struct {
		strategy bohuxv1.BuildStrategy
		ok       bool
	}{
		{bohuxv1.BuildStrategy_BUILD_STRATEGY_UNSPECIFIED, true},
		{bohuxv1.BuildStrategy_BUILD_STRATEGY_DOCKERFILE, true},
		{bohuxv1.BuildStrategy_BUILD_STRATEGY_RAILPACK, true},
		{bohuxv1.BuildStrategy(9), false},
	} {
		err := validSource(git(tc.strategy))
		if tc.ok && err != nil {
			t.Errorf("%v: %v", tc.strategy, err)
		}
		if !tc.ok && connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Errorf("%v: %v, want InvalidArgument", tc.strategy, err)
		}
	}
}

func TestValidSLO(t *testing.T) {
	ok := func(f func(*bohuxv1.SLO)) *bohuxv1.SLO {
		s := &bohuxv1.SLO{AvailabilityTarget: 0.999,
			Latency: &bohuxv1.LatencyObjective{ThresholdMs: 250, Target: 0.95}, MaxBurnRate: 10,
			Watch: durationpb.New(10 * time.Minute)}
		if f != nil {
			f(s)
		}
		return s
	}
	for _, tc := range []struct {
		name string
		slo  *bohuxv1.SLO
		ok   bool
	}{
		{"none", nil, true},
		{"complete", ok(nil), true},
		{"availability only", &bohuxv1.SLO{AvailabilityTarget: 0.9}, true},
		{"availability too low", ok(func(s *bohuxv1.SLO) { s.AvailabilityTarget = 0.5 }), false},
		{"availability of one", ok(func(s *bohuxv1.SLO) { s.AvailabilityTarget = 1 }), false},
		{"no availability", ok(func(s *bohuxv1.SLO) { s.AvailabilityTarget = 0 }), false},
		{"threshold off the buckets", ok(func(s *bohuxv1.SLO) { s.Latency.ThresholdMs = 300 }), false},
		{"latency target too low", ok(func(s *bohuxv1.SLO) { s.Latency.Target = 0.2 }), false},
		{"default burn", ok(func(s *bohuxv1.SLO) { s.MaxBurnRate = 0 }), true},
		{"burn below one", ok(func(s *bohuxv1.SLO) { s.MaxBurnRate = 0.5 }), false},
		{"burn too high", ok(func(s *bohuxv1.SLO) { s.MaxBurnRate = 5000 }), false},
		{"default watch", ok(func(s *bohuxv1.SLO) { s.Watch = nil }), true},
		{"watch too short", ok(func(s *bohuxv1.SLO) { s.Watch = durationpb.New(30 * time.Second) }), false},
		{"watch too long", ok(func(s *bohuxv1.SLO) { s.Watch = durationpb.New(2 * time.Hour) }), false},
		{"watch not whole seconds", ok(func(s *bohuxv1.SLO) { s.Watch = durationpb.New(90*time.Second + 1) }), false},
	} {
		err := validSLO(tc.slo)
		if tc.ok && err != nil {
			t.Errorf("%s: %v", tc.name, err)
		}
		if !tc.ok && connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Errorf("%s: %v, want InvalidArgument", tc.name, err)
		}
	}
}
