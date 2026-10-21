package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	bohuxv1 "github.com/bohumix/bohux/gen/bohux/v1"
)

// Resource commands are `bohux <noun> <verb>` (ADR-0036).

// ResourceCommands lists the resource commands for the binary's usage text.
const ResourceCommands = `  project list|create      projects
  env list|create          environments of a project (-p)
  app list|show|create|pull-secret|deploy-key
                           apps of an environment (-p, -e); pull-secret and deploy-key
                           name the secret with a registry credential or an SSH key
  route list|add|remove    an app's hosts on the node's ingress
  secret list|set          an environment's secrets; set reads the value from stdin
  node list|token          nodes, and bootstrap tokens to register one
  webhook list|create|remove
                           git push webhooks of an app (--path: only when files under it
                           change); create prints the secret once
  channel list|create|remove
                           a project's deploy notifications; create reads the URL (or,
                           for email, mailto:a@x,b@y) from stdin
  audit list               who changed what, newest first (-p for one project)
  token list|create|revoke API tokens; create prints the secret once; -p (not
                           BOHUX_PROJECT) scopes it to a project`

// IsResource reports whether cmd is a resource noun.
func IsResource(cmd string) bool {
	switch cmd {
	case "project", "env", "app", "route", "secret", "node", "webhook", "channel", "audit", "token":
		return true
	}
	return false
}

// resource is one invocation's shared flags and plumbing.
type resource struct {
	*command
	fs       *flag.FlagSet
	t        target
	output   *string
	idemKey  *string
	stdin    io.Reader
	verbName string
}

func (c *command) resource(noun string, args []string, stdin io.Reader) (*resource, []string, error) {
	if len(args) == 0 {
		return nil, nil, fmt.Errorf("%s: missing verb: %w", noun, ErrUsage)
	}
	fs, t := c.flags(noun + " " + args[0])
	r := &resource{command: c, fs: fs, t: t, stdin: stdin, verbName: noun + " " + args[0],
		output:  fs.String("o", "table", "output: table or json"),
		idemKey: fs.String("idempotency-key", "", "idempotency key; a fresh one by default")}
	return r, args[1:], nil
}

// positional parses flags around exactly n positional arguments.
func (r *resource) positional(args []string, n int, names string) ([]string, error) {
	var pos []string
	for {
		if err := r.fs.Parse(args); err != nil {
			return nil, fmt.Errorf("%s: %w: %w", r.verbName, err, ErrUsage)
		}
		rest := r.fs.Args()
		if len(rest) == 0 {
			break
		}
		pos, args = append(pos, rest[0]), rest[1:]
	}
	if len(pos) != n {
		return nil, fmt.Errorf("%s: want %s: %w", r.verbName, names, ErrUsage)
	}
	if *r.output != "table" && *r.output != "json" {
		return nil, fmt.Errorf("%s: -o must be table or json: %w", r.verbName, ErrUsage)
	}
	return pos, r.connect()
}

func (r *resource) key() string {
	if *r.idemKey != "" {
		return *r.idemKey
	}
	return key()
}

// emit prints msg as protojson with -o json, else the table.
func (r *resource) emit(msg proto.Message, header []string, rows [][]string) error {
	if *r.output == "json" {
		b, err := protojson.MarshalOptions{UseProtoNames: true, Multiline: true}.Marshal(msg)
		if err != nil {
			return fmt.Errorf("encode output: %w", err)
		}
		r.printf("%s\n", b)
		return nil
	}
	tw := tabwriter.NewWriter(r.out, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, strings.Join(header, "\t"))
	for _, row := range rows {
		_, _ = fmt.Fprintln(tw, strings.Join(row, "\t"))
	}
	return tw.Flush()
}

// runResource dispatches `bohux <noun> <verb>`.
func (c *command) runResource(ctx context.Context, noun string, args []string, stdin io.Reader) error {
	r, rest, err := c.resource(noun, args, stdin)
	if err != nil {
		return err
	}
	verb := noun + " " + args[0]
	switch verb {
	case "project list":
		return r.projectList(ctx, rest)
	case "project create":
		return r.projectCreate(ctx, rest)
	case "env list":
		return r.envList(ctx, rest)
	case "env create":
		return r.envCreate(ctx, rest)
	case "app list":
		return r.appList(ctx, rest)
	case "app show":
		return r.appShow(ctx, rest)
	case "app create":
		return r.appCreate(ctx, rest)
	case "app pull-secret":
		return r.appPullSecret(ctx, rest)
	case "app deploy-key":
		return r.appDeployKey(ctx, rest)
	case "route list":
		return r.routeList(ctx, rest)
	case "route add":
		return r.routeChange(ctx, rest, true)
	case "route remove":
		return r.routeChange(ctx, rest, false)
	case "secret list":
		return r.secretList(ctx, rest)
	case "secret set":
		return r.secretSet(ctx, rest)
	case "node list":
		return r.nodeList(ctx, rest)
	case "node token":
		return r.nodeToken(ctx, rest)
	case "webhook list":
		return r.webhookList(ctx, rest)
	case "webhook create":
		return r.webhookCreate(ctx, rest)
	case "webhook remove":
		return r.webhookRemove(ctx, rest)
	case "channel list":
		return r.channelList(ctx, rest)
	case "channel create":
		return r.channelCreate(ctx, rest)
	case "channel remove":
		return r.channelRemove(ctx, rest)
	case "audit list":
		return r.auditList(ctx, rest)
	case "token list":
		return r.tokenList(ctx, rest)
	case "token create":
		return r.tokenCreate(ctx, rest)
	case "token revoke":
		return r.tokenRevoke(ctx, rest)
	}
	return fmt.Errorf("unknown command %q: %w", verb, ErrUsage)
}

// --- project ---------------------------------------------------------------------

