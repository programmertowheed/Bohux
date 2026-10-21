//go:build integration

package mcp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	"github.com/bohumix/bohux/internal/api"
	"github.com/bohumix/bohux/internal/auth"
	"github.com/bohumix/bohux/internal/cli"
	"github.com/bohumix/bohux/internal/pki"
	"github.com/bohumix/bohux/internal/store"
	"github.com/bohumix/bohux/internal/store/storetest"
)

// TestServer drives the MCP server with a real MCP client against a real API: the
// tools are listed with their hints, calls go through the API as the token, and the
// API's refusals come back as tool errors (ADR-0018).
func TestServer(t *testing.T) {
	ctx := context.Background()
	dsn := storetest.NewAppDSN(t)
	if err := store.MigrateDSN(ctx, dsn); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	_, root, err := api.Bootstrap(ctx, st, "acme", "Acme")
	if err != nil {
		t.Fatal(err)
	}
	authority, err := pki.LoadOrCreate(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	(&api.Server{Store: st, Authn: &auth.Authenticator{Store: st}, PKI: authority}).Mount(mux)
	hs := httptest.NewServer(h2c.NewHandler(mux, &http2.Server{}))
	t.Cleanup(hs.Close)

	connectAs := func(token string) *sdk.ClientSession {
		t.Helper()
		srv, err := NewServer(Config{APIURL: hs.URL, Token: token, HTTP: cli.HTTPClient(hs.URL)})
		if err != nil {
			t.Fatal(err)
		}
		st, ct := sdk.NewInMemoryTransports()
		if _, err := srv.Connect(ctx, st, nil); err != nil {
			t.Fatal(err)
		}
		cs, err := sdk.NewClient(&sdk.Implementation{Name: "test", Version: "1"}, nil).Connect(ctx, ct, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = cs.Close() })
		return cs
	}
	cs := connectAs(root)

	list, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]*sdk.Tool{}
	for _, tool := range list.Tools {
		byName[tool.Name] = tool
	}
	if byName["SetSecret"] != nil || byName["ListProjects"] == nil || !byName["ListProjects"].Annotations.ReadOnlyHint ||
		byName["DeleteProject"] == nil || !*byName["DeleteProject"].Annotations.DestructiveHint {
		t.Fatalf("tools: %d listed; SetSecret %v, ListProjects %+v", len(list.Tools), byName["SetSecret"], byName["ListProjects"])
	}

	call := func(cs *sdk.ClientSession, name string, args map[string]any) (string, bool) {
		t.Helper()
		res, err := cs.CallTool(ctx, &sdk.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		var b strings.Builder
		for _, c := range res.Content {
			if tc, ok := c.(*sdk.TextContent); ok {
				b.WriteString(tc.Text)
			}
		}
		return b.String(), res.IsError
	}
	if out, isErr := call(cs, "CreateProject", map[string]any{"slug": "shop", "display_name": "Shop"}); isErr ||
		!strings.Contains(out, `"shop"`) {
		t.Fatalf("CreateProject without an idempotency key (error %v): %q", isErr, out)
	}
	if out, isErr := call(cs, "ListProjects", nil); isErr || !strings.Contains(out, "shop") {
		t.Errorf("ListProjects: %s", out)
	}
	if out, isErr := call(cs, "CreateProject", map[string]any{"slug": "Bad Slug"}); !isErr || !strings.Contains(out, "invalid_argument") {
		t.Errorf("an invalid call: %v %s", isErr, out)
	}
	if out, isErr := call(cs, "CreateProject", map[string]any{"nope": 1}); !isErr || !strings.Contains(out, "invalid arguments") {
		t.Errorf("unknown arguments: %v %s", isErr, out)
	}
	anon := connectAs("bxa_not-a-token")
	if out, isErr := call(anon, "ListProjects", nil); !isErr || !strings.Contains(out, "unauthenticated") {
		t.Errorf("a bad token: %v %s", isErr, out)
	}
}
