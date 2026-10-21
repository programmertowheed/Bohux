//go:build integration

package api

import (
	"context"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protojson"

	agentv1 "github.com/bohumix/bohux/gen/bohux/agent/v1"
	bohuxv1 "github.com/bohumix/bohux/gen/bohux/v1"
	"github.com/bohumix/bohux/gen/bohux/v1/bohuxv1connect"
	"github.com/bohumix/bohux/internal/store"
)

// TestUsage: the latest sample a node reported reads back through Node.usage and
// GetAppUsage, for viewers of the App's project only (ADR-0040).
func TestUsage(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	srv := h.server()
	workloads := bohuxv1connect.NewWorkloadServiceClient(srv.Client(), srv.URL)
	nodes := bohuxv1connect.NewNodeServiceClient(srv.Client(), srv.URL)
	apps := bohuxv1connect.NewAppServiceClient(srv.Client(), srv.URL)
	envs := bohuxv1connect.NewEnvironmentServiceClient(srv.Client(), srv.URL)
	project := h.project("web")
	node := h.node("vps-1")
	e, err := envs.CreateEnvironment(ctx, req(h.root, &bohuxv1.CreateEnvironmentRequest{ProjectId: project.String(),
		Slug: "prod", ProviderBinding: dockerOn(node), IdempotencyKey: "e"}))
	if err != nil {
		t.Fatal(err)
	}
	a, err := apps.CreateApp(ctx, req(h.root, &bohuxv1.CreateAppRequest{EnvironmentId: e.Msg.GetEnvironment().GetMeta().GetId(),
		Slug: "api", Source: &bohuxv1.Source{Kind: &bohuxv1.Source_Image{Image: &bohuxv1.ImageSource{
			Repository: "ghcr.io/acme/api", Digest: "sha256:" + strings.Repeat("ab", 32)}}},
		Runtime: &bohuxv1.Runtime{Replicas: 2}, IdempotencyKey: "a"}))
	if err != nil {
		t.Fatal(err)
	}
	appID := a.Msg.GetApp().GetMeta().GetId()

	got, err := workloads.GetAppUsage(ctx, req(h.root, &bohuxv1.GetAppUsageRequest{AppId: appID}))
	if err != nil || got.Msg.GetUsage() != nil {
		t.Fatalf("before any report: %v, %v", got, err)
	}
	report := &agentv1.StatusReport{Generation: 1,
		Usage: &bohuxv1.NodeUsage{CpuCores: 2, CpuBusy: 0.25, MemoryTotalBytes: 2 << 30, MemoryAvailableBytes: 1 << 30},
		Workloads: []*agentv1.WorkloadStatus{{AppId: appID, ReadyReplicas: 2,
			Usage: &bohuxv1.Usage{CpuMillicores: 120, MemoryBytes: 64 << 20}}}}
	b, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.st.InTenant(ctx, h.tenant, func(tx *store.Tx) error {
		return tx.Exec(ctx, `UPDATE nodes_heads SET report = $1, report_generation = 1 WHERE id = $2`, b, node)
	}); err != nil {
		t.Fatal(err)
	}
	got, err = workloads.GetAppUsage(ctx, req(h.root, &bohuxv1.GetAppUsageRequest{AppId: appID}))
	if err != nil || got.Msg.GetUsage().GetCpuMillicores() != 120 || got.Msg.GetReadyReplicas() != 2 {
		t.Fatalf("GetAppUsage = %v, %v", got, err)
	}
	n, err := nodes.GetNode(ctx, req(h.root, &bohuxv1.GetNodeRequest{Lookup: &bohuxv1.GetNodeRequest_Id{Id: node.String()}}))
	if err != nil || n.Msg.GetNode().GetUsage().GetCpuBusy() != 0.25 {
		t.Fatalf("GetNode usage = %v, %v", n, err)
	}
	other, err := h.createToken(h.root, h.project("other"), bohuxv1.Role_ROLE_VIEWER, "o")
	if err != nil {
		t.Fatal(err)
	}
	_, err = workloads.GetAppUsage(ctx, req(other.GetSecret(), &bohuxv1.GetAppUsageRequest{AppId: appID}))
	wantCode(t, err, connect.CodeNotFound)
}
