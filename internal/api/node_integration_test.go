//go:build integration

package api

import (
	"context"
	"crypto/sha256"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	bohuxv1 "github.com/bohumix/bohux/gen/bohux/v1"
	"github.com/bohumix/bohux/gen/bohux/v1/bohuxv1connect"
	"github.com/bohumix/bohux/internal/auth"
	"github.com/bohumix/bohux/internal/store"
)

func TestNodes(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	srv := h.server()
	nodes := bohuxv1connect.NewNodeServiceClient(srv.Client(), srv.URL)
	p1 := h.project("one")
	projOwner, err := h.createToken(h.root, p1, bohuxv1.Role_ROLE_OWNER, "po")
	if err != nil {
		t.Fatal(err)
	}
	existing := h.node("vps-1")

	bootstrap := func(token, slug, key string, exp *timestamppb.Timestamp) (*bohuxv1.CreateBootstrapTokenResponse, error) {
		resp, err := nodes.CreateBootstrapToken(ctx, req(token, &bohuxv1.CreateBootstrapTokenRequest{
			NodeSlug: slug, ExpiresAt: exp, IdempotencyKey: key}))
		if err != nil {
			return nil, err
		}
		return resp.Msg, nil
	}

	first, err := bootstrap(h.root, "vps-2", "b1", nil)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("bootstrap token carries the CA pin and is stored hashed", func(t *testing.T) {
		if !strings.HasPrefix(first.GetToken(), "bxb_") {
			t.Errorf("token %q is not a bootstrap token", first.GetToken()[:4])
		}
		if first.GetCaCertSha256() != h.pki.CACertSHA256() {
			t.Error("ca_cert_sha256 does not match the node CA")
		}
		tok, err := auth.Parse(first.GetToken())
		if err != nil {
			t.Fatal(err)
		}
		var hash []byte
		var audited int
		err = h.st.InTenant(ctx, h.tenant, func(tx *store.Tx) error {
			if err := tx.QueryRow(ctx, `SELECT token_hash FROM node_bootstrap_tokens WHERE id = $1`,
				tok.ID).Scan(&hash); err != nil {
				return err
			}
			return tx.QueryRow(ctx, `SELECT count(*) FROM audit_events
				WHERE resource_id = $1 AND revision_id IS NULL`, tok.ID).Scan(&audited)
		})
		if err != nil {
			t.Fatal(err)
		}
		if !tok.Matches(hash) || len(hash) != sha256.Size {
			t.Error("stored hash does not match the token")
		}
		if audited != 1 {
			t.Errorf("audit events for the token = %d", audited)
		}
	})

	t.Run("bootstrap token rules", func(t *testing.T) {
		again, err := bootstrap(h.root, "vps-2", "b1", nil)
		if err != nil {
			t.Fatal(err)
		}
		if again.GetToken() != "" || again.GetCaCertSha256() == "" {
			t.Error("replay must omit the token and keep the CA pin")
		}
		_, err = bootstrap(projOwner.GetSecret(), "vps-3", "b2", nil)
		wantCode(t, err, connect.CodePermissionDenied)
		_, err = bootstrap(h.root, "vps-1", "b3", nil)
		wantCode(t, err, connect.CodeAlreadyExists)
		_, err = bootstrap(h.root, "vps-4", "b4", timestamppb.New(time.Now().Add(48*time.Hour)))
		wantCode(t, err, connect.CodeInvalidArgument)
	})

	t.Run("get and list need a tenant-wide token", func(t *testing.T) {
		got, err := nodes.GetNode(ctx, req(h.root, &bohuxv1.GetNodeRequest{
			Lookup: &bohuxv1.GetNodeRequest_Slug{Slug: "vps-1"}}))
		if err != nil {
			t.Fatal(err)
		}
		n := got.Msg.GetNode()
		if n.GetMeta().GetId() != existing.String() || n.GetState() != bohuxv1.NodeState_NODE_STATE_ONLINE ||
			n.GetAgentVersion() != "0.1.0" {
			t.Errorf("node = %v", n)
		}
		list, err := nodes.ListNodes(ctx, req(h.root, &bohuxv1.ListNodesRequest{}))
		if err != nil {
			t.Fatal(err)
		}
		if len(list.Msg.GetNodes()) != 1 {
			t.Errorf("listed %d nodes", len(list.Msg.GetNodes()))
		}
		_, err = nodes.ListNodes(ctx, req(projOwner.GetSecret(), &bohuxv1.ListNodesRequest{}))
		wantCode(t, err, connect.CodePermissionDenied)
	})

	t.Run("revoke", func(t *testing.T) {
		got, err := nodes.GetNode(ctx, req(h.root, &bohuxv1.GetNodeRequest{
			Lookup: &bohuxv1.GetNodeRequest_Id{Id: existing.String()}}))
		if err != nil {
			t.Fatal(err)
		}
		rev := got.Msg.GetNode().GetMeta().GetRevisionId()
		resp, err := nodes.RevokeNode(ctx, req(h.root, &bohuxv1.RevokeNodeRequest{
			Id: existing.String(), ExpectedRevisionId: rev, IdempotencyKey: "r1"}))
		if err != nil {
			t.Fatal(err)
		}
		n := resp.Msg.GetNode()
		if n.GetState() != bohuxv1.NodeState_NODE_STATE_REVOKED || n.GetRevokedAt() == nil ||
			n.GetAgentVersion() != "0.1.0" {
			t.Errorf("revoked node = %v", n)
		}
		_, err = nodes.RevokeNode(ctx, req(h.root, &bohuxv1.RevokeNodeRequest{
			Id: existing.String(), ExpectedRevisionId: rev, IdempotencyKey: "r2"}))
		wantCode(t, err, connect.CodeAborted)
	})
}
