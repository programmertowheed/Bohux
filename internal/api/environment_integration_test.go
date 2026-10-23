//go:build integration

package api

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	bohuxv1 "github.com/bohumix/bohux/gen/bohux/v1"
	"github.com/bohumix/bohux/gen/bohux/v1/bohuxv1connect"
	"github.com/bohumix/bohux/internal/store"
)

// node registers a node directly in the store (agent registration arrives in S4).
func (h *harness) node(slug string) uuid.UUID {
	h.t.Helper()
	var id uuid.UUID
	err := h.st.InTenant(context.Background(), h.tenant, func(tx *store.Tx) error {
		var err error
		id, _, err = tx.Create(context.Background(), "nodes",
			[]store.Col{{Name: "slug", Value: slug}, {Name: "sealing_public_key", Value: []byte("pk")}},
			[]store.Col{{Name: "agent_version", Value: "0.1.0"}, {Name: "protocol_version", Value: 1},
				{Name: "state", Value: "online"}},
			store.Audit{Actor: store.Actor{ID: store.SystemActorID, Kind: store.ActorSystem},
				Action: "test", ResourceType: "bohux.v1.Node"})
		return err
	})
	if err != nil {
		h.t.Fatal(err)
	}
	return id
}

func dockerOn(node uuid.UUID) *bohuxv1.ProviderBinding {
	return &bohuxv1.ProviderBinding{Provider: "docker",
		Target: &bohuxv1.ProviderBinding_NodeId{NodeId: node.String()}}
}

