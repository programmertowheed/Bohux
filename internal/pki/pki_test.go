package pki

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
)

var (
	tenant = uuid.MustParse("0192f0a0-0000-7000-8000-00000000000a")
	node   = uuid.MustParse("0192f0a0-0000-7000-8000-000000000001")
)

func csrPEM(t *testing.T, key any) []byte {
	t.Helper()
	der, err := x509.CreateCertificateRequest(rand.Reader,
		&x509.CertificateRequest{Subject: pkix.Name{CommonName: "evil"}}, key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der})
}

func TestLoadOrCreate(t *testing.T) {
	dir := t.TempDir()
	a, err := LoadOrCreate(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{caKeyFile, caCertFile, signingKeyFile} {
		info, err := os.Stat(filepath.Join(dir, "pki", f))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Errorf("%s mode %v, want 0600", f, info.Mode().Perm())
		}
	}
	again, err := LoadOrCreate(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if again.CACertSHA256() != a.CACertSHA256() || KeyID(again.SigningPublicKey()) != KeyID(a.SigningPublicKey()) {
		t.Error("second load produced different keys")
	}

	t.Run("refuses a partial set", func(t *testing.T) {
		d := t.TempDir()
		if _, err := LoadOrCreate(d, nil); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(filepath.Join(d, "pki", signingKeyFile)); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadOrCreate(d, nil); err == nil {
			t.Fatal("loaded a partial key set")
		}
	})
	t.Run("refuses readable keys", func(t *testing.T) {
		d := t.TempDir()
		if _, err := LoadOrCreate(d, nil); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(filepath.Join(d, "pki", caKeyFile), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadOrCreate(d, nil); err == nil {
			t.Fatal("loaded a world-readable key")
		}
	})
}

func TestSignNodeCSR(t *testing.T) {
	now := time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC)
	a, err := LoadOrCreate(t.TempDir(), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	certPEM, err := a.SignNodeCSR(csrPEM(t, key), tenant, node)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(certPEM)
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cert.Verify(x509.VerifyOptions{Roots: a.CAPool(), CurrentTime: now,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		t.Errorf("node cert does not verify for client auth: %v", err)
	}
	if got := cert.NotAfter.Sub(now); got < 89*24*time.Hour || got > 91*24*time.Hour {
		t.Errorf("lifetime %v, want ~90 days", got)
	}
	gotTenant, gotNode, err := NodeIdentity(cert)
	if err != nil || gotTenant != tenant || gotNode != node {
		t.Errorf("identity = %s %s %v", gotTenant, gotNode, err)
	}
	if cert.Subject.CommonName == "evil" {
		t.Error("CSR subject was copied into the certificate")
	}

	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	p384, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tampered := csrPEM(t, key)
	blk, _ := pem.Decode(tampered)
	blk.Bytes[len(blk.Bytes)-3] ^= 0xff
	bad := []struct {
		name string
		csr  []byte
	}{
		{"garbage", []byte("nope")},
		{"rsa key", csrPEM(t, rsaKey)},
		{"p-384 key", csrPEM(t, p384)},
		{"bad signature", pem.EncodeToMemory(blk)},
	}
	for _, tt := range bad {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := a.SignNodeCSR(tt.csr, tenant, node); !errors.Is(err, ErrBadCSR) {
				t.Errorf("got %v, want ErrBadCSR", err)
			}
		})
	}
}

func TestServerCertificate(t *testing.T) {
	a, err := LoadOrCreate(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	tc, err := a.ServerCertificate()
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(tc.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cert.Verify(x509.VerifyOptions{Roots: a.CAPool(), DNSName: ServerName}); err != nil {
		t.Errorf("server cert does not verify as %s: %v", ServerName, err)
	}
	if _, _, err := NodeIdentity(cert); !errors.Is(err, ErrNoNodeIdentity) {
		t.Error("server cert carries a node identity")
	}
}

func TestAssignmentSignature(t *testing.T) {
	a, err := LoadOrCreate(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte("serialized AssignmentPayload")
	sig, keyID := a.SignAssignment(payload)
	pub := a.SigningPublicKey()
	if keyID != KeyID(pub) || len(keyID) != 16 {
		t.Errorf("key id %q", keyID)
	}
	if !VerifyAssignment(pub, payload, sig) {
		t.Fatal("valid signature rejected")
	}
	if VerifyAssignment(pub, append(payload, '!'), sig) {
		t.Error("tampered payload accepted")
	}
	if ed25519.Verify(pub, payload, sig) {
		t.Error("signature verifies without the domain prefix")
	}
	other, err := LoadOrCreate(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if VerifyAssignment(other.SigningPublicKey(), payload, sig) {
		t.Error("another control plane's key accepted the signature")
	}
}
