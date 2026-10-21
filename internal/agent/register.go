package agent

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"os/exec"
	"strings"

	"connectrpc.com/connect"

	agentv1 "github.com/bohumix/bohux/gen/bohux/agent/v1"
	"github.com/bohumix/bohux/gen/bohux/agent/v1/agentv1connect"
	bohuxv1 "github.com/bohumix/bohux/gen/bohux/v1"
	"github.com/bohumix/bohux/internal/pki"
	"github.com/bohumix/bohux/internal/version"
)

// LiveRestoreChecker reports whether the container runtime keeps containers running
// across its own restarts (ADR-0020 item 9).
type LiveRestoreChecker interface {
	LiveRestoreEnabled(ctx context.Context) (bool, error)
}

// DockerCLI checks live-restore with `docker info`.
type DockerCLI struct{}

// LiveRestoreEnabled runs `docker info --format {{.LiveRestoreEnabled}}`.
func (DockerCLI) LiveRestoreEnabled(ctx context.Context) (bool, error) {
	out, err := exec.CommandContext(ctx, "docker", "info", "--format", "{{.LiveRestoreEnabled}}").Output()
	if err != nil {
		return false, fmt.Errorf("docker info: %w", err)
	}
	return strings.TrimSpace(string(out)) == "true", nil
}

// ErrLiveRestoreOff names the Docker setting the agent requires.
var ErrLiveRestoreOff = errors.New(`docker live-restore is off: containers would stop whenever ` +
	`dockerd restarts. Set "live-restore": true in /etc/docker/daemon.json and reload dockerd`)

// RegisterOptions configure a one-time registration.
type RegisterOptions struct {
	StateDir string
	// ControlPlane is the agent listener's host:port.
	ControlPlane string
	// Token is the bxb_ bootstrap token. It is never logged (rule 3).
	Token string
	// CACertSHA256 pins the node CA (from CreateBootstrapToken).
	CACertSHA256 string
	Docker       LiveRestoreChecker
	Providers    []*bohuxv1.ProviderDescriptor
}

// Register enrolls this node once and writes its state directory.
func Register(ctx context.Context, o RegisterOptions) error {
	if _, err := Load(o.StateDir); err == nil {
		return fmt.Errorf("already registered (state in %s)", o.StateDir)
	} else if !errors.Is(err, ErrNotRegistered) {
		return err
	}
	pin, err := hex.DecodeString(o.CACertSHA256)
	if err != nil || len(pin) != sha256.Size {
		return errors.New("--ca-cert-sha256 must be 64 hex characters")
	}
	ok, err := o.Docker.LiveRestoreEnabled(ctx)
	if err != nil {
		return fmt.Errorf("check Docker live-restore: %w", err)
	}
	if !ok {
		return ErrLiveRestoreOff
	}

	nodeKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return fmt.Errorf("generate node key: %w", err)
	}
	sealing, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return fmt.Errorf("generate sealing key: %w", err)
	}
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, nodeKey)
	if err != nil {
		return fmt.Errorf("create CSR: %w", err)
	}

	client := agentv1connect.NewAgentServiceClient(&http.Client{Transport: &http.Transport{
		TLSClientConfig: pinnedTLS(pin), ForceAttemptHTTP2: true,
	}}, "https://"+o.ControlPlane)
	resp, err := client.Register(ctx, connect.NewRequest(&agentv1.RegisterRequest{
		BootstrapToken:   o.Token,
		Csr:              pemFile("CERTIFICATE REQUEST", csrDER),
		SealingPublicKey: sealing.PublicKey().Bytes(),
		AgentVersion:     version.Version,
		ProtocolVersion:  version.Protocol,
		Providers:        o.Providers,
	}))
	if err != nil {
		return fmt.Errorf("register: %w", err)
	}
	msg := resp.Msg
	block, _ := pem.Decode(msg.GetCaCertificate())
	if block == nil {
		return errors.New("register: control plane sent no CA certificate")
	}
	if sum := sha256.Sum256(block.Bytes); !bytes.Equal(sum[:], pin) {
		return errors.New("register: CA certificate does not match --ca-cert-sha256")
	}
	return save(o.StateDir, identity{
		Node: msg.GetNodeId(), ControlPlane: o.ControlPlane, SigningPublicKey: msg.GetAssignmentSigningPublicKey(),
	}, nodeKey, sealing, msg.GetClientCertificate(), msg.GetCaCertificate())
}

// pinnedTLS trusts exactly the CA whose certificate hashes to pin, which the server
// sends in its chain (ADR-0024), and checks the fixed server name.
func pinnedTLS(pin []byte) *tls.Config {
	return &tls.Config{
		MinVersion: tls.VersionTLS13,
		// Standard verification is replaced, not skipped: VerifyConnection below
		// verifies the chain against the pinned CA.
		InsecureSkipVerify: true, //nolint:gosec // see VerifyConnection
		VerifyConnection: func(cs tls.ConnectionState) error {
			roots := x509.NewCertPool()
			for _, c := range cs.PeerCertificates {
				if sum := sha256.Sum256(c.Raw); bytes.Equal(sum[:], pin) {
					roots.AddCert(c)
				}
			}
			if len(cs.PeerCertificates) == 0 {
				return errors.New("server sent no certificate")
			}
			_, err := cs.PeerCertificates[0].Verify(x509.VerifyOptions{Roots: roots, DNSName: pki.ServerName})
			if err != nil {
				return fmt.Errorf("server is not signed by the pinned node CA: %w", err)
			}
			return nil
		},
	}
}

// mutualTLS is the registered agent's TLS: its client certificate, the stored CA.
func (s *State) mutualTLS() *tls.Config {
	return &tls.Config{
		MinVersion:   tls.VersionTLS13,
		RootCAs:      s.caPool,
		ServerName:   pki.ServerName,
		Certificates: []tls.Certificate{s.cert},
	}
}
