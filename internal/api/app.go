package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"

	bohuxv1 "github.com/bohumix/bohux/gen/bohux/v1"
	"github.com/bohumix/bohux/gen/bohux/v1/bohuxv1connect"
	"github.com/bohumix/bohux/internal/auth"
	"github.com/bohumix/bohux/internal/store"
)

const appType = "bohux.v1.App"

var (
	envVarName  = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)
	imageDigest = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

	// Stored form of nested messages (data-model.md, "Schema rules"). Proto names, not
	// JSON names, so a stored row reads like the .proto file.
	storeJSON = protojson.MarshalOptions{UseProtoNames: true}
	loadJSON  = protojson.UnmarshalOptions{DiscardUnknown: true}
)

type appService struct {
	s *Server
}

// appState is an App's mutable state, as one revision stores it.
type appState struct {
	source  *bohuxv1.Source
	runtime *bohuxv1.Runtime
	secrets []*bohuxv1.SecretRef
	options *structpb.Struct
	routes  []*bohuxv1.Route
	// pullSecret is the key of an environment secret (ADR-0041).
	pullSecret string
}

func stateOf(app *bohuxv1.App) appState {
	return appState{app.GetSource(), app.GetRuntime(), app.GetSecrets(), app.GetProviderOptions(), app.GetRoutes(),
		app.GetImagePullSecret()}
}

// appSpec validates an App's mutable state and returns its revision columns.
func appSpec(ctx context.Context, tx *store.Tx, env uuid.UUID, st appState) ([]store.Col, error) {
	if err := validSource(st.source); err != nil {
		return nil, err
	}
	if err := validRuntime(st.runtime); err != nil {
		return nil, err
	}
	if err := validSecretRefs(ctx, tx, env, st.secrets); err != nil {
		return nil, err
	}
	if err := validRoutes(st.routes, st.runtime); err != nil {
		return nil, err
	}
	if err := validPullSecret(ctx, tx, env, st.pullSecret); err != nil {
		return nil, err
	}
	srcJSON, err := storeJSON.Marshal(st.source)
	if err != nil {
		return nil, fmt.Errorf("encode source: %w", err)
	}
	rtJSON, err := storeJSON.Marshal(st.runtime)
	if err != nil {
		return nil, fmt.Errorf("encode runtime: %w", err)
	}
	secJSON, err := jsonArray(st.secrets)
	if err != nil {
		return nil, fmt.Errorf("encode secret refs: %w", err)
	}
	routesJSON, err := jsonArray(st.routes)
	if err != nil {
		return nil, fmt.Errorf("encode routes: %w", err)
	}
	optJSON := []byte("{}")
	if len(st.options.GetFields()) > 0 {
		if optJSON, err = protojson.Marshal(st.options); err != nil {
			return nil, fmt.Errorf("encode provider options: %w", err)
		}
	}
	return []store.Col{
		{Name: "source", Value: srcJSON}, {Name: "runtime", Value: rtJSON}, {Name: "secrets", Value: secJSON},
		{Name: "provider_options", Value: optJSON}, {Name: "routes", Value: routesJSON},
		{Name: "image_pull_secret", Value: st.pullSecret},
	}, nil
}

// jsonArray stores repeated messages as a JSON array in proto field names.
func jsonArray[M proto.Message](ms []M) ([]byte, error) {
	items := make([]json.RawMessage, 0, len(ms))
	for _, m := range ms {
		b, err := storeJSON.Marshal(m)
		if err != nil {
			return nil, err
		}
		items = append(items, b)
	}
	return json.Marshal(items)
}

// authorizeOptions requires the owner role to set or change provider options: they
// can reach outside the workload (ADR-0032).
func authorizeOptions(ctx context.Context, project uuid.UUID, cur, next *structpb.Struct) error {
	if proto.Equal(normalize(cur), normalize(next)) {
		return nil
	}
	return authorizeProject(ctx, project, auth.RoleOwner)
}