func TestEnvironments(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	srv := h.server()
	envs := bohuxv1connect.NewEnvironmentServiceClient(srv.Client(), srv.URL)
	p1, p2 := h.project("one"), h.project("two")
	n := h.node("vps-1")
	owner1, err := h.createToken(h.root, p1, bohuxv1.Role_ROLE_OWNER, "o1")
	if err != nil {
		t.Fatal(err)
	}
	deployer1, err := h.createToken(h.root, p1, bohuxv1.Role_ROLE_DEPLOYER, "d1")
	if err != nil {
		t.Fatal(err)
	}
	o1, d1 := owner1.GetSecret(), deployer1.GetSecret()

	create := func(token string, project uuid.UUID, slug string, b *bohuxv1.ProviderBinding, key string) (*bohuxv1.Environment, error) {
		resp, err := envs.CreateEnvironment(ctx, req(token, &bohuxv1.CreateEnvironmentRequest{
			ProjectId: project.String(), Slug: slug, ProviderBinding: b,
			Config: map[string]string{"LOG_LEVEL": "info"}, IdempotencyKey: key}))
		if err != nil {
			return nil, err
		}
		return resp.Msg.GetEnvironment(), nil
	}
	prod, err := create(o1, p1, "prod", dockerOn(n), "c1")
	if err != nil {
		t.Fatal(err)
	}

	t.Run("create round-trips binding and config", func(t *testing.T) {
		if prod.GetProviderBinding().GetNodeId() != n.String() || prod.GetConfig()["LOG_LEVEL"] != "info" {
			t.Errorf("created %v", prod)
		}
		if prod.GetProjectId() != p1.String() {
			t.Errorf("project_id %s", prod.GetProjectId())
		}
	})

	t.Run("create checks role, scope, and binding", func(t *testing.T) {
		tests := []struct {
			name    string
			token   string
			project uuid.UUID
			binding *bohuxv1.ProviderBinding
			want    connect.Code
		}{
			{"deployer cannot", d1, p1, dockerOn(n), connect.CodePermissionDenied},
			{"other project is hidden", o1, p2, dockerOn(n), connect.CodeNotFound},
			{"unknown node", o1, p1, dockerOn(uuid.Must(uuid.NewV7())), connect.CodeInvalidArgument},
			{"no target", o1, p1, &bohuxv1.ProviderBinding{Provider: "docker"}, connect.CodeInvalidArgument},
			{"no provider", o1, p1, &bohuxv1.ProviderBinding{
				Target: &bohuxv1.ProviderBinding_TargetRef{TargetRef: "x"}}, connect.CodeInvalidArgument},
		}
		for i, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				_, err := create(tt.token, tt.project, "staging", tt.binding, "bad-"+string(rune('a'+i)))
				wantCode(t, err, tt.want)
			})
		}
	})

	t.Run("get by id and by project+slug", func(t *testing.T) {
		got, err := envs.GetEnvironment(ctx, req(d1, &bohuxv1.GetEnvironmentRequest{
			Lookup: &bohuxv1.GetEnvironmentRequest_Slug{Slug: &bohuxv1.EnvironmentSlug{
				ProjectId: p1.String(), Slug: "prod"}}}))
		if err != nil {
			t.Fatal(err)
		}
		if got.Msg.GetEnvironment().GetMeta().GetId() != prod.GetMeta().GetId() {
			t.Error("slug lookup returned another environment")
		}
		_, err = envs.GetEnvironment(ctx, req(d1, &bohuxv1.GetEnvironmentRequest{
			Lookup: &bohuxv1.GetEnvironmentRequest_Id{Id: uuid.Must(uuid.NewV7()).String()}}))
		wantCode(t, err, connect.CodeNotFound)
	})

	t.Run("list is per project and authorized", func(t *testing.T) {
		got, err := envs.ListEnvironments(ctx, req(d1, &bohuxv1.ListEnvironmentsRequest{ProjectId: p1.String()}))
		if err != nil {
			t.Fatal(err)
		}
		if len(got.Msg.GetEnvironments()) != 1 {
			t.Errorf("listed %d environments", len(got.Msg.GetEnvironments()))
		}
		_, err = envs.ListEnvironments(ctx, req(d1, &bohuxv1.ListEnvironmentsRequest{ProjectId: p2.String()}))
		wantCode(t, err, connect.CodeNotFound)
	})

	t.Run("update replaces binding and config", func(t *testing.T) {
		upd := func(rev, key string) (*bohuxv1.Environment, error) {
			resp, err := envs.UpdateEnvironment(ctx, req(o1, &bohuxv1.UpdateEnvironmentRequest{
				Id: prod.GetMeta().GetId(), ExpectedRevisionId: rev, IdempotencyKey: key,
				ProviderBinding: &bohuxv1.ProviderBinding{Provider: "docker",
					Target: &bohuxv1.ProviderBinding_TargetRef{TargetRef: "ctx-1"}}}))
			if err != nil {
				return nil, err
			}
			return resp.Msg.GetEnvironment(), nil
		}
		got, err := upd(prod.GetMeta().GetRevisionId(), "u1")
		if err != nil {
			t.Fatal(err)
		}
		if got.GetProviderBinding().GetTargetRef() != "ctx-1" || len(got.GetConfig()) != 0 {
			t.Errorf("updated %v", got)
		}
		_, err = upd(prod.GetMeta().GetRevisionId(), "u2")
		wantCode(t, err, connect.CodeAborted)
		prod = got
	})

	t.Run("delete", func(t *testing.T) {
		_, err := envs.DeleteEnvironment(ctx, req(d1, &bohuxv1.DeleteEnvironmentRequest{
			Id: prod.GetMeta().GetId(), ExpectedRevisionId: prod.GetMeta().GetRevisionId(), IdempotencyKey: "x1"}))
		wantCode(t, err, connect.CodePermissionDenied)
		_, err = envs.DeleteEnvironment(ctx, req(o1, &bohuxv1.DeleteEnvironmentRequest{
			Id: prod.GetMeta().GetId(), ExpectedRevisionId: prod.GetMeta().GetRevisionId(), IdempotencyKey: "x2"}))
		if err != nil {
			t.Fatal(err)
		}
		_, err = envs.GetEnvironment(ctx, req(o1, &bohuxv1.GetEnvironmentRequest{
			Lookup: &bohuxv1.GetEnvironmentRequest_Id{Id: prod.GetMeta().GetId()}}))
		wantCode(t, err, connect.CodeNotFound)
	})
}
