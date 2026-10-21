package cli

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"io"
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	tests := []struct {
		args    []string
		app     string
		env     string
		wantErr bool
	}{
		{[]string{"api", "-e", "prod"}, "api", "prod", false},
		{[]string{"-e", "prod", "api"}, "api", "prod", false},
		{[]string{"-e", "prod"}, "", "", true},
		{[]string{"api", "web", "-e", "prod"}, "", "", true},
		{[]string{"api", "-nope"}, "", "", true},
	}
	for _, tt := range tests {
		fs := flag.NewFlagSet("t", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		env := fs.String("e", "", "")
		app, err := parse(fs, tt.args)
		if (err != nil) != tt.wantErr || app != tt.app || (!tt.wantErr && *env != tt.env) {
			t.Errorf("parse(%q) = %q, env %q, %v", tt.args, app, *env, err)
		}
		if err != nil && !errors.Is(err, ErrUsage) {
			t.Errorf("parse(%q) error %v is not a usage error", tt.args, err)
		}
	}
}

func TestParseImage(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	if img, err := parseImage("ghcr.io/acme/api@" + digest); err != nil || img.GetRepository() != "ghcr.io/acme/api" || img.GetDigest() != digest {
		t.Errorf("parseImage = %v, %v", img, err)
	}
	for _, bad := range []string{"ghcr.io/acme/api:latest", "@" + digest, "api@md5:x"} {
		if _, err := parseImage(bad); !errors.Is(err, ErrUsage) {
			t.Errorf("parseImage(%q) = %v, want a usage error", bad, err)
		}
	}
}

func TestRuntimeFromFlags(t *testing.T) {
	rt, routes, err := runtimeFromFlags(2, []string{"8080", "9090:80"}, "/healthz", []string{"api.example.com", "admin.example.com:9090"})
	if err != nil {
		t.Fatal(err)
	}
	if rt.GetReplicas() != 2 || len(rt.GetPorts()) != 2 || rt.GetPorts()[1].GetHostPort() != 80 ||
		rt.GetHealth().GetPort() != 8080 || rt.GetHealth().GetPath() != "/healthz" {
		t.Errorf("runtime = %v", rt)
	}
	if len(routes) != 2 || routes[0].GetPort() != 8080 || routes[1].GetPort() != 9090 {
		t.Errorf("routes = %v", routes)
	}
	for _, bad := range [][]string{{"x"}, {"8080:y"}} {
		if _, _, err := runtimeFromFlags(1, bad, "", nil); !errors.Is(err, ErrUsage) {
			t.Errorf("ports %q = %v, want a usage error", bad, err)
		}
	}
	if _, _, err := runtimeFromFlags(1, nil, "", []string{"api.example.com"}); !errors.Is(err, ErrUsage) {
		t.Errorf("a route without a port to default to = %v, want a usage error", err)
	}
}

func TestState(t *testing.T) {
	if got := state("DEPLOYMENT_STATE_HEALTH_GATE"); got != "health_gate" {
		t.Errorf("state = %q", got)
	}
	if got := state("RELEASE_STATE_BUILDING"); got != "building" {
		t.Errorf("state = %q", got)
	}
}

func TestRunRefusesBeforeCalling(t *testing.T) {
	getenv := func(k string) string { return map[string]string{EnvProject: "shop"}[k] }
	tests := []struct {
		cmd  string
		args []string
		want string
	}{
		{"deploy", []string{"api", "-e", "prod"}, EnvToken},
		{"deploy", []string{"api", "-e", "prod", "--ref", "main", "--image", "x@sha256:a"}, "exclusive"},
		{"scale", []string{"api", "-e", "prod"}, "--replicas"},
		{"nope", nil, "unknown command"},
		{"app", nil, "missing verb"},
		{"app", []string{"rename", "api"}, "unknown command"},
		{"app", []string{"create", "api", "-e", "prod"}, EnvToken},
		{"project", []string{"list", "-o", "yaml"}, "-o must be"},
		{"route", []string{"add", "api"}, "an app slug and a host"},
		{"secret", []string{"set", "A", "B", "-e", "prod"}, "the secret's key"},
	}
	for _, tt := range tests {
		var out bytes.Buffer
		err := Run(context.Background(), tt.cmd, tt.args, strings.NewReader(""), &out, getenv)
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s %q = %v, want an error naming %q", tt.cmd, tt.args, err, tt.want)
		}
	}
}

func TestBytesHuman(t *testing.T) {
	for n, want := range map[int64]string{512: "512B", 64 << 20: "64MiB", 3 << 29: "1.5GiB", 2048: "2KiB"} {
		if got := bytesHuman(n); got != want {
			t.Errorf("bytesHuman(%d) = %q, want %q", n, got, want)
		}
	}
}
