package notify

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Outcome is what a message says about one Deployment. It carries names, IDs, and a
// stable failure reason, never secret values (rule 3).
type Outcome struct {
	DeploymentID string `json:"deployment_id"`
	Project      string `json:"project"`
	Environment  string `json:"environment"`
	App          string `json:"app"`
	ReleaseID    string `json:"release_id"`
	// State is the terminal state's stored name, e.g. "rolled_back".
	State  string `json:"state"`
	Reason string `json:"reason,omitempty"`
	// StartedBy names the actor kind that started it, e.g. "api_token", "webhook".
	StartedBy string `json:"started_by"`
}

// Channel is one destination, with its credentials resolved.
type Channel struct {
	// Kind is "slack", "discord", or "webhook".
	Kind    string
	URL     string
	Signing string
}

// Text is the one-line message chat channels get.
func (o Outcome) Text() string {
	icon := map[string]string{"succeeded": "✅", "failed": "❌", "rolled_back": "↩️", "cancelled": "⏹️"}[o.State]
	s := fmt.Sprintf("%s %s/%s/%s: deployment %s %s", icon, o.Project, o.Environment, o.App,
		shortID(o.DeploymentID), strings.ReplaceAll(o.State, "_", " "))
	if o.Reason != "" {
		s += " (" + o.Reason + ")"
	}
	return s + ", release " + shortID(o.ReleaseID) + ", started by " + o.StartedBy
}

func shortID(id string) string {
	if len(id) > 8 {
		return id[len(id)-8:]
	}
	return id
}

// Send posts o to ch. Its errors carry the channel kind and the HTTP status, never the
// URL, which is a credential.
func Send(ctx context.Context, client *http.Client, ch Channel, o Outcome) error {
	var payload any
	switch ch.Kind {
	case "slack":
		payload = map[string]string{"text": o.Text()}
	case "discord":
		payload = map[string]string{"content": o.Text()}
	case "webhook":
		payload = o
	default:
		return fmt.Errorf("unknown channel kind %q", ch.Kind)
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode %s message: %w", ch.Kind, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ch.URL, bytes.NewReader(body))
	if err != nil {
		return errors.New("build " + ch.Kind + " request: the stored URL is invalid")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Bohux")
	if ch.Kind == "webhook" {
		mac := hmac.New(sha256.New, []byte(ch.Signing))
		mac.Write(body)
		req.Header.Set("X-Bohux-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	}
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		// url.Error carries the URL; report the kind only.
		return fmt.Errorf("post to %s channel: %w", ch.Kind, errors.Unwrap(err))
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("post to %s channel: %s", ch.Kind, resp.Status)
	}
	return nil
}