func (r *resource) projectList(ctx context.Context, args []string) error {
	if _, err := r.positional(args, 0, "no arguments"); err != nil {
		return err
	}
	resp := &bohuxv1.ListProjectsResponse{}
	token := ""
	for {
		page, err := r.projects.ListProjects(ctx, call(r.token, &bohuxv1.ListProjectsRequest{PageToken: token}))
		if err != nil {
			return fmt.Errorf("list projects: %w", err)
		}
		resp.Projects = append(resp.Projects, page.Msg.GetProjects()...)
		if token = page.Msg.GetNextPageToken(); token == "" {
			break
		}
	}
	rows := make([][]string, 0, len(resp.GetProjects()))
	for _, p := range resp.GetProjects() {
		rows = append(rows, []string{p.GetSlug(), p.GetDisplayName(), p.GetMeta().GetId()})
	}
	return r.emit(resp, []string{"SLUG", "NAME", "ID"}, rows)
}

func (r *resource) projectCreate(ctx context.Context, args []string) error {
	name := r.fs.String("name", "", "display name")
	pos, err := r.positional(args, 1, "a project slug")
	if err != nil {
		return err
	}
	resp, err := r.projects.CreateProject(ctx, call(r.token, &bohuxv1.CreateProjectRequest{
		Slug: pos[0], DisplayName: *name, IdempotencyKey: r.key()}))
	if err != nil {
		return fmt.Errorf("create project: %w", err)
	}
	p := resp.Msg.GetProject()
	return r.emit(resp.Msg, []string{"SLUG", "NAME", "ID"}, [][]string{{p.GetSlug(), p.GetDisplayName(), p.GetMeta().GetId()}})
}

// --- env -------------------------------------------------------------------------

func (r *resource) projectID(ctx context.Context) (string, error) {
	if *r.t.project == "" {
		return "", fmt.Errorf("-p (project) or %s is required: %w", EnvProject, ErrUsage)
	}
	p, err := r.projects.GetProject(ctx, call(r.token, &bohuxv1.GetProjectRequest{
		Lookup: &bohuxv1.GetProjectRequest_Slug{Slug: *r.t.project}}))
	if err != nil {
		return "", fmt.Errorf("project %q: %w", *r.t.project, err)
	}
	return p.Msg.GetProject().GetMeta().GetId(), nil
}

func (r *resource) envID(ctx context.Context) (string, error) {
	if *r.t.env == "" {
		return "", fmt.Errorf("-e (environment) is required: %w", ErrUsage)
	}
	project, err := r.projectID(ctx)
	if err != nil {
		return "", err
	}
	e, err := r.envs.GetEnvironment(ctx, call(r.token, &bohuxv1.GetEnvironmentRequest{
		Lookup: &bohuxv1.GetEnvironmentRequest_Slug{Slug: &bohuxv1.EnvironmentSlug{ProjectId: project, Slug: *r.t.env}}}))
	if err != nil {
		return "", fmt.Errorf("environment %q: %w", *r.t.env, err)
	}
	return e.Msg.GetEnvironment().GetMeta().GetId(), nil
}

func (r *resource) envList(ctx context.Context, args []string) error {
	if _, err := r.positional(args, 0, "no arguments"); err != nil {
		return err
	}
	project, err := r.projectID(ctx)
	if err != nil {
		return err
	}
	resp := &bohuxv1.ListEnvironmentsResponse{}
	token := ""
	for {
		page, err := r.envs.ListEnvironments(ctx, call(r.token, &bohuxv1.ListEnvironmentsRequest{ProjectId: project, PageToken: token}))
		if err != nil {
			return fmt.Errorf("list environments: %w", err)
		}
		resp.Environments = append(resp.Environments, page.Msg.GetEnvironments()...)
		if token = page.Msg.GetNextPageToken(); token == "" {
			break
		}
	}
	rows := make([][]string, 0, len(resp.GetEnvironments()))
	for _, e := range resp.GetEnvironments() {
		b := e.GetProviderBinding()
		rows = append(rows, []string{e.GetSlug(), b.GetProvider(), b.GetNodeId(), e.GetMeta().GetId()})
	}
	return r.emit(resp, []string{"SLUG", "PROVIDER", "NODE", "ID"}, rows)
}

func (r *resource) envCreate(ctx context.Context, args []string) error {
	node := r.fs.String("node", "", "slug of the node the environment runs on (required)")
	providerName := r.fs.String("provider", "docker", "provider the node runs it with")
	pos, err := r.positional(args, 1, "an environment slug")
	if err != nil {
		return err
	}
	if *node == "" {
		return fmt.Errorf("env create: --node is required: %w", ErrUsage)
	}
	project, err := r.projectID(ctx)
	if err != nil {
		return err
	}
	nodeID, err := r.nodeID(ctx, *node)
	if err != nil {
		return err
	}
	resp, err := r.envs.CreateEnvironment(ctx, call(r.token, &bohuxv1.CreateEnvironmentRequest{ProjectId: project,
		Slug: pos[0], ProviderBinding: &bohuxv1.ProviderBinding{Provider: *providerName,
			Target: &bohuxv1.ProviderBinding_NodeId{NodeId: nodeID}},
		IdempotencyKey: r.key()}))
	if err != nil {
		return fmt.Errorf("create environment: %w", err)
	}
	e := resp.Msg.GetEnvironment()
	return r.emit(resp.Msg, []string{"SLUG", "PROVIDER", "NODE", "ID"},
		[][]string{{e.GetSlug(), *providerName, nodeID, e.GetMeta().GetId()}})
}

// --- node ------------------------------------------------------------------------

func (r *resource) nodes(ctx context.Context) (*bohuxv1.ListNodesResponse, error) {
	resp := &bohuxv1.ListNodesResponse{}
	token := ""
	for {
		page, err := r.nodesAPI.ListNodes(ctx, call(r.token, &bohuxv1.ListNodesRequest{PageToken: token}))
		if err != nil {
			return nil, fmt.Errorf("list nodes: %w", err)
		}
		resp.Nodes = append(resp.Nodes, page.Msg.GetNodes()...)
		if token = page.Msg.GetNextPageToken(); token == "" {
			return resp, nil
		}
	}
}