func normalize(s *structpb.Struct) *structpb.Struct {
	if s == nil {
		return &structpb.Struct{}
	}
	return s
}

func validSource(src *bohuxv1.Source) error {
	switch k := src.GetKind().(type) {
	case *bohuxv1.Source_Git:
		if k.Git.GetRepoUrl() == "" {
			return invalidArgument("source.git.repo_url", "REQUIRED", "repo_url is required")
		}
		if strings.Contains(k.Git.GetSubpath(), "..") {
			return invalidArgument("source.git.subpath", "INVALID", "subpath must stay inside the repository")
		}
	case *bohuxv1.Source_Image:
		if k.Image.GetRepository() == "" {
			return invalidArgument("source.image.repository", "REQUIRED", "repository is required")
		}
		if !imageDigest.MatchString(k.Image.GetDigest()) {
			return invalidArgument("source.image.digest", "INVALID", "digest must be sha256:<64 hex>")
		}
	default:
		return invalidArgument("source", "REQUIRED", "source must be git or image")
	}
	return nil
}

func validRuntime(rt *bohuxv1.Runtime) error {
	if rt == nil {
		return invalidArgument("runtime", "REQUIRED", "runtime is required")
	}
	if rt.GetReplicas() < 0 {
		return invalidArgument("runtime.replicas", "NEGATIVE", "replicas must not be negative")
	}
	containerPorts := map[int32]bool{}
	hostPorts := map[int32]bool{}
	for i, p := range rt.GetPorts() {
		field := fmt.Sprintf("runtime.ports[%d]", i)
		cp, hp := p.GetContainerPort(), p.GetHostPort()
		if cp < 1 || cp > 65535 {
			return invalidArgument(field+".container_port", "OUT_OF_RANGE", "container_port must be 1-65535")
		}
		if hp < 0 || hp > 65535 {
			return invalidArgument(field+".host_port", "OUT_OF_RANGE", "host_port must be 0-65535")
		}
		if hp != 0 {
			if hostPorts[hp] {
				return invalidArgument(field+".host_port", "DUPLICATE", "host_port is published twice")
			}
			hostPorts[hp] = true
		}
		containerPorts[cp] = true
	}
	if h := rt.GetHealth(); h != nil {
		if !strings.HasPrefix(h.GetPath(), "/") {
			return invalidArgument("runtime.health.path", "INVALID", "health path must start with /")
		}
		if !containerPorts[h.GetPort()] {
			return invalidArgument("runtime.health.port", "UNKNOWN_PORT", "health port must be one of the container ports")
		}
		if h.GetIntervalSeconds() < 0 || h.GetTimeoutSeconds() < 0 || h.GetFailureThreshold() < 0 {
			return invalidArgument("runtime.health", "NEGATIVE", "health check settings must not be negative")
		}
	}
	return nil
}

// validSecretRefs checks names and that each (key, handle) is a live secret of the
// app's environment. Values are never read here or anywhere in the API path but
// SetSecret (rule 3).
func validSecretRefs(ctx context.Context, tx *store.Tx, env uuid.UUID, refs []*bohuxv1.SecretRef) error {
	seen := map[string]bool{}
	for i, r := range refs {
		field := fmt.Sprintf("secrets[%d]", i)
		if !envVarName.MatchString(r.GetKey()) {
			return invalidArgument(field+".key", "INVALID", "key must be an environment variable name")
		}
		if seen[r.GetKey()] {
			return invalidArgument(field+".key", "DUPLICATE", "key appears twice")
		}
		seen[r.GetKey()] = true
		var ok bool
		err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM secret_refs_heads
			WHERE environment_id = $1 AND key = $2 AND handle = $3 AND NOT deleted)`,
			env, r.GetKey(), r.GetHandle()).Scan(&ok)
		if err != nil {
			return fmt.Errorf("check secret ref: %w", err)
		}
		if !ok {
			return invalidArgument(field, "UNKNOWN_SECRET", "no such secret in this environment")
		}
	}
	return nil
}

// validPullSecret checks the key names a live secret of the environment. Only the key
// is stored; the value is resolved and sealed for the node (ADR-0041).
func validPullSecret(ctx context.Context, tx *store.Tx, env uuid.UUID, key string) error {
	if key == "" {
		return nil
	}
	if !envVarName.MatchString(key) {
		return invalidArgument("image_pull_secret", "INVALID", "image_pull_secret must be a secret key")
	}
	var ok bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM secret_refs_heads
		WHERE environment_id = $1 AND key = $2 AND NOT deleted)`, env, key).Scan(&ok); err != nil {
		return fmt.Errorf("check image pull secret: %w", err)
	}
	if !ok {
		return invalidArgument("image_pull_secret", "UNKNOWN_SECRET", fmt.Sprintf("no secret %q in this environment", key))
	}
	return nil
}

