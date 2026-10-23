package api

import (
	"bytes"
	"testing"

	"google.golang.org/protobuf/proto"

	bohuxv1 "github.com/bohumix/bohux/gen/bohux/v1"
)

// ADR-0022: a secret value never feeds the stored request hash.
func TestRequestHashIgnoresRedactedFields(t *testing.T) {
	base := &bohuxv1.SetSecretRequest{EnvironmentId: "e", Key: "DB_URL", Value: "hunter2", IdempotencyKey: "k"}
	hash := func(m proto.Message) []byte {
		t.Helper()
		h, err := requestHash(m)
		if err != nil {
			t.Fatal(err)
		}
		return h
	}
	otherValue := proto.Clone(base).(*bohuxv1.SetSecretRequest)
	otherValue.Value = "correct horse"
	otherKey := proto.Clone(base).(*bohuxv1.SetSecretRequest)
	otherKey.Key = "API_KEY"

	if !bytes.Equal(hash(base), hash(otherValue)) {
		t.Error("hash depends on the redacted value field")
	}
	if bytes.Equal(hash(base), hash(otherKey)) {
		t.Error("hash ignores a non-redacted field")
	}
	if base.GetValue() != "hunter2" {
		t.Error("requestHash modified the caller's request")
	}
}
