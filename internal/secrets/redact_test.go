package secrets

import (
	"bytes"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"

	agentv1 "github.com/bohumix/bohux/gen/bohux/agent/v1"
	bohuxv1 "github.com/bohumix/bohux/gen/bohux/v1"
)

const secret = "hunter2-do-not-log"

func TestRedacted(t *testing.T) {
	tests := []struct {
		name string
		msg  proto.Message
		keep string // a non-secret value that must survive
	}{
		{"SetSecretRequest.value", &bohuxv1.SetSecretRequest{Key: "DB_URL", Value: secret}, "DB_URL"},
		{"CreateApiTokenResponse.secret", &bohuxv1.CreateApiTokenResponse{
			ApiToken: &bohuxv1.ApiToken{Name: "ci"}, Secret: secret}, "ci"},
		{"CreateBootstrapTokenResponse.token", &bohuxv1.CreateBootstrapTokenResponse{Token: secret}, ""},
		{"agent RegisterRequest.bootstrap_token", &agentv1.RegisterRequest{
			BootstrapToken: secret, AgentVersion: "0.1.0"}, "0.1.0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := proto.Clone(tt.msg)
			w := Message{M: tt.msg}
			var logs bytes.Buffer
			slog.New(slog.NewJSONHandler(&logs, nil)).Info("x", "msg", w)
			slog.New(slog.NewTextHandler(&logs, nil)).Info("x", "msg", w)
			outputs := map[string]string{
				"slog": logs.String(), "String": w.String(),
				"%v": fmt.Sprintf("%v", w), "%+v": fmt.Sprintf("%+v", w),
				"%#v": fmt.Sprintf("%#v", w), "%q": fmt.Sprintf("%q", w),
			}
			for name, out := range outputs {
				if strings.Contains(out, secret) {
					t.Errorf("%s output contains the secret: %s", name, out)
				}
				if tt.keep != "" && !strings.Contains(out, tt.keep) {
					t.Errorf("%s output lost non-secret %q: %s", name, tt.keep, out)
				}
			}
			if !proto.Equal(before, tt.msg) {
				t.Error("redaction modified the original message")
			}
		})
	}
}

func TestRedactedNil(t *testing.T) {
	if Redacted(nil) != nil {
		t.Error("Redacted(nil) != nil")
	}
	if got := (Message{}).String(); got != "<nil>" {
		t.Errorf("String of empty wrapper = %q", got)
	}
}
