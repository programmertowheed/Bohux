//go:build integration

package deploy

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
	"google.golang.org/protobuf/encoding/protojson"

	enumspb "go.temporal.io/api/enums/v1"
	historypb "go.temporal.io/api/history/v1"
)

// stubWorker runs a worker on queue with the deploy and notification workflows and
// stub activities under the real activities' names (a history records names, not
// code). A release named "release-broken" fails its health gate; notifyChannel
// decides each delivery to the channels "chat" and "hook".
func stubWorker(t *testing.T, c client.Client, queue string, notifyChannel func(context.Context, NotifyInput) error) {
	t.Helper()
	w := worker.New(c, queue, worker.Options{})
	w.RegisterWorkflow(Workflow)
	w.RegisterWorkflow(NotifyWorkflow)
	stubs := map[string]any{
		"RecordStart":        func(context.Context, Input) (bool, error) { return false, nil },
		"LastHealthyRelease": func(context.Context, LastHealthyInput) (string, error) { return goodRelease, nil },
		"SetState":           func(context.Context, StateChange) error { return nil },
		"AssignRelease": func(context.Context, AssignInput) (Assigned, error) {
			return Assigned{NodeID: "node-1", Generation: 1}, nil
		},
		"WaitHealthy": func(_ context.Context, in WaitInput) error {
			if in.ReleaseID == "release-broken" {
				return nonRetryable(ErrTypeHealthTimeout)
			}
			return nil
		},
		"NotificationChannels": func(context.Context, NotifyInput) ([]string, error) {
			return []string{"chat", "hook"}, nil
		},
		"NotifyChannel": notifyChannel,
	}
	for name, fn := range stubs {
		w.RegisterActivityWithOptions(fn, activity.RegisterOptions{Name: name})
	}
	if err := w.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(w.Stop)
}

func temporalClient(t *testing.T) client.Client {
	t.Helper()
	addr := os.Getenv("BOHUX_TEST_TEMPORAL_ADDRESS")
	if addr == "" {
		t.Skip("BOHUX_TEST_TEMPORAL_ADDRESS is not set")
	}
	c, err := client.Dial(client.Options{HostPort: addr})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	return c
}

// TestNotificationsDoNotHoldTheApp: a Deployment whose channel keeps failing frees its
// App as soon as it ends, so the next deploy or rollback starts at once while the
// notification run still retries (ADR-0048).
func TestNotificationsDoNotHoldTheApp(t *testing.T) {
	c := temporalClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	queue := "notify-" + uuid.NewString()
	stubWorker(t, c, queue, func(context.Context, NotifyInput) error { return errors.New("chat service down") })
	s := &Starter{Client: c, TaskQueue: queue}
	first := input()
	first.AppID, first.DeploymentID, first.ReleaseID = "app-"+uuid.NewString(), "dep-"+uuid.NewString(), "release-broken"
	id, runID, err := s.Start(ctx, first)
	if err != nil {
		t.Fatal(err)
	}
	var res Result
	if err := c.GetWorkflow(ctx, id, runID).Get(ctx, &res); err != nil || res.State != StateRolledBack {
		t.Fatalf("first run = %+v, %v; want rolled back", res, err)
	}
	next := first
	next.DeploymentID, next.ReleaseID = "dep-"+uuid.NewString(), goodRelease
	nid, nrun, err := s.Start(ctx, next)
	if err != nil {
		t.Fatalf("starting the next deploy right after the first ended = %v, want it started", err)
	}
	t.Cleanup(func() {
		_ = c.TerminateWorkflow(context.Background(), NotifyWorkflowID(first.DeploymentID), "", "test done")
		_ = c.TerminateWorkflow(context.Background(), NotifyWorkflowID(next.DeploymentID), "", "test done")
	})
	if err := c.GetWorkflow(ctx, nid, nrun).Get(ctx, &res); err != nil || res.State != StateSucceeded {
		t.Errorf("next run = %+v, %v; want succeeded", res, err)
	}
	d, err := c.DescribeWorkflowExecution(ctx, NotifyWorkflowID(first.DeploymentID), "")
	if err != nil {
		t.Fatal(err)
	}
	if st := d.GetWorkflowExecutionInfo().GetStatus(); st != enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING {
		t.Errorf("the first Deployment's notification run is %v, want still running (retrying)", st)
	}
}

// TestRecordHistory writes the history of a deploy run, made by the code as it is now,
// to testdata/, for TestReplay to check that later code still replays it (the
// new-workflow skill). Run it before changing the workflow's commands:
//
//	BOHUX_RECORD_HISTORY=1 go test -tags=integration -run TestRecordHistory ./internal/workflow/deploy
func TestRecordHistory(t *testing.T) {
	if os.Getenv("BOHUX_RECORD_HISTORY") == "" {
		t.Skip("set BOHUX_RECORD_HISTORY (and BOHUX_TEST_TEMPORAL_ADDRESS) to record histories")
	}
	c := temporalClient(t)
	queue := "record-" + uuid.NewString()
	stubWorker(t, c, queue, func(ctx context.Context, in NotifyInput) error {
		if in.ChannelID == "hook" && activity.GetInfo(ctx).Attempt < 2 {
			return errors.New("hook down")
		}
		return nil
	})
	for name, release := range map[string]string{"succeeded": newRelease, "rolled-back": "release-broken"} {
		in := input()
		in.AppID, in.ReleaseID = "app-"+name, release
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		run, err := c.ExecuteWorkflow(ctx, client.StartWorkflowOptions{ID: "record-" + uuid.NewString(), TaskQueue: queue}, Workflow, in)
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		var res Result
		if err := run.Get(ctx, &res); err != nil {
			cancel()
			t.Fatal(err)
		}
		hist := &historypb.History{}
		it := c.GetWorkflowHistory(ctx, run.GetID(), run.GetRunID(), false, 0)
		for it.HasNext() {
			e, err := it.Next()
			if err != nil {
				cancel()
				t.Fatal(err)
			}
			hist.Events = append(hist.Events, e)
		}
		cancel()
		b, err := protojson.MarshalOptions{Indent: "  "}.Marshal(hist)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join("testdata", "history-"+name+"-v1.json")
		if err := os.WriteFile(path, b, 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("%s: %s, %d events -> %s", name, res.State, len(hist.Events), path)
	}
}
