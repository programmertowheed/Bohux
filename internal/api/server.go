package api

import (
	"context"
	"net/http"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"

	agentv1 "github.com/bohumix/bohux/gen/bohux/agent/v1"
	bohuxv1 "github.com/bohumix/bohux/gen/bohux/v1"
	"github.com/bohumix/bohux/gen/bohux/v1/bohuxv1connect"
	"github.com/bohumix/bohux/internal/agentapi"
	"github.com/bohumix/bohux/internal/pki"
	"github.com/bohumix/bohux/internal/store"
	"github.com/bohumix/bohux/internal/workflow/deploy"
)

// Server holds the bohux.v1 handlers.
type Server struct {
	Store *store.Store
	Authn Authenticator
	// PKI issues node credentials (ADR-0024). Nil disables node registration.
	PKI *pki.Authority
	// Deploys starts and cancels deploy workflows (ADR-0026). Nil refuses deploys.
	Deploys Deployer
	// Secrets stores secret values (OpenBao, ADR-0008). Nil makes SetSecret
	// unavailable.
	Secrets SecretBackend
	// Agents reaches nodes outside a deploy: assignments for ScaleApp (ADR-0027) and
	// rotated secrets, and logs and exec for WorkloadService. Nil means ScaleApp changes
	// only the App, and logs and exec are unavailable.
	Agents NodeAgents
	// Now is the clock; nil means time.Now.
	Now func() time.Time
	// MailRelay reports whether the install has an SMTP relay; EMAIL channels need one
	// (ADR-0043).
	MailRelay bool
}

// NodeAgents is what the API needs from the agent endpoint; agentapi.Server
// implements it.
type NodeAgents interface {
	FollowAppTx(ctx context.Context, tx *store.Tx, node, app uuid.UUID, replicas int32, routes []*bohuxv1.Route, actor store.Actor) (bool, error)
	RotateTx(ctx context.Context, tx *store.Tx, handle string, actor store.Actor) ([]uuid.UUID, error)
	Wake(node uuid.UUID)
	Logs(ctx context.Context, node uuid.UUID, req *agentv1.LogRequest) (<-chan *agentv1.LogChunk, error)
	Exec(ctx context.Context, node uuid.UUID, open *agentv1.ExecOpen) (*agentapi.ExecRelay, error)
}

// Deployer starts and cancels deploy runs; deploy.Starter implements it.
type Deployer interface {
	Start(ctx context.Context, in deploy.Input) (workflowID, runID string, err error)
	StartBuild(ctx context.Context, in deploy.BuildInput) (workflowID string, err error)
	Cancel(ctx context.Context, workflowID, runID string) error
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
	mux.Handle(bohuxv1connect.NewReleaseServiceHandler(&releaseService{s}, opts))
	mux.Handle(bohuxv1connect.NewDeploymentServiceHandler(&deploymentService{s}, opts))
	mux.Handle(bohuxv1connect.NewSecretServiceHandler(&secretService{s}, opts))
	mux.Handle(bohuxv1connect.NewWorkloadServiceHandler(&workloadService{s}, opts))
	mux.Handle(bohuxv1connect.NewWebhookServiceHandler(&webhookService{s}, opts))
	mux.Handle(bohuxv1connect.NewNotificationServiceHandler(&notificationService{s}, opts))
	// Git hosts post here without a token; the signature authenticates (ADR-0037).
	mux.HandleFunc(HookPrefix, s.ServeHook)
	mux.Handle(bohuxv1connect.NewAuditServiceHandler(&auditService{s: s, auditReader: auditReader{s}}, opts))
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
