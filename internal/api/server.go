package api

import (
	"net/http"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"

	bohuxv1 "github.com/bohumix/bohux/gen/bohux/v1"
	"github.com/bohumix/bohux/gen/bohux/v1/bohuxv1connect"
	"github.com/bohumix/bohux/internal/pki"
	"github.com/bohumix/bohux/internal/store"
)

// Server holds the bohux.v1 handlers.
type Server struct {
	Store *store.Store
	Authn Authenticator
	// PKI issues node credentials (ADR-0024). Nil disables node registration.
	PKI *pki.Authority
	// Now is the clock; nil means time.Now.
	Now func() time.Time
}

func (s *Server) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Mount registers every implemented service on mux behind the auth interceptor.
func (s *Server) Mount(mux *http.ServeMux) {
	// Logging is outermost so it records denied calls too.
	opts := connect.WithInterceptors(NewLoggingInterceptor(), NewAuthInterceptor(s.Authn))
	mux.Handle(bohuxv1connect.NewIdentityServiceHandler(&identityService{s}, opts))
	mux.Handle(bohuxv1connect.NewTenantServiceHandler(&tenantService{s}, opts))
	mux.Handle(bohuxv1connect.NewProjectServiceHandler(&projectService{s}, opts))
	mux.Handle(bohuxv1connect.NewEnvironmentServiceHandler(&environmentService{s}, opts))
	mux.Handle(bohuxv1connect.NewAppServiceHandler(&appService{s}, opts))
	mux.Handle(bohuxv1connect.NewNodeServiceHandler(&nodeService{s}, opts))

	// Not written yet: mounted so every RPC is authenticated, logged, and answers
	// Unimplemented rather than 404. Each is replaced as its slice lands.
	mux.Handle(bohuxv1connect.NewReleaseServiceHandler(bohuxv1connect.UnimplementedReleaseServiceHandler{}, opts))
	mux.Handle(bohuxv1connect.NewDeploymentServiceHandler(bohuxv1connect.UnimplementedDeploymentServiceHandler{}, opts))
	mux.Handle(bohuxv1connect.NewWorkloadServiceHandler(bohuxv1connect.UnimplementedWorkloadServiceHandler{}, opts))
	mux.Handle(bohuxv1connect.NewSecretServiceHandler(bohuxv1connect.UnimplementedSecretServiceHandler{}, opts))
	mux.Handle(bohuxv1connect.NewAuditServiceHandler(bohuxv1connect.UnimplementedAuditServiceHandler{}, opts))
}

// metaRow is the ResourceMeta columns every head+revision read selects, in this order:
// h.id, h.tenant_id, h.revision_id, h.created_at, r.created_at, h.created_by, r.actor_id.
type metaRow struct {
	ID, Tenant, Revision uuid.UUID
	CreatedAt, UpdatedAt time.Time
	CreatedBy, UpdatedBy uuid.UUID
}

const metaColumns = `h.id, h.tenant_id, h.revision_id, h.created_at, r.created_at, h.created_by, r.actor_id`

func (m *metaRow) dest() []any {
	return []any{&m.ID, &m.Tenant, &m.Revision, &m.CreatedAt, &m.UpdatedAt, &m.CreatedBy, &m.UpdatedBy}
}

func (m *metaRow) proto() *bohuxv1.ResourceMeta {
	return &bohuxv1.ResourceMeta{
		Id: m.ID.String(), TenantId: m.Tenant.String(), RevisionId: m.Revision.String(),
		CreatedAt: timestamppb.New(m.CreatedAt), UpdatedAt: timestamppb.New(m.UpdatedAt),
		CreatedBy: m.CreatedBy.String(), UpdatedBy: m.UpdatedBy.String(),
	}
}

// parseID parses a request UUID field; empty or malformed is InvalidArgument.
func parseID(field, s string) (uuid.UUID, error) {
	id, err := uuid.Parse(s)
	if err != nil || id == uuid.Nil {
		return uuid.Nil, invalidArgument(field, "INVALID_ID", field+" must be a UUID")
	}
	return id, nil
}

func timePtr(ts *timestamppb.Timestamp) *time.Time {
	if ts == nil {
		return nil
	}
	t := ts.AsTime()
	return &t
}

func tsOrNil(t *time.Time) *timestamppb.Timestamp {
	if t == nil {
		return nil
	}
	return timestamppb.New(*t)
}
