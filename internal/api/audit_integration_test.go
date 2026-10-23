//go:build integration

package api

import (
	"context"
	"errors"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	bohuxv1 "github.com/bohumix/bohux/gen/bohux/v1"
	"github.com/bohumix/bohux/gen/bohux/v1/bohuxv1connect"
	"github.com/bohumix/bohux/internal/store"
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

// TestRevisions: every revision of a resource reads back as the full resource it was,
// deleted ones included, newest first, for whoever may see its project (ADR-0017).
func TestRevisions(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	srv := h.server()
	audit := bohuxv1connect.NewAuditServiceClient(srv.Client(), srv.URL)
	projects := bohuxv1connect.NewProjectServiceClient(srv.Client(), srv.URL)
	created, err := projects.CreateProject(ctx, req(h.root, &bohuxv1.CreateProjectRequest{Slug: "shop", DisplayName: "Shop v1", IdempotencyKey: "c"}))
	if err != nil {
		t.Fatal(err)
	}
	p := created.Msg.GetProject()
	updated, err := projects.UpdateProject(ctx, req(h.root, &bohuxv1.UpdateProjectRequest{Id: p.GetMeta().GetId(),
		DisplayName: "Shop v2", ExpectedRevisionId: p.GetMeta().GetRevisionId(), IdempotencyKey: "u"}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := projects.DeleteProject(ctx, req(h.root, &bohuxv1.DeleteProjectRequest{Id: p.GetMeta().GetId(),
		ExpectedRevisionId: updated.Msg.GetProject().GetMeta().GetRevisionId(), IdempotencyKey: "d"})); err != nil {
		t.Fatal(err)
	}
	ref := &bohuxv1.ResourceRef{Type: "bohux.v1.Project", Id: p.GetMeta().GetId()}
	list, err := audit.ListRevisions(ctx, req(h.root, &bohuxv1.ListRevisionsRequest{Resource: ref}))
	if err != nil {
		t.Fatal(err)
	}
	revs := list.Msg.GetRevisions()
	if len(revs) != 3 || !revs[0].GetDeleted() || revs[2].GetParentRevisionId() != "" ||
		revs[1].GetParentRevisionId() != revs[2].GetId() {
		t.Fatalf("revisions = %v", revs)
	}
	names := make([]string, 0, 3)
	for _, r := range revs {
		var snap bohuxv1.Project
		if err := r.GetSnapshot().UnmarshalTo(&snap); err != nil {
			t.Fatal(err)
		}
		if snap.GetMeta().GetRevisionId() != r.GetId() {
			t.Errorf("snapshot of %s says revision %s", r.GetId(), snap.GetMeta().GetRevisionId())
		}
		names = append(names, snap.GetDisplayName())
	}
	if names[1] != "Shop v2" || names[2] != "Shop v1" {
		t.Errorf("display names newest first = %v", names)
	}
	one, err := audit.GetRevision(ctx, req(h.root, &bohuxv1.GetRevisionRequest{Id: revs[2].GetId()}))
	if err != nil || one.Msg.GetRevision().GetResource().GetType() != "bohux.v1.Project" {
		t.Fatalf("GetRevision = %v, %v", one, err)
	}

	other := h.project("other")
	viewer, err := h.createToken(h.root, other, bohuxv1.Role_ROLE_VIEWER, "v")
	if err != nil {
		t.Fatal(err)
	}
	_, err = audit.ListRevisions(ctx, req(viewer.GetSecret(), &bohuxv1.ListRevisionsRequest{Resource: ref}))
	wantCode(t, err, connect.CodeNotFound)
	_, err = audit.ListRevisions(ctx, req(h.root, &bohuxv1.ListRevisionsRequest{
		Resource: &bohuxv1.ResourceRef{Type: "bohux.v1.Nope", Id: p.GetMeta().GetId()}}))
	wantCode(t, err, connect.CodeInvalidArgument)
}

// TestHistoryQueries runs every type's historical select: each must be valid SQL that
// finds no row for an unknown revision, so a change to a select cannot break history.
func TestHistoryQueries(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	for typ, hist := range histories {
		err := h.st.InTenant(ctx, h.tenant, func(tx *store.Tx) error {
			_, err := hist.scan(tx.QueryRow(ctx, hist.at(), uuid.Must(uuid.NewV7())))
			return err
		})
		if !errors.Is(err, pgx.ErrNoRows) {
			t.Errorf("%s: %v, want no rows", typ, err)
		}
	}
}
