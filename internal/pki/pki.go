// Package pki holds the control plane's node CA and assignment signing key, and the
// signing and identity rules built on them (ADR-0024, ADR-0022).
package pki

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"math/big"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	caKeyFile      = "node-ca.key"
	caCertFile     = "node-ca.crt"
	signingKeyFile = "assignment-signing.key"

	caLifetime     = 10 * 365 * 24 * time.Hour
	nodeLifetime   = 90 * 24 * time.Hour
	serverLifetime = 365 * 24 * time.Hour

	// ServerName is the name in the agent listener's certificate. Agents verify it
	// against the pinned CA whatever address they dial, so no hostname is configured.
	ServerName = "control-plane.bohux.internal"

	// signaturePrefix separates assignment signatures from any other use of the key.
	signaturePrefix = "bohux-assignment-v1\x00"
)

// Authority is the loaded key material.
type Authority struct {
	caCert  *x509.Certificate
	caKey   *ecdsa.PrivateKey
	signKey ed25519.PrivateKey
	now     func() time.Time
}

// LoadOrCreate loads the keys from dir/pki, creating them on first use. It refuses
// key files readable by group or others, and refuses a half-created set rather than
// overwriting anything.
func LoadOrCreate(dir string, now func() time.Time) (*Authority, error) {
	if now == nil {
		now = time.Now
	}
	pkiDir := filepath.Join(dir, "pki")
	files := []string{caKeyFile, caCertFile, signingKeyFile}
	present := 0
	for _, f := range files {
		if _, err := os.Stat(filepath.Join(pkiDir, f)); err == nil {
			present++
		} else if !errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("pki: stat %s: %w", f, err)
		}
	}
	switch present {
	case 0:
		if err := create(pkiDir, now()); err != nil {
			return nil, err
		}
	case len(files):
	default:
		return nil, fmt.Errorf("pki: %s has only some of %v; restore it from backup "+
			"rather than regenerating, or every node must re-register", pkiDir, files)
	}
	return load(pkiDir, now)
}

func create(pkiDir string, now time.Time) error {
	if err := os.MkdirAll(pkiDir, 0o700); err != nil {
		return fmt.Errorf("pki: create %s: %w", pkiDir, err)
	}
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return fmt.Errorf("pki: generate CA key: %w", err)
	}
	serial, err := randomSerial()
	if err != nil {
		return err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "Bohux node CA"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(caLifetime),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLenZero:        true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, caKey.Public(), caKey)
	if err != nil {
		return fmt.Errorf("pki: self-sign CA: %w", err)
	}
	_, signKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return fmt.Errorf("pki: generate signing key: %w", err)
	}
	caKeyDER, err := x509.MarshalPKCS8PrivateKey(caKey)
	if err != nil {
		return fmt.Errorf("pki: encode CA key: %w", err)
	}
	signDER, err := x509.MarshalPKCS8PrivateKey(signKey)
	if err != nil {
		return fmt.Errorf("pki: encode signing key: %w", err)
	}
	writes := []struct {
		name, typ string
		der       []byte
	}{
		{caKeyFile, "PRIVATE KEY", caKeyDER},
		{signingKeyFile, "PRIVATE KEY", signDER},
		{caCertFile, "CERTIFICATE", caDER},
	}
	for _, w := range writes {
		data := pem.EncodeToMemory(&pem.Block{Type: w.typ, Bytes: w.der})
		// O_EXCL: never overwrite key material.
		f, err := os.OpenFile(filepath.Join(pkiDir, w.name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return fmt.Errorf("pki: create %s: %w", w.name, err)
		}
		if _, err := f.Write(data); err != nil {
			_ = f.Close()
			return fmt.Errorf("pki: write %s: %w", w.name, err)
		}
		if err := f.Close(); err != nil {
			return fmt.Errorf("pki: close %s: %w", w.name, err)
		}
	}
	return nil
}

