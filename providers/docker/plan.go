package docker

import (
	"bytes"
	"context"
	"os"
	"path/filepath"

	"github.com/bohumix/bohux/internal/provider"
)

// Plan compares the rendered artifact and the running containers with spec. It reads
// only; nothing is written or started.
func (p *Provider) Plan(ctx context.Context, req provider.PlanRequest) (provider.Plan, error) {
	spec := req.Spec
	composeYAML, secretsEnv, _, err := render(spec)
	if err != nil {
		return provider.Plan{}, err
	}
	st, err := p.Status(ctx, spec.Ref)
	if err != nil {
		return provider.Plan{}, err
	}
	name := project(spec.Ref)
	unchanged := p.unchanged(spec.Ref, composeYAML, secretsEnv)
	switch {
	case !st.Exists:
		return provider.Plan{Changes: []provider.Change{{Action: "create", Detail: name}}}, nil
	case !st.Running && unchanged:
		// Status reports no hash for a workload that is not running, so the files on
		// disk decide: the same artifact, not running, is a restart.
		return provider.Plan{Changes: []provider.Change{{Action: "restart", Detail: name}}}, nil
	case st.Hash != spec.Hash || !unchanged:
		return provider.Plan{Changes: []provider.Change{{Action: "update", Detail: name}}}, nil
	}
	return provider.Plan{}, nil
}

// unchanged reports whether the files on disk already hold exactly this content.
func (p *Provider) unchanged(ref provider.ResourceRef, composeYAML, secretsEnv []byte) bool {
	dir := p.dir(ref)
	c, err1 := os.ReadFile(filepath.Join(dir, composeFile))
	s, err2 := os.ReadFile(filepath.Join(dir, secretsFile))
	return err1 == nil && err2 == nil && bytes.Equal(c, composeYAML) && bytes.Equal(s, secretsEnv)
}