// hostName is a lowercase DNS name: dot-separated labels of letters, digits and
// inner hyphens, at least two labels (ADR-0034: no wildcard, port, or path).
var hostName = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

func validRoutes(routes []*bohuxv1.Route, rt *bohuxv1.Runtime) error {
	ports := map[int32]bool{}
	for _, p := range rt.GetPorts() {
		ports[p.GetContainerPort()] = true
	}
	seen := map[string]bool{}
	for i, r := range routes {
		field := fmt.Sprintf("routes[%d]", i)
		if len(r.GetHost()) > 253 || !hostName.MatchString(r.GetHost()) {
			return invalidArgument(field+".host", "INVALID", "host must be a lowercase DNS name, without port or path")
		}
		if seen[r.GetHost()] {
			return invalidArgument(field+".host", "DUPLICATE", "host appears twice")
		}
		seen[r.GetHost()] = true
		if !ports[r.GetPort()] {
			return invalidArgument(field+".port", "UNKNOWN_PORT", "port must be one of the container ports")
		}
	}
	return nil
}

// claimHosts makes route_hosts hold exactly the App's routes. A host another App
// holds, in this tenant or any other, fails with ALREADY_EXISTS (ADR-0034).
func claimHosts(ctx context.Context, tx *store.Tx, app uuid.UUID, routes []*bohuxv1.Route) error {
	hosts := make([]string, 0, len(routes))
	for _, r := range routes {
		hosts = append(hosts, r.GetHost())
	}
	if err := tx.Exec(ctx, `DELETE FROM route_hosts WHERE app_id = $1 AND NOT host = ANY($2)`, app, hosts); err != nil {
		return fmt.Errorf("release hosts: %w", err)
	}
	for i, h := range hosts {
		var taken bool
		err := tx.QueryRow(ctx, `WITH ins AS (
				INSERT INTO route_hosts (host, tenant_id, app_id) VALUES ($1, $2, $3)
				ON CONFLICT (host) DO NOTHING RETURNING 1)
			SELECT NOT EXISTS (SELECT 1 FROM ins)
				AND NOT EXISTS (SELECT 1 FROM route_hosts WHERE host = $1 AND app_id = $3)`,
			h, tx.Tenant(), app).Scan(&taken)
		if err != nil {
			return fmt.Errorf("claim host: %w", err)
		}
		if taken {
			return fieldError(connect.CodeAlreadyExists, fmt.Sprintf("routes[%d].host", i),
				"HOST_IN_USE", "host is in use by another app")
		}
	}
	return nil
}