func load(pkiDir string, now func() time.Time) (*Authority, error) {
	read := func(name, typ string) ([]byte, error) {
		path := filepath.Join(pkiDir, name)
		info, err := os.Stat(path)
		if err != nil {
			return nil, fmt.Errorf("pki: stat %s: %w", name, err)
		}
		if info.Mode().Perm()&0o077 != 0 {
			return nil, fmt.Errorf("pki: %s is readable by group or others (mode %v); chmod 600 it",
				path, info.Mode().Perm())
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("pki: read %s: %w", name, err)
		}
		block, _ := pem.Decode(data)
		if block == nil || block.Type != typ {
			return nil, fmt.Errorf("pki: %s is not a PEM %s", name, typ)
		}
		return block.Bytes, nil
	}
	caKeyDER, err := read(caKeyFile, "PRIVATE KEY")
	if err != nil {
		return nil, err
	}
	signDER, err := read(signingKeyFile, "PRIVATE KEY")
	if err != nil {
		return nil, err
	}
	caDER, err := read(caCertFile, "CERTIFICATE")
	if err != nil {
		return nil, err
	}
	caKeyAny, err := x509.ParsePKCS8PrivateKey(caKeyDER)
	if err != nil {
		return nil, fmt.Errorf("pki: parse CA key: %w", err)
	}
	caKey, ok := caKeyAny.(*ecdsa.PrivateKey)
	if !ok {
		return nil, errors.New("pki: CA key is not ECDSA")
	}
	signAny, err := x509.ParsePKCS8PrivateKey(signDER)
	if err != nil {
		return nil, fmt.Errorf("pki: parse signing key: %w", err)
	}
	signKey, ok := signAny.(ed25519.PrivateKey)
	if !ok {
		return nil, errors.New("pki: signing key is not Ed25519")
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		return nil, fmt.Errorf("pki: parse CA certificate: %w", err)
	}
	if certPub, ok := caCert.PublicKey.(*ecdsa.PublicKey); !ok || !certPub.Equal(caKey.Public()) {
		return nil, errors.New("pki: CA certificate does not match the CA key")
	}
	return &Authority{caCert: caCert, caKey: caKey, signKey: signKey, now: now}, nil
}

func randomSerial() (*big.Int, error) {
	n, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return nil, fmt.Errorf("pki: serial: %w", err)
	}
	return n, nil
}

// CACertPEM is the CA certificate agents pin and trust.
func (a *Authority) CACertPEM() []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: a.caCert.Raw})
}

// CACertSHA256 is the hex SHA-256 of the CA certificate's DER, shown with bootstrap
// tokens so the installer can pin the CA (ADR-0024).
func (a *Authority) CACertSHA256() string {
	sum := sha256.Sum256(a.caCert.Raw)
	return hex.EncodeToString(sum[:])
}

// CAPool is a pool holding only the node CA.
func (a *Authority) CAPool() *x509.CertPool {
	p := x509.NewCertPool()
	p.AddCert(a.caCert)
	return p
}

// NodeURI is the only identity in a node certificate (ADR-0022).
func NodeURI(tenant, node uuid.UUID) *url.URL {
	return &url.URL{Scheme: "bohux", Host: "tenant", Path: "/" + tenant.String() + "/node/" + node.String()}
}

// ErrBadCSR means the CSR is malformed, badly signed, or uses another key type.
var ErrBadCSR = errors.New("invalid certificate signing request")

// SignNodeCSR verifies a PEM CSR and issues a 90-day client certificate for the node.
// The CSR's subject and extensions are ignored; identity comes from the arguments.
func (a *Authority) SignNodeCSR(csrPEM []byte, tenant, node uuid.UUID) ([]byte, error) {
	block, _ := pem.Decode(csrPEM)
	if block == nil || block.Type != "CERTIFICATE REQUEST" {
		return nil, ErrBadCSR
	}
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		return nil, ErrBadCSR
	}
	if err := csr.CheckSignature(); err != nil {
		return nil, ErrBadCSR
	}
	pub, ok := csr.PublicKey.(*ecdsa.PublicKey)
	if !ok || pub.Curve != elliptic.P256() {
		return nil, ErrBadCSR
	}
	return a.issue(pub, pkix.Name{CommonName: node.String()}, nil, []*url.URL{NodeURI(tenant, node)},
		x509.ExtKeyUsageClientAuth, nodeLifetime)
}