func (r *resource) nodeID(ctx context.Context, slug string) (string, error) {
	resp, err := r.nodes(ctx)
	if err != nil {
		return "", err
	}
	for _, n := range resp.GetNodes() {
		if n.GetSlug() == slug {
			return n.GetMeta().GetId(), nil
		}
	}
	return "", fmt.Errorf("no node %q", slug)
}

func (r *resource) nodeList(ctx context.Context, args []string) error {
	if _, err := r.positional(args, 0, "no arguments"); err != nil {
		return err
	}
	resp, err := r.nodes(ctx)
	if err != nil {
		return err
	}
	rows := make([][]string, 0, len(resp.GetNodes()))
	for _, n := range resp.GetNodes() {
		seen := "never"
		if n.GetLastSeenAt() != nil {
			seen = n.GetLastSeenAt().AsTime().UTC().Format(time.RFC3339)
		}
		cpu, mem := "-", "-"
		if u := n.GetUsage(); u != nil {
			cpu = fmt.Sprintf("%.0f%% of %d", u.GetCpuBusy()*100, u.GetCpuCores())
			mem = fmt.Sprintf("%s / %s", bytesHuman(u.GetMemoryTotalBytes()-u.GetMemoryAvailableBytes()), bytesHuman(u.GetMemoryTotalBytes()))
		}
		rows = append(rows, []string{n.GetSlug(), strings.ToLower(strings.TrimPrefix(n.GetState().String(), "NODE_STATE_")),
			n.GetAgentVersion(), seen, cpu, mem, n.GetMeta().GetId()})
	}
	return r.emit(resp, []string{"SLUG", "STATE", "AGENT", "LAST SEEN", "CPU", "MEMORY", "ID"}, rows)
}

// nodeToken prints a bootstrap token for `bohux agent register` on a new node. The
// token is a credential: it goes to stdout only, once.
func (r *resource) nodeToken(ctx context.Context, args []string) error {
	pos, err := r.positional(args, 1, "the new node's slug")
	if err != nil {
		return err
	}
	resp, err := r.nodesAPI.CreateBootstrapToken(ctx, call(r.token, &bohuxv1.CreateBootstrapTokenRequest{
		NodeSlug: pos[0], IdempotencyKey: r.key()}))
	if err != nil {
		return fmt.Errorf("create bootstrap token: %w", err)
	}
	m := resp.Msg
	return r.emit(m, []string{"TOKEN", "CA PIN", "EXPIRES"},
		[][]string{{m.GetToken(), m.GetCaCertSha256(), m.GetExpiresAt().AsTime().UTC().Format(time.RFC3339)}})
}

// --- secret ----------------------------------------------------------------------

func (r *resource) secretList(ctx context.Context, args []string) error {
	if _, err := r.positional(args, 0, "no arguments"); err != nil {
		return err
	}
	env, err := r.envID(ctx)
	if err != nil {
		return err
	}
	resp := &bohuxv1.ListSecretRefsResponse{}
	token := ""
	for {
		page, err := r.secrets.ListSecretRefs(ctx, call(r.token, &bohuxv1.ListSecretRefsRequest{EnvironmentId: env, PageToken: token}))
		if err != nil {
			return fmt.Errorf("list secrets: %w", err)
		}
		resp.Secrets = append(resp.Secrets, page.Msg.GetSecrets()...)
		if token = page.Msg.GetNextPageToken(); token == "" {
			break
		}
	}
	rows := make([][]string, 0, len(resp.GetSecrets()))
	for _, s := range resp.GetSecrets() {
		rows = append(rows, []string{s.GetKey(), s.GetHandle()})
	}
	return r.emit(resp, []string{"KEY", "HANDLE"}, rows)
}

// secretSet reads the value from stdin (ADR-0036 item 4), never from an argument, and
// prints only the handle. With --app it also adds the secret to that App.
func (r *resource) secretSet(ctx context.Context, args []string) error {
	app := r.fs.String("app", "", "also give this app the secret as an environment variable")
	pos, err := r.positional(args, 1, "the secret's key, e.g. DATABASE_URL")
	if err != nil {
		return err
	}
	if r.stdin == nil {
		return fmt.Errorf("secret set: the value is read from stdin: %w", ErrUsage)
	}
	raw, err := io.ReadAll(io.LimitReader(r.stdin, 1<<20))
	if err != nil {
		return fmt.Errorf("secret set: read the value from stdin: %w", err)
	}
	value := strings.TrimSuffix(string(raw), "\n")
	if value == "" {
		return errors.New("secret set: the value on stdin is empty")
	}
	env, err := r.envID(ctx)
	if err != nil {
		return err
	}
	resp, err := r.secrets.SetSecret(ctx, call(r.token, &bohuxv1.SetSecretRequest{EnvironmentId: env,
		Key: pos[0], Value: value, IdempotencyKey: r.key()}))
	if err != nil {
		return fmt.Errorf("set secret: %w", err)
	}
	s := resp.Msg.GetSecret()
	if *app != "" {
		if err := r.attachSecret(ctx, *app, s); err != nil {
			return err
		}
	}
	return r.emit(resp.Msg, []string{"KEY", "HANDLE"}, [][]string{{s.GetKey(), s.GetHandle()}})
}

func (r *resource) attachSecret(ctx context.Context, slug string, s *bohuxv1.Secret) error {
	return r.updateApp(ctx, slug, func(a *bohuxv1.App) bool {
		for _, ref := range a.GetSecrets() {
			if ref.GetKey() == s.GetKey() {
				if ref.GetHandle() == s.GetHandle() {
					return false
				}
				ref.Handle = s.GetHandle()
				return true
			}
		}
		a.Secrets = append(a.Secrets, &bohuxv1.SecretRef{Key: s.GetKey(), Handle: s.GetHandle()})
		return true
	})
}

