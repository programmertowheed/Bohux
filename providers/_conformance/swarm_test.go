package conformance_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	bohuxv1 "github.com/bohumix/bohux/gen/bohux/v1"
	"github.com/bohumix/bohux/internal/provider"
	"github.com/bohumix/bohux/internal/provider/conformance"
	"github.com/bohumix/bohux/providers/swarm"
)

// swarmSubject builds the fixture images on the swarm's daemon and returns the Swarm
// provider (ADR-0045). target is that daemon's DOCKER_HOST: a single-manager swarm,
// with busybox:1.36 present.
func swarmSubject(target string) (conformance.Subject, error) {
	ctx := context.Background()
	build, err := os.MkdirTemp("", "bohux-conformance-build")
	if err != nil {
		return conformance.Subject{}, err
	}
	defer func() { _ = os.RemoveAll(build) }()
	if err := os.WriteFile(filepath.Join(build, "Dockerfile"), []byte(fixtureDockerfile), 0o644); err != nil {
		return conformance.Subject{}, err
	}
	for release := 1; release <= 4; release++ {
		// The fixture's build needs no network; a swarm daemon may have no bridge.
		c := exec.CommandContext(ctx, "docker", "build", "-q", "--network", "none", "--build-arg",
			fmt.Sprintf("RELEASE=%d", release), "-t", fixtureImage(release), build)
		if target != "" {
			c.Env = append(os.Environ(), "DOCKER_HOST="+target)
		}
		if out, err := c.CombinedOutput(); err != nil {
			return conformance.Subject{}, fmt.Errorf("build fixture image: %w: %s", err, strings.TrimSpace(string(out)))
		}
	}
	root, err := os.MkdirTemp("", "bohux-conformance-stacks")
	if err != nil {
		return conformance.Subject{}, err
	}
	p, err := swarm.New(root, target)
	if err != nil {
		return conformance.Subject{}, err
	}
	return conformance.Subject{
		Provider: p, Inspector: p,
		Fixture: func(ref provider.ResourceRef, release int, replicas int32) provider.Spec {
			return provider.Spec{Ref: ref, Release: fmt.Sprintf("r%d", release), Image: fixtureImage(release),
				Runtime: &bohuxv1.Runtime{Replicas: replicas, Cpu: "500m", Memory: "64Mi",
					Ports:  []*bohuxv1.Port{{ContainerPort: 8080}},
					Health: &bohuxv1.HealthCheck{Path: "/healthz", Port: 8080, TimeoutSeconds: 2}}}
		},
		LogMarker: func(release int) string { return fmt.Sprintf("started r%d", release) },
	}, nil
}
