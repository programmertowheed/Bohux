//go:build integration

package api

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"

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

	gitWithKey := func(repo, key string) *bohuxv1.Source {
		return &bohuxv1.Source{Kind: &bohuxv1.Source_Git{Git: &bohuxv1.GitSource{RepoUrl: repo, SshKeySecret: key}}}
	}

	app, err := create(deployer1, "api", source, runtime(), []*bohuxv1.SecretRef{dbURL}, "a1")
	if err != nil {
		t.Fatal(err)
	}

	t.Run("a git source names its deploy key by secret key (ADR-0041)", func(t *testing.T) {
		// Its own environment, so the apps here do not count in the tests below.
		keysEnv, err := envs.CreateEnvironment(ctx, req(owner1, &bohuxv1.CreateEnvironmentRequest{
			ProjectId: p1.String(), Slug: "keys", ProviderBinding: dockerOn(node), IdempotencyKey: "e-keys"}))
		if err != nil {
			t.Fatal(err)
		}
		keysID := keysEnv.Msg.GetEnvironment().GetMeta().GetId()
		h.secretRef(mustUUID(t, keysID), "DEPLOY_KEY")
		for i, repo := range []string{"git@example.com:acme/private.git", "ssh://git@example.com:2222/acme/private.git"} {
			src := gitWithKey(repo, "DEPLOY_KEY")
			resp, err := apps.CreateApp(ctx, req(deployer1, &bohuxv1.CreateAppRequest{EnvironmentId: keysID,
				Slug: fmt.Sprintf("private-%d", i), Source: src, Runtime: runtime(), IdempotencyKey: fmt.Sprintf("k%d", i)}))
			if err != nil {
				t.Fatalf("%s: %v", repo, err)
			}
			if !proto.Equal(resp.Msg.GetApp().GetSource(), src) {
				t.Errorf("stored source %v, want %v", resp.Msg.GetApp().GetSource(), src)
			}
		}
		// The key is a secret of the app's own environment, not of a sibling.
		_, err = create(deployer1, "borrowed", gitWithKey("git@example.com:acme/private.git", "DEPLOY_KEY"), runtime(), nil, "k-borrowed")
		wantCode(t, err, connect.CodeInvalidArgument)
	})

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
			{"deploy key not a secret", gitWithKey("git@example.com:acme/api.git", "NO_SUCH"), runtime(), nil},
			{"deploy key over https", gitWithKey("https://example.com/acme/api.git", "DATABASE_URL"), runtime(), nil},
			{"deploy key bad name", gitWithKey("ssh://git@example.com/acme/api.git", "lower"), runtime(), nil},
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

// TestAppProviderOptions: provider options reach outside the workload, so only an
// owner may set or change them (ADR-0032); everyone else keeps them as they are.
func TestAppProviderOptions(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	srv := h.server()
	apps := bohuxv1connect.NewAppServiceClient(srv.Client(), srv.URL)
	envs := bohuxv1connect.NewEnvironmentServiceClient(srv.Client(), srv.URL)
	p := h.project("one")
	tok := func(role bohuxv1.Role, key string) string {
		r, err := h.createToken(h.root, p, role, key)
		if err != nil {
			t.Fatal(err)
		}
		return r.GetSecret()
	}
	owner, deployer := tok(bohuxv1.Role_ROLE_OWNER, "o"), tok(bohuxv1.Role_ROLE_DEPLOYER, "d")
	e, err := envs.CreateEnvironment(ctx, req(owner, &bohuxv1.CreateEnvironmentRequest{
		ProjectId: p.String(), Slug: "prod", ProviderBinding: dockerOn(h.node("vps-1")), IdempotencyKey: "e"}))
	if err != nil {
		t.Fatal(err)
	}
	opts, err := structpb.NewStruct(map[string]any{"networks": []any{"bohux-infra"}})
	if err != nil {
		t.Fatal(err)
	}
	src := &bohuxv1.Source{Kind: &bohuxv1.Source_Image{Image: &bohuxv1.ImageSource{
		Repository: "ghcr.io/acme/api", Digest: "sha256:" + strings.Repeat("ab", 32)}}}
	create := func(token, key string, o *structpb.Struct) (*bohuxv1.App, error) {
		r, err := apps.CreateApp(ctx, req(token, &bohuxv1.CreateAppRequest{EnvironmentId: e.Msg.GetEnvironment().GetMeta().GetId(),
			Slug: key, Source: src, Runtime: &bohuxv1.Runtime{Replicas: 1}, ProviderOptions: o, IdempotencyKey: key}))
		if err != nil {
			return nil, err
		}
		return r.Msg.GetApp(), nil
	}

	_, err = create(deployer, "by-deployer", opts)
	wantCode(t, err, connect.CodePermissionDenied)
	app, err := create(owner, "by-owner", opts)
	if err != nil {
		t.Fatal(err)
	}
	if !proto.Equal(app.GetProviderOptions(), opts) {
		t.Errorf("options = %v, want %v", app.GetProviderOptions(), opts)
	}

	update := func(token, key string, o *structpb.Struct) (*bohuxv1.App, error) {
		r, err := apps.UpdateApp(ctx, req(token, &bohuxv1.UpdateAppRequest{Id: app.GetMeta().GetId(), Source: src,
			Runtime: &bohuxv1.Runtime{Replicas: 1}, ProviderOptions: o,
			ExpectedRevisionId: app.GetMeta().GetRevisionId(), IdempotencyKey: key}))
		if err != nil {
			return nil, err
		}
		return r.Msg.GetApp(), nil
	}
	_, err = update(deployer, "u1", nil)
	wantCode(t, err, connect.CodePermissionDenied) // dropping them is a change too
	if app, err = update(deployer, "u2", opts); err != nil {
		t.Fatalf("a deployer keeping the options as they are: %v", err)
	}
	scaled, err := apps.ScaleApp(ctx, req(deployer, &bohuxv1.ScaleAppRequest{Id: app.GetMeta().GetId(), Replicas: 2,
		ExpectedRevisionId: app.GetMeta().GetRevisionId(), IdempotencyKey: "s1"}))
	if err != nil || !proto.Equal(scaled.Msg.GetApp().GetProviderOptions(), opts) {
		t.Errorf("scale = %v, %v; want the options kept", scaled, err)
	}
}

