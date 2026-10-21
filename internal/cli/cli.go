// Package cli is the bohux command-line client: deploy, rollback, scale, logs, exec,
// and the resource commands (ADR-0036). It is a client of the public API and nothing more (CLAUDE.md rule 8): every command is RPCs
// any other client could make.
package cli

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"golang.org/x/net/http2"

	bohuxv1 "github.com/bohumix/bohux/gen/bohux/v1"
	"github.com/bohumix/bohux/gen/bohux/v1/bohuxv1connect"
)

// Environment variables the CLI reads. See docs/reference/config.md.
const (
	EnvAPIURL  = "BOHUX_API_URL"
	EnvToken   = "BOHUX_TOKEN"
	EnvProject = "BOHUX_PROJECT"

	DefaultAPIURL = "http://127.0.0.1:7400"
)

// ErrUsage marks a malformed command line.
var ErrUsage = errors.New("usage")

// Commands lists the CLI's commands for the binary's usage text.
const Commands = `  deploy          build or import a release of an app and deploy it
  rollback        deploy the app's last healthy release again
  scale           change an app's replicas
  logs            print an app's log lines
  exec            run a command in one of an app's instances`

// Run executes one CLI command. stdin is read only by `secret set`.
func Run(ctx context.Context, cmd string, args []string, stdin io.Reader, stdout io.Writer, getenv func(string) string) error {
	c := &command{out: stdout, getenv: getenv}
	if IsResource(cmd) {
		return c.runResource(ctx, cmd, args, stdin)
	}
	switch cmd {
	case "deploy":
		return c.deploy(ctx, args)
	case "rollback":
		return c.rollback(ctx, args)
	case "scale":
		return c.scale(ctx, args)
	case "logs":
		return c.logs(ctx, args)
	case "exec":
		return c.exec(ctx, args)
	}
	return fmt.Errorf("unknown command %q: %w", cmd, ErrUsage)
}

type command struct {
	out    io.Writer
	getenv func(string) string

	token       string
	projects    bohuxv1connect.ProjectServiceClient
	envs        bohuxv1connect.EnvironmentServiceClient
	apps        bohuxv1connect.AppServiceClient
	releases    bohuxv1connect.ReleaseServiceClient
	deployments bohuxv1connect.DeploymentServiceClient
	workloads   bohuxv1connect.WorkloadServiceClient
	nodesAPI    bohuxv1connect.NodeServiceClient
	secrets     bohuxv1connect.SecretServiceClient
	webhooks    bohuxv1connect.WebhookServiceClient
	// notifications is NotificationService (ADR-0038).
	notifications bohuxv1connect.NotificationServiceClient
	audit         bohuxv1connect.AuditServiceClient
	identity      bohuxv1connect.IdentityServiceClient
}

// target is the flags every App command takes.
type target struct {
	project, env *string
}

func (c *command) flags(name string) (*flag.FlagSet, target) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	t := target{
		project: fs.String("p", c.getenv(EnvProject), "project slug (env "+EnvProject+")"),
		env:     fs.String("e", "", "environment slug (required)"),
	}
	return fs, t
}

// parse parses flags that may come before or after the one positional App slug.
func parse(fs *flag.FlagSet, args []string) (string, error) {
	var app string
	for {
		if err := fs.Parse(args); err != nil {
			return "", fmt.Errorf("%s: %w: %w", fs.Name(), err, ErrUsage)
		}
		rest := fs.Args()
		if len(rest) == 0 {
			break
		}
		if app != "" {
			return "", fmt.Errorf("%s: unexpected argument %q: %w", fs.Name(), rest[0], ErrUsage)
		}
		app, args = rest[0], rest[1:]
	}
	if app == "" {
		return "", fmt.Errorf("%s: missing app: %w", fs.Name(), ErrUsage)
	}
	return app, nil
}

