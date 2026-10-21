package mcp

import (
	"slices"
	"testing"

	bohuxv1 "github.com/bohumix/bohux/gen/bohux/v1"
)

// TestTools: every RPC has a policy (Tools fails otherwise), credentials and streams
// are never tools, and the hints follow the policy (ADR-0018).
func TestTools(t *testing.T) {
	tools, err := Tools()
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]Tool{}
	for _, tool := range tools {
		names[tool.Name] = tool
	}
	for _, hidden := range []string{"SetSecret", "CreateApiToken", "CreateBootstrapToken", "CreateWebhook",
		"CreateNotificationChannel", "Exec", "StreamLogs", "WatchDeployment"} {
		if _, ok := names[hidden]; ok {
			t.Errorf("%s is a tool; it carries a credential or streams", hidden)
		}
	}
	for name, want := range map[string]bohuxv1.ToolPolicy{
		"ListApps": bohuxv1.ToolPolicy_TOOL_POLICY_READ_ONLY, "CreateProject": bohuxv1.ToolPolicy_TOOL_POLICY_MUTATING,
		"Rollback": bohuxv1.ToolPolicy_TOOL_POLICY_DESTRUCTIVE, "DeleteApp": bohuxv1.ToolPolicy_TOOL_POLICY_DESTRUCTIVE,
	} {
		tool, ok := names[name]
		if !ok || tool.Policy != want {
			t.Errorf("%s = %v, want %v", name, tool.Policy, want)
			continue
		}
		m := tool.mcpTool()
		if m.Annotations.ReadOnlyHint != (want == bohuxv1.ToolPolicy_TOOL_POLICY_READ_ONLY) ||
			*m.Annotations.DestructiveHint != (want == bohuxv1.ToolPolicy_TOOL_POLICY_DESTRUCTIVE) {
			t.Errorf("%s hints = %+v", name, m.Annotations)
		}
	}
}

func TestSchema(t *testing.T) {
	s := Schema((&bohuxv1.CreateAppRequest{}).ProtoReflect().Descriptor())
	props := s["properties"].(map[string]any)
	for _, f := range []string{"environment_id", "slug", "source", "runtime", "routes", "idempotency_key"} {
		if _, ok := props[f]; !ok {
			t.Errorf("CreateAppRequest schema lacks %s", f)
		}
	}
	req := s["required"].([]string)
	if !slices.Contains(req, "environment_id") || slices.Contains(req, "idempotency_key") {
		t.Errorf("required = %v: idempotency_key is filled in for the model", req)
	}
	app := Schema((&bohuxv1.App{}).ProtoReflect().Descriptor())["properties"].(map[string]any)
	meta := app["meta"].(map[string]any)["properties"].(map[string]any)
	if _, ok := meta["created_at"]; ok {
		t.Error("output-only fields are in the input schema")
	}
	routes := props["routes"].(map[string]any)
	if routes["type"] != "array" || routes["items"].(map[string]any)["properties"].(map[string]any)["host"] == nil {
		t.Errorf("routes = %v", routes)
	}
}