// updateApp applies change to the App's current state and writes it back, unless
// change reports nothing changed.
func (r *resource) updateApp(ctx context.Context, slug string, change func(*bohuxv1.App) bool) error {
	a, err := r.app(ctx, r.t, slug)
	if err != nil {
		return err
	}
	if !change(a) {
		return nil
	}
	_, err = r.apps.UpdateApp(ctx, call(r.token, &bohuxv1.UpdateAppRequest{Id: a.GetMeta().GetId(),
		Source: a.GetSource(), Runtime: a.GetRuntime(), Secrets: a.GetSecrets(),
		ProviderOptions: a.GetProviderOptions(), Routes: a.GetRoutes(), ImagePullSecret: a.GetImagePullSecret(),
		ExpectedRevisionId: a.GetMeta().GetRevisionId(), IdempotencyKey: r.key()}))
	if err != nil {
		return fmt.Errorf("update app %q: %w", slug, err)
	}
	return nil
}

// --- app -------------------------------------------------------------------------

func appRow(a *bohuxv1.App) []string {
	src := ""
	switch k := a.GetSource().GetKind().(type) {
	case *bohuxv1.Source_Image:
		src = k.Image.GetRepository() + "@" + k.Image.GetDigest()
	case *bohuxv1.Source_Git:
		src = k.Git.GetRepoUrl()
	}
	var hosts []string
	for _, rt := range a.GetRoutes() {
		hosts = append(hosts, rt.GetHost())
	}
	return []string{a.GetSlug(), strconv.Itoa(int(a.GetRuntime().GetReplicas())), strings.Join(hosts, ","), src}
}

var appHeader = []string{"SLUG", "REPLICAS", "ROUTES", "SOURCE"}

func (r *resource) appList(ctx context.Context, args []string) error {
	if _, err := r.positional(args, 0, "no arguments"); err != nil {
		return err
	}
	env, err := r.envID(ctx)
	if err != nil {
		return err
	}
	resp := &bohuxv1.ListAppsResponse{}
	token := ""
	for {
		page, err := r.apps.ListApps(ctx, call(r.token, &bohuxv1.ListAppsRequest{EnvironmentId: env, PageToken: token}))
		if err != nil {
			return fmt.Errorf("list apps: %w", err)
		}
		resp.Apps = append(resp.Apps, page.Msg.GetApps()...)
		if token = page.Msg.GetNextPageToken(); token == "" {
			break
		}
	}
	rows := make([][]string, 0, len(resp.GetApps()))
	for _, a := range resp.GetApps() {
		rows = append(rows, appRow(a))
	}
	return r.emit(resp, appHeader, rows)
}

func (r *resource) appShow(ctx context.Context, args []string) error {
	pos, err := r.positional(args, 1, "an app slug")
	if err != nil {
		return err
	}
	a, err := r.app(ctx, r.t, pos[0])
	if err != nil {
		return err
	}
	if *r.output == "json" {
		return r.emit(&bohuxv1.GetAppResponse{App: a}, nil, nil)
	}
	// The latest resource sample, when the node has reported one (ADR-0040).
	cpu, mem := "-", "-"
	if u, err := r.workloads.GetAppUsage(ctx, call(r.token, &bohuxv1.GetAppUsageRequest{AppId: a.GetMeta().GetId()})); err == nil &&
		u.Msg.GetUsage() != nil {
		cpu = fmt.Sprintf("%dm", u.Msg.GetUsage().GetCpuMillicores())
		mem = bytesHuman(u.Msg.GetUsage().GetMemoryBytes())
	}
	pull := a.GetImagePullSecret()
	if pull == "" {
		pull = "-"
	}
	return r.emit(&bohuxv1.GetAppResponse{App: a}, append(append([]string{}, appHeader...), "CPU", "MEMORY", "PULL SECRET"),
		[][]string{append(appRow(a), cpu, mem, pull)})
}

// appDeployKey sets or clears the secret a Git App's source is fetched with
// (ADR-0041). The next build uses it.
func (r *resource) appDeployKey(ctx context.Context, args []string) error {
	key := r.fs.String("secret", "", "environment secret holding a private SSH key")
	none := r.fs.Bool("clear", false, "fetch without a key")
	pos, err := r.positional(args, 1, "an app slug")
	if err != nil {
		return err
	}
	if (*key == "") == !*none {
		return fmt.Errorf("app deploy-key: give --secret KEY or --clear: %w", ErrUsage)
	}
	var cerr error
	uerr := r.updateApp(ctx, pos[0], func(a *bohuxv1.App) bool {
		git := a.GetSource().GetGit()
		if git == nil {
			cerr = fmt.Errorf("app deploy-key: %s is not built from Git: %w", pos[0], ErrUsage)
			return false
		}
		if git.GetSshKeySecret() == *key {
			return false
		}
		git.SshKeySecret = *key
		return true
	})
	if cerr != nil {
		return cerr
	}
	return uerr
}

// appPullSecret sets or clears the secret the App's image is pulled with (ADR-0041).
// It is part of the App's spec: the next deploy uses it.
func (r *resource) appPullSecret(ctx context.Context, args []string) error {
	key := r.fs.String("secret", "", "environment secret holding a Docker config.json")
	none := r.fs.Bool("clear", false, "pull without a credential")
	pos, err := r.positional(args, 1, "an app slug")
	if err != nil {
		return err
	}
	if (*key == "") == !*none {
		return fmt.Errorf("app pull-secret: give --secret KEY or --clear: %w", ErrUsage)
	}
	return r.updateApp(ctx, pos[0], func(a *bohuxv1.App) bool {
		if a.GetImagePullSecret() == *key {
			return false
		}
		a.ImagePullSecret = *key
		return true
	})
}

// repeated is a flag that may be given more than once.
type repeated []string

func (s *repeated) String() string     { return strings.Join(*s, ",") }
func (s *repeated) Set(v string) error { *s = append(*s, v); return nil }

