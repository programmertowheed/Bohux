package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	agentv1 "github.com/bohumix/bohux/gen/bohux/agent/v1"
	"github.com/bohumix/bohux/gen/bohux/agent/v1/agentv1connect"
	bohuxv1 "github.com/bohumix/bohux/gen/bohux/v1"
	"github.com/bohumix/bohux/internal/version"
)

const (
	defaultMinBackoff = time.Second
	defaultMaxBackoff = time.Minute
	// After a VersionRejected, retrying soon cannot help until someone upgrades.
	rejectedBackoff = 5 * time.Minute
	// A stream that lived this long counts as healthy and resets the backoff.
	healthyStream = time.Minute
)

// RunOptions configure the agent's connection loop.
type RunOptions struct {
	State     *State
	Providers []*bohuxv1.ProviderDescriptor
	// MinBackoff and MaxBackoff bound reconnect delays; zero means 1s and 1m.
	MinBackoff, MaxBackoff time.Duration
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
		NodeId:          o.State.NodeID(),
		AgentVersion:    version.Version,
		ProtocolVersion: version.Protocol,
		Providers:       o.Providers,
	}}}); err != nil {
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
	first := true
	for {
		select {
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
			switch m := msg.GetMsg().(type) {
			case *agentv1.ConnectResponse_VersionRejected:
				return &rejectedError{m.VersionRejected}
			case *agentv1.ConnectResponse_Ping:
			default:
				// Assignments, logs, and exec arrive from S4d on.
				slog.DebugContext(ctx, "unhandled control-plane message", "type", fmt.Sprintf("%T", m))
			}
		}
	}
}