// TestAppRoutes: hosts are validated, unique across all tenants without revealing who
// holds one, follow the App through updates, and are freed by delete (ADR-0034).
func TestAppRoutes(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	srv := h.server()
	apps := bohuxv1connect.NewAppServiceClient(srv.Client(), srv.URL)
	envs := bohuxv1connect.NewEnvironmentServiceClient(srv.Client(), srv.URL)

	// A second tenant, set up the same way through its own root token.
	tenant2, root2, err := Bootstrap(ctx, h.st, "globex", "Globex")
	if err != nil {
		t.Fatal(err)
	}
	h2 := *h
	h2.tenant, h2.root = tenant2, root2

	src := &bohuxv1.Source{Kind: &bohuxv1.Source_Image{Image: &bohuxv1.ImageSource{
		Repository: "ghcr.io/acme/api", Digest: "sha256:" + strings.Repeat("ab", 32)}}}
	rt := &bohuxv1.Runtime{Replicas: 1, Ports: []*bohuxv1.Port{{ContainerPort: 8080}}}
	envFor := func(h *harness, token string) string {
		e, err := envs.CreateEnvironment(ctx, req(token, &bohuxv1.CreateEnvironmentRequest{
			ProjectId: h.project("web").String(), Slug: "prod", ProviderBinding: dockerOn(h.node("vps-1")),
			IdempotencyKey: "env"}))
		if err != nil {
			t.Fatal(err)
		}
		return e.Msg.GetEnvironment().GetMeta().GetId()
	}
	env1, env2 := envFor(h, h.root), envFor(&h2, root2)
	create := func(token, env, slug string, routes ...*bohuxv1.Route) (*bohuxv1.App, error) {
		r, err := apps.CreateApp(ctx, req(token, &bohuxv1.CreateAppRequest{EnvironmentId: env, Slug: slug,
			Source: src, Runtime: rt, Routes: routes, IdempotencyKey: slug}))
		if err != nil {
			return nil, err
		}
		return r.Msg.GetApp(), nil
	}
	route := func(host string) *bohuxv1.Route { return &bohuxv1.Route{Host: host, Port: 8080} }

	for _, tt := range []struct {
		name   string
		routes []*bohuxv1.Route
	}{
		{"uppercase", []*bohuxv1.Route{route("API.example.com")}},
		{"wildcard", []*bohuxv1.Route{route("*.example.com")}},
		{"port", []*bohuxv1.Route{route("api.example.com:443")}},
		{"single label", []*bohuxv1.Route{route("localhost")}},
		{"twice", []*bohuxv1.Route{route("a.example.com"), route("a.example.com")}},
		{"unknown port", []*bohuxv1.Route{{Host: "b.example.com", Port: 9090}}},
	} {
		_, err := create(h.root, env1, "bad-"+strings.ReplaceAll(tt.name, " ", "-"), tt.routes...)
		wantCode(t, err, connect.CodeInvalidArgument)
	}

	api, err := create(h.root, env1, "api", route("api.example.com"))
	if err != nil {
		t.Fatal(err)
	}
	if len(api.GetRoutes()) != 1 || api.GetRoutes()[0].GetHost() != "api.example.com" {
		t.Fatalf("routes = %v", api.GetRoutes())
	}
	_, err = create(root2, env2, "squat", route("api.example.com"))
	wantCode(t, err, connect.CodeAlreadyExists)
	if err != nil && strings.Contains(err.Error(), "acme") {
		t.Errorf("the conflict names the other tenant: %v", err)
	}
	_, err = create(h.root, env1, "same-tenant", route("api.example.com"))
	wantCode(t, err, connect.CodeAlreadyExists)

	// Moving the App to another host frees the first one.
	moved, err := apps.UpdateApp(ctx, req(h.root, &bohuxv1.UpdateAppRequest{Id: api.GetMeta().GetId(), Source: src,
		Runtime: rt, Routes: []*bohuxv1.Route{route("www.example.com")},
		ExpectedRevisionId: api.GetMeta().GetRevisionId(), IdempotencyKey: "move"}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := create(root2, env2, "takes-freed", route("api.example.com")); err != nil {
		t.Fatalf("a freed host: %v", err)
	}
	// Scaling keeps the routes.
	scaled, err := apps.ScaleApp(ctx, req(h.root, &bohuxv1.ScaleAppRequest{Id: api.GetMeta().GetId(), Replicas: 2,
		ExpectedRevisionId: moved.Msg.GetApp().GetMeta().GetRevisionId(), IdempotencyKey: "scale"}))
	if err != nil || len(scaled.Msg.GetApp().GetRoutes()) != 1 {
		t.Fatalf("scale = %v, %v; want the route kept", scaled, err)
	}
	// Delete frees the host.
	if _, err := apps.DeleteApp(ctx, req(h.root, &bohuxv1.DeleteAppRequest{Id: api.GetMeta().GetId(),
		ExpectedRevisionId: scaled.Msg.GetApp().GetMeta().GetRevisionId(), IdempotencyKey: "del"})); err != nil {
		t.Fatal(err)
	}
	if _, err := create(root2, env2, "takes-deleted", route("www.example.com")); err != nil {
		t.Fatalf("a deleted app's host: %v", err)
	}
}