func (r *resource) appCreate(ctx context.Context, args []string) error {
	image := r.fs.String("image", "", "prebuilt image, repository@sha256:<digest>")
	git := r.fs.String("git", "", "Git repository URL to build from (Dockerfile)")
	ref := r.fs.String("ref", "", "default branch, tag, or commit to build")
	deployKey := r.fs.String("ssh-key-secret", "", "environment secret holding an SSH deploy key for --git")
	replicas := r.fs.Int("replicas", 1, "replicas")
	health := r.fs.String("health", "", "HTTP health check, PATH or PATH:PORT (default port: the first)")
	pull := r.fs.String("image-pull-secret", "", "environment secret holding a Docker config.json for a private registry")
	var ports, routes repeated
	r.fs.Var(&ports, "port", "container port, or CONTAINER:HOST to publish on the host; repeatable")
	r.fs.Var(&routes, "route", "host the node's ingress serves, HOST or HOST:PORT; repeatable")
	pos, err := r.positional(args, 1, "an app slug")
	if err != nil {
		return err
	}
	src := &bohuxv1.Source{}
	switch {
	case *image != "" && *git != "":
		return fmt.Errorf("app create: --image and --git are exclusive: %w", ErrUsage)
	case *image != "" && *deployKey != "":
		return fmt.Errorf("app create: --ssh-key-secret needs --git: %w", ErrUsage)
	case *image != "":
		img, err := parseImage(*image)
		if err != nil {
			return err
		}
		src.Kind = &bohuxv1.Source_Image{Image: img}
	case *git != "":
		src.Kind = &bohuxv1.Source_Git{Git: &bohuxv1.GitSource{RepoUrl: *git, Ref: *ref, SshKeySecret: *deployKey}}
	default:
		return fmt.Errorf("app create: --image or --git is required: %w", ErrUsage)
	}
	rt, rs, err := runtimeFromFlags(*replicas, ports, *health, routes)
	if err != nil {
		return err
	}
	env, err := r.envID(ctx)
	if err != nil {
		return err
	}
	resp, err := r.apps.CreateApp(ctx, call(r.token, &bohuxv1.CreateAppRequest{EnvironmentId: env, Slug: pos[0],
		Source: src, Runtime: rt, Routes: rs, ImagePullSecret: *pull, IdempotencyKey: r.key()}))
	if err != nil {
		return fmt.Errorf("create app: %w", err)
	}
	return r.emit(resp.Msg, appHeader, [][]string{appRow(resp.Msg.GetApp())})
}

// runtimeFromFlags builds the Runtime and routes of `app create`.
func runtimeFromFlags(replicas int, ports []string, health string, routes []string) (*bohuxv1.Runtime, []*bohuxv1.Route, error) {
	rt := &bohuxv1.Runtime{Replicas: int32(replicas)}
	for _, p := range ports {
		c, h, _ := strings.Cut(p, ":")
		cp, err := strconv.Atoi(c)
		if err != nil {
			return nil, nil, fmt.Errorf("--port %q: want CONTAINER or CONTAINER:HOST: %w", p, ErrUsage)
		}
		port := &bohuxv1.Port{ContainerPort: int32(cp)}
		if h != "" {
			hp, err := strconv.Atoi(h)
			if err != nil {
				return nil, nil, fmt.Errorf("--port %q: want CONTAINER or CONTAINER:HOST: %w", p, ErrUsage)
			}
			port.HostPort = int32(hp)
		}
		rt.Ports = append(rt.Ports, port)
	}
	first := int32(0)
	if len(rt.GetPorts()) > 0 {
		first = rt.GetPorts()[0].GetContainerPort()
	}
	if health != "" {
		path, p, err := hostPort(health, first, "--health", "PATH or PATH:PORT")
		if err != nil {
			return nil, nil, err
		}
		rt.Health = &bohuxv1.HealthCheck{Path: path, Port: p}
	}
	var rs []*bohuxv1.Route
	for _, s := range routes {
		host, p, err := hostPort(s, first, "--route", "HOST or HOST:PORT")
		if err != nil {
			return nil, nil, err
		}
		rs = append(rs, &bohuxv1.Route{Host: host, Port: p})
	}
	return rt, rs, nil
}

// hostPort splits NAME[:PORT], defaulting the port to def.
func hostPort(s string, def int32, flagName, want string) (string, int32, error) {
	name, p, ok := strings.Cut(s, ":")
	if !ok {
		if def == 0 {
			return "", 0, fmt.Errorf("%s %q: no --port to default to; want %s: %w", flagName, s, want, ErrUsage)
		}
		return name, def, nil
	}
	n, err := strconv.Atoi(p)
	if err != nil || name == "" {
		return "", 0, fmt.Errorf("%s %q: want %s: %w", flagName, s, want, ErrUsage)
	}
	return name, int32(n), nil
}

// --- route -----------------------------------------------------------------------

func (r *resource) routeList(ctx context.Context, args []string) error {
	pos, err := r.positional(args, 1, "an app slug")
	if err != nil {
		return err
	}
	a, err := r.app(ctx, r.t, pos[0])
	if err != nil {
		return err
	}
	rows := make([][]string, 0, len(a.GetRoutes()))
	for _, rt := range a.GetRoutes() {
		rows = append(rows, []string{rt.GetHost(), strconv.Itoa(int(rt.GetPort()))})
	}
	return r.emit(&bohuxv1.App{Routes: a.GetRoutes()}, []string{"HOST", "PORT"}, rows)
}

// routeChange adds HOST[:PORT] to the App's routes, or removes HOST. It takes effect
// at once, without a deploy (ADR-0034).
func (r *resource) routeChange(ctx context.Context, args []string, add bool) error {
	pos, err := r.positional(args, 2, "an app slug and a host")
	if err != nil {
		return err
	}
	slug, spec := pos[0], pos[1]
	var cerr error
	uerr := r.updateApp(ctx, slug, func(a *bohuxv1.App) bool {
		first := int32(0)
		if ps := a.GetRuntime().GetPorts(); len(ps) > 0 {
			first = ps[0].GetContainerPort()
		}
		host, port, perr := hostPort(spec, first, "route", "HOST or HOST:PORT")
		if !add {
			host, _, _ = strings.Cut(spec, ":")
		}
		kept := a.GetRoutes()[:0]
		changed := false
		for _, rt := range a.GetRoutes() {
			if rt.GetHost() == host {
				changed = true
				continue
			}
			kept = append(kept, rt)
		}
		a.Routes = kept
		if add {
			if perr != nil {
				cerr = perr
				return false
			}
			a.Routes = append(a.Routes, &bohuxv1.Route{Host: host, Port: port})
			return true
		}
		if !changed {
			cerr = fmt.Errorf("route remove: %s has no route %q", slug, host)
		}
		return changed
	})
	if cerr != nil {
		return cerr
	}
	return uerr
}