// connect builds the API clients from BOHUX_API_URL and BOHUX_TOKEN.
func (c *command) connect() error {
	c.token = c.getenv(EnvToken)
	if c.token == "" {
		return fmt.Errorf("set %s to an API token", EnvToken)
	}
	base := c.getenv(EnvAPIURL)
	if base == "" {
		base = DefaultAPIURL
	}
	hc := HTTPClient(base)
	c.projects = bohuxv1connect.NewProjectServiceClient(hc, base)
	c.envs = bohuxv1connect.NewEnvironmentServiceClient(hc, base)
	c.apps = bohuxv1connect.NewAppServiceClient(hc, base)
	c.releases = bohuxv1connect.NewReleaseServiceClient(hc, base)
	c.deployments = bohuxv1connect.NewDeploymentServiceClient(hc, base)
	c.workloads = bohuxv1connect.NewWorkloadServiceClient(hc, base)
	c.nodesAPI = bohuxv1connect.NewNodeServiceClient(hc, base)
	c.secrets = bohuxv1connect.NewSecretServiceClient(hc, base)
	c.webhooks = bohuxv1connect.NewWebhookServiceClient(hc, base)
	c.notifications = bohuxv1connect.NewNotificationServiceClient(hc, base)
	c.audit = bohuxv1connect.NewAuditServiceClient(hc, base)
	c.identity = bohuxv1connect.NewIdentityServiceClient(hc, base)
	return nil
}

// HTTPClient speaks HTTP/2 to the API: cleartext (h2c) for http:// URLs, which the
// control plane serves (streams need HTTP/2), and TLS otherwise.
func HTTPClient(base string) *http.Client {
	if strings.HasPrefix(base, "http://") {
		return &http.Client{Transport: &http2.Transport{
			AllowHTTP: true,
			DialTLSContext: func(ctx context.Context, network, addr string, _ *tls.Config) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, network, addr)
			},
		}}
	}
	return &http.Client{Transport: &http.Transport{ForceAttemptHTTP2: true}}
}

func call[T any](token string, msg *T) *connect.Request[T] {
	r := connect.NewRequest(msg)
	r.Header().Set("Authorization", "Bearer "+token)
	return r
}

// app resolves project/environment/app slugs to the App.
func (c *command) app(ctx context.Context, t target, slug string) (*bohuxv1.App, error) {
	if *t.env == "" {
		return nil, fmt.Errorf("-e (environment) is required: %w", ErrUsage)
	}
	if *t.project == "" {
		return nil, fmt.Errorf("-p (project) or %s is required: %w", EnvProject, ErrUsage)
	}
	p, err := c.projects.GetProject(ctx, call(c.token, &bohuxv1.GetProjectRequest{
		Lookup: &bohuxv1.GetProjectRequest_Slug{Slug: *t.project}}))
	if err != nil {
		return nil, fmt.Errorf("project %q: %w", *t.project, err)
	}
	e, err := c.envs.GetEnvironment(ctx, call(c.token, &bohuxv1.GetEnvironmentRequest{
		Lookup: &bohuxv1.GetEnvironmentRequest_Slug{Slug: &bohuxv1.EnvironmentSlug{
			ProjectId: p.Msg.GetProject().GetMeta().GetId(), Slug: *t.env}}}))
	if err != nil {
		return nil, fmt.Errorf("environment %q: %w", *t.env, err)
	}
	a, err := c.apps.GetApp(ctx, call(c.token, &bohuxv1.GetAppRequest{
		Lookup: &bohuxv1.GetAppRequest_Slug{Slug: &bohuxv1.AppSlug{
			EnvironmentId: e.Msg.GetEnvironment().GetMeta().GetId(), Slug: slug}}}))
	if err != nil {
		return nil, fmt.Errorf("app %q: %w", slug, err)
	}
	return a.Msg.GetApp(), nil
}

// key is a fresh idempotency key: one invocation is one request (ADR-0022).
func key() string { return "cli-" + uuid.NewString() }
