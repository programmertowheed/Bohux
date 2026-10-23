package swarm

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/bohumix/bohux/internal/provider"
)

const (
	stackFile   = "stack.yaml"
	secretsFile = "secrets.env"
	stateFile   = ".bohux-state.json"
	service     = "app"

	labelApp         = "io.bohux.app"
	labelEnvironment = "io.bohux.environment"
	labelRelease     = "io.bohux.release"
	labelSpecHash    = "io.bohux.spec-hash"
	labelStack       = "com.docker.stack.namespace"
	labelService     = "com.docker.swarm.service.name"
	labelTask        = "com.docker.swarm.task.name"

	// IngressNetwork is the network the node's ingress shares with the workloads it
	// serves (ADR-0034); on a swarm manager it is an attachable overlay (ADR-0045).
	IngressNetwork = "bohux-ingress"
)

var unsafeName = regexp.MustCompile(`[^a-z0-9-]+`)

// stack is the stack name, also the workload's directory name and its ingress alias,
// in the Docker provider's pattern: bohux-<app slug>-<environment slug>-<8 hex of the
// IDs>[-<slot>]. Slugs are cut to 18 characters so the service name ("<stack>_app")
// stays within Swarm's 63.
func stack(ref provider.ResourceRef) string {
	sum := sha256.Sum256([]byte(ref.Environment + "/" + ref.App))
	name := "bohux-" + slug(ref.AppSlug, "app") + "-" + slug(ref.EnvironmentSlug, "env") + "-" +
		hex.EncodeToString(sum[:])[:8]
	if ref.Slot != "" {
		name += "-" + slug(ref.Slot, "slot")
	}
	return name
}

// serviceName is the stack's one service.
func serviceName(ref provider.ResourceRef) string { return stack(ref) + "_" + service }

func slug(s, fallback string) string {
	s = strings.Trim(unsafeName.ReplaceAllString(strings.ToLower(s), "-"), "-")
	if s == "" {
		return fallback
	}
	if len(s) > 18 {
		s = strings.TrimRight(s[:18], "-")
	}
	return s
}

func (p *Provider) dir(ref provider.ResourceRef) string {
	return filepath.Join(p.Root, stack(ref))
}