// --- webhook ---------------------------------------------------------------------

func (r *resource) hookURL(path string) string {
	base := r.getenv(EnvAPIURL)
	if base == "" {
		base = DefaultAPIURL
	}
	return strings.TrimSuffix(base, "/") + path
}

func flavorName(f bohuxv1.WebhookFlavor) string {
	if f == bohuxv1.WebhookFlavor_WEBHOOK_FLAVOR_GENERIC {
		return "generic"
	}
	return "github"
}

func (r *resource) webhooksOf(ctx context.Context, appID string) (*bohuxv1.ListWebhooksResponse, error) {
	resp := &bohuxv1.ListWebhooksResponse{}
	token := ""
	for {
		page, err := r.webhooks.ListWebhooks(ctx, call(r.token, &bohuxv1.ListWebhooksRequest{AppId: appID, PageToken: token}))
		if err != nil {
			return nil, fmt.Errorf("list webhooks: %w", err)
		}
		resp.Webhooks = append(resp.Webhooks, page.Msg.GetWebhooks()...)
		if token = page.Msg.GetNextPageToken(); token == "" {
			return resp, nil
		}
	}
}

func (r *resource) webhookList(ctx context.Context, args []string) error {
	pos, err := r.positional(args, 1, "an app slug")
	if err != nil {
		return err
	}
	a, err := r.app(ctx, r.t, pos[0])
	if err != nil {
		return err
	}
	resp, err := r.webhooksOf(ctx, a.GetMeta().GetId())
	if err != nil {
		return err
	}
	rows := make([][]string, 0, len(resp.GetWebhooks()))
	for _, w := range resp.GetWebhooks() {
		paths := strings.Join(w.GetPaths(), ",")
		if paths == "" {
			paths = "*"
		}
		rows = append(rows, []string{w.GetMeta().GetId(), w.GetBranch(), flavorName(w.GetFlavor()), paths, r.hookURL(w.GetPath())})
	}
	return r.emit(resp, []string{"ID", "BRANCH", "FLAVOR", "PATHS", "URL"}, rows)
}

// webhookCreate prints the URL and the signing secret: the only time the secret is
// shown (ADR-0037).
func (r *resource) webhookCreate(ctx context.Context, args []string) error {
	branch := r.fs.String("branch", "main", "branch whose pushes deploy")
	flavor := r.fs.String("flavor", "github", "github (GitHub, Gitea) or generic")
	var paths repeated
	r.fs.Var(&paths, "path", "deploy only when a push changes files under this path, e.g. services/api; repeatable (ADR-0044)")
	pos, err := r.positional(args, 1, "an app slug")
	if err != nil {
		return err
	}
	f := bohuxv1.WebhookFlavor_WEBHOOK_FLAVOR_GITHUB
	switch *flavor {
	case "github":
	case "generic":
		f = bohuxv1.WebhookFlavor_WEBHOOK_FLAVOR_GENERIC
	default:
		return fmt.Errorf("webhook create: --flavor must be github or generic: %w", ErrUsage)
	}
	a, err := r.app(ctx, r.t, pos[0])
	if err != nil {
		return err
	}
	resp, err := r.webhooks.CreateWebhook(ctx, call(r.token, &bohuxv1.CreateWebhookRequest{
		AppId: a.GetMeta().GetId(), Branch: *branch, Flavor: f, Paths: paths, IdempotencyKey: r.key()}))
	if err != nil {
		return fmt.Errorf("create webhook: %w", err)
	}
	w := resp.Msg.GetWebhook()
	return r.emit(resp.Msg, []string{"ID", "BRANCH", "URL", "SECRET"},
		[][]string{{w.GetMeta().GetId(), w.GetBranch(), r.hookURL(w.GetPath()), resp.Msg.GetSecret()}})
}

func (r *resource) webhookRemove(ctx context.Context, args []string) error {
	pos, err := r.positional(args, 2, "an app slug and a webhook ID")
	if err != nil {
		return err
	}
	a, err := r.app(ctx, r.t, pos[0])
	if err != nil {
		return err
	}
	list, err := r.webhooksOf(ctx, a.GetMeta().GetId())
	if err != nil {
		return err
	}
	for _, w := range list.GetWebhooks() {
		if w.GetMeta().GetId() != pos[1] {
			continue
		}
		resp, err := r.webhooks.DeleteWebhook(ctx, call(r.token, &bohuxv1.DeleteWebhookRequest{Id: pos[1],
			ExpectedRevisionId: w.GetMeta().GetRevisionId(), IdempotencyKey: r.key()}))
		if err != nil {
			return fmt.Errorf("remove webhook: %w", err)
		}
		return r.emit(resp.Msg, []string{"REMOVED"}, [][]string{{pos[1]}})
	}
	return fmt.Errorf("webhook remove: %s has no webhook %q", pos[0], pos[1])
}

// --- channel ---------------------------------------------------------------------

func channelRow(c *bohuxv1.NotificationChannel) []string {
	var states []string
	for _, s := range c.GetStates() {
		states = append(states, state(s.String()))
	}
	kind := strings.ToLower(strings.TrimPrefix(c.GetKind().String(), "NOTIFICATION_KIND_"))
	return []string{c.GetMeta().GetId(), kind, c.GetUrlHost(), strings.Join(states, ",")}
}

