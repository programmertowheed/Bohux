package swarm

import (
	"bytes"
	"context"
	"os"
	"path/filepath"

	"github.com/bohumix/bohux/internal/provider"
)

// Plan compares the rendered artifact and the running tasks with spec. It reads only;
// nothing is written or started.
func (p *Provider) Plan(ctx context.Context, req provider.PlanRequest) (provider.Plan, error) {
	spec := req.Spec
	stackYAML, secretsEnv, _, err := render(spec)
	if err != nil {
		return provider.Plan{}, err
	}
	st, err := p.Status(ctx, spec.Ref)
	if err != nil {
		return provider.Plan{}, err
	}
	name := stack(spec.Ref)
	unchanged := p.unchanged(spec.Ref, stackYAML, secretsEnv)
	switch {
	case !st.Exists:
		return provider.Plan{Changes: []provider.Change{{Action: "create", Detail: name}}}, nil
	case !st.Running && unchanged:
		// The same artifact, not running: tasks that keep failing, or a deploy cut
		// short. Applying redeploys and forces new tasks.
		return provider.Plan{Changes: []provider.Change{{Action: "restart", Detail: name}}}, nil
	case st.Hash != spec.Hash || !unchanged:
		return provider.Plan{Changes: []provider.Change{{Action: "update", Detail: name}}}, nil
	}
	return provider.Plan{}, nil
}

// unchanged reports whether the files on disk already hold exactly this content.
func (p *Provider) unchanged(ref provider.ResourceRef, stackYAML, secretsEnv []byte) bool {
	dir := p.dir(ref)
	c, err1 := os.ReadFile(filepath.Join(dir, stackFile))
	s, err2 := os.ReadFile(filepath.Join(dir, secretsFile))
	return err1 == nil && err2 == nil && bytes.Equal(c, stackYAML) && bytes.Equal(s, secretsEnv)
}
