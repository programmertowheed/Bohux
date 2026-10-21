package api

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	bohuxv1 "github.com/bohumix/bohux/gen/bohux/v1"
	"github.com/bohumix/bohux/gen/bohux/v1/bohuxv1connect"
	"github.com/bohumix/bohux/internal/auth"
	"github.com/bohumix/bohux/internal/secrets/openbao"
	"github.com/bohumix/bohux/internal/store"
	"github.com/bohumix/bohux/internal/workflow/deploy"
)

const (
	webhookType = "bohux.v1.Webhook"
	// HookPrefix is where Git hosts post (ADR-0037).
	HookPrefix = "/hooks/v1/"
	// maxHookBody bounds a delivery; GitHub push payloads are far smaller.
	maxHookBody = 1 << 20
)

type webhookService struct {
	s *Server
}

func hookPath(tenant, id uuid.UUID) string {
	return HookPrefix + hex.EncodeToString(tenant[:]) + "/" + hex.EncodeToString(id[:])
}

// webhookSecretPath keeps signing secrets apart from App secrets in the tenant's KV
// space, so no App can reference one.
func webhookSecretPath(tenant, id uuid.UUID) string {
	return "bohux/" + tenant.String() + "/webhooks/" + id.String()
}

func validBranch(b string) error {
	if b == "" || strings.ContainsAny(b, " \t\n~^:?*[\\") || strings.Contains(b, "..") ||
		strings.HasPrefix(b, "/") || strings.HasSuffix(b, "/") || strings.HasPrefix(b, "refs/") {
		return invalidArgument("branch", "INVALID", "branch must be a branch name, e.g. main")
	}
	return nil
}

// CreateWebhook writes the signing secret to OpenBao, then the row, and returns the
// secret this once. A replay of the same idempotency key returns the webhook without
// it: the secret is never read back for a client.
func (ws *webhookService) CreateWebhook(ctx context.Context, req *connect.Request[bohuxv1.CreateWebhookRequest]) (*connect.Response[bohuxv1.CreateWebhookResponse], error) {
	msg := req.Msg
	p, _ := auth.FromContext(ctx)
	if ws.s.Secrets == nil {
		return nil, errNoSecrets
	}
	app, err := parseID("app_id", msg.GetAppId())
	if err != nil {
		return nil, err
	}
	if err := validBranch(msg.GetBranch()); err != nil {
		return nil, err
	}
	flavor := "github"
	switch msg.GetFlavor() {
	case bohuxv1.WebhookFlavor_WEBHOOK_FLAVOR_UNSPECIFIED, bohuxv1.WebhookFlavor_WEBHOOK_FLAVOR_GITHUB:
	case bohuxv1.WebhookFlavor_WEBHOOK_FLAVOR_GENERIC:
		flavor = "generic"
	default:
		return nil, invalidArgument("flavor", "INVALID", "unknown flavor")
	}
	idem, err := newIdempotency(ctx, bohuxv1connect.WebhookServiceCreateWebhookProcedure, msg.GetIdempotencyKey(), msg)
	if err != nil {
		return nil, toConnect(ctx, err)
	}
	resp := &bohuxv1.CreateWebhookResponse{}
	var project uuid.UUID
	err = ws.s.Store.InTenant(ctx, p.Tenant, func(tx *store.Tx) error {
		prior, err := idem.lookup(ctx, tx)
		if err != nil {
			return err
		}
		if prior != nil {
			resp.Webhook, err = getWebhook(ctx, tx, prior.ResourceID)
			return err
		}
		if project, _, err = appProject(ctx, tx, app); err != nil {
			return err
		}
		if err := authorizeProject(ctx, project, auth.RoleDeployer); err != nil {
			return err
		}
		a, err := getApp(ctx, tx, app, uuid.Nil, "")
		if err != nil {
			return err
		}
		if a.GetSource().GetGit() == nil {
			return connect.NewError(connect.CodeFailedPrecondition, errors.New("the app has no git source to build"))
		}
		return nil
	})
	if err != nil || resp.Webhook != nil {
		return connectResp(resp, toConnect(ctx, err))
	}
	id, err := store.NewID()
	if err != nil {
		return nil, toConnect(ctx, err)
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return nil, toConnect(ctx, fmt.Errorf("generate webhook secret: %w", err))
	}
	secret := hex.EncodeToString(raw)
	if _, err := ws.s.Secrets.Put(ctx, webhookSecretPath(p.Tenant, id), secret); err != nil {
		slog.ErrorContext(ctx, "store webhook secret", "webhook", id.String(), "err", err)
		return nil, errNoSecrets
	}
	err = ws.s.Store.InTenant(ctx, p.Tenant, func(tx *store.Tx) error {
		rev, err := tx.CreateWithID(ctx, "webhooks", id, []store.Col{
			{Name: "app_id", Value: app}, {Name: "branch", Value: msg.GetBranch()}, {Name: "flavor", Value: flavor},
			{Name: "secret_handle", Value: openbao.HandlePrefix + webhookSecretPath(p.Tenant, id)},
		}, nil, store.Audit{Actor: p.Actor(), Action: bohuxv1connect.WebhookServiceCreateWebhookProcedure,
			ResourceType: webhookType, ProjectID: project})
		if err != nil {
			return err
		}
		if err := idem.record(ctx, tx, webhookType, id, rev); err != nil {
			return err
		}
		resp.Webhook, err = getWebhook(ctx, tx, id)
		return err
	})
	if err != nil {
		// The row never committed: drop the secret too (best effort).
		_ = ws.s.Secrets.Destroy(context.WithoutCancel(ctx), webhookSecretPath(p.Tenant, id))
		return nil, toConnect(ctx, err)
	}
	resp.Secret = secret
	return connect.NewResponse(resp), nil
}

