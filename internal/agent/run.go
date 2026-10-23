package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"

	"google.golang.org/protobuf/proto"

	agentv1 "github.com/bohumix/bohux/gen/bohux/agent/v1"
	"github.com/bohumix/bohux/gen/bohux/agent/v1/agentv1connect"
	bohuxv1 "github.com/bohumix/bohux/gen/bohux/v1"
	"github.com/bohumix/bohux/internal/version"
)

const (
	// defaultReportInterval is how often the agent sends a StatusReport even when
	// nothing changed; the control plane closes streams silent for three of these.
	defaultReportInterval = 30 * time.Second

	defaultMinBackoff = time.Second
	defaultMaxBackoff = time.Minute
	// After a VersionRejected, retrying soon cannot help until someone upgrades.
	rejectedBackoff = 5 * time.Minute
	// A stream that lived this long counts as healthy and resets the backoff.
	healthyStream = time.Minute
)

// ReportSource supplies StatusReports for the stream: the Reconciler.
type ReportSource interface {
	Latest() *agentv1.StatusReport
	Updated() <-chan struct{}
}

// RunOptions configure the agent's connection loop.
type RunOptions struct {
	State *State
	// Cache receives verified assignments and supplies Hello.applied_generation.
	Cache     *Cache
	Providers []*bohuxv1.ProviderDescriptor
	// Reports, if set, is sent on every change and at least every ReportInterval.
	// Without it the agent still sends an empty report each interval as a heartbeat.
	Reports        ReportSource
	ReportInterval time.Duration
	// Disk, if set, marks every report degraded while the node is under disk pressure
	// (ADR-0020 item 4), and a change is sent at once.
	Disk *DiskMonitor
	// Builder, if set, answers build requests (ADR-0028); without it every request
	// fails with BUILDER_UNAVAILABLE.
	Builder BuildRunner
	// Workloads, if set, serves log and exec requests (WorkloadService); without it
	// logs are empty and exec fails.
	Workloads WorkloadAccess
	// MinBackoff and MaxBackoff bound reconnect delays; zero means 1s and 1m.
	MinBackoff, MaxBackoff time.Duration
	// Link, if set, records whether the control plane last rejected this agent.
	Link *Link
}

// Link records the control plane's last verdict on this agent: rejected (a version it
// refuses) or accepted (any other message). A release that is healthy while the
// control plane rejects its agent cannot be seen healthy there, so the reconciler
// does not let it cancel its fallback (ADR-0042). An unreachable control plane is no
// verdict: rule 1, its outage changes nothing on the node.
type Link struct{ rejected atomic.Bool }

// Rejected reports whether the control plane's last answer was a version rejection.
func (l *Link) Rejected() bool { return l != nil && l.rejected.Load() }

func (l *Link) set(rejected bool) {
	if l != nil {
		l.rejected.Store(rejected)
	}
}

// BuildRunner builds a Release; Builder implements it.
type BuildRunner interface {
	Build(ctx context.Context, req *agentv1.BuildRequest) *agentv1.BuildResult
}

// report is what to send now: the reconciler's latest, or a heartbeat that carries
// only the cached generation.
func (o RunOptions) report() *agentv1.StatusReport {
	rep := &agentv1.StatusReport{Generation: o.Cache.Generation()}
	if o.Reports != nil {
		if r := o.Reports.Latest(); r != nil {
			// A copy: the reconciler's report is shared.
			rep = proto.Clone(r).(*agentv1.StatusReport)
		}
	}
	if o.Disk != nil {
		rep.Degraded, rep.DegradedDetail = o.Disk.Degraded()
	}
	return rep
}

// Run keeps a stream to the control plane open until ctx ends, reconnecting with
// capped exponential backoff. It never gives up: the control plane being away is
// normal, and nothing on this node depends on it (rule 1).
func Run(ctx context.Context, o RunOptions) error {
	minB, maxB := o.MinBackoff, o.MaxBackoff
	if minB <= 0 {
		minB = defaultMinBackoff
	}
	if maxB <= 0 {
		maxB = defaultMaxBackoff
	}
	client := agentv1connect.NewAgentServiceClient(&http.Client{Transport: &http.Transport{
		TLSClientConfig: o.State.mutualTLS(), ForceAttemptHTTP2: true,
	}}, "https://"+o.State.ControlPlane)

	backoff := minB
	for {
		start := time.Now()
		err := session(ctx, client, o)
		if ctx.Err() != nil {
			return nil
		}
		wait := backoff
		var rej *rejectedError
		switch {
		case errors.As(err, &rej):
			slog.WarnContext(ctx, "control plane rejected this agent version; workloads keep running",
				"min_agent_version", rej.MinAgentVersion, "reason", rej.Message)
			wait = rejectedBackoff
		case time.Since(start) >= healthyStream:
			backoff, wait = minB, minB
		default:
			slog.WarnContext(ctx, "control plane unreachable; workloads keep running", "err", err, "retry_in", wait)
			backoff = min(backoff*2, maxB)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(wait):
		}
	}
}

type rejectedError struct {
	*agentv1.VersionRejected
}

func (e *rejectedError) Error() string { return "version rejected: " + e.GetMessage() }

// closeGrace bounds how long a shutting-down agent waits for the server to end the
// stream after the agent closes its side.
const closeGrace = 2 * time.Second