func (as *appService) CreateApp(ctx context.Context, req *connect.Request[bohuxv1.CreateAppRequest]) (*connect.Response[bohuxv1.CreateAppResponse], error) {
	msg := req.Msg
	p, _ := auth.FromContext(ctx)
	env, err := parseID("environment_id", msg.GetEnvironmentId())
	if err != nil {
		return nil, err
	}
	if err := validSlug("slug", msg.GetSlug()); err != nil {
		return nil, err
	}
	idem, err := newIdempotency(ctx, bohuxv1connect.AppServiceCreateAppProcedure, msg.GetIdempotencyKey(), msg)
	if err != nil {
		return nil, toConnect(ctx, err)
	}
	resp := &bohuxv1.CreateAppResponse{}
	err = as.s.Store.InTenant(ctx, p.Tenant, func(tx *store.Tx) error {
		prior, err := idem.lookup(ctx, tx)
		if err != nil {
			return err
		}
		if prior != nil {
			resp.App, err = getApp(ctx, tx, prior.ResourceID, uuid.Nil, "")
			return err
		}
		project, err := environmentProject(ctx, tx, env)
		if err != nil {
			return err
		}
		if err := authorizeProject(ctx, project, auth.RoleDeployer); err != nil {
			return err
		}
		if err := authorizeOptions(ctx, project, nil, msg.GetProviderOptions()); err != nil {
			return err
		}
		cols, err := appSpec(ctx, tx, env, appState{msg.GetSource(), msg.GetRuntime(), msg.GetSecrets(),
			msg.GetProviderOptions(), msg.GetRoutes(), msg.GetImagePullSecret()})
		if err != nil {
			return err
		}
		id, rev, err := tx.Create(ctx, "apps",
			[]store.Col{{Name: "environment_id", Value: env}, {Name: "slug", Value: msg.GetSlug()}},
			cols, store.Audit{Actor: p.Actor(), Action: bohuxv1connect.AppServiceCreateAppProcedure,
				ResourceType: appType, ProjectID: project})
		if err != nil {
			return err
		}
		if err := claimHosts(ctx, tx, id, msg.GetRoutes()); err != nil {
			return err
		}
		if err := idem.record(ctx, tx, appType, id, rev); err != nil {
			return err
		}
		resp.App, err = getApp(ctx, tx, id, uuid.Nil, "")
		return err
	})
	if err != nil {
		return nil, toConnect(ctx, err)
	}
	return connect.NewResponse(resp), nil
}

func (as *appService) GetApp(ctx context.Context, req *connect.Request[bohuxv1.GetAppRequest]) (*connect.Response[bohuxv1.GetAppResponse], error) {
	p, _ := auth.FromContext(ctx)
	var (
		id, env uuid.UUID
		slug    string
		err     error
	)
	switch l := req.Msg.GetLookup().(type) {
	case *bohuxv1.GetAppRequest_Id:
		if id, err = parseID("id", l.Id); err != nil {
			return nil, err
		}
	case *bohuxv1.GetAppRequest_Slug:
		if env, err = parseID("slug.environment_id", l.Slug.GetEnvironmentId()); err != nil {
			return nil, err
		}
		slug = l.Slug.GetSlug()
	default:
		return nil, invalidArgument("lookup", "REQUIRED", "id or slug is required")
	}
	resp := &bohuxv1.GetAppResponse{}
	err = as.s.Store.InTenant(ctx, p.Tenant, func(tx *store.Tx) error {
		app, err := getApp(ctx, tx, id, env, slug)
		if err != nil {
			return err
		}
		appID, err := uuid.Parse(app.GetMeta().GetId())
		if err != nil {
			return fmt.Errorf("stored app id: %w", err)
		}
		project, _, err := appProject(ctx, tx, appID)
		if err != nil {
			return err
		}
		if err := authorizeProject(ctx, project, auth.RoleViewer); err != nil {
			return err
		}
		resp.App = app
		return nil
	})
	if err != nil {
		return nil, toConnect(ctx, err)
	}
	return connect.NewResponse(resp), nil
}

