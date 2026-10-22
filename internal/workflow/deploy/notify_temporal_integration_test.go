//go:build integration

package deploy

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/client"

	"github.com/bohumix/bohux/internal/store"
	"github.com/bohumix/bohux/internal/store/fixture"
)

// TestFailingChannelDoesNotBlockTheNextDeploy runs real deploy workflows on a Temporal
// dev server: a deploy whose notification channel keeps failing reaches its terminal
// state, and the App's next deploy starts right after, while the notifications are
// still being retried in their own workflow (ADR-0048).
func TestFailingChannelDoesNotBlockTheNextDeploy(t *testing.T) {
	addr := os.Getenv("BOHUX_TEST_TEMPORAL_ADDRESS")
	if addr == "" {
		t.Skip("BOHUX_TEST_TEMPORAL_ADDRESS is not set")
	}
	ctx := context.Background()
	h := newHarness(t)
	var posts atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		posts.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)
	secrets := memSecrets{}
	h.acts.Secrets, h.acts.HTTP = secrets, srv.Client()

	app, env := fixture.DeployableApp(t, h.st, h.tenant, h.node, "api", 1)
	h.channel(secrets, h.projectOf(env), "slack", srv.URL+"/slack", "", "failed")

	c, err := client.Dial(client.Options{HostPort: addr, Namespace: "default"})
	if err != nil {
		t.Fatalf("dial Temporal: %v", err)
	}
	queue := "bohux-deploy-test-" + uuid.NewString()
	wctx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		RunWorker(wctx, c, queue, h.acts)
	}()
	var notifyIDs []string
	t.Cleanup(func() {
		// The notifications would retry for another minute on a queue nobody polls.
		for _, id := range notifyIDs {
			_ = c.TerminateWorkflow(ctx, id, "", "test over")
		}
		stop()
		<-done
		c.Close()
	})
	starter := &Starter{Client: c, TaskQueue: queue, HealthTimeout: 20 * time.Second}

	// A Release that failed to build makes the deploy fail at once, notifying "failed".
	first := h.input(app, env, fixture.Release(t, h.st, h.tenant, app, "failed", false))
	if _, _, err := starter.Start(ctx, first); err != nil {
		t.Fatalf("first Start: %v", err)
	}
	notifyIDs = append(notifyIDs, NotifyWorkflowID(first.DeploymentID))
	waitUntil(t, 30*time.Second, "the first deployment to fail", func() bool {
		return h.stateOf(first.DeploymentID) == StateFailed
	})

	// Only the workflow task that starts the child separates the terminal row from the
	// run closing; the failing channel is retried for over a minute.
	second := h.input(app, env, fixture.Release(t, h.st, h.tenant, app, "failed", false))
	terminal := time.Now()
	for {
		_, _, err := starter.Start(ctx, second)
		if err == nil {
			break
		}
		if !errors.Is(err, ErrRunning) || time.Since(terminal) > 2*time.Second {
			t.Fatalf("second Start %s after the first deployment failed: %v", time.Since(terminal), err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Logf("second Start accepted %s after the first deployment's terminal state", time.Since(terminal))
	notifyIDs = append(notifyIDs, NotifyWorkflowID(second.DeploymentID))

	d, err := c.DescribeWorkflowExecution(ctx, NotifyWorkflowID(first.DeploymentID), "")
	if err != nil {
		t.Fatalf("describe the first deployment's notifications: %v", err)
	}
	if s := d.GetWorkflowExecutionInfo().GetStatus(); s != enums.WORKFLOW_EXECUTION_STATUS_RUNNING {
		t.Fatalf("first deployment's notifications are %s; the test needs them still retrying", s)
	}
	waitUntil(t, 30*time.Second, "the failing channel to be tried", func() bool { return posts.Load() > 0 })
	waitUntil(t, 30*time.Second, "the second deployment to fail", func() bool {
		return h.stateOf(second.DeploymentID) == StateFailed
	})
}

// stateOf reads a Deployment's state, "" while its row does not exist: without the
// API, RecordStart writes it.
func (h *harness) stateOf(id string) State {
	h.t.Helper()
	ctx := context.Background()
	var state string
	err := h.st.InTenant(ctx, h.tenant, func(tx *store.Tx) error {
		return tx.QueryRow(ctx, `SELECT r.state FROM deployments_heads h
			JOIN deployments_revisions r ON r.id = h.revision_id WHERE h.id = $1`, id).Scan(&state)
	})
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		h.t.Fatalf("read deployment: %v", err)
	}
	return State(state)
}

func waitUntil(t *testing.T, limit time.Duration, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(limit)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
