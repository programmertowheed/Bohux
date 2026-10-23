package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	agentv1 "github.com/bohumix/bohux/gen/bohux/agent/v1"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	bohuxv1 "github.com/bohumix/bohux/gen/bohux/v1"
	"github.com/bohumix/bohux/gen/bohux/v1/bohuxv1connect"
	"github.com/bohumix/bohux/internal/auth"
	"github.com/bohumix/bohux/internal/store"
)

const (
	nodeType           = "bohux.v1.Node"
	bootstrapTokenType = "bohux.v1.BootstrapToken"

	defaultBootstrapTTL = time.Hour
	maxBootstrapTTL     = 24 * time.Hour
)

var errNoPKI = errors.New("node registration is not configured on this control plane")

type nodeService struct {
	s *Server
}

func (ns *nodeService) CreateBootstrapToken(ctx context.Context, req *connect.Request[bohuxv1.CreateBootstrapTokenRequest]) (*connect.Response[bohuxv1.CreateBootstrapTokenResponse], error) {
	msg := req.Msg
	p, _ := auth.FromContext(ctx)
	if ns.s.PKI == nil {
		return nil, connect.NewError(connect.CodeUnavailable, errNoPKI)
	}
	if err := validSlug("node_slug", msg.GetNodeSlug()); err != nil {
		return nil, err
	}
	now := ns.s.now()
	expires := now.Add(defaultBootstrapTTL)
	if t := timePtr(msg.GetExpiresAt()); t != nil {
		if !t.After(now) || t.Sub(now) > maxBootstrapTTL {
			return nil, invalidArgument("expires_at", "OUT_OF_RANGE", "expires_at must be within the next 24 hours")
		}
		expires = *t
	}
	idem, err := newIdempotency(ctx, bohuxv1connect.NodeServiceCreateBootstrapTokenProcedure, msg.GetIdempotencyKey(), msg)
	if err != nil {
		return nil, toConnect(ctx, err)
	}
	resp := &bohuxv1.CreateBootstrapTokenResponse{CaCertSha256: ns.s.PKI.CACertSHA256()}
	err = ns.s.Store.InTenant(ctx, p.Tenant, func(tx *store.Tx) error {
		prior, err := idem.lookup(ctx, tx)
		if err != nil {
			return err
		}
		if prior != nil {
			// The token is shown once only (ADR-0022).
			var exp time.Time
			if err := tx.QueryRow(ctx, `SELECT expires_at FROM node_bootstrap_tokens WHERE id = $1`,
				prior.ResourceID).Scan(&exp); err != nil {
				return fmt.Errorf("read bootstrap token: %w", err)
			}
			resp.ExpiresAt = tsOrNil(&exp)
			return nil
		}
		var taken bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM nodes_heads
			WHERE slug = $1 AND NOT deleted)`, msg.GetNodeSlug()).Scan(&taken); err != nil {
			return fmt.Errorf("check node slug: %w", err)
		}
		if taken {
			return store.ErrAlreadyExists
		}
		id, err := store.NewID()
		if err != nil {
			return err
		}
		raw, hash, err := auth.Issue(auth.KindBootstrap, p.Tenant, id)
		if err != nil {
			return err
		}
		if err := tx.Exec(ctx, `INSERT INTO node_bootstrap_tokens
			(id, tenant_id, node_slug, token_hash, expires_at, created_by)
			VALUES ($1, $2, $3, $4, $5, $6)`,
			id, p.Tenant, msg.GetNodeSlug(), hash, expires, p.ID); err != nil {
			return fmt.Errorf("store bootstrap token: %w", err)
		}
		if err := tx.AuditOnly(ctx, store.Audit{Actor: p.Actor(),
			Action:       bohuxv1connect.NodeServiceCreateBootstrapTokenProcedure,
			ResourceType: bootstrapTokenType}, id); err != nil {
			return err
		}
		if err := idem.record(ctx, tx, bootstrapTokenType, id, uuid.Nil); err != nil {
			return err
		}
		resp.Token = raw
		resp.ExpiresAt = tsOrNil(&expires)
		return nil
	})
	if err != nil {
		return nil, toConnect(ctx, err)
	}
	return connect.NewResponse(resp), nil
}

func (ns *nodeService) GetNode(ctx context.Context, req *connect.Request[bohuxv1.GetNodeRequest]) (*connect.Response[bohuxv1.GetNodeResponse], error) {
	p, _ := auth.FromContext(ctx)
	var (
		id   uuid.UUID
		slug string
		err  error
	)
	switch l := req.Msg.GetLookup().(type) {
	case *bohuxv1.GetNodeRequest_Id:
		if id, err = parseID("id", l.Id); err != nil {
			return nil, err
		}
	case *bohuxv1.GetNodeRequest_Slug:
		slug = l.Slug
	default:
		return nil, invalidArgument("lookup", "REQUIRED", "id or slug is required")
	}
	resp := &bohuxv1.GetNodeResponse{}
	err = ns.s.Store.InTenant(ctx, p.Tenant, func(tx *store.Tx) error {
		resp.Node, err = getNode(ctx, tx, id, slug)
		return err
	})
	if err != nil {
		return nil, toConnect(ctx, err)
	}
	return connect.NewResponse(resp), nil
}

func (ns *nodeService) ListNodes(ctx context.Context, req *connect.Request[bohuxv1.ListNodesRequest]) (*connect.Response[bohuxv1.ListNodesResponse], error) {
	p, _ := auth.FromContext(ctx)
	size, after, err := page(req.Msg.GetPageSize(), req.Msg.GetPageToken())
	if err != nil {
		return nil, err
	}
	resp := &bohuxv1.ListNodesResponse{}
	err = ns.s.Store.InTenant(ctx, p.Tenant, func(tx *store.Tx) error {
		rows, err := tx.Query(ctx, nodeSelect+`
			WHERE NOT h.deleted AND ($1::uuid IS NULL OR h.id > $1)
			ORDER BY h.id LIMIT $2`, after, size+1)
		if err != nil {
			return fmt.Errorf("list nodes: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			n, err := scanNode(rows)
			if err != nil {
				return err
			}
			resp.Nodes = append(resp.Nodes, n)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, toConnect(ctx, err)
	}
	if len(resp.Nodes) > size {
		resp.Nodes = resp.Nodes[:size]
		resp.NextPageToken = resp.Nodes[size-1].GetMeta().GetId()
	}
	return connect.NewResponse(resp), nil
}

// RevokeNode marks the node revoked; its next Connect is refused (ADR-0024). Rotating
// the secrets it held is a separate step (ADR-0008).
func (ns *nodeService) RevokeNode(ctx context.Context, req *connect.Request[bohuxv1.RevokeNodeRequest]) (*connect.Response[bohuxv1.RevokeNodeResponse], error) {
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
	idem, err := newIdempotency(ctx, bohuxv1connect.NodeServiceRevokeNodeProcedure, msg.GetIdempotencyKey(), msg)
	if err != nil {
		return nil, toConnect(ctx, err)
	}
	resp := &bohuxv1.RevokeNodeResponse{}
	err = ns.s.Store.InTenant(ctx, p.Tenant, func(tx *store.Tx) error {
		prior, err := idem.lookup(ctx, tx)
		if err != nil {
			return err
		}
		if prior == nil {
			var (
				agentVersion string
				protocol     int32
				providers    []byte
			)
			err := tx.QueryRow(ctx, `SELECT r.agent_version, r.protocol_version, r.providers
				FROM nodes_heads h JOIN nodes_revisions r ON r.id = h.revision_id
				WHERE h.id = $1 AND NOT h.deleted`, id).Scan(&agentVersion, &protocol, &providers)
			if errors.Is(err, pgx.ErrNoRows) {
				return store.ErrNotFound
			}
			if err != nil {
				return fmt.Errorf("read node: %w", err)
			}
			rev, err := tx.Update(ctx, "nodes", id, expected, []store.Col{
				{Name: "agent_version", Value: agentVersion}, {Name: "protocol_version", Value: protocol},
				{Name: "providers", Value: providers}, {Name: "state", Value: "revoked"},
				{Name: "revoked_at", Value: ns.s.now()},
			}, store.Audit{Actor: p.Actor(), Action: bohuxv1connect.NodeServiceRevokeNodeProcedure,
				ResourceType: nodeType})
			if err != nil {
				return err
			}
			if err := idem.record(ctx, tx, nodeType, id, rev); err != nil {
				return err
			}
		}
		resp.Node, err = getNode(ctx, tx, id, "")
		return err
	})
	if err != nil {
		return nil, toConnect(ctx, err)
	}
	return connect.NewResponse(resp), nil
}

const nodeSelect = `SELECT ` + metaColumns + `, h.slug, h.last_seen_at, r.agent_version,
		r.protocol_version, r.providers, r.state, r.state_detail, r.revoked_at, h.report
	FROM nodes_heads h JOIN nodes_revisions r ON r.id = h.revision_id`

func getNode(ctx context.Context, tx *store.Tx, id uuid.UUID, slug string) (*bohuxv1.Node, error) {
	var row pgx.Row
	if id != uuid.Nil {
		row = tx.QueryRow(ctx, nodeSelect+` WHERE h.id = $1 AND NOT h.deleted`, id)
	} else {
		row = tx.QueryRow(ctx, nodeSelect+` WHERE h.slug = $1 AND NOT h.deleted`, slug)
	}
	n, err := scanNode(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	return n, err
}

var nodeStates = map[string]bohuxv1.NodeState{
	"online":   bohuxv1.NodeState_NODE_STATE_ONLINE,
	"offline":  bohuxv1.NodeState_NODE_STATE_OFFLINE,
	"degraded": bohuxv1.NodeState_NODE_STATE_DEGRADED,
	"rejected": bohuxv1.NodeState_NODE_STATE_REJECTED,
	"revoked":  bohuxv1.NodeState_NODE_STATE_REVOKED,
}

func scanNode(row pgx.Row) (*bohuxv1.Node, error) {
	var (
		m                   metaRow
		slug, version, st   string
		lastSeen, revokedAt *time.Time
		protocol            int32
		providers, detail   []byte
		report              []byte
	)
	if err := row.Scan(append(m.dest(), &slug, &lastSeen, &version, &protocol, &providers, &st,
		&detail, &revokedAt, &report)...); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
		return nil, fmt.Errorf("read node: %w", err)
	}
	n := &bohuxv1.Node{
		Meta: m.proto(), Slug: slug, AgentVersion: version, ProtocolVersion: uint32(protocol),
		LastSeenAt: tsOrNil(lastSeen), State: nodeStates[st], RevokedAt: tsOrNil(revokedAt),
	}
	var items []json.RawMessage
	if err := json.Unmarshal(providers, &items); err != nil {
		return nil, fmt.Errorf("decode node providers: %w", err)
	}
	for _, it := range items {
		d := &bohuxv1.ProviderDescriptor{}
		if err := loadJSON.Unmarshal(it, d); err != nil {
			return nil, fmt.Errorf("decode provider descriptor: %w", err)
		}
		n.Providers = append(n.Providers, d)
	}
	if detail != nil {
		n.StateDetail = &bohuxv1.ErrorDetail{}
		if err := loadJSON.Unmarshal(detail, n.StateDetail); err != nil {
			return nil, fmt.Errorf("decode node state detail: %w", err)
		}
	}
	if report != nil {
		// The node's latest resource sample travels in its stored report (ADR-0040).
		r := &agentv1.StatusReport{}
		if err := loadJSON.Unmarshal(report, r); err != nil {
			return nil, fmt.Errorf("decode node report: %w", err)
		}
		n.Usage = r.GetUsage()
	}
	return n, nil
}