// session runs one stream until it fails or ctx ends.
func session(ctx context.Context, client agentv1connect.AgentServiceClient, o RunOptions) error {
	// The stream has its own context so that on shutdown the agent can close its side
	// in order: an HTTP/2 stream whose context is already canceled cannot send the
	// end-of-stream that tells the server the agent left (see the S4c1 note).
	streamCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	defer cancel()
	stream := client.Connect(streamCtx)

	defer func() { _ = stream.CloseRequest() }()
	if err := stream.Send(&agentv1.ConnectRequest{Msg: &agentv1.ConnectRequest_Hello{Hello: &agentv1.Hello{
		NodeId:            o.State.NodeID(),
		AgentVersion:      version.Version,
		ProtocolVersion:   version.Protocol,
		Providers:         o.Providers,
		AppliedGeneration: o.Cache.Generation(),
	}}}); err != nil {
		if errors.Is(err, io.EOF) {
			// The control plane ended the stream first; its reason is on the receive
			// side.
			if _, rerr := stream.Receive(); rerr != nil {
				err = rerr
			}
		}
		return fmt.Errorf("send hello: %w", err)
	}

	msgs := make(chan *agentv1.ConnectResponse)
	errc := make(chan error, 1)
	go func() {
		for {
			msg, err := stream.Receive()
			if err != nil {
				errc <- err
				return
			}
			select {
			case msgs <- msg:
			case <-streamCtx.Done():
				return
			}
		}
	}()
	interval := o.ReportInterval
	if interval <= 0 {
		interval = defaultReportInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	var updated, diskChanged <-chan struct{}
	if o.Reports != nil {
		updated = o.Reports.Updated()
	}
	if o.Disk != nil {
		diskChanged = o.Disk.Changed()
	}
	sendReport := func() error {
		if err := stream.Send(&agentv1.ConnectRequest{Msg: &agentv1.ConnectRequest_StatusReport{
			StatusReport: o.report()}}); err != nil {
			return fmt.Errorf("send status report: %w", err)
		}
		return nil
	}
	if err := sendReport(); err != nil {
		return err
	}
	// Builds run beside the loop; their results come back here, the stream's only
	// sender. A result for a stream that ended is dropped: the control plane asks
	// again, and the Builder answers from its recent results.
	builds := make(chan *agentv1.BuildResult, 4)
	// Log lines and exec output take the same way out.
	outbox := make(chan *agentv1.ConnectRequest, 16)
	rl := newRelays(streamCtx, o.Workloads, outbox)
	defer rl.closeAll()
	first := true
	for {
		select {
		case m := <-outbox:
			if err := stream.Send(m); err != nil {
				return fmt.Errorf("send workload output: %w", err)
			}
		case r := <-builds:
			if err := stream.Send(&agentv1.ConnectRequest{Msg: &agentv1.ConnectRequest_BuildResult{BuildResult: r}}); err != nil {
				return fmt.Errorf("send build result: %w", err)
			}
		case <-ticker.C:
			if err := sendReport(); err != nil {
				return err
			}
		case <-updated:
			if err := sendReport(); err != nil {
				return err
			}
		case <-diskChanged:
			if err := sendReport(); err != nil {
				return err
			}
		case <-ctx.Done():
			_ = stream.CloseRequest()
			grace := time.After(closeGrace)
			for {
				select {
				case <-msgs: // late messages; the server ends the stream next
				case <-errc:
					return nil
				case <-grace:
					return nil
				}
			}
		case err := <-errc:
			return fmt.Errorf("stream: %w", err)
		case msg := <-msgs:
			if first {
				slog.InfoContext(ctx, "connected to control plane", "node", o.State.NodeID())
				first = false
			}
			_, rejected := msg.GetMsg().(*agentv1.ConnectResponse_VersionRejected)
			o.Link.set(rejected)
			switch m := msg.GetMsg().(type) {
			case *agentv1.ConnectResponse_VersionRejected:
				return &rejectedError{m.VersionRejected}
			case *agentv1.ConnectResponse_Ping:
			case *agentv1.ConnectResponse_Assignment:
				if err := accept(ctx, stream, o.Cache, m.Assignment); err != nil {
					return err
				}
			case *agentv1.ConnectResponse_BuildRequest:
				go func(req *agentv1.BuildRequest) {
					res := &agentv1.BuildResult{ReleaseId: req.GetReleaseId(),
						Failure: &bohuxv1.ErrorDetail{Reason: BuildNoBuilder}}
					if o.Builder != nil {
						res = o.Builder.Build(ctx, req)
					}
					select {
					case builds <- res:
					case <-streamCtx.Done():
					}
				}(m.BuildRequest)
			default:
				if !rl.handle(msg) {
					slog.DebugContext(ctx, "unhandled control-plane message", "type", fmt.Sprintf("%T", m))
				}
			}
		}
	}
}

// accept caches a verified, newer assignment and acknowledges it. A forged, foreign,
// or stale assignment is logged and ignored: the cached one stays in force.
func accept(ctx context.Context, stream interface {
	Send(*agentv1.ConnectRequest) error
}, c *Cache, a *agentv1.Assignment) error {
	cached, err := c.Accept(a)
	if err != nil {
		slog.WarnContext(ctx, "assignment refused", "err", err)
		return nil
	}
	if !cached {
		return nil
	}
	gen := c.Generation()
	slog.InfoContext(ctx, "assignment cached", "generation", gen)
	if err := stream.Send(&agentv1.ConnectRequest{Msg: &agentv1.ConnectRequest_Ack{
		Ack: &agentv1.Ack{Generation: gen}}}); err != nil {
		return fmt.Errorf("send ack: %w", err)
	}
	return nil
}