func (as *appService) ListApps(ctx context.Context, req *connect.Request[bohuxv1.ListAppsRequest]) (*connect.Response[bohuxv1.ListAppsResponse], error) {
	p, _ := auth.FromContext(ctx)
	env, err := parseID("environment_id", req.Msg.GetEnvironmentId())
	if err != nil {
		return nil, err
	}
	size, after, err := page(req.Msg.GetPageSize(), req.Msg.GetPageToken())
	if err != nil {
		return nil, err
	}
	resp := &bohuxv1.ListAppsResponse{}
	err = as.s.Store.InTenant(ctx, p.Tenant, func(tx *store.Tx) error {
		project, err := environmentProject(ctx, tx, env)
		if err != nil {
			return err
		}
		if err := authorizeProject(ctx, project, auth.RoleViewer); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, appSelect+`
			WHERE NOT h.deleted AND h.environment_id = $1 AND ($2::uuid IS NULL OR h.id > $2)
			ORDER BY h.id LIMIT $3`, env, after, size+1)
		if err != nil {
			return fmt.Errorf("list apps: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			app, err := scanApp(rows)
			if err != nil {
				return err
			}
			resp.Apps = append(resp.Apps, app)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, toConnect(ctx, err)
	}
	if len(resp.Apps) > size {
		resp.Apps = resp.Apps[:size]
		resp.NextPageToken = resp.Apps[size-1].GetMeta().GetId()
	}
	return connect.NewResponse(resp), nil
}

func (as *appService) UpdateApp(ctx context.Context, req *connect.Request[bohuxv1.UpdateAppRequest]) (*connect.Response[bohuxv1.UpdateAppResponse], error) {
	msg := req.Msg
	app, err := as.update(ctx, bohuxv1connect.AppServiceUpdateAppProcedure, msg.GetId(),
		msg.GetExpectedRevisionId(), msg.GetIdempotencyKey(), msg, auth.RoleDeployer,
		func(*bohuxv1.App) appState {
			return appState{msg.GetSource(), msg.GetRuntime(), msg.GetSecrets(), msg.GetProviderOptions(), msg.GetRoutes(),
				msg.GetImagePullSecret()}
		})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&bohuxv1.UpdateAppResponse{App: app}), nil
}

// ScaleApp writes a new App revision with only replicas changed and, in the same
// transaction, the node generation that runs the App's current workload at that scale
// (ADR-0027). No new Release.
func (as *appService) ScaleApp(ctx context.Context, req *connect.Request[bohuxv1.ScaleAppRequest]) (*connect.Response[bohuxv1.ScaleAppResponse], error) {
	msg := req.Msg
	app, err := as.update(ctx, bohuxv1connect.AppServiceScaleAppProcedure, msg.GetId(),
		msg.GetExpectedRevisionId(), msg.GetIdempotencyKey(), msg, auth.RoleDeployer,
		func(cur *bohuxv1.App) appState {
			st := stateOf(cur)
			st.runtime = proto.Clone(cur.GetRuntime()).(*bohuxv1.Runtime)
			st.runtime.Replicas = msg.GetReplicas()
			return st
		})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&bohuxv1.ScaleAppResponse{App: app}), nil
}

