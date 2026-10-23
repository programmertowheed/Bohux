//go:build integration

package agentapi

import (
	"context"
	"strings"
	"testing"
	"time"

	agentv1 "github.com/bohumix/bohux/gen/bohux/agent/v1"
	"github.com/bohumix/bohux/internal/store"
)

func TestStatusReportsAndSilence(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	h.srv.SilenceTimeout = 300 * time.Millisecond
	a := h.register("vps-1")

	sctx, cancel := context.WithCancel(ctx)
	stream, _, err := open(sctx, a.client, hello(a.node, "0.3.0"))
	if err != nil {
		t.Fatal(err)
	}
	defer closeStream(stream, cancel)
	err = stream.Send(&agentv1.ConnectRequest{Msg: &agentv1.ConnectRequest_StatusReport{
		StatusReport: &agentv1.StatusReport{Generation: 7, Workloads: []*agentv1.WorkloadStatus{{
			AppId: "app-1", State: agentv1.WorkloadState_WORKLOAD_STATE_HEALTHY, ReadyReplicas: 2}}}}})
	if err != nil {
		t.Fatal(err)
	}

	t.Run("the latest report is stored", func(t *testing.T) {
		deadline := time.Now().Add(5 * time.Second)
		for {
			var gen *int64
			var report *string
			err := h.st.InTenant(ctx, h.tenant, func(tx *store.Tx) error {
				return tx.QueryRow(ctx, `SELECT report_generation, report::text FROM nodes_heads WHERE id = $1`,
					a.node).Scan(&gen, &report)
			})
			if err != nil {
				t.Fatal(err)
			}
			if gen != nil && *gen == 7 && report != nil {
				if !strings.Contains(*report, "WORKLOAD_STATE_HEALTHY") || !strings.Contains(*report, "app-1") {
					t.Errorf("stored report %s", *report)
				}
				return
			}
			if time.Now().After(deadline) {
				t.Fatal("report not stored")
			}
			time.Sleep(20 * time.Millisecond)
		}
	})

	t.Run("a silent stream is closed and the node goes offline", func(t *testing.T) {
		h.waitState(a.node, "offline")
	})
}
