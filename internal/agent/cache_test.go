package agent

import (
	"bytes"
	"crypto/ecdh"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/crypto/nacl/box"
	"google.golang.org/protobuf/proto"

	agentv1 "github.com/bohumix/bohux/gen/bohux/agent/v1"
	"github.com/bohumix/bohux/internal/pki"
)

const nodeID = "0192f0a0-0000-7000-8000-000000000001"

func testState(t *testing.T, signer *pki.Authority) *State {
	t.Helper()
	sealing, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return &State{identity: identity{Node: nodeID, SigningPublicKey: signer.SigningPublicKey()},
		dir: t.TempDir(), sealingKey: sealing}
}

// signed builds an assignment as the control plane does, sealing value for st.
func signed(t *testing.T, signer *pki.Authority, st *State, node string, gen uint64, value string) *agentv1.Assignment {
	t.Helper()
	w := &agentv1.Workload{AppId: "app"}
	if value != "" {
		plain, err := proto.Marshal(&agentv1.SecretValues{Values: map[string]string{"DB": value}})
		if err != nil {
			t.Fatal(err)
		}
		var pub [32]byte
		copy(pub[:], st.sealingKey.PublicKey().Bytes())
		if w.SealedSecrets, err = box.SealAnonymous(nil, plain, &pub, rand.Reader); err != nil {
			t.Fatal(err)
		}
	}
	payload, err := proto.Marshal(&agentv1.AssignmentPayload{NodeId: node, Generation: gen,
		Workloads: []*agentv1.Workload{w}})
	if err != nil {
		t.Fatal(err)
	}
	sig, keyID := signer.SignAssignment(payload)
	return &agentv1.Assignment{Payload: payload, Signature: sig, KeyId: keyID}
}

func TestCache(t *testing.T) {
	signer, err := pki.LoadOrCreate(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	other, err := pki.LoadOrCreate(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	st := testState(t, signer)
	c, err := OpenCache(st)
	if err != nil {
		t.Fatal(err)
	}
	const secret = "hunter2-sealed"

	ok, err := c.Accept(signed(t, signer, st, nodeID, 2, secret))
	if err != nil || !ok || c.Generation() != 2 {
		t.Fatalf("Accept gen 2 = %v, %v; generation %d", ok, err, c.Generation())
	}
	select {
	case <-c.Changed():
	default:
		t.Error("no change signal after caching")
	}

	tampered := signed(t, signer, st, nodeID, 3, "")
	tampered.Payload[len(tampered.Payload)-1] ^= 1
	bad := []struct {
		name string
		a    *agentv1.Assignment
		want error
	}{
		{"tampered payload", tampered, ErrBadSignature},
		{"another control plane's key", signed(t, other, st, nodeID, 3, ""), ErrBadSignature},
		{"another node", signed(t, signer, st, "0192f0a0-0000-7000-8000-000000000009", 3, ""), ErrWrongNode},
	}
	for _, tt := range bad {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := c.Accept(tt.a); !errors.Is(err, tt.want) {
				t.Errorf("got %v, want %v", err, tt.want)
			}
			if c.Generation() != 2 {
				t.Error("a refused assignment replaced the cache")
			}
		})
	}

	t.Run("older and equal generations are ignored", func(t *testing.T) {
		for _, g := range []uint64{1, 2} {
			ok, err := c.Accept(signed(t, signer, st, nodeID, g, ""))
			if err != nil || ok {
				t.Errorf("gen %d: cached=%v err=%v", g, ok, err)
			}
		}
	})

	t.Run("persisted, 0600, still sealed, reloads", func(t *testing.T) {
		path := filepath.Join(st.dir, assignmentFile)
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Errorf("mode %v", info.Mode().Perm())
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(raw, []byte(secret)) {
			t.Error("cache file holds the secret in clear")
		}
		again, err := OpenCache(st)
		if err != nil {
			t.Fatal(err)
		}
		if again.Generation() != 2 {
			t.Errorf("reloaded generation %d", again.Generation())
		}
		vals, err := st.OpenSecrets(again.Current().GetWorkloads()[0])
		if err != nil || vals["DB"] != secret {
			t.Errorf("OpenSecrets = %v, %v", vals, err)
		}
	})

	t.Run("a corrupted cache is set aside and the agent starts empty", func(t *testing.T) {
		path := filepath.Join(st.dir, assignmentFile)
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		raw[len(raw)/2] ^= 0xff
		if err := os.WriteFile(path, raw, 0o600); err != nil {
			t.Fatal(err)
		}
		c, err := OpenCache(st)
		if err != nil {
			t.Fatalf("OpenCache: %v", err)
		}
		if c.Generation() != 0 || c.Current() != nil {
			t.Error("a corrupt cache was trusted")
		}
		if _, err := os.Stat(path + ".corrupt"); err != nil {
			t.Errorf("corrupt cache not kept for inspection: %v", err)
		}
	})
}
