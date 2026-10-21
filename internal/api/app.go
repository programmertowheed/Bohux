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

// appSpec validates an App's mutable state and returns its revision columns.
func appSpec(ctx context.Context, tx *store.Tx, env uuid.UUID, src *bohuxv1.Source, rt *bohuxv1.Runtime, refs []*bohuxv1.SecretRef) ([]store.Col, error) {
	if err := validSource(src); err != nil {
		return nil, err
	}
	if err := validRuntime(rt); err != nil {
		return nil, err
	}
	if err := validSecretRefs(ctx, tx, env, refs); err != nil {
		return nil, err
	}
	srcJSON, err := storeJSON.Marshal(src)
	if err != nil {
		return nil, fmt.Errorf("encode source: %w", err)
	}
	rtJSON, err := storeJSON.Marshal(rt)
	if err != nil {
		return nil, fmt.Errorf("encode runtime: %w", err)
	}
	items := make([]json.RawMessage, 0, len(refs))
	for _, r := range refs {
		b, err := storeJSON.Marshal(r)
		if err != nil {
			return nil, fmt.Errorf("encode secret ref: %w", err)
		}
		items = append(items, b)
	}
	secJSON, err := json.Marshal(items)
	if err != nil {
		return nil, fmt.Errorf("encode secret refs: %w", err)
	}
	return []store.Col{
		{Name: "source", Value: srcJSON}, {Name: "runtime", Value: rtJSON}, {Name: "secrets", Value: secJSON},
	}, nil
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
		cols, err := appSpec(ctx, tx, env, msg.GetSource(), msg.GetRuntime(), msg.GetSecrets())
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
		func(*bohuxv1.App) (*bohuxv1.Source, *bohuxv1.Runtime, []*bohuxv1.SecretRef) {
			return msg.GetSource(), msg.GetRuntime(), msg.GetSecrets()
		})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&bohuxv1.UpdateAppResponse{App: app}), nil
}

// ScaleApp writes a new App revision with only replicas changed. No new Release; the
// node assignment that applies it comes with the agent (S4) and workflow (S6).
func (as *appService) ScaleApp(ctx context.Context, req *connect.Request[bohuxv1.ScaleAppRequest]) (*connect.Response[bohuxv1.ScaleAppResponse], error) {
	msg := req.Msg
	app, err := as.update(ctx, bohuxv1connect.AppServiceScaleAppProcedure, msg.GetId(),
		msg.GetExpectedRevisionId(), msg.GetIdempotencyKey(), msg, auth.RoleDeployer,
		func(cur *bohuxv1.App) (*bohuxv1.Source, *bohuxv1.Runtime, []*bohuxv1.SecretRef) {
			rt := proto.Clone(cur.GetRuntime()).(*bohuxv1.Runtime)
			rt.Replicas = msg.GetReplicas()
			return cur.GetSource(), rt, cur.GetSecrets()
		})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&bohuxv1.ScaleAppResponse{App: app}), nil
}

// update is the shared body of UpdateApp and ScaleApp: next computes the new state
// from the current one.
func (as *appService) update(ctx context.Context, rpc, rawID, rawExpected, key string, msg proto.Message, min auth.Role,
	next func(cur *bohuxv1.App) (*bohuxv1.Source, *bohuxv1.Runtime, []*bohuxv1.SecretRef),
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
	var out *bohuxv1.App
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
			src, rt, refs := next(cur)
			cols, err := appSpec(ctx, tx, env, src, rt, refs)
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
		}
		out, err = getApp(ctx, tx, id, uuid.Nil, "")
		return err
	})
	if err != nil {
		return nil, toConnect(ctx, err)
	}
	return out, nil
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
		resp.RevisionId = rev.String()
		return idem.record(ctx, tx, appType, id, rev)
	})
	if err != nil {
		return nil, toConnect(ctx, err)
	}
	return connect.NewResponse(resp), nil
}

const appSelect = `SELECT ` + metaColumns + `, h.environment_id, h.slug, r.source, r.runtime, r.secrets
	FROM apps_heads h JOIN apps_revisions r ON r.id = h.revision_id`

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
	)
	if err := row.Scan(append(m.dest(), &env, &slug, &src, &rt, &secs)...); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
		return nil, fmt.Errorf("read app: %w", err)
	}
	app := &bohuxv1.App{Meta: m.proto(), EnvironmentId: env.String(), Slug: slug,
		Source: &bohuxv1.Source{}, Runtime: &bohuxv1.Runtime{}}
	if err := loadJSON.Unmarshal(src, app.Source); err != nil {
		return nil, fmt.Errorf("decode app source: %w", err)
	}
	if err := loadJSON.Unmarshal(rt, app.Runtime); err != nil {
		return nil, fmt.Errorf("decode app runtime: %w", err)
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
