//go:build integration

package api

import (
	"context"
	"testing"

	"connectrpc.com/connect"

	bohuxv1 "github.com/bohumix/bohux/gen/bohux/v1"
	"github.com/bohumix/bohux/gen/bohux/v1/bohuxv1connect"
)

// TestAuditEvents: every change is in the trail, newest first, attributed to its
// principal; a project's viewer reads that project's events only, and the whole
// tenant's trail needs a tenant-wide token (rule 10).
func TestAuditEvents(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	srv := h.server()
	audit := bohuxv1connect.NewAuditServiceClient(srv.Client(), srv.URL)
	envs := bohuxv1connect.NewEnvironmentServiceClient(srv.Client(), srv.URL)
	one, two := h.project("one"), h.project("two")
	node := h.node("vps-1")
	for i, slug := range []string{"prod", "staging", "dev"} {
		if _, err := envs.CreateEnvironment(ctx, req(h.root, &bohuxv1.CreateEnvironmentRequest{ProjectId: one.String(),
			Slug: slug, ProviderBinding: dockerOn(node), IdempotencyKey: "e" + string(rune('0'+i))})); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := envs.CreateEnvironment(ctx, req(h.root, &bohuxv1.CreateEnvironmentRequest{ProjectId: two.String(),
		Slug: "prod", ProviderBinding: dockerOn(node), IdempotencyKey: "e-two"})); err != nil {
		t.Fatal(err)
	}
	viewer, err := h.createToken(h.root, one, bohuxv1.Role_ROLE_VIEWER, "v")
	if err != nil {
		t.Fatal(err)
	}
	list := func(token string, r *bohuxv1.ListAuditEventsRequest) (*bohuxv1.ListAuditEventsResponse, error) {
		resp, err := audit.ListAuditEvents(ctx, req(token, r))
		if err != nil {
			return nil, err
		}
		return resp.Msg, nil
	}

	all, err := list(h.root, &bohuxv1.ListAuditEventsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(all.GetAuditEvents()) < 5 {
		t.Fatalf("tenant trail has %d events", len(all.GetAuditEvents()))
	}
	newest := all.GetAuditEvents()[0]
	if newest.GetAction() != "bohux.v1.IdentityService/CreateApiToken" ||
		newest.GetActor().GetKind() != bohuxv1.PrincipalKind_PRINCIPAL_KIND_API_TOKEN {
		t.Errorf("newest event = %v", newest)
	}

	mine, err := list(viewer.GetSecret(), &bohuxv1.ListAuditEventsRequest{ProjectId: one.String()})
	if err != nil {
		t.Fatal(err)
	}
	envEvents := 0
	for _, e := range mine.GetAuditEvents() {
		if e.GetProjectId() != one.String() {
			t.Errorf("project one's trail holds %v", e)
		}
		if e.GetAction() == "bohux.v1.EnvironmentService/CreateEnvironment" {
			envEvents++
		}
	}
	if envEvents != 3 {
		t.Errorf("project one's trail has %d environment creations, want 3", envEvents)
	}

	_, err = list(viewer.GetSecret(), &bohuxv1.ListAuditEventsRequest{})
	wantCode(t, err, connect.CodePermissionDenied)
	_, err = list(viewer.GetSecret(), &bohuxv1.ListAuditEventsRequest{ProjectId: two.String()})
	wantCode(t, err, connect.CodeNotFound)

	first, err := list(h.root, &bohuxv1.ListAuditEventsRequest{ProjectId: one.String(), PageSize: 2})
	if err != nil || len(first.GetAuditEvents()) != 2 || first.GetNextPageToken() == "" {
		t.Fatalf("first page = %v, %v", first, err)
	}
	second, err := list(h.root, &bohuxv1.ListAuditEventsRequest{ProjectId: one.String(), PageSize: 2, PageToken: first.GetNextPageToken()})
	if err != nil || len(second.GetAuditEvents()) == 0 || second.GetAuditEvents()[0].GetId() >= first.GetAuditEvents()[1].GetId() {
		t.Errorf("second page = %v, %v; want older events", second, err)
	}

	env := mine.GetAuditEvents()[0].GetResource()
	byRes, err := list(h.root, &bohuxv1.ListAuditEventsRequest{Resource: env})
	if err != nil || len(byRes.GetAuditEvents()) == 0 {
		t.Fatalf("by resource = %v, %v", byRes, err)
	}
	for _, e := range byRes.GetAuditEvents() {
		if e.GetResource().GetId() != env.GetId() {
			t.Errorf("by resource holds %v", e)
		}
	}
}
