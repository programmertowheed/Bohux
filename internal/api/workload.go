package api

import (
	"context"
	"errors"
	"fmt"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	agentv1 "github.com/bohumix/bohux/gen/bohux/agent/v1"
	bohuxv1 "github.com/bohumix/bohux/gen/bohux/v1"
	"github.com/bohumix/bohux/gen/bohux/v1/bohuxv1connect"
	"github.com/bohumix/bohux/internal/agentapi"
	"github.com/bohumix/bohux/internal/auth"
	"github.com/bohumix/bohux/internal/store"
)

var errNodeOffline = connect.NewError(connect.CodeUnavailable, errors.New("the app's node is not connected"))

type workloadService struct {
	s *Server
}

// appNodeFor checks the caller holds min on the App and returns the App's node.
func (ws *workloadService) appNodeFor(ctx context.Context, rawApp string, min auth.Role) (uuid.UUID, uuid.UUID, error) {
	p, _ := auth.FromContext(ctx)
	app, err := parseID("app_id", rawApp)
	if err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	var node *uuid.UUID
	err = ws.s.Store.InTenant(ctx, p.Tenant, func(tx *store.Tx) error {
		project, env, err := appProject(ctx, tx, app)
		if err != nil {
			return err
		}
		if err := authorizeProject(ctx, project, min); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT r.node_id FROM environments_heads h
			JOIN environments_revisions r ON r.id = h.revision_id WHERE h.id = $1`, env).Scan(&node)
	})
	if err != nil {
		return uuid.Nil, uuid.Nil, toConnect(ctx, err)
	}
	if node == nil {
		return uuid.Nil, uuid.Nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("the app's environment has no node"))
	}
	if ws.s.Agents == nil {
		return uuid.Nil, uuid.Nil, errNodeOffline
	}
	return app, *node, nil
}

// StreamLogs relays the App's log lines from its node (ADR-0007).
func (ws *workloadService) StreamLogs(ctx context.Context, req *connect.Request[bohuxv1.StreamLogsRequest], stream *connect.ServerStream[bohuxv1.StreamLogsResponse]) error {
	msg := req.Msg
	app, node, err := ws.appNodeFor(ctx, msg.GetAppId(), auth.RoleViewer)
	if err != nil {
		return err
	}
	chunks, err := ws.s.Agents.Logs(ctx, node, &agentv1.LogRequest{AppId: app.String(), Instance: msg.GetInstance(),
		Follow: msg.GetFollow(), Since: msg.GetSince(), TailLines: msg.GetTailLines()})
	if errors.Is(err, agentapi.ErrNodeOffline) {
		return errNodeOffline
	}
	if err != nil {
		return toConnect(ctx, err)
	}
	for c := range chunks {
		if len(c.GetLines()) > 0 {
			if err := stream.Send(&bohuxv1.StreamLogsResponse{Lines: c.GetLines()}); err != nil {
				return err
			}
		}
	}
	return nil
}

// Exec runs a command in one of the App's instances. Every session is audited
// (rule 10); its input and output are not recorded anywhere.
func (ws *workloadService) Exec(ctx context.Context, stream *connect.BidiStream[bohuxv1.ExecRequest, bohuxv1.ExecResponse]) error {
	first, err := stream.Receive()
	if err != nil {
		return fmt.Errorf("receive exec start: %w", err)
	}
	start := first.GetStart()
	if start == nil {
		return invalidArgument("start", "REQUIRED", "the first message must be start")
	}
	if len(start.GetCommand()) == 0 {
		return invalidArgument("start.command", "REQUIRED", "command is required")
	}
	app, node, err := ws.appNodeFor(ctx, start.GetAppId(), auth.RoleDeployer)
	if err != nil {
		return err
	}
	p, _ := auth.FromContext(ctx)
	if err := ws.s.Store.InTenant(ctx, p.Tenant, func(tx *store.Tx) error {
		project, _, err := appProject(ctx, tx, app)
		if err != nil {
			return err
		}
		return tx.AuditOnly(ctx, store.Audit{Actor: p.Actor(), Action: bohuxv1connect.WorkloadServiceExecProcedure,
			ResourceType: appType, ProjectID: project}, app)
	}); err != nil {
		return toConnect(ctx, err)
	}
	relay, err := ws.s.Agents.Exec(ctx, node, &agentv1.ExecOpen{AppId: app.String(), Instance: start.GetInstance(),
		Command: start.GetCommand(), Tty: start.GetTty(), Size: start.GetSize()})
	if errors.Is(err, agentapi.ErrNodeOffline) {
		return errNodeOffline
	}
	if err != nil {
		return toConnect(ctx, err)
	}
	defer relay.Close(context.WithoutCancel(ctx))

	// Client input goes to the process until the client closes its side.
	go func() {
		for {
			in, err := stream.Receive()
			if err != nil {
				// The client sent all its input; the process sees end of file.
				relay.CloseStdin(ctx)
				return
			}
			d := &agentv1.ExecData{}
			switch m := in.GetMsg().(type) {
			case *bohuxv1.ExecRequest_Stdin:
				d.Msg = &agentv1.ExecData_Stdin{Stdin: m.Stdin}
			case *bohuxv1.ExecRequest_Resize:
				d.Msg = &agentv1.ExecData_Resize{Resize: m.Resize}
			default:
				continue
			}
			if relay.Send(ctx, d) != nil {
				return
			}
		}
	}()
	for o := range relay.Output {
		out := &bohuxv1.ExecResponse{}
		switch m := o.GetMsg().(type) {
		case *agentv1.ExecOutput_Stdout:
			out.Msg = &bohuxv1.ExecResponse_Stdout{Stdout: m.Stdout}
		case *agentv1.ExecOutput_Stderr:
			out.Msg = &bohuxv1.ExecResponse_Stderr{Stderr: m.Stderr}
		case *agentv1.ExecOutput_Exit:
			out.Msg = &bohuxv1.ExecResponse_Exit{Exit: m.Exit}
		default:
			continue
		}
		if err := stream.Send(out); err != nil {
			return err
		}
		if o.GetExit() != nil {
			return nil
		}
	}
	return errNodeOffline
}
