package api

import (
	"encoding/json"
	"strings"
	"testing"

	bohuxv1 "github.com/bohumix/bohux/gen/bohux/v1"
)

func TestWatchPaths(t *testing.T) {
	got, err := watchPaths([]string{"./services/api/", "go.mod", "services/api"})
	if err != nil || strings.Join(got, " ") != "services/api go.mod" {
		t.Errorf("watchPaths = %v, %v", got, err)
	}
	for _, bad := range [][]string{{""}, {"/etc"}, {"a/../b"}, {"./"}, {"a/./b"}, make([]string, maxWatchPaths+1)} {
		if _, err := watchPaths(bad); err == nil {
			t.Errorf("watchPaths(%q) accepted", bad)
		}
	}
}

func TestPushedCommitPaths(t *testing.T) {
	hook := &bohuxv1.Webhook{Branch: "main", Flavor: bohuxv1.WebhookFlavor_WEBHOOK_FLAVOR_GITHUB,
		Paths: []string{"services/api", "go.mod"}}
	const after = "c0ffeec0ffeec0ffeec0ffeec0ffeec0ffeec0ff"
	type commit struct {
		Added    []string `json:"added,omitempty"`
		Modified []string `json:"modified,omitempty"`
		Removed  []string `json:"removed,omitempty"`
	}
	payload := func(commits []commit, total *int) []byte {
		b, _ := json.Marshal(map[string]any{"ref": "refs/heads/main", "after": after, "commits": commits,
			"total_commits": total})
		return b
	}
	many := make([]commit, githubCommitCap)
	for i := range many {
		many[i] = commit{Modified: []string{"web/index.html"}}
	}
	one, three := 1, 3
	tests := []struct {
		name    string
		hook    *bohuxv1.Webhook
		body    []byte
		deploys bool
	}{
		{"a file under a watched directory", hook, payload([]commit{{Modified: []string{"services/api/main.go"}}}, nil), true},
		{"a watched file, removed", hook, payload([]commit{{Removed: []string{"go.mod"}}}, nil), true},
		{"in a later commit", hook, payload([]commit{{Added: []string{"web/a.js"}}, {Added: []string{"services/api/b.go"}}}, nil), true},
		{"only other paths", hook, payload([]commit{{Modified: []string{"web/index.html", "services/api-gateway/x"}}}, nil), false},
		{"no commits listed: deploy", hook, payload(nil, nil), true},
		{"GitHub's cap reached: deploy", hook, payload(many, nil), true},
		{"Gitea lists fewer than it pushed: deploy", hook, payload([]commit{{Modified: []string{"web/x"}}}, &three), true},
		{"Gitea's complete list is filtered", hook, payload([]commit{{Modified: []string{"web/x"}}}, &one), false},
		{"no paths: every push", &bohuxv1.Webhook{Branch: "main"}, payload([]commit{{Modified: []string{"web/x"}}}, nil), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			commit, deploys, why := pushedCommit(tt.hook, "push", tt.body)
			if deploys != tt.deploys {
				t.Fatalf("deploys = %v (%s), want %v", deploys, why, tt.deploys)
			}
			if deploys && commit != after {
				t.Errorf("commit = %q", commit)
			}
			if !deploys && why != "no watched path changed" {
				t.Errorf("why = %q", why)
			}
		})
	}
}