// ServerCertificate issues a certificate for the agent listener, named ServerName.
func (a *Authority) ServerCertificate() (tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("pki: generate server key: %w", err)
	}
	certPEM, err := a.issue(&key.PublicKey, pkix.Name{CommonName: ServerName}, []string{ServerName}, nil,
		x509.ExtKeyUsageServerAuth, serverLifetime)
	if err != nil {
		return tls.Certificate{}, err
	}
	block, _ := pem.Decode(certPEM)
	return tls.Certificate{Certificate: [][]byte{block.Bytes}, PrivateKey: key}, nil
}

func (a *Authority) issue(pub crypto.PublicKey, subject pkix.Name, dns []string, uris []*url.URL, usage x509.ExtKeyUsage, life time.Duration) ([]byte, error) {
	serial, err := randomSerial()
	if err != nil {
		return nil, err
	}
	now := a.now()
	notAfter := now.Add(life)
	if notAfter.After(a.caCert.NotAfter) {
		notAfter = a.caCert.NotAfter
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      subject,
		NotBefore:    now.Add(-5 * time.Minute),
		NotAfter:     notAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{usage},
		DNSNames:     dns,
		URIs:         uris,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, a.caCert, pub, a.caKey)
	if err != nil {
		return nil, fmt.Errorf("pki: issue certificate: %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), nil
}

// ErrNoNodeIdentity means a certificate has no valid node URI SAN.
var ErrNoNodeIdentity = errors.New("certificate has no node identity")

// NodeIdentity reads the tenant and node from a verified client certificate.
func NodeIdentity(cert *x509.Certificate) (tenant, node uuid.UUID, err error) {
	if len(cert.URIs) != 1 {
		return uuid.Nil, uuid.Nil, ErrNoNodeIdentity
	}
	u := cert.URIs[0]
	parts := strings.Split(strings.TrimPrefix(u.Path, "/"), "/")
	if u.Scheme != "bohux" || u.Host != "tenant" || len(parts) != 3 || parts[1] != "node" {
		return uuid.Nil, uuid.Nil, ErrNoNodeIdentity
	}
	if tenant, err = uuid.Parse(parts[0]); err != nil {
		return uuid.Nil, uuid.Nil, ErrNoNodeIdentity
	}
	if node, err = uuid.Parse(parts[2]); err != nil {
		return uuid.Nil, uuid.Nil, ErrNoNodeIdentity
	}
	return tenant, node, nil
}

// SigningPublicKey is the key agents verify assignments with.
func (a *Authority) SigningPublicKey() ed25519.PublicKey {
	return a.signKey.Public().(ed25519.PublicKey)
}

// KeyID identifies a signing public key: the first 16 hex characters of its SHA-256.
func KeyID(pub ed25519.PublicKey) string {
	sum := sha256.Sum256(pub)
	return hex.EncodeToString(sum[:])[:16]
}

// SignAssignment signs serialized AssignmentPayload bytes (ADR-0024).
func (a *Authority) SignAssignment(payload []byte) (sig []byte, keyID string) {
	return ed25519.Sign(a.signKey, signedBytes(payload)), KeyID(a.SigningPublicKey())
}

// VerifyAssignment checks an assignment signature. Agents call it before caching.
func VerifyAssignment(pub ed25519.PublicKey, payload, sig []byte) bool {
	return len(pub) == ed25519.PublicKeySize && ed25519.Verify(pub, signedBytes(payload), sig)
}

func signedBytes(payload []byte) []byte {
	return append([]byte(signaturePrefix), payload...)
}
