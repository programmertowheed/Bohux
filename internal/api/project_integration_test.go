//go:build integration

package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"

	bohuxv1 "github.com/bohumix/bohux/gen/bohux/v1"
	"github.com/bohumix/bohux/gen/bohux/v1/bohuxv1connect"
	"github.com/bohumix/bohux/internal/auth"
)

// server starts another API server on the harness store.
func (h *harness) server() *httptest.Server {
	h.t.Helper()
	mux := http.NewServeMux()
	(&Server{Store: h.st, Authn: &auth.Authenticator{Store: h.st}}).Mount(mux)
	srv := httptest.NewServer(mux)
	h.t.Cleanup(srv.Close)
	return srv
}

func TestProjects(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	srv := h.server()
	projects := bohuxv1connect.NewProjectServiceClient(srv.Client(), srv.URL)

	create := func(token, slug, key string) (*bohuxv1.Project, error) {
		resp, err := projects.CreateProject(ctx, req(token, &bohuxv1.CreateProjectRequest{
			Slug: slug, DisplayName: slug, IdempotencyKey: key}))
		if err != nil {
			return nil, err
		}
		return resp.Msg.GetProject(), nil
	}
	web, err := create(h.root, "web", "c1")
	if err != nil {
		t.Fatal(err)
	}
	api, err := create(h.root, "api", "c2")
	if err != nil {
		t.Fatal(err)
	}
	webOwner, err := h.createToken(h.root, mustUUID(t, web.GetMeta().GetId()), bohuxv1.Role_ROLE_OWNER, "web-owner")
	if err != nil {
		t.Fatal(err)
	}
	webViewer, err := h.createToken(h.root, mustUUID(t, web.GetMeta().GetId()), bohuxv1.Role_ROLE_VIEWER, "web-viewer")
	if err != nil {
		t.Fatal(err)
	}
	wo, wv := webOwner.GetSecret(), webViewer.GetSecret()

	t.Run("create validates and is idempotent", func(t *testing.T) {
		_, err := create(h.root, "Not A Slug", "c3")
		wantCode(t, err, connect.CodeInvalidArgument)
		again, err := create(h.root, "web", "c1")
		if err != nil {
			t.Fatal(err)
		}
		if again.GetMeta().GetId() != web.GetMeta().GetId() {
			t.Error("idempotent retry created a second project")
		}
		_, err = create(h.root, "web", "c4")
		wantCode(t, err, connect.CodeAlreadyExists)
		_, err = create(wo, "mine", "c5")
		wantCode(t, err, connect.CodePermissionDenied)
	})

	t.Run("get by id and slug, hidden across projects", func(t *testing.T) {
		byID, err := projects.GetProject(ctx, req(wv, &bohuxv1.GetProjectRequest{
			Lookup: &bohuxv1.GetProjectRequest_Id{Id: web.GetMeta().GetId()}}))
		if err != nil {
			t.Fatal(err)
		}
		bySlug, err := projects.GetProject(ctx, req(wv, &bohuxv1.GetProjectRequest{
			Lookup: &bohuxv1.GetProjectRequest_Slug{Slug: "web"}}))
		if err != nil {
			t.Fatal(err)
		}
		if byID.Msg.GetProject().GetMeta().GetId() != bySlug.Msg.GetProject().GetMeta().GetId() {
			t.Error("id and slug lookups disagree")
		}
		_, err = projects.GetProject(ctx, req(wv, &bohuxv1.GetProjectRequest{
			Lookup: &bohuxv1.GetProjectRequest_Slug{Slug: "api"}}))
		wantCode(t, err, connect.CodeNotFound)
	})

	t.Run("list is scoped", func(t *testing.T) {
		all, err := projects.ListProjects(ctx, req(h.root, &bohuxv1.ListProjectsRequest{}))
		if err != nil {
			t.Fatal(err)
		}
		mine, err := projects.ListProjects(ctx, req(wv, &bohuxv1.ListProjectsRequest{}))
		if err != nil {
			t.Fatal(err)
		}
		if len(all.Msg.GetProjects()) < 2 {
			t.Errorf("tenant owner sees %d projects", len(all.Msg.GetProjects()))
		}
		if len(mine.Msg.GetProjects()) != 1 || mine.Msg.GetProjects()[0].GetSlug() != "web" {
			t.Errorf("project token sees %v", mine.Msg.GetProjects())
		}
	})

	t.Run("update needs owner and the current revision", func(t *testing.T) {
		upd := func(token, rev, key string) (*bohuxv1.Project, error) {
			resp, err := projects.UpdateProject(ctx, req(token, &bohuxv1.UpdateProjectRequest{
				Id: web.GetMeta().GetId(), DisplayName: "Web!", ExpectedRevisionId: rev, IdempotencyKey: key}))
			if err != nil {
				return nil, err
			}
			return resp.Msg.GetProject(), nil
		}
		_, err := upd(wv, web.GetMeta().GetRevisionId(), "u1")
		wantCode(t, err, connect.CodePermissionDenied)
		_, err = upd(wo, "", "u2")
		wantCode(t, err, connect.CodeInvalidArgument)
		got, err := upd(wo, web.GetMeta().GetRevisionId(), "u3")
		if err != nil {
			t.Fatal(err)
		}
		if got.GetDisplayName() != "Web!" || got.GetMeta().GetRevisionId() == web.GetMeta().GetRevisionId() {
			t.Errorf("update result %v", got)
		}
		_, err = upd(wo, web.GetMeta().GetRevisionId(), "u4")
		wantCode(t, err, connect.CodeAborted)
		_, err = projects.UpdateProject(ctx, req(wo, &bohuxv1.UpdateProjectRequest{
			Id: api.GetMeta().GetId(), ExpectedRevisionId: api.GetMeta().GetRevisionId(), IdempotencyKey: "u5"}))
		wantCode(t, err, connect.CodeNotFound)
	})

	t.Run("delete is tenant-wide and frees the slug", func(t *testing.T) {
		_, err := projects.DeleteProject(ctx, req(wo, &bohuxv1.DeleteProjectRequest{
			Id: api.GetMeta().GetId(), ExpectedRevisionId: api.GetMeta().GetRevisionId(), IdempotencyKey: "d1"}))
		wantCode(t, err, connect.CodePermissionDenied)
		del, err := projects.DeleteProject(ctx, req(h.root, &bohuxv1.DeleteProjectRequest{
			Id: api.GetMeta().GetId(), ExpectedRevisionId: api.GetMeta().GetRevisionId(), IdempotencyKey: "d2"}))
		if err != nil {
			t.Fatal(err)
		}
		if del.Msg.GetRevisionId() == "" {
			t.Error("delete returned no revision")
		}
		if _, err := create(h.root, "api", "c6"); err != nil {
			t.Errorf("slug not freed: %v", err)
		}
	})

	t.Run("role bindings say why they are unavailable", func(t *testing.T) {
		_, err := projects.ListRoleBindings(ctx, req(h.root, &bohuxv1.ListRoleBindingsRequest{
			ProjectId: web.GetMeta().GetId()}))
		wantCode(t, err, connect.CodeUnimplemented)
	})
}
