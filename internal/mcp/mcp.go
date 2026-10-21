// Package mcp is `bohux mcp`: a Model Context Protocol server over stdio whose tools
// are the public API's RPCs, read from proto/ at run time (ADR-0018). It is a client
// of the API and nothing more (rule 8): the caller's token decides what succeeds, and
// RPCs that carry credentials are never tools.
package mcp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/dynamicpb"

	bohuxv1 "github.com/bohumix/bohux/gen/bohux/v1"
	"github.com/bohumix/bohux/internal/version"
)

// Package is the proto package whose RPCs become tools.
const Package = "bohux.v1"

// Tool is one RPC as an MCP tool.
type Tool struct {
	Name      string
	Procedure string
	Method    protoreflect.MethodDescriptor
	Policy    bohuxv1.ToolPolicy
}

// Tools lists the tools the API's RPCs make, sorted by name. Every RPC must carry a
// ToolPolicy; a streaming RPC must be HIDDEN.
func Tools() ([]Tool, error) {
	_ = bohuxv1.File_bohux_v1_options_proto // the generated package registers the files
	var (
		out  []Tool
		errs []error
	)
	seen := map[string]string{}
	protoregistry.GlobalFiles.RangeFilesByPackage(Package, func(fd protoreflect.FileDescriptor) bool {
		svcs := fd.Services()
		for i := 0; i < svcs.Len(); i++ {
			svc := svcs.Get(i)
			ms := svc.Methods()
			for j := 0; j < ms.Len(); j++ {
				m := ms.Get(j)
				policy, _ := proto.GetExtension(m.Options(), bohuxv1.E_Tool).(bohuxv1.ToolPolicy)
				switch {
				case policy == bohuxv1.ToolPolicy_TOOL_POLICY_UNSPECIFIED:
					errs = append(errs, fmt.Errorf("%s has no tool policy", m.FullName()))
					continue
				case policy == bohuxv1.ToolPolicy_TOOL_POLICY_HIDDEN:
					continue
				case m.IsStreamingClient() || m.IsStreamingServer():
					errs = append(errs, fmt.Errorf("%s streams and must be HIDDEN", m.FullName()))
					continue
				}
				name := string(m.Name())
				if prev, dup := seen[name]; dup {
					errs = append(errs, fmt.Errorf("tool %s comes from %s and %s", name, prev, m.FullName()))
					continue
				}
				seen[name] = string(m.FullName())
				out = append(out, Tool{Name: name, Procedure: "/" + string(svc.FullName()) + "/" + name,
					Method: m, Policy: policy})
			}
		}
		return true
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, errors.Join(errs...)
}

// Config is where the server sends its calls.
type Config struct {
	APIURL string
	Token  string
	// HTTP speaks to the API; it must do HTTP/2 for h2c URLs, as the CLI's does.
	HTTP *http.Client
}

// NewServer returns the MCP server with one tool per non-hidden RPC.
func NewServer(cfg Config) (*sdk.Server, error) {
	tools, err := Tools()
	if err != nil {
		return nil, err
	}
	s := sdk.NewServer(&sdk.Implementation{Name: "bohux", Version: version.Version}, nil)
	for _, t := range tools {
		s.AddTool(t.mcpTool(), t.handler(cfg))
	}
	return s, nil
}

func (t Tool) mcpTool() *sdk.Tool {
	readOnly := t.Policy == bohuxv1.ToolPolicy_TOOL_POLICY_READ_ONLY
	destructive := t.Policy == bohuxv1.ToolPolicy_TOOL_POLICY_DESTRUCTIVE
	kind := map[bohuxv1.ToolPolicy]string{
		bohuxv1.ToolPolicy_TOOL_POLICY_READ_ONLY:   "Reads only.",
		bohuxv1.ToolPolicy_TOOL_POLICY_MUTATING:    "Creates or changes a resource.",
		bohuxv1.ToolPolicy_TOOL_POLICY_DESTRUCTIVE: "Deletes, revokes, or changes what runs: confirm with the user first.",
	}[t.Policy]
	desc := fmt.Sprintf("Bohux API %s. %s Arguments are the fields of %s (proto field names).",
		strings.TrimPrefix(t.Procedure, "/"), kind, t.Method.Input().FullName())
	if t.Method.Input().Fields().ByName("idempotency_key") != nil {
		desc += " idempotency_key may be left out: a fresh one is used."
	}
	open := false
	return &sdk.Tool{Name: t.Name, Description: desc, InputSchema: Schema(t.Method.Input()),
		Annotations: &sdk.ToolAnnotations{ReadOnlyHint: readOnly, DestructiveHint: &destructive,
			IdempotentHint: readOnly, OpenWorldHint: &open}}
}

func (t Tool) handler(cfg Config) sdk.ToolHandler {
	in, out := t.Method.Input(), t.Method.Output()
	client := connect.NewClient[dynamicpb.Message, dynamicpb.Message](cfg.HTTP,
		strings.TrimSuffix(cfg.APIURL, "/")+t.Procedure,
		connect.WithSchema(t.Method),
		connect.WithResponseInitializer(func(_ connect.Spec, msg any) error {
			dm, ok := msg.(*dynamicpb.Message)
			if !ok {
				return fmt.Errorf("unexpected response type %T", msg)
			}
			*dm = *dynamicpb.NewMessage(out)
			return nil
		}))
	return func(ctx context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		msg := dynamicpb.NewMessage(in)
		if args := req.Params.Arguments; len(args) > 0 && string(args) != "null" {
			if err := (protojson.UnmarshalOptions{DiscardUnknown: false}).Unmarshal(args, msg); err != nil {
				return failed("invalid arguments for " + t.Name + ": " + err.Error()), nil
			}
		}
		if f := in.Fields().ByName("idempotency_key"); f != nil && msg.Get(f).String() == "" {
			msg.Set(f, protoreflect.ValueOfString("mcp-"+uuid.NewString()))
		}
		r := connect.NewRequest(msg)
		r.Header().Set("Authorization", "Bearer "+cfg.Token)
		r.Header().Set("User-Agent", "bohux-mcp/"+version.Version)
		resp, err := client.CallUnary(ctx, r)
		if err != nil {
			// The API's errors never carry secret values (rule 3); the model sees them.
			return failed(t.Name + ": " + err.Error()), nil
		}
		b, err := protojson.MarshalOptions{UseProtoNames: true, Multiline: true}.Marshal(resp.Msg)
		if err != nil {
			return nil, fmt.Errorf("encode %s response: %w", t.Name, err)
		}
		return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: string(b)}}}, nil
	}
}

func failed(text string) *sdk.CallToolResult {
	return &sdk.CallToolResult{IsError: true, Content: []sdk.Content{&sdk.TextContent{Text: text}}}
}

// Run serves MCP on stdin and stdout until the client hangs up or ctx ends.
func Run(ctx context.Context, cfg Config) error {
	s, err := NewServer(cfg)
	if err != nil {
		return err
	}
	// The client hanging up (EOF on stdin) is the normal end of a session.
	if err := s.Run(ctx, &sdk.StdioTransport{}); err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, io.EOF) {
		return fmt.Errorf("mcp: %w", err)
	}
	return nil
}
