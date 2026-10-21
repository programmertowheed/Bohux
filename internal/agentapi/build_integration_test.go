//go:build integration

package agentapi

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	agentv1 "github.com/bohumix/bohux/gen/bohux/agent/v1"
)

func TestBuildOverStream(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	a := h.register("vps-1")
	node := uuid.MustParse(a.node)
	req := &agentv1.BuildRequest{ReleaseId: "rel-1", AppId: "app-1", RepoUrl: "https://example.com/r.git", Ref: "main"}

	if _, err := h.srv.Build(ctx, node, req); !errors.Is(err, ErrNodeOffline) {
		t.Fatalf("Build with no stream = %v, want ErrNodeOffline", err)
	}

	sctx, cancel := context.WithCancel(ctx)
	stream, _, err := open(sctx, a.client, hello(a.node, "0.3.0"))
	if err != nil {
		t.Fatal(err)
	}
	h.waitState(a.node, "online")
	type outcome struct {
		res *agentv1.BuildResult
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		r, err := h.srv.Build(ctx, node, req)
		done <- outcome{r, err}
	}()
	// Play the agent: wait for the request, answer it.
	var got *agentv1.BuildRequest
	for got == nil {
		msg, err := stream.Receive()
		if err != nil {
			t.Fatal(err)
		}
		got = msg.GetBuildRequest()
	}
	if got.GetReleaseId() != "rel-1" || got.GetRepoUrl() != req.GetRepoUrl() {
		t.Errorf("agent got %v", got)
	}
	if err := stream.Send(&agentv1.ConnectRequest{Msg: &agentv1.ConnectRequest_BuildResult{BuildResult: &agentv1.BuildResult{
		ReleaseId: "rel-1", ImageId: "sha256:abc", Commit: "c0ffee"}}}); err != nil {
		t.Fatal(err)
	}
	select {
	case o := <-done:
		if o.err != nil || o.res.GetImageId() != "sha256:abc" {
			t.Errorf("Build = %v, %v", o.res, o.err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Build never returned")
	}

	t.Run("a stream that ends while building is offline", func(t *testing.T) {
		go func() {
			_, err := h.srv.Build(ctx, node, &agentv1.BuildRequest{ReleaseId: "rel-2"})
			done <- outcome{nil, err}
		}()
		for {
			msg, err := stream.Receive()
			if err != nil {
				t.Fatal(err)
			}
			if msg.GetBuildRequest() != nil {
				break
			}
		}
		closeStream(stream, cancel)
		select {
		case o := <-done:
			if !errors.Is(o.err, ErrNodeOffline) {
				t.Errorf("Build = %v, want ErrNodeOffline", o.err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("Build never returned after the stream ended")
		}
	})
}
