//go:build integration

package api

import (
	"context"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"

	bohuxv1 "github.com/bohumix/bohux/gen/bohux/v1"
	"github.com/bohumix/bohux/gen/bohux/v1/bohuxv1connect"
	"github.com/bohumix/bohux/internal/store"
)

// secretRef creates secret metadata directly (SecretService arrives in S7).
func (h *harness) secretRef(env uuid.UUID, key string) *bohuxv1.SecretRef {
	h.t.Helper()
	handle := "openbao:" + key + ":" + uuid.Must(uuid.NewV7()).String()
	err := h.st.InTenant(context.Background(), h.tenant, func(tx *store.Tx) error {
		_, _, err := tx.Create(context.Background(), "secret_refs",
			[]store.Col{{Name: "environment_id", Value: env}, {Name: "key", Value: key}, {Name: "handle", Value: handle}},
			[]store.Col{{Name: "value_version", Value: 1}},
			store.Audit{Actor: store.Actor{ID: store.SystemActorID, Kind: store.ActorSystem},
				Action: "test", ResourceType: "bohux.v1.Secret"})
		return err
	})
	if err != nil {
		h.t.Fatal(err)
	}
	return &bohuxv1.SecretRef{Key: key, Handle: handle}
}

func TestApps(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	srv := h.server()
	apps := bohuxv1connect.NewAppServiceClient(srv.Client(), srv.URL)
	envs := bohuxv1connect.NewEnvironmentServiceClient(srv.Client(), srv.URL)
	p1, p2 := h.project("one"), h.project("two")
	node := h.node("vps-1")

	tok := func(project uuid.UUID, role bohuxv1.Role, key string) string {
		r, err := h.createToken(h.root, project, role, key)
		if err != nil {
			t.Fatal(err)
		}
		return r.GetSecret()
	}
	owner1, deployer1, viewer1 := tok(p1, bohuxv1.Role_ROLE_OWNER, "o1"),
		tok(p1, bohuxv1.Role_ROLE_DEPLOYER, "d1"), tok(p1, bohuxv1.Role_ROLE_VIEWER, "v1")
	deployer2 := tok(p2, bohuxv1.Role_ROLE_DEPLOYER, "d2")

	envResp, err := envs.CreateEnvironment(ctx, req(owner1, &bohuxv1.CreateEnvironmentRequest{
		ProjectId: p1.String(), Slug: "prod", ProviderBinding: dockerOn(node), IdempotencyKey: "e1"}))
	if err != nil {
		t.Fatal(err)
	}
	env := envResp.Msg.GetEnvironment()
	envID := mustUUID(t, env.GetMeta().GetId())
	dbURL := h.secretRef(envID, "DATABASE_URL")

	digest := "sha256:" + strings.Repeat("ab", 32)
	source := &bohuxv1.Source{Kind: &bohuxv1.Source_Image{Image: &bohuxv1.ImageSource{
		Repository: "ghcr.io/acme/api", Digest: digest}}}
	runtime := func() *bohuxv1.Runtime {
		return &bohuxv1.Runtime{Replicas: 1, Cpu: "500m", Memory: "256Mi",
			Ports:  []*bohuxv1.Port{{ContainerPort: 8080, HostPort: 80}},
			Health: &bohuxv1.HealthCheck{Path: "/healthz", Port: 8080}}
	}
	create := func(token, slug string, src *bohuxv1.Source, rt *bohuxv1.Runtime, refs []*bohuxv1.SecretRef, key string) (*bohuxv1.App, error) {
		resp, err := apps.CreateApp(ctx, req(token, &bohuxv1.CreateAppRequest{
			EnvironmentId: env.GetMeta().GetId(), Slug: slug, Source: src, Runtime: rt,
			Secrets: refs, IdempotencyKey: key}))
		if err != nil {
			return nil, err
		}
		return resp.Msg.GetApp(), nil
	}

	app, err := create(deployer1, "api", source, runtime(), []*bohuxv1.SecretRef{dbURL}, "a1")
	if err != nil {
		t.Fatal(err)
	}

	t.Run("create round-trips source, runtime, and secret refs", func(t *testing.T) {
		got, err := apps.GetApp(ctx, req(viewer1, &bohuxv1.GetAppRequest{Lookup: &bohuxv1.GetAppRequest_Slug{
			Slug: &bohuxv1.AppSlug{EnvironmentId: env.GetMeta().GetId(), Slug: "api"}}}))
		if err != nil {
			t.Fatal(err)
		}
		a := got.Msg.GetApp()
		if !proto.Equal(a.GetSource(), source) || !proto.Equal(a.GetRuntime(), runtime()) ||
			len(a.GetSecrets()) != 1 || !proto.Equal(a.GetSecrets()[0], dbURL) {
			t.Errorf("stored app differs: %v", a)
		}
	})

	t.Run("create validates", func(t *testing.T) {
		badRuntime := func(f func(*bohuxv1.Runtime)) *bohuxv1.Runtime {
			rt := runtime()
			f(rt)
			return rt
		}
		tests := []struct {
			name string
			src  *bohuxv1.Source
			rt   *bohuxv1.Runtime
			refs []*bohuxv1.SecretRef
		}{
			{"no source", &bohuxv1.Source{}, runtime(), nil},
			{"image by tag", &bohuxv1.Source{Kind: &bohuxv1.Source_Image{Image: &bohuxv1.ImageSource{
				Repository: "ghcr.io/acme/api", Digest: "latest"}}}, runtime(), nil},
			{"git without repo", &bohuxv1.Source{Kind: &bohuxv1.Source_Git{Git: &bohuxv1.GitSource{}}}, runtime(), nil},
			{"no runtime", source, nil, nil},
			{"negative replicas", source, badRuntime(func(r *bohuxv1.Runtime) { r.Replicas = -1 }), nil},
			{"port out of range", source, badRuntime(func(r *bohuxv1.Runtime) { r.Ports[0].ContainerPort = 70000 }), nil},
			{"duplicate host port", source, badRuntime(func(r *bohuxv1.Runtime) {
				r.Ports = append(r.Ports, &bohuxv1.Port{ContainerPort: 9090, HostPort: 80})
			}), nil},
			{"health on unknown port", source, badRuntime(func(r *bohuxv1.Runtime) { r.Health.Port = 9999 }), nil},
			{"bad secret key", source, runtime(), []*bohuxv1.SecretRef{{Key: "lower", Handle: dbURL.GetHandle()}}},
			{"unknown handle", source, runtime(), []*bohuxv1.SecretRef{{Key: "DATABASE_URL", Handle: "nope"}}},
			{"duplicate key", source, runtime(), []*bohuxv1.SecretRef{dbURL, dbURL}},
		}
		for i, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				_, err := create(deployer1, "bad", tt.src, tt.rt, tt.refs, "bad-"+string(rune('a'+i)))
				wantCode(t, err, connect.CodeInvalidArgument)
			})
		}
	})

	t.Run("authorization", func(t *testing.T) {
		_, err := create(viewer1, "web", source, runtime(), nil, "z1")
		wantCode(t, err, connect.CodePermissionDenied)
		_, err = create(deployer2, "web", source, runtime(), nil, "z2")
		wantCode(t, err, connect.CodeNotFound)
		_, err = apps.GetApp(ctx, req(deployer2, &bohuxv1.GetAppRequest{
			Lookup: &bohuxv1.GetAppRequest_Id{Id: app.GetMeta().GetId()}}))
		wantCode(t, err, connect.CodeNotFound)
	})

	t.Run("update and scale write revisions", func(t *testing.T) {
		rt := runtime()
		rt.Memory = "512Mi"
		upd, err := apps.UpdateApp(ctx, req(deployer1, &bohuxv1.UpdateAppRequest{
			Id: app.GetMeta().GetId(), Source: source, Runtime: rt,
			ExpectedRevisionId: app.GetMeta().GetRevisionId(), IdempotencyKey: "u1"}))
		if err != nil {
			t.Fatal(err)
		}
		if upd.Msg.GetApp().GetRuntime().GetMemory() != "512Mi" || len(upd.Msg.GetApp().GetSecrets()) != 0 {
			t.Errorf("update did not replace runtime and secrets: %v", upd.Msg.GetApp())
		}
		_, err = apps.ScaleApp(ctx, req(deployer1, &bohuxv1.ScaleAppRequest{
			Id: app.GetMeta().GetId(), Replicas: 3,
			ExpectedRevisionId: app.GetMeta().GetRevisionId(), IdempotencyKey: "s0"}))
		wantCode(t, err, connect.CodeAborted)
		sc, err := apps.ScaleApp(ctx, req(deployer1, &bohuxv1.ScaleAppRequest{
			Id: app.GetMeta().GetId(), Replicas: 3,
			ExpectedRevisionId: upd.Msg.GetApp().GetMeta().GetRevisionId(), IdempotencyKey: "s1"}))
		if err != nil {
			t.Fatal(err)
		}
		got := sc.Msg.GetApp()
		if got.GetRuntime().GetReplicas() != 3 || got.GetRuntime().GetMemory() != "512Mi" ||
			!proto.Equal(got.GetSource(), source) {
			t.Errorf("scale changed more than replicas: %v", got)
		}
		app = got
	})

	t.Run("list", func(t *testing.T) {
		got, err := apps.ListApps(ctx, req(viewer1, &bohuxv1.ListAppsRequest{EnvironmentId: env.GetMeta().GetId()}))
		if err != nil {
			t.Fatal(err)
		}
		if len(got.Msg.GetApps()) != 1 {
			t.Errorf("listed %d apps", len(got.Msg.GetApps()))
		}
	})

	t.Run("delete needs owner and unblocks the environment", func(t *testing.T) {
		_, err := envs.DeleteEnvironment(ctx, req(owner1, &bohuxv1.DeleteEnvironmentRequest{
			Id: env.GetMeta().GetId(), ExpectedRevisionId: env.GetMeta().GetRevisionId(), IdempotencyKey: "ed1"}))
		wantCode(t, err, connect.CodeFailedPrecondition)
		_, err = apps.DeleteApp(ctx, req(deployer1, &bohuxv1.DeleteAppRequest{
			Id: app.GetMeta().GetId(), ExpectedRevisionId: app.GetMeta().GetRevisionId(), IdempotencyKey: "ad1"}))
		wantCode(t, err, connect.CodePermissionDenied)
		_, err = apps.DeleteApp(ctx, req(owner1, &bohuxv1.DeleteAppRequest{
			Id: app.GetMeta().GetId(), ExpectedRevisionId: app.GetMeta().GetRevisionId(), IdempotencyKey: "ad2"}))
		if err != nil {
			t.Fatal(err)
		}
		_, err = envs.DeleteEnvironment(ctx, req(owner1, &bohuxv1.DeleteEnvironmentRequest{
			Id: env.GetMeta().GetId(), ExpectedRevisionId: env.GetMeta().GetRevisionId(), IdempotencyKey: "ed2"}))
		if err != nil {
			t.Fatalf("environment delete after its app was deleted: %v", err)
		}
	})
}
