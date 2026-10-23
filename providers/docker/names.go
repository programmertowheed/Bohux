package docker

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/bohumix/bohux/internal/provider"
)

const (
	composeFile = "compose.yaml"
	secretsFile = "secrets.env"
	stateFile   = ".bohux-state.json"
	service     = "app"

	labelApp         = "io.bohux.app"
	labelEnvironment = "io.bohux.environment"
	labelRelease     = "io.bohux.release"
	labelSpecHash    = "io.bohux.spec-hash"
	labelProject     = "com.docker.compose.project"

	// IngressNetwork is the network the node's ingress shares with every workload it
	// serves (ADR-0034). Shared by all workloads, so Destroy never removes it.
	IngressNetwork = "bohux-ingress"
)

var unsafeName = regexp.MustCompile(`[^a-z0-9-]+`)

// project is the Compose project name, also the workload's directory name:
// bohux-<app slug>-<environment slug>-<8 hex of the IDs>[-<slot>] (ADR-0025, ADR-0035).
// The hash keeps it unique on the node; the slugs keep it readable.
func project(ref provider.ResourceRef) string {
	sum := sha256.Sum256([]byte(ref.Environment + "/" + ref.App))
	name := "bohux-" + slug(ref.AppSlug, "app") + "-" + slug(ref.EnvironmentSlug, "env") + "-" +
		hex.EncodeToString(sum[:])[:8]
	if ref.Slot != "" {
		name += "-" + slug(ref.Slot, "slot")
	}
	return name
}

func slug(s, fallback string) string {
	s = strings.Trim(unsafeName.ReplaceAllString(strings.ToLower(s), "-"), "-")
	if s == "" {
		return fallback
	}
	if len(s) > 30 {
		s = s[:30]
	}
	return s
}

func (p *Provider) dir(ref provider.ResourceRef) string {
	return filepath.Join(p.Root, project(ref))
}