func (ws *webhookService) ListWebhooks(ctx context.Context, req *connect.Request[bohuxv1.ListWebhooksRequest]) (*connect.Response[bohuxv1.ListWebhooksResponse], error) {
	p, _ := auth.FromContext(ctx)
	app, err := parseID("app_id", req.Msg.GetAppId())
	if err != nil {
		return nil, err
	}
	size, after, err := page(req.Msg.GetPageSize(), req.Msg.GetPageToken())
	if err != nil {
		return nil, err
	}
	resp := &bohuxv1.ListWebhooksResponse{}
	err = ws.s.Store.InTenant(ctx, p.Tenant, func(tx *store.Tx) error {
		project, _, err := appProject(ctx, tx, app)
		if err != nil {
			return err
		}
		if err := authorizeProject(ctx, project, auth.RoleViewer); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, webhookSelect+` WHERE NOT h.deleted AND h.app_id = $1
			AND ($2::uuid IS NULL OR h.id > $2) ORDER BY h.id LIMIT $3`, app, after, size+1)
		if err != nil {
			return fmt.Errorf("list webhooks: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			w, err := scanWebhook(rows)
			if err != nil {
				return err
			}
			resp.Webhooks = append(resp.Webhooks, w)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, toConnect(ctx, err)
	}
	if len(resp.Webhooks) > size {
		resp.Webhooks = resp.Webhooks[:size]
		resp.NextPageToken = resp.Webhooks[size-1].GetMeta().GetId()
	}
	return connect.NewResponse(resp), nil
}

// DeleteWebhook revokes the webhook, then destroys its secret.
func (ws *webhookService) DeleteWebhook(ctx context.Context, req *connect.Request[bohuxv1.DeleteWebhookRequest]) (*connect.Response[bohuxv1.DeleteWebhookResponse], error) {
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
	idem, err := newIdempotency(ctx, bohuxv1connect.WebhookServiceDeleteWebhookProcedure, msg.GetIdempotencyKey(), msg)
	if err != nil {
		return nil, toConnect(ctx, err)
	}
	resp := &bohuxv1.DeleteWebhookResponse{}
	deleted := false
	err = ws.s.Store.InTenant(ctx, p.Tenant, func(tx *store.Tx) error {
		prior, err := idem.lookup(ctx, tx)
		if err != nil {
			return err
		}
		if prior != nil {
			resp.RevisionId = prior.RevisionID.String()
			return nil
		}
		w, err := getWebhook(ctx, tx, id)
		if err != nil {
			return err
		}
		app, err := uuid.Parse(w.GetAppId())
		if err != nil {
			return fmt.Errorf("stored app id: %w", err)
		}
		project, _, err := appProject(ctx, tx, app)
		if err != nil {
			return err
		}
		if err := authorizeProject(ctx, project, auth.RoleDeployer); err != nil {
			return err
		}
		rev, err := tx.Delete(ctx, "webhooks", id, expected, store.Audit{Actor: p.Actor(),
			Action: bohuxv1connect.WebhookServiceDeleteWebhookProcedure, ResourceType: webhookType, ProjectID: project})
		if err != nil {
			return err
		}
		resp.RevisionId, deleted = rev.String(), true
		return idem.record(ctx, tx, webhookType, id, rev)
	})
	if err != nil {
		return nil, toConnect(ctx, err)
	}
	if deleted && ws.s.Secrets != nil {
		if err := ws.s.Secrets.Destroy(ctx, webhookSecretPath(p.Tenant, id)); err != nil {
			// Revoked already: the row is deleted, so the secret verifies nothing.
			slog.WarnContext(ctx, "destroy webhook secret", "webhook", id.String(), "err", err)
		}
	}
	return connect.NewResponse(resp), nil
}

const webhookSelect = `SELECT ` + metaColumns + `, h.app_id, h.branch, h.flavor
	FROM webhooks_heads h JOIN webhooks_revisions r ON r.id = h.revision_id`

func getWebhook(ctx context.Context, tx *store.Tx, id uuid.UUID) (*bohuxv1.Webhook, error) {
	w, err := scanWebhook(tx.QueryRow(ctx, webhookSelect+` WHERE h.id = $1 AND NOT h.deleted`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	return w, err
}

func scanWebhook(row pgx.Row) (*bohuxv1.Webhook, error) {
	var (
		m      metaRow
		app    uuid.UUID
		w      bohuxv1.Webhook
		flavor string
	)
	if err := row.Scan(append(m.dest(), &app, &w.Branch, &flavor)...); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
		return nil, fmt.Errorf("read webhook: %w", err)
	}
	w.Meta, w.AppId, w.Path = m.proto(), app.String(), hookPath(m.Tenant, m.ID)
	w.Flavor = bohuxv1.WebhookFlavor_WEBHOOK_FLAVOR_GITHUB
	if flavor == "generic" {
		w.Flavor = bohuxv1.WebhookFlavor_WEBHOOK_FLAVOR_GENERIC
	}
	return &w, nil
}

// --- deliveries ------------------------------------------------------------------

// errHookNotFound covers every refusal that must not reveal why (ADR-0037 item 2).
var errHookNotFound = errors.New("not found")

// ServeHook handles POST /hooks/v1/<tenant>/<webhook>: it verifies the delivery and
// deploys the pushed commit as the webhook (ADR-0037).
func (s *Server) ServeHook(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	status, msg := s.hook(ctx, r)
	if status >= 500 {
		slog.ErrorContext(ctx, "webhook delivery failed", "status", status)
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, msg+"\n")
}

func (s *Server) hook(ctx context.Context, r *http.Request) (int, string) {
	tenant, id, err := parseHookPath(strings.TrimPrefix(r.URL.Path, HookPrefix))
	if err != nil || s.Secrets == nil {
		return http.StatusNotFound, errHookNotFound.Error()
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxHookBody+1))
	if err != nil {
		return http.StatusBadRequest, "unreadable body"
	}
	if len(body) > maxHookBody {
		return http.StatusRequestEntityTooLarge, "body too large"
	}
	var (
		hook    *bohuxv1.Webhook
		handle  string
		project uuid.UUID
		env     uuid.UUID
	)
	err = s.Store.InTenant(ctx, tenant, func(tx *store.Tx) error {
		var err error
		if hook, err = getWebhook(ctx, tx, id); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT secret_handle FROM webhooks_heads WHERE id = $1`, id).Scan(&handle); err != nil {
			return fmt.Errorf("read webhook secret handle: %w", err)
		}
		app, err := uuid.Parse(hook.GetAppId())
		if err != nil {
			return fmt.Errorf("stored app id: %w", err)
		}
		project, env, err = appProject(ctx, tx, app)
		return err
	})
	if errors.Is(err, store.ErrNotFound) {
		return http.StatusNotFound, errHookNotFound.Error()
	}
	if err != nil {
		slog.ErrorContext(ctx, "look up webhook", "err", err)
		return http.StatusInternalServerError, "internal error"
	}
	path, ok := strings.CutPrefix(handle, openbao.HandlePrefix)
	if !ok || path != webhookSecretPath(tenant, id) {
		slog.ErrorContext(ctx, "webhook secret handle is foreign", "webhook", id.String())
		return http.StatusNotFound, errHookNotFound.Error()
	}
	secret, err := s.Secrets.Get(ctx, path, 0)
	if err != nil {
		slog.ErrorContext(ctx, "read webhook secret", "webhook", id.String(), "err", err)
		return http.StatusServiceUnavailable, "secrets unavailable"
	}
	header := "X-Hub-Signature-256"
	if hook.GetFlavor() == bohuxv1.WebhookFlavor_WEBHOOK_FLAVOR_GENERIC {
		header = "X-Bohux-Signature"
	}
	if !validSignature(secret, body, r.Header.Get(header)) {
		return http.StatusNotFound, errHookNotFound.Error()
	}
	commit, deploys, reason := pushedCommit(hook, r.Header.Get("X-GitHub-Event"), body)
	if !deploys {
		return http.StatusNoContent, reason
	}
	p := auth.Principal{ID: id, Kind: store.ActorWebhook, Tenant: tenant, ProjectID: project, Role: auth.RoleDeployer}
	dep, err := s.deployCommit(auth.WithPrincipal(ctx, p), hook, env, commit)
	switch {
	case errors.Is(err, deploy.ErrRunning):
		return http.StatusConflict, "a deployment of this app is already in progress"
	case err != nil:
		var ce *connect.Error
		if errors.As(err, &ce) {
			return httpStatus(ce.Code()), ce.Message()
		}
		slog.ErrorContext(ctx, "webhook deploy", "webhook", id.String(), "err", err)
		return http.StatusInternalServerError, "internal error"
	}
	slog.InfoContext(ctx, "webhook deploy started", "webhook", id.String(), "commit", commit, "deployment", dep)
	return http.StatusAccepted, "deployment " + dep
}

// deployCommit creates the commit's Release and its Deployment through the API's own
// handlers, as the webhook principal in ctx; the idempotency keys make a redelivered
// push start nothing new.
func (s *Server) deployCommit(ctx context.Context, hook *bohuxv1.Webhook, env uuid.UUID, commit string) (string, error) {
	key := "webhook/" + hook.GetMeta().GetId() + "/" + commit
	rel, err := (&releaseService{s}).CreateRelease(ctx, connect.NewRequest(&bohuxv1.CreateReleaseRequest{
		AppId: hook.GetAppId(), From: &bohuxv1.CreateReleaseRequest_GitRef{GitRef: commit},
		IdempotencyKey: key + "/release"}))
	if err != nil {
		return "", err
	}
	dep, err := (&deploymentService{s}).CreateDeployment(ctx, connect.NewRequest(&bohuxv1.CreateDeploymentRequest{
		ReleaseId: rel.Msg.GetRelease().GetMeta().GetId(), EnvironmentId: env.String(),
		Reason: "git push " + commit, IdempotencyKey: key + "/deploy"}))
	if err != nil {
		return "", err
	}
	return dep.Msg.GetDeployment().GetMeta().GetId(), nil
}

func parseHookPath(rest string) (tenant, id uuid.UUID, err error) {
	t, i, ok := strings.Cut(rest, "/")
	if !ok {
		return uuid.Nil, uuid.Nil, errHookNotFound
	}
	if tenant, err = parseHexUUID(t); err != nil {
		return uuid.Nil, uuid.Nil, errHookNotFound
	}
	if id, err = parseHexUUID(i); err != nil {
		return uuid.Nil, uuid.Nil, errHookNotFound
	}
	return tenant, id, nil
}

func parseHexUUID(s string) (uuid.UUID, error) {
	b, err := hex.DecodeString(s)
	if err != nil || len(b) != 16 {
		return uuid.Nil, errHookNotFound
	}
	return uuid.FromBytes(b)
}

// validSignature checks "sha256=<hex>" against the body's HMAC, in constant time.
func validSignature(secret string, body []byte, header string) bool {
	got, ok := strings.CutPrefix(header, "sha256=")
	if !ok {
		return false
	}
	sig, err := hex.DecodeString(got)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hmac.Equal(sig, mac.Sum(nil))
}

// pushedCommit reads only the ref and commit (ADR-0037 item 4). It reports whether
// the delivery deploys, and why not when it does not.
func pushedCommit(hook *bohuxv1.Webhook, event string, body []byte) (commit string, deploys bool, why string) {
	if hook.GetFlavor() == bohuxv1.WebhookFlavor_WEBHOOK_FLAVOR_GENERIC {
		var p struct {
			Ref string `json:"ref"`
		}
		if err := json.Unmarshal(body, &p); err != nil || p.Ref == "" {
			return "", false, "no ref in the body"
		}
		return p.Ref, true, ""
	}
	switch event {
	case "ping":
		return "", false, "pong"
	case "push":
	default:
		return "", false, "ignored event " + event
	}
	var p struct {
		Ref     string `json:"ref"`
		After   string `json:"after"`
		Deleted bool   `json:"deleted"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		return "", false, "unreadable push payload"
	}
	if p.Ref != "refs/heads/"+hook.GetBranch() {
		return "", false, "push to another branch"
	}
	if p.Deleted || strings.Trim(p.After, "0") == "" {
		return "", false, "branch deleted"
	}
	return p.After, true, ""
}

// httpStatus maps a Connect code to the status a Git host shows for a delivery.
func httpStatus(c connect.Code) int {
	switch c {
	case connect.CodeInvalidArgument, connect.CodeFailedPrecondition:
		return http.StatusUnprocessableEntity
	case connect.CodeNotFound, connect.CodePermissionDenied, connect.CodeUnauthenticated:
		return http.StatusNotFound
	case connect.CodeAborted, connect.CodeAlreadyExists:
		return http.StatusConflict
	case connect.CodeUnavailable:
		return http.StatusServiceUnavailable
	}
	return http.StatusInternalServerError
}