var channelHeader = []string{"ID", "KIND", "HOST", "STATES"}

func (r *resource) channels(ctx context.Context, project string) (*bohuxv1.ListNotificationChannelsResponse, error) {
	resp := &bohuxv1.ListNotificationChannelsResponse{}
	token := ""
	for {
		page, err := r.notifications.ListNotificationChannels(ctx, call(r.token,
			&bohuxv1.ListNotificationChannelsRequest{ProjectId: project, PageToken: token}))
		if err != nil {
			return nil, fmt.Errorf("list channels: %w", err)
		}
		resp.Channels = append(resp.Channels, page.Msg.GetChannels()...)
		if token = page.Msg.GetNextPageToken(); token == "" {
			return resp, nil
		}
	}
}

func (r *resource) channelList(ctx context.Context, args []string) error {
	if _, err := r.positional(args, 0, "no arguments"); err != nil {
		return err
	}
	project, err := r.projectID(ctx)
	if err != nil {
		return err
	}
	resp, err := r.channels(ctx, project)
	if err != nil {
		return err
	}
	rows := make([][]string, 0, len(resp.GetChannels()))
	for _, c := range resp.GetChannels() {
		rows = append(rows, channelRow(c))
	}
	return r.emit(resp, channelHeader, rows)
}

// channelCreate reads the URL from stdin: a chat webhook URL is a credential
// (ADR-0038, ADR-0036 item 4). For email it is mailto:a@x,b@y (ADR-0043).
func (r *resource) channelCreate(ctx context.Context, args []string) error {
	kind := r.fs.String("kind", "", "slack, discord, webhook, or email (required)")
	var states repeated
	r.fs.Var(&states, "state", "deployment end state to post (succeeded, failed, rolled_back, cancelled); "+
		"repeatable; default failed, rolled_back, cancelled")
	if _, err := r.positional(args, 0, "no arguments"); err != nil {
		return err
	}
	k, ok := map[string]bohuxv1.NotificationKind{"slack": bohuxv1.NotificationKind_NOTIFICATION_KIND_SLACK,
		"discord": bohuxv1.NotificationKind_NOTIFICATION_KIND_DISCORD,
		"webhook": bohuxv1.NotificationKind_NOTIFICATION_KIND_WEBHOOK,
		"email":   bohuxv1.NotificationKind_NOTIFICATION_KIND_EMAIL}[*kind]
	if !ok {
		return fmt.Errorf("channel create: --kind must be slack, discord, webhook, or email: %w", ErrUsage)
	}
	var sts []bohuxv1.DeploymentState
	for _, s := range states {
		v, ok := bohuxv1.DeploymentState_value["DEPLOYMENT_STATE_"+strings.ToUpper(s)]
		if !ok {
			return fmt.Errorf("channel create: unknown --state %q: %w", s, ErrUsage)
		}
		sts = append(sts, bohuxv1.DeploymentState(v))
	}
	if r.stdin == nil {
		return fmt.Errorf("channel create: the URL is read from stdin: %w", ErrUsage)
	}
	raw, err := io.ReadAll(io.LimitReader(r.stdin, 8192))
	if err != nil {
		return fmt.Errorf("channel create: read the URL from stdin: %w", err)
	}
	u := strings.TrimSpace(string(raw))
	if u == "" {
		return errors.New("channel create: the URL on stdin is empty")
	}
	project, err := r.projectID(ctx)
	if err != nil {
		return err
	}
	resp, err := r.notifications.CreateNotificationChannel(ctx, call(r.token, &bohuxv1.CreateNotificationChannelRequest{
		ProjectId: project, Kind: k, Url: u, States: sts, IdempotencyKey: r.key()}))
	if err != nil {
		return fmt.Errorf("create channel: %w", err)
	}
	row := channelRow(resp.Msg.GetChannel())
	header := channelHeader
	if s := resp.Msg.GetSigningSecret(); s != "" {
		header, row = append(append([]string{}, header...), "SIGNING SECRET"), append(row, s)
	}
	return r.emit(resp.Msg, header, [][]string{row})
}

func (r *resource) channelRemove(ctx context.Context, args []string) error {
	pos, err := r.positional(args, 1, "a channel ID")
	if err != nil {
		return err
	}
	project, err := r.projectID(ctx)
	if err != nil {
		return err
	}
	list, err := r.channels(ctx, project)
	if err != nil {
		return err
	}
	for _, c := range list.GetChannels() {
		if c.GetMeta().GetId() != pos[0] {
			continue
		}
		resp, err := r.notifications.DeleteNotificationChannel(ctx, call(r.token, &bohuxv1.DeleteNotificationChannelRequest{
			Id: pos[0], ExpectedRevisionId: c.GetMeta().GetRevisionId(), IdempotencyKey: r.key()}))
		if err != nil {
			return fmt.Errorf("remove channel: %w", err)
		}
		return r.emit(resp.Msg, []string{"REMOVED"}, [][]string{{pos[0]}})
	}
	return fmt.Errorf("channel remove: project %s has no channel %q", *r.t.project, pos[0])
}

// --- audit -----------------------------------------------------------------------

// auditList prints the trail, newest first: a project's with -p, else the tenant's
// (which needs a tenant-wide token).
func (r *resource) auditList(ctx context.Context, args []string) error {
	limit := r.fs.Int("limit", 50, "how many events")
	if _, err := r.positional(args, 0, "no arguments"); err != nil {
		return err
	}
	req := &bohuxv1.ListAuditEventsRequest{PageSize: int32(*limit)}
	if *r.t.project != "" {
		project, err := r.projectID(ctx)
		if err != nil {
			return err
		}
		req.ProjectId = project
	}
	resp, err := r.audit.ListAuditEvents(ctx, call(r.token, req))
	if err != nil {
		return fmt.Errorf("list audit events: %w", err)
	}
	rows := make([][]string, 0, len(resp.Msg.GetAuditEvents()))
	for _, e := range resp.Msg.GetAuditEvents() {
		kind := strings.ToLower(strings.TrimPrefix(e.GetActor().GetKind().String(), "PRINCIPAL_KIND_"))
		rows = append(rows, []string{e.GetOccurredAt().AsTime().UTC().Format(time.RFC3339),
			kind + ":" + shortID(e.GetActor().GetId()), e.GetAction(),
			strings.TrimPrefix(e.GetResource().GetType(), "bohux.v1.") + ":" + shortID(e.GetResource().GetId())})
	}
	return r.emit(resp.Msg, []string{"TIME", "ACTOR", "ACTION", "RESOURCE"}, rows)
}

