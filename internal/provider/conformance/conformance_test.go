package conformance_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	bohuxv1 "github.com/bohumix/bohux/gen/bohux/v1"
	"github.com/bohumix/bohux/internal/provider"
	"github.com/bohumix/bohux/internal/provider/conformance"
	"github.com/bohumix/bohux/internal/provider/memory"
)

func memorySubject(p provider.Provider, insp conformance.Inspector) conformance.Subject {
	return conformance.Subject{
		Provider: p, Inspector: insp, Timeout: 2 * time.Second,
		Fixture: func(ref provider.ResourceRef, release int, replicas int32) provider.Spec {
			return provider.Spec{Ref: ref, Release: fmt.Sprintf("r%d", release),
				Image: fmt.Sprintf("mem:%d", release), Runtime: &bohuxv1.Runtime{Replicas: replicas}}
		},
		LogMarker: func(release int) string { return fmt.Sprintf("started r%d", release) },
	}
}

// run executes every case and returns the names of the ones that failed.
func run(t *testing.T, s conformance.Subject) map[string]error {
	t.Helper()
	ctx := context.Background()
	h := conformance.NewHarness(s)
	defer h.Cleanup(ctx)
	failed := map[string]error{}
	for _, c := range conformance.Cases() {
		if err := c.Run(ctx, h); err != nil {
			failed[c.Name] = err
		}
	}
	return failed
}

func TestMemoryProviderPasses(t *testing.T) {
	p := memory.New()
	p.StepDelay = 20 * time.Millisecond // so the interruption case really interrupts
	for name, err := range run(t, memorySubject(p, p)) {
		t.Errorf("%s: %v", name, err)
	}
}

// Broken providers prove each check catches what it claims to.

type leakyDestroy struct{ *memory.Provider }

func (leakyDestroy) Destroy(context.Context, provider.ResourceRef) error { return nil }

type alwaysChanged struct{ *memory.Provider }

func (a alwaysChanged) Apply(ctx context.Context, req provider.ApplyRequest) (provider.ApplyResult, error) {
	if err := a.Destroy(ctx, req.Spec.Ref); err != nil {
		return provider.ApplyResult{}, err
	}
	return a.Provider.Apply(ctx, req)
}

type noRollback struct{ *memory.Provider }

// Apply refuses to go back to release 1, like a provider that cannot re-apply an
// older release.
func (n noRollback) Apply(ctx context.Context, req provider.ApplyRequest) (provider.ApplyResult, error) {
	if req.Spec.Release == "r1" {
		if st, _ := n.Status(ctx, req.Spec.Ref); st.Exists && st.Hash != "" {
			return provider.ApplyResult{}, nil
		}
	}
	return n.Provider.Apply(ctx, req)
}

// leakyArtifacts writes the secret file's content into the artifact.
type leakyArtifacts struct{ *memory.Provider }

func (l leakyArtifacts) Artifacts(ctx context.Context, ref provider.ResourceRef) (map[string][]byte, error) {
	a, err := l.Provider.Artifacts(ctx, ref)
	if err != nil {
		return nil, err
	}
	s, err := l.SecretFiles(ctx, ref)
	if err != nil {
		return nil, err
	}
	if a != nil {
		a["compose.yaml"] = append(a["compose.yaml"], s["secrets.env"]...)
	}
	return a, nil
}

func TestBrokenProvidersFail(t *testing.T) {
	tests := []struct {
		name string
		make func(*memory.Provider) (provider.Provider, conformance.Inspector)
		want string
	}{
		{"leaky destroy", func(m *memory.Provider) (provider.Provider, conformance.Inspector) {
			return leakyDestroy{m}, m
		}, "destroy leaves nothing and is repeatable"},
		{"re-creates on every apply", func(m *memory.Provider) (provider.Provider, conformance.Inspector) {
			return alwaysChanged{m}, m
		}, "unchanged re-apply is a no-op"},
		{"cannot roll back", func(m *memory.Provider) (provider.Provider, conformance.Inspector) {
			return noRollback{m}, m
		}, "rollback is applying the previous release again"},
		{"secret in the artifact", func(m *memory.Provider) (provider.Provider, conformance.Inspector) {
			return m, leakyArtifacts{m}
		}, "secrets only in the secret file"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, insp := tt.make(memory.New())
			failed := run(t, memorySubject(p, insp))
			if _, ok := failed[tt.want]; !ok {
				t.Errorf("case %q passed; failures were %v", tt.want, failed)
			}
		})
	}
}
