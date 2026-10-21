// Package agent is the node side of Bohux: registration, the stream to the control
// plane, and (from S4d) the cached assignment and reconcile loop (ADR-0003, ADR-0007).
package agent

import (
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// State directory layout. Everything is 0600 in a 0700 directory.
const (
	nodeKeyFile    = "node.key"    // ECDSA P-256, TLS client key
	sealingKeyFile = "sealing.key" // X25519, opens sealed secret values (ADR-0008)
	nodeCertFile   = "node.crt"    // client certificate from the node CA
	caCertFile     = "ca.crt"      // the pinned node CA
	identityFile   = "identity.json"
)

// ErrNotRegistered means the state directory holds no identity yet.
var ErrNotRegistered = errors.New("agent is not registered; run `bohux agent register`")

// identity is the non-key part of the state.
type identity struct {
	Node         string `json:"node_id"`
	ControlPlane string `json:"control_plane"`
	// Ed25519 public key that assignments must be signed with (ADR-0024).
	SigningPublicKey []byte `json:"signing_public_key"`
}

// State is a registered agent's identity and keys.
type State struct {
	identity
	cert       tls.Certificate
	caPool     *x509.CertPool
	sealingKey *ecdh.PrivateKey
}

// NodeID is the node's ID.
func (s *State) NodeID() string { return s.Node }

// SigningPublicKey is the key assignments are verified with.
func (s *State) SigningPublicKey() ed25519.PublicKey { return s.identity.SigningPublicKey }

func writeFile(dir, name string, data []byte) error {
	path := filepath.Join(dir, name)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", name, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("install %s: %w", name, err)
	}
	return nil
}

func pemFile(typ string, der []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: typ, Bytes: der})
}

// save writes a fresh registration. The identity file goes last: its presence is
// what marks the directory as registered.
func save(dir string, id identity, nodeKey *ecdsa.PrivateKey, sealing *ecdh.PrivateKey, certPEM, caPEM []byte) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create state dir: %w", err)
	}
	nodeDER, err := x509.MarshalPKCS8PrivateKey(nodeKey)
	if err != nil {
		return fmt.Errorf("encode node key: %w", err)
	}
	sealDER, err := x509.MarshalPKCS8PrivateKey(sealing)
	if err != nil {
		return fmt.Errorf("encode sealing key: %w", err)
	}
	idJSON, err := json.MarshalIndent(id, "", "  ")
	if err != nil {
		return fmt.Errorf("encode identity: %w", err)
	}
	for _, f := range []struct {
		name string
		data []byte
	}{
		{nodeKeyFile, pemFile("PRIVATE KEY", nodeDER)},
		{sealingKeyFile, pemFile("PRIVATE KEY", sealDER)},
		{nodeCertFile, certPEM},
		{caCertFile, caPEM},
		{identityFile, idJSON},
	} {
		if err := writeFile(dir, f.name, f.data); err != nil {
			return err
		}
	}
	return nil
}

// Load reads a registered agent's state.
func Load(dir string) (*State, error) {
	idJSON, err := os.ReadFile(filepath.Join(dir, identityFile))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrNotRegistered
	}
	if err != nil {
		return nil, fmt.Errorf("read identity: %w", err)
	}
	var st State
	if err := json.Unmarshal(idJSON, &st.identity); err != nil {
		return nil, fmt.Errorf("decode identity: %w", err)
	}
	read := func(name string) ([]byte, error) {
		path := filepath.Join(dir, name)
		info, err := os.Stat(path)
		if err != nil {
			return nil, fmt.Errorf("stat %s: %w", name, err)
		}
		if info.Mode().Perm()&0o077 != 0 {
			return nil, fmt.Errorf("%s is readable by group or others (mode %v); chmod 600 it", path, info.Mode().Perm())
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", name, err)
		}
		return b, nil
	}
	keyPEM, err := read(nodeKeyFile)
	if err != nil {
		return nil, err
	}
	certPEM, err := read(nodeCertFile)
	if err != nil {
		return nil, err
	}
	if st.cert, err = tls.X509KeyPair(certPEM, keyPEM); err != nil {
		return nil, fmt.Errorf("load node certificate: %w", err)
	}
	caPEM, err := read(caCertFile)
	if err != nil {
		return nil, err
	}
	st.caPool = x509.NewCertPool()
	if !st.caPool.AppendCertsFromPEM(caPEM) {
		return nil, errors.New("ca.crt holds no certificate")
	}
	sealPEM, err := read(sealingKeyFile)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(sealPEM)
	if block == nil {
		return nil, errors.New("sealing.key is not PEM")
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse sealing key: %w", err)
	}
	var ok bool
	if st.sealingKey, ok = key.(*ecdh.PrivateKey); !ok || st.sealingKey.Curve() != ecdh.X25519() {
		return nil, errors.New("sealing.key is not an X25519 key")
	}
	if len(st.identity.SigningPublicKey) != ed25519.PublicKeySize {
		return nil, errors.New("identity has no valid signing public key")
	}
	return &st, nil
}