func shortID(id string) string {
	if len(id) > 8 {
		return id[len(id)-8:]
	}
	return id
}

// --- token -----------------------------------------------------------------------

var tokenHeader = []string{"ID", "NAME", "PROJECT", "ROLE", "EXPIRES", "LAST USED", "REVOKED"}

// stamp prints a time in UTC, or "-" when it is unset.
func stamp(t *timestamppb.Timestamp) string {
	if t == nil {
		return "-"
	}
	return t.AsTime().UTC().Format(time.RFC3339)
}

func roleName(role bohuxv1.Role) string {
	return strings.ToLower(strings.TrimPrefix(role.String(), "ROLE_"))
}

// tokenRow names the token's project by slug when it is known; a tenant-wide token
// shows "*".
func tokenRow(t *bohuxv1.ApiToken, slugs map[string]string) []string {
	project := "*"
	if id := t.GetProjectId(); id != "" {
		if project = slugs[id]; project == "" {
			project = id
		}
	}
	return []string{t.GetMeta().GetId(), t.GetName(), project, roleName(t.GetRole()),
		stamp(t.GetExpiresAt()), stamp(t.GetLastUsedAt()), stamp(t.GetRevokedAt())}
}

// projectSlugs maps the projects this token can see, by ID, to their slugs.
func (r *resource) projectSlugs(ctx context.Context) map[string]string {
	out := map[string]string{}
	token := ""
	for {
		page, err := r.projects.ListProjects(ctx, call(r.token, &bohuxv1.ListProjectsRequest{PageToken: token}))
		if err != nil {
			return out // IDs are shown instead
		}
		for _, p := range page.Msg.GetProjects() {
			out[p.GetMeta().GetId()] = p.GetSlug()
		}
		if token = page.Msg.GetNextPageToken(); token == "" {
			return out
		}
	}
}

func (r *resource) tokenList(ctx context.Context, args []string) error {
	if _, err := r.positional(args, 0, "no arguments"); err != nil {
		return err
	}
	resp := &bohuxv1.ListApiTokensResponse{}
	token := ""
	for {
		page, err := r.identity.ListApiTokens(ctx, call(r.token, &bohuxv1.ListApiTokensRequest{PageToken: token}))
		if err != nil {
			return fmt.Errorf("list tokens: %w", err)
		}
		resp.ApiTokens = append(resp.ApiTokens, page.Msg.GetApiTokens()...)
		if token = page.Msg.GetNextPageToken(); token == "" {
			break
		}
	}
	slugs := r.projectSlugs(ctx)
	rows := make([][]string, 0, len(resp.GetApiTokens()))
	for _, t := range resp.GetApiTokens() {
		rows = append(rows, tokenRow(t, slugs))
	}
	return r.emit(resp, tokenHeader, rows)
}

func (r *resource) tokenCreate(ctx context.Context, args []string) error {
	role := r.fs.String("role", "", "viewer, deployer, or owner (required)")
	expires := r.fs.Duration("expires", 0, "lifetime, e.g. 720h; none by default")
	pos, err := r.positional(args, 1, "a token name")
	if err != nil {
		return err
	}
	v, ok := bohuxv1.Role_value["ROLE_"+strings.ToUpper(*role)]
	if !ok || v == 0 {
		return fmt.Errorf("token create: --role must be viewer, deployer, or owner: %w", ErrUsage)
	}
	if *expires < 0 {
		return fmt.Errorf("token create: --expires must be positive: %w", ErrUsage)
	}
	req := &bohuxv1.CreateApiTokenRequest{Name: pos[0], Role: bohuxv1.Role(v), IdempotencyKey: r.key()}
	// Only an explicit -p scopes the token: a BOHUX_PROJECT left in the environment for
	// app commands must not quietly narrow it.
	scoped := false
	r.fs.Visit(func(f *flag.Flag) { scoped = scoped || f.Name == "p" })
	if scoped && *r.t.project != "" {
		if req.ProjectId, err = r.projectID(ctx); err != nil {
			return err
		}
	}
	if *expires > 0 {
		req.ExpiresAt = timestamppb.New(time.Now().Add(*expires))
	}
	resp, err := r.identity.CreateApiToken(ctx, call(r.token, req))
	if err != nil {
		return fmt.Errorf("create token: %w", err)
	}
	// The secret is returned this once (ADR-0022); it goes to stdout, never a log.
	row := append(tokenRow(resp.Msg.GetApiToken(), map[string]string{req.GetProjectId(): *r.t.project}), resp.Msg.GetSecret())
	return r.emit(resp.Msg, append(append([]string{}, tokenHeader...), "SECRET"), [][]string{row})
}

func (r *resource) tokenRevoke(ctx context.Context, args []string) error {
	pos, err := r.positional(args, 1, "a token ID")
	if err != nil {
		return err
	}
	resp, err := r.identity.RevokeApiToken(ctx, call(r.token, &bohuxv1.RevokeApiTokenRequest{Id: pos[0], IdempotencyKey: r.key()}))
	if err != nil {
		return fmt.Errorf("revoke token %s: %w", pos[0], err)
	}
	return r.emit(resp.Msg, tokenHeader, [][]string{tokenRow(resp.Msg.GetApiToken(), r.projectSlugs(ctx))})
}

// bytesHuman prints a byte count as KiB, MiB, or GiB.
func bytesHuman(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1fGiB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.0fMiB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0fKiB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%dB", n)
}
