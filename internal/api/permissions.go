// Package api serves the public bohux.v1 API: authentication, authorization, and (from
// S3e) the service handlers.
package api

import "github.com/bohumix/bohux/internal/auth"

// Permission is the interceptor's check for one RPC (ADR-0023, step one). Handlers
// still check the resource's project with auth.RequireProject (step two).
type Permission struct {
	// MinRole is the lowest role that may call the RPC at all.
	MinRole auth.Role
	// TenantWide requires a tenant-wide token: the RPC acts on the tenant, not a project.
	TenantWide bool
}

var (
	viewer       = Permission{MinRole: auth.RoleViewer}
	deployer     = Permission{MinRole: auth.RoleDeployer}
	owner        = Permission{MinRole: auth.RoleOwner}
	tenantViewer = Permission{MinRole: auth.RoleViewer, TenantWide: true}
	tenantOwner  = Permission{MinRole: auth.RoleOwner, TenantWide: true}
)

// permissions covers every bohux.v1 RPC; a test enforces it. An RPC not listed is
// denied. Deployers can already run arbitrary code through a deploy, so Exec and secret
// writes are deployer, not owner.
var permissions = map[string]Permission{
	"/bohux.v1.IdentityService/WhoAmI":         viewer,
	"/bohux.v1.IdentityService/CreateApiToken": owner,
	"/bohux.v1.IdentityService/ListApiTokens":  owner,
	"/bohux.v1.IdentityService/RevokeApiToken": owner,

	"/bohux.v1.TenantService/GetTenant": viewer,

	"/bohux.v1.ProjectService/CreateProject":     tenantOwner,
	"/bohux.v1.ProjectService/GetProject":        viewer,
	"/bohux.v1.ProjectService/ListProjects":      viewer,
	"/bohux.v1.ProjectService/UpdateProject":     owner,
	"/bohux.v1.ProjectService/DeleteProject":     tenantOwner,
	"/bohux.v1.ProjectService/SetRoleBinding":    owner,
	"/bohux.v1.ProjectService/DeleteRoleBinding": owner,
	"/bohux.v1.ProjectService/ListRoleBindings":  viewer,

	"/bohux.v1.EnvironmentService/CreateEnvironment": owner,
	"/bohux.v1.EnvironmentService/GetEnvironment":    viewer,
	"/bohux.v1.EnvironmentService/ListEnvironments":  viewer,
	"/bohux.v1.EnvironmentService/UpdateEnvironment": owner,
	"/bohux.v1.EnvironmentService/DeleteEnvironment": owner,

	"/bohux.v1.AppService/CreateApp": deployer,
	"/bohux.v1.AppService/GetApp":    viewer,
	"/bohux.v1.AppService/ListApps":  viewer,
	"/bohux.v1.AppService/UpdateApp": deployer,
	"/bohux.v1.AppService/DeleteApp": owner,
	"/bohux.v1.AppService/ScaleApp":  deployer,

	"/bohux.v1.ReleaseService/CreateRelease": deployer,
	"/bohux.v1.ReleaseService/GetRelease":    viewer,
	"/bohux.v1.ReleaseService/ListReleases":  viewer,

	"/bohux.v1.DeploymentService/CreateDeployment": deployer,
	"/bohux.v1.DeploymentService/GetDeployment":    viewer,
	"/bohux.v1.DeploymentService/ListDeployments":  viewer,
	"/bohux.v1.DeploymentService/WatchDeployment":  viewer,
	"/bohux.v1.DeploymentService/Rollback":         deployer,
	"/bohux.v1.DeploymentService/CancelDeployment": deployer,

	"/bohux.v1.WorkloadService/StreamLogs": viewer,
	"/bohux.v1.WorkloadService/Exec":       deployer,

	"/bohux.v1.NodeService/CreateBootstrapToken": tenantOwner,
	"/bohux.v1.NodeService/GetNode":              tenantViewer,
	"/bohux.v1.NodeService/ListNodes":            tenantViewer,
	"/bohux.v1.NodeService/RevokeNode":           tenantOwner,

	"/bohux.v1.SecretService/SetSecret":      deployer,
	"/bohux.v1.SecretService/ListSecretRefs": viewer,
	"/bohux.v1.SecretService/DeleteSecret":   deployer,

	"/bohux.v1.WebhookService/CreateWebhook": deployer,
	"/bohux.v1.WebhookService/ListWebhooks":  viewer,
	"/bohux.v1.WebhookService/DeleteWebhook": deployer,

	"/bohux.v1.AuditService/ListAuditEvents": viewer,
	"/bohux.v1.AuditService/ListRevisions":   viewer,
	"/bohux.v1.AuditService/GetRevision":     viewer,
}