// update is the shared body of UpdateApp and ScaleApp: next computes the new state
// from the current one. Replicas (ADR-0027) and routes (ADR-0034) are not Release
// state: when they change, the node's assignment follows in the same transaction.
func (as *appService) update(ctx context.Context, rpc, rawID, rawExpected, key string, msg proto.Message, min auth.Role,
	next func(cur *bohuxv1.App) appState,
) (*bohuxv1.App, error) {
	p, _ := auth.FromContext(ctx)
	id, err := parseID("id", rawID)
	if err != nil {
		return nil, err
	}
	expected, err := requiredRevision(rawExpected)
	if err != nil {
		return nil, err
	}
	idem, err := newIdempotency(ctx, rpc, key, msg)
	if err != nil {
		return nil, toConnect(ctx, err)
	}
	var (
		out  *bohuxv1.App
		wake *uuid.UUID
	)
	err = as.s.Store.InTenant(ctx, p.Tenant, func(tx *store.Tx) error {
		prior, err := idem.lookup(ctx, tx)
		if err != nil {
			return err
		}
		if prior == nil {
			project, env, err := appProject(ctx, tx, id)
			if err != nil {
				return err
			}
			if err := authorizeProject(ctx, project, min); err != nil {
				return err
			}
			cur, err := getApp(ctx, tx, id, uuid.Nil, "")
			if err != nil {
				return err
			}
			st := next(cur)
			if err := authorizeOptions(ctx, project, cur.GetProviderOptions(), st.options); err != nil {
				return err
			}
			cols, err := appSpec(ctx, tx, env, st)
			if err != nil {
				return err
			}
			rev, err := tx.Update(ctx, "apps", id, expected, cols,
				store.Audit{Actor: p.Actor(), Action: rpc, ResourceType: appType, ProjectID: project})
			if err != nil {
				return err
			}
			if err := idem.record(ctx, tx, appType, id, rev); err != nil {
				return err
			}
			if err := claimHosts(ctx, tx, id, st.routes); err != nil {
				return err
			}
			if as.s.Agents != nil {
				if wake, err = as.followOnNode(ctx, tx, id, env, st.runtime.GetReplicas(), st.routes, p.Actor()); err != nil {
					return err
				}
			}
		}
		out, err = getApp(ctx, tx, id, uuid.Nil, "")
		return err
	})
	if err != nil {
		return nil, toConnect(ctx, err)
	}
	if wake != nil {
		as.s.Agents.Wake(*wake)
	}
	return out, nil
}

