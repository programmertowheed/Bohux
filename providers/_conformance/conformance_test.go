// Command-line runner for the provider conformance suite (CLAUDE.md rule 4):
//
//	go test ./providers/_conformance -provider=<name> -target=<target> -v
//
// The directory name starts with "_" so `go test ./...` skips it: conformance needs a
// real target. The suite itself is internal/provider/conformance.
package conformance_test

import (
	"context"
	"flag"
	"fmt"
	"testing"
	"time"

	bohuxv1 "github.com/bohumix/bohux/gen/bohux/v1"
	"github.com/bohumix/bohux/internal/provider"
	"github.com/bohumix/bohux/internal/provider/conformance"
	"github.com/bohumix/bohux/internal/provider/memory"
	"github.com/bohumix/bohux/providers/docker"
	"github.com/bohumix/bohux/providers/swarm"
)

var (
	providerName = flag.String("provider", "", "provider to test, e.g. docker")
	target       = flag.String("target", "", "provider-specific target, e.g. a Docker host")
)

// subjects builds each provider under test with its inspector and fixture.
var subjects = map[string]func(target string) (conformance.Subject, error){
	docker.Name: dockerSubject,
	swarm.Name:  swarmSubject,
	memory.Name: func(string) (conformance.Subject, error) {
		p := memory.New()
		p.StepDelay = 20 * time.Millisecond
		return conformance.Subject{
			Provider: p, Inspector: p,
			Fixture: func(ref provider.ResourceRef, release int, replicas int32) provider.Spec {
				return provider.Spec{Ref: ref, Release: fmt.Sprintf("r%d", release),
					Image: fmt.Sprintf("mem:%d", release), Runtime: &bohuxv1.Runtime{Replicas: replicas,
						Ports: []*bohuxv1.Port{{ContainerPort: 8080}}}}
			},
			LogMarker: func(release int) string { return fmt.Sprintf("started r%d", release) },
		}, nil
	},
}

func TestConformance(t *testing.T) {
	if *providerName == "" {
		t.Skip("set -provider (and -target) to run the conformance suite")
	}
	build, ok := subjects[*providerName]
	if !ok {
		t.Fatalf("no conformance subject for provider %q", *providerName)
	}
	s, err := build(*target)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	h := conformance.NewHarness(s)
	t.Cleanup(func() { h.Cleanup(ctx) })
	for _, c := range conformance.Cases() {
		if !t.Run(c.Name, func(t *testing.T) {
			if err := c.Run(ctx, h); err != nil {
				t.Fatal(err)
			}
		}) {
			// Cases build on each other; stop at the first failure.
			return
		}
	}
}
