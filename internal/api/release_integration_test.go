//go:build integration

package api

import (
	"context"
	"strings"
	"testing"

	"connectrpc.com/connect"

	bohuxv1 "github.com/bohumix/bohux/gen/bohux/v1"
	"github.com/bohumix/bohux/gen/bohux/v1/bohuxv1connect"
)

func TestReleases(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	srv := h.server()
	apps := bohuxv1connect.NewAppServiceClient(srv.Client(), srv.URL)
	envs := bohuxv1connect.NewEnvironmentServiceClient(srv.Client(), srv.URL)
	releases := bohuxv1connect.NewReleaseServiceClient(srv.Client(), srv.URL)
	p1, p2 := h.project("one"), h.project("two")
	tok := func(project string, role bohuxv1.Role, key string) string {
		r, err := h.createToken(h.root, mustUUID(t, project), role, key)
		if err != nil {
			t.Fatal(err)
		}
		return r.GetSecret()
	}
	owner := tok(p1.String(), bohuxv1.Role_ROLE_OWNER, "o1")
	deployer := tok(p1.String(), bohuxv1.Role_ROLE_DEPLOYER, "d1")
	viewer := tok(p1.String(), bohuxv1.Role_ROLE_VIEWER, "v1")
	outsider := tok(p2.String(), bohuxv1.Role_ROLE_DEPLOYER, "d2")

	envResp, err := envs.CreateEnvironment(ctx, req(owner, &bohuxv1.CreateEnvironmentRequest{
		ProjectId: p1.String(), Slug: "prod", ProviderBinding: dockerOn(h.node("vps-1")), IdempotencyKey: "e1"}))
	if err != nil {
		t.Fatal(err)
	}
	env := envResp.Msg.GetEnvironment().GetMeta().GetId()
	image := func(repo, hex string) *bohuxv1.ImageSource {
		return &bohuxv1.ImageSource{Repository: repo, Digest: "sha256:" + strings.Repeat(hex, 32)}
	}
	newApp := func(slug string, src *bohuxv1.Source) *bohuxv1.App {
		resp, err := apps.CreateApp(ctx, req(deployer, &bohuxv1.CreateAppRequest{
			EnvironmentId: env, Slug: slug, Source: src,
			Runtime: &bohuxv1.Runtime{Replicas: 1}, IdempotencyKey: "app-" + slug}))
		if err != nil {
			t.Fatal(err)
		}
		return resp.Msg.GetApp()
	}
	api := newApp("api", &bohuxv1.Source{Kind: &bohuxv1.Source_Image{Image: image("ghcr.io/acme/api", "ab")}})
	web := newApp("web", &bohuxv1.Source{Kind: &bohuxv1.Source_Git{Git: &bohuxv1.GitSource{RepoUrl: "https://example.com/web.git"}}})
	create := func(token string, r *bohuxv1.CreateReleaseRequest) (*bohuxv1.Release, error) {
		resp, err := releases.CreateRelease(ctx, req(token, r))
		if err != nil {
			return nil, err
		}
		return resp.Msg.GetRelease(), nil
	}

	first, err := create(deployer, &bohuxv1.CreateReleaseRequest{AppId: api.GetMeta().GetId(), IdempotencyKey: "r1"})
	if err != nil {
		t.Fatal(err)
	}
	t.Run("from the app's image source", func(t *testing.T) {
		if first.GetState() != bohuxv1.ReleaseState_RELEASE_STATE_READY || !first.GetArtifactAvailable() ||
			first.GetSourceRef() != "ghcr.io/acme/api@sha256:"+strings.Repeat("ab", 32) ||
			first.GetArtifactDigest() != "sha256:"+strings.Repeat("ab", 32) ||
			first.GetConfigRevisionId() != api.GetMeta().GetRevisionId() {
			t.Errorf("release = %v", first)
		}
	})
	t.Run("replay returns the same release", func(t *testing.T) {
		again, err := create(deployer, &bohuxv1.CreateReleaseRequest{AppId: api.GetMeta().GetId(), IdempotencyKey: "r1"})
		if err != nil || again.GetMeta().GetId() != first.GetMeta().GetId() {
			t.Errorf("replay = %v, %v", again, err)
		}
	})
	second, err := create(deployer, &bohuxv1.CreateReleaseRequest{AppId: api.GetMeta().GetId(),
		From: &bohuxv1.CreateReleaseRequest_Image{Image: image("ghcr.io/acme/api-next", "cd")}, IdempotencyKey: "r2"})
	if err != nil {
		t.Fatal(err)
	}
	t.Run("an explicit image wins", func(t *testing.T) {
		if second.GetSourceRef() != "ghcr.io/acme/api-next@sha256:"+strings.Repeat("cd", 32) {
			t.Errorf("source_ref = %s", second.GetSourceRef())
		}
	})

	t.Run("refusals", func(t *testing.T) {
		tests := []struct {
			name  string
			token string
			r     *bohuxv1.CreateReleaseRequest
			want  connect.Code
		}{
			{"bad digest", deployer, &bohuxv1.CreateReleaseRequest{AppId: api.GetMeta().GetId(),
				From:           &bohuxv1.CreateReleaseRequest_Image{Image: &bohuxv1.ImageSource{Repository: "x", Digest: "latest"}},
				IdempotencyKey: "x1"}, connect.CodeInvalidArgument},
			{"git ref on an image app", deployer, &bohuxv1.CreateReleaseRequest{AppId: api.GetMeta().GetId(),
				From: &bohuxv1.CreateReleaseRequest_GitRef{GitRef: "main"}, IdempotencyKey: "x2"}, connect.CodeInvalidArgument},
			{"a build without Temporal", deployer, &bohuxv1.CreateReleaseRequest{AppId: web.GetMeta().GetId(),
				IdempotencyKey: "x3"}, connect.CodeUnavailable},
			{"viewer", viewer, &bohuxv1.CreateReleaseRequest{AppId: api.GetMeta().GetId(), IdempotencyKey: "x4"},
				connect.CodePermissionDenied},
			{"other project", outsider, &bohuxv1.CreateReleaseRequest{AppId: api.GetMeta().GetId(), IdempotencyKey: "x5"},
				connect.CodeNotFound},
			{"no idempotency key", deployer, &bohuxv1.CreateReleaseRequest{AppId: api.GetMeta().GetId()},
				connect.CodeInvalidArgument},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				_, err := create(tt.token, tt.r)
				wantCode(t, err, tt.want)
			})
		}
	})

	t.Run("get and list", func(t *testing.T) {
		got, err := releases.GetRelease(ctx, req(viewer, &bohuxv1.GetReleaseRequest{Id: first.GetMeta().GetId()}))
		if err != nil || got.Msg.GetRelease().GetMeta().GetId() != first.GetMeta().GetId() {
			t.Errorf("GetRelease = %v, %v", got, err)
		}
		_, err = releases.GetRelease(ctx, req(outsider, &bohuxv1.GetReleaseRequest{Id: first.GetMeta().GetId()}))
		wantCode(t, err, connect.CodeNotFound)

		page1, err := releases.ListReleases(ctx, req(viewer, &bohuxv1.ListReleasesRequest{AppId: api.GetMeta().GetId(), PageSize: 1}))
		if err != nil {
			t.Fatal(err)
		}
		if ids := page1.Msg.GetReleases(); len(ids) != 1 || ids[0].GetMeta().GetId() != second.GetMeta().GetId() {
			t.Errorf("page 1 = %v, want the newest release", ids)
		}
		page2, err := releases.ListReleases(ctx, req(viewer, &bohuxv1.ListReleasesRequest{AppId: api.GetMeta().GetId(),
			PageSize: 1, PageToken: page1.Msg.GetNextPageToken()}))
		if err != nil {
			t.Fatal(err)
		}
		if ids := page2.Msg.GetReleases(); len(ids) != 1 || ids[0].GetMeta().GetId() != first.GetMeta().GetId() ||
			page2.Msg.GetNextPageToken() != "" {
			t.Errorf("page 2 = %v (next %q), want only the first release", ids, page2.Msg.GetNextPageToken())
		}
	})
}