// followOnNode brings the App's running workload on its environment's node to these
// replicas and routes, and returns the node to wake if a generation was written.
func (as *appService) followOnNode(ctx context.Context, tx *store.Tx, app, env uuid.UUID, replicas int32, routes []*bohuxv1.Route, actor store.Actor) (*uuid.UUID, error) {
	var node *uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT r.node_id FROM environments_heads h
		JOIN environments_revisions r ON r.id = h.revision_id WHERE h.id = $1`, env).Scan(&node); err != nil {
		return nil, fmt.Errorf("find app node: %w", err)
	}
	if node == nil {
		return nil, nil
	}
	written, err := as.s.Agents.FollowAppTx(ctx, tx, *node, app, replicas, routes, actor)
	if err != nil || !written {
		return nil, err
	}
	return node, nil
}

func (as *appService) DeleteApp(ctx context.Context, req *connect.Request[bohuxv1.DeleteAppRequest]) (*connect.Response[bohuxv1.DeleteAppResponse], error) {
	msg := req.Msg
	p, _ := auth.FromContext(ctx)
	id, err := parseID("id", msg.GetId())
	if err != nil {
		return nil, err
	}
	expected, err := requiredRevision(msg.GetExpectedRevisionId())
	if err != nil {
		return nil, err
	}
	idem, err := newIdempotency(ctx, bohuxv1connect.AppServiceDeleteAppProcedure, msg.GetIdempotencyKey(), msg)
	if err != nil {
		return nil, toConnect(ctx, err)
	}
	resp := &bohuxv1.DeleteAppResponse{}
	err = as.s.Store.InTenant(ctx, p.Tenant, func(tx *store.Tx) error {
		prior, err := idem.lookup(ctx, tx)
		if err != nil {
			return err
		}
		if prior != nil {
			resp.RevisionId = prior.RevisionID.String()
			return nil
		}
		project, _, err := appProject(ctx, tx, id)
		if err != nil {
			return err
		}
		if err := authorizeProject(ctx, project, auth.RoleOwner); err != nil {
			return err
		}
		rev, err := tx.Delete(ctx, "apps", id, expected,
			store.Audit{Actor: p.Actor(), Action: bohuxv1connect.AppServiceDeleteAppProcedure,
				ResourceType: appType, ProjectID: project})
		if err != nil {
			return err
		}
		if err := claimHosts(ctx, tx, id, nil); err != nil {
			return err
		}
		resp.RevisionId = rev.String()
		return idem.record(ctx, tx, appType, id, rev)
	})
	if err != nil {
		return nil, toConnect(ctx, err)
	}
	return connect.NewResponse(resp), nil
}

const appSelect = `SELECT ` + metaColumns + `, h.environment_id, h.slug, r.source, r.runtime, r.secrets,
	r.provider_options, r.routes, r.image_pull_secret FROM apps_heads h JOIN apps_revisions r ON r.id = h.revision_id`

// getApp reads a live app by id, or by (environment, slug) if id is nil.
func getApp(ctx context.Context, tx *store.Tx, id, env uuid.UUID, slug string) (*bohuxv1.App, error) {
	var row pgx.Row
	if id != uuid.Nil {
		row = tx.QueryRow(ctx, appSelect+` WHERE h.id = $1 AND NOT h.deleted`, id)
	} else {
		row = tx.QueryRow(ctx, appSelect+` WHERE h.environment_id = $1 AND h.slug = $2
			AND NOT h.deleted`, env, slug)
	}
	app, err := scanApp(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	return app, err
}

func scanApp(row pgx.Row) (*bohuxv1.App, error) {
	var (
		m             metaRow
		env           uuid.UUID
		slug          string
		src, rt, secs []byte
		opts, routes  []byte
		pull          string
	)
	if err := row.Scan(append(m.dest(), &env, &slug, &src, &rt, &secs, &opts, &routes, &pull)...); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
		return nil, fmt.Errorf("read app: %w", err)
	}
	app := &bohuxv1.App{Meta: m.proto(), EnvironmentId: env.String(), Slug: slug,
		Source: &bohuxv1.Source{}, Runtime: &bohuxv1.Runtime{}, ImagePullSecret: pull}
	if err := loadJSON.Unmarshal(src, app.Source); err != nil {
		return nil, fmt.Errorf("decode app source: %w", err)
	}
	if err := loadJSON.Unmarshal(rt, app.Runtime); err != nil {
		return nil, fmt.Errorf("decode app runtime: %w", err)
	}
	if string(opts) != "{}" {
		app.ProviderOptions = &structpb.Struct{}
		if err := loadJSON.Unmarshal(opts, app.ProviderOptions); err != nil {
			return nil, fmt.Errorf("decode app provider options: %w", err)
		}
	}
	var items []json.RawMessage
	if err := json.Unmarshal(secs, &items); err != nil {
		return nil, fmt.Errorf("decode app secret refs: %w", err)
	}
	for _, it := range items {
		ref := &bohuxv1.SecretRef{}
		if err := loadJSON.Unmarshal(it, ref); err != nil {
			return nil, fmt.Errorf("decode app secret ref: %w", err)
		}
		app.Secrets = append(app.Secrets, ref)
	}
	var rs []json.RawMessage
	if err := json.Unmarshal(routes, &rs); err != nil {
		return nil, fmt.Errorf("decode app routes: %w", err)
	}
	for _, it := range rs {
		r := &bohuxv1.Route{}
		if err := loadJSON.Unmarshal(it, r); err != nil {
			return nil, fmt.Errorf("decode app route: %w", err)
		}
		app.Routes = append(app.Routes, r)
	}
	return app, nil
}

// appProject returns the project and environment of a live app.
func appProject(ctx context.Context, tx *store.Tx, id uuid.UUID) (project, env uuid.UUID, err error) {
	err = tx.QueryRow(ctx, `SELECT e.project_id, a.environment_id
		FROM apps_heads a JOIN environments_heads e ON e.id = a.environment_id
		WHERE a.id = $1 AND NOT a.deleted`, id).Scan(&project, &env)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, uuid.Nil, store.ErrNotFound
	}
	if err != nil {
		return uuid.Nil, uuid.Nil, fmt.Errorf("look up app: %w", err)
	}
	return project, env, nil
}
