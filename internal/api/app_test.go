package api

import (
	"testing"

	"connectrpc.com/connect"

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
