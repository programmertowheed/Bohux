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
	}
	for _, tt := range tests {
		var out bytes.Buffer
		err := Run(context.Background(), tt.cmd, tt.args, &out, getenv)
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s %q = %v, want an error naming %q", tt.cmd, tt.args, err, tt.want)
		}
	}
}
