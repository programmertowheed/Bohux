package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"

	bohuxv1 "github.com/bohumix/bohux/gen/bohux/v1"
	"github.com/bohumix/bohux/gen/bohux/v1/bohuxv1connect"
	"github.com/bohumix/bohux/internal/auth"
	"github.com/bohumix/bohux/internal/store"
)

// Every bohux.v1 RPC has exactly one permission entry, and the table names no RPC
// that does not exist.
func TestPermissionsCoverEveryRPC(t *testing.T) {
	rpcs := map[string]bool{}
	protoregistry.GlobalFiles.RangeFilesByPackage("bohux.v1", func(fd protoreflect.FileDescriptor) bool {
		for i := 0; i < fd.Services().Len(); i++ {
			sd := fd.Services().Get(i)
			for j := 0; j < sd.Methods().Len(); j++ {
				rpcs["/"+string(sd.FullName())+"/"+string(sd.Methods().Get(j).Name())] = true
			}
		}
		return true
	})
	if len(rpcs) == 0 {
		t.Fatal("no bohux.v1 RPCs registered; is gen/ imported?")
	}
	for rpc := range rpcs {
		if _, ok := permissions[rpc]; !ok {
			t.Errorf("%s has no permission entry", rpc)
		}
	}
	for rpc, p := range permissions {
		if !rpcs[rpc] {
			t.Errorf("permission entry %s matches no RPC", rpc)
		}
		if p.MinRole == auth.RoleNone {
			t.Errorf("%s allows RoleNone", rpc)
		}
	}
}

type fakeAuthn map[string]auth.Principal

var errDown = errors.New("database down")

func (f fakeAuthn) AuthenticateAPIToken(_ context.Context, raw string) (auth.Principal, error) {
	if raw == "infra-error" {
		return auth.Principal{}, errDown
	}
	p, ok := f[raw]
	if !ok {
		return auth.Principal{}, auth.ErrUnauthenticated
	}
	return p, nil
}

// tenantEcho returns the caller's tenant, proving the principal reached the handler.
type tenantEcho struct {
	bohuxv1connect.UnimplementedTenantServiceHandler
}

func (tenantEcho) GetTenant(ctx context.Context, _ *connect.Request[bohuxv1.GetTenantRequest]) (*connect.Response[bohuxv1.GetTenantResponse], error) {
	p, ok := auth.FromContext(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeInternal, errors.New("no principal"))
	}
	return connect.NewResponse(&bohuxv1.GetTenantResponse{
		Tenant: &bohuxv1.Tenant{Meta: &bohuxv1.ResourceMeta{TenantId: p.Tenant.String()}},
	}), nil
}

func TestAuthInterceptor(t *testing.T) {
	tenant := uuid.MustParse("0192f0a0-0000-7000-8000-00000000000a")
	project := uuid.MustParse("0192f0a0-0000-7000-8000-000000000001")
	authn := fakeAuthn{
		"viewer":        {Kind: store.ActorAPIToken, Tenant: tenant, ProjectID: project, Role: auth.RoleViewer},
		"project-owner": {Kind: store.ActorAPIToken, Tenant: tenant, ProjectID: project, Role: auth.RoleOwner},
		"tenant-owner":  {Kind: store.ActorAPIToken, Tenant: tenant, Role: auth.RoleOwner},
	}
	opts := connect.WithInterceptors(NewAuthInterceptor(authn))
	mux := http.NewServeMux()
	mux.Handle(bohuxv1connect.NewTenantServiceHandler(tenantEcho{}, opts))
	mux.Handle(bohuxv1connect.NewProjectServiceHandler(bohuxv1connect.UnimplementedProjectServiceHandler{}, opts))
	mux.Handle(bohuxv1connect.NewDeploymentServiceHandler(bohuxv1connect.UnimplementedDeploymentServiceHandler{}, opts))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	tenants := bohuxv1connect.NewTenantServiceClient(srv.Client(), srv.URL)
	projects := bohuxv1connect.NewProjectServiceClient(srv.Client(), srv.URL)
	deployments := bohuxv1connect.NewDeploymentServiceClient(srv.Client(), srv.URL)

	withToken := func(req interface{ Header() http.Header }, token string) {
		if token != "" {
			req.Header().Set("Authorization", "Bearer "+token)
		}
	}
	calls := map[string]func(ctx context.Context, token string) error{
		"GetTenant": func(ctx context.Context, token string) error {
			req := connect.NewRequest(&bohuxv1.GetTenantRequest{})
			withToken(req, token)
			resp, err := tenants.GetTenant(ctx, req)
			if err == nil && resp.Msg.GetTenant().GetMeta().GetTenantId() != tenant.String() {
				return errors.New("handler saw the wrong tenant")
			}
			return err
		},
		"CreateProject": func(ctx context.Context, token string) error {
			req := connect.NewRequest(&bohuxv1.CreateProjectRequest{})
			withToken(req, token)
			_, err := projects.CreateProject(ctx, req)
			return err
		},
		"WatchDeployment": func(ctx context.Context, token string) error {
			req := connect.NewRequest(&bohuxv1.WatchDeploymentRequest{})
			withToken(req, token)
			stream, err := deployments.WatchDeployment(ctx, req)
			if err != nil {
				return err
			}
			for stream.Receive() {
			}
			return stream.Err()
		},
	}

	// CodeUnimplemented means the call got past the interceptor to the stub handler.
	const passed = connect.CodeUnimplemented
	tests := []struct {
		name  string
		call  string
		token string
		want  connect.Code // 0 means success
	}{
		{"no token", "GetTenant", "", connect.CodeUnauthenticated},
		{"unknown token", "GetTenant", "nope", connect.CodeUnauthenticated},
		{"authentication backend down", "GetTenant", "infra-error", connect.CodeUnavailable},
		{"viewer reads tenant, principal in ctx", "GetTenant", "viewer", 0},
		{"viewer cannot create a project", "CreateProject", "viewer", connect.CodePermissionDenied},
		{"project owner cannot create a project", "CreateProject", "project-owner", connect.CodePermissionDenied},
		{"tenant owner creates a project", "CreateProject", "tenant-owner", passed},
		{"stream without token", "WatchDeployment", "", connect.CodeUnauthenticated},
		{"stream with viewer", "WatchDeployment", "viewer", passed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := calls[tt.call](context.Background(), tt.token)
			if tt.want == 0 {
				if err != nil {
					t.Fatalf("got %v, want success", err)
				}
				return
			}
			if got := connect.CodeOf(err); got != tt.want {
				t.Fatalf("got %v (%v), want %v", got, err, tt.want)
			}
		})
	}
}
