// Package memory is a complete provider that keeps its "target" in memory. It exists
// to prove the conformance suite (internal/provider/conformance) and to stand in for a
// real target in agent tests. It is not a deployment target.
package memory

import (
	"context"
	"fmt"
	"io"
	"iter"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	bohuxv1 "github.com/bohumix/bohux/gen/bohux/v1"
	"github.com/bohumix/bohux/internal/provider"
)

// Name is what Describe reports.
const Name = "memory"

type workload struct {
	spec      provider.Spec
	artifact  string
	secrets   map[string]string
	instances []*instance
	hash      string // set only after a complete Apply
}

type instance struct {
	name    string
	env     map[string]string
	running bool
	logs    []provider.LogLine
}

// Provider is the in-memory target.
type Provider struct {
	mu        sync.Mutex
	workloads map[provider.ResourceRef]*workload
	seq       int
	// StepDelay slows Apply between steps so tests can interrupt it midway.
	StepDelay time.Duration
}

// New returns an empty in-memory target.
func New() *Provider { return &Provider{workloads: map[provider.ResourceRef]*workload{}} }

var _ provider.Provider = (*Provider)(nil)

// Describe declares every verb.
func (p *Provider) Describe(context.Context) (provider.Descriptor, error) {
	return provider.Descriptor{Name: Name, Version: "1", Capabilities: provider.AllVerbs}, nil
}

// Plan lists what Apply would change, without changing anything.
func (p *Provider) Plan(_ context.Context, req provider.PlanRequest) (provider.Plan, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	w, ok := p.workloads[req.Spec.Ref]
	switch {
	case !ok:
		return provider.Plan{Changes: []provider.Change{{Action: "create", Detail: req.Spec.Ref.App}}}, nil
	case w.hash != req.Spec.Hash:
		return provider.Plan{Changes: []provider.Change{{Action: "update", Detail: req.Spec.Ref.App}}}, nil
	case !allRunning(w, replicas(req.Spec)):
		return provider.Plan{Changes: []provider.Change{{Action: "restart", Detail: req.Spec.Ref.App}}}, nil
	}
	return provider.Plan{}, nil
}

func replicas(s provider.Spec) int { return int(s.Runtime.GetReplicas()) }

func allRunning(w *workload, n int) bool {
	if len(w.instances) != n {
		return false
	}
	for _, in := range w.instances {
		if !in.running {
			return false
		}
	}
	return true
}

// Apply converges in steps (artifact, secrets, instances, record hash), checking ctx
// between them, so an interrupted Apply leaves partial state that the next one
// finishes without duplicating instances.
func (p *Provider) Apply(ctx context.Context, req provider.ApplyRequest) (provider.ApplyResult, error) {
	spec := req.Spec
	plan, err := p.Plan(ctx, provider.PlanRequest{Spec: spec})
	if err != nil {
		return provider.ApplyResult{}, err
	}
	if len(plan.Changes) == 0 {
		return provider.ApplyResult{}, nil
	}
	var values map[string]string
	if spec.Secrets != nil {
		if values, err = spec.Secrets.Open(); err != nil {
			return provider.ApplyResult{}, fmt.Errorf("open secrets: %w", err)
		}
	}
	step := func() error {
		if p.StepDelay > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(p.StepDelay):
			}
		}
		return ctx.Err()
	}

	p.mu.Lock()
	w, ok := p.workloads[spec.Ref]
	if !ok {
		w = &workload{}
		p.workloads[spec.Ref] = w
	}
	// Step 1: the readable artifact, never holding secret values (rule 2, rule 3).
	w.spec = spec
	w.artifact = fmt.Sprintf("app: %s\nrelease: %s\nimage: %s\nreplicas: %d\nenv_file: secrets.env\n",
		spec.Ref.App, spec.Release, spec.Image, replicas(spec))
	w.hash = ""
	p.mu.Unlock()
	if err := step(); err != nil {
		return provider.ApplyResult{}, err
	}
	// Step 2: the declared secret file.
	p.mu.Lock()
	w.secrets = values
	p.mu.Unlock()
	if err := step(); err != nil {
		return provider.ApplyResult{}, err
	}
	// Step 3: replace instances one at a time: recreate, as a host-port deploy does.
	p.mu.Lock()
	w.instances = nil
	p.mu.Unlock()
	for i := 0; i < replicas(spec); i++ {
		p.mu.Lock()
		p.seq++
		w.instances = append(w.instances, &instance{
			name: spec.Ref.App + "-" + strconv.Itoa(p.seq), env: copyMap(values), running: true,
			logs: []provider.LogLine{{Time: time.Now(), Message: "started " + spec.Release}},
		})
		p.mu.Unlock()
		if err := step(); err != nil {
			return provider.ApplyResult{}, err
		}
	}
	// Step 4: record the hash only once everything is in place.
	p.mu.Lock()
	w.hash = spec.Hash
	p.mu.Unlock()
	return provider.ApplyResult{Changed: true}, nil
}

func copyMap(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// Status reports the observed state.
func (p *Provider) Status(_ context.Context, ref provider.ResourceRef) (provider.Status, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	w, ok := p.workloads[ref]
	if !ok {
		return provider.Status{}, nil
	}
	var ready int32
	for _, in := range w.instances {
		if in.running {
			ready++
		}
	}
	n := replicas(w.spec)
	running := allRunning(w, n)
	return provider.Status{Exists: true, Hash: w.hash, Running: running, Healthy: running,
		ReadyReplicas: ready}, nil
}

// Logs yields the instances' lines; with Follow it waits for ctx to end.
func (p *Provider) Logs(ctx context.Context, req provider.LogRequest) (iter.Seq2[provider.LogLine, error], error) {
	p.mu.Lock()
	w, ok := p.workloads[req.Ref]
	var lines []provider.LogLine
	if ok {
		for _, in := range w.instances {
			if req.Instance != "" && req.Instance != in.name {
				continue
			}
			for _, l := range in.logs {
				l.Instance = in.name
				lines = append(lines, l)
			}
		}
	}
	p.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("no workload %s", req.Ref.App)
	}
	sort.SliceStable(lines, func(i, j int) bool { return lines[i].Time.Before(lines[j].Time) })
	return func(yield func(provider.LogLine, error) bool) {
		for _, l := range lines {
			if !yield(l, nil) {
				return
			}
		}
		if req.Follow {
			<-ctx.Done()
		}
	}, nil
}

// Exec runs a tiny command language: "echo ARGS", "printenv NAME", "exit N".
func (p *Provider) Exec(_ context.Context, req provider.ExecRequest) (provider.Session, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	w, ok := p.workloads[req.Ref]
	if !ok || len(w.instances) == 0 {
		return nil, fmt.Errorf("no running instance of %s", req.Ref.App)
	}
	in := w.instances[0]
	var out string
	code := 0
	switch {
	case len(req.Command) == 0:
		code = 127
	case req.Command[0] == "echo":
		out = strings.Join(req.Command[1:], " ") + "\n"
	case req.Command[0] == "printenv" && len(req.Command) == 2:
		v, ok := in.env[req.Command[1]]
		if ok {
			out = v + "\n"
		} else {
			code = 1
		}
	case req.Command[0] == "exit" && len(req.Command) == 2:
		code, _ = strconv.Atoi(req.Command[1])
	case len(req.Command) == 3 && req.Command[0] == "sh" && req.Command[1] == "-c" &&
		strings.HasPrefix(req.Command[2], "exit "):
		code, _ = strconv.Atoi(strings.TrimPrefix(req.Command[2], "exit "))
	default:
		code = 127
	}
	return &session{stdout: strings.NewReader(out), code: code}, nil
}

type session struct {
	stdout io.Reader
	code   int
}

type nopWriteCloser struct{}

func (nopWriteCloser) Write(b []byte) (int, error) { return len(b), nil }
func (nopWriteCloser) Close() error                { return nil }

func (s *session) Stdin() io.WriteCloser       { return nopWriteCloser{} }
func (s *session) Stdout() io.Reader           { return s.stdout }
func (s *session) Stderr() io.Reader           { return strings.NewReader("") }
func (s *session) Resize(uint16, uint16) error { return nil }
func (s *session) Wait() (int, error)          { return s.code, nil }
func (s *session) Close() error                { return nil }

// Scale changes the replica count by re-applying with the new count, so the target
// never drifts from its artifact.
func (p *Provider) Scale(ctx context.Context, req provider.ScaleRequest) error {
	p.mu.Lock()
	w, ok := p.workloads[req.Ref]
	if !ok {
		p.mu.Unlock()
		return fmt.Errorf("no workload %s", req.Ref.App)
	}
	spec := w.spec
	p.mu.Unlock()
	if spec.Runtime == nil {
		return fmt.Errorf("workload %s has no runtime", req.Ref.App)
	}
	rt := proto.Clone(spec.Runtime).(*bohuxv1.Runtime)
	rt.Replicas = req.Replicas
	spec.Runtime = rt
	spec.Hash = spec.Hash + "+scale" + strconv.Itoa(int(req.Replicas))
	_, err := p.Apply(ctx, provider.ApplyRequest{Spec: spec})
	return err
}

// Destroy removes everything Apply created.
func (p *Provider) Destroy(_ context.Context, ref provider.ResourceRef) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.workloads, ref)
	return nil
}

// Crash stops every instance of ref, as an OOM kill would.
func (p *Provider) Crash(ref provider.ResourceRef) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if w, ok := p.workloads[ref]; ok {
		for _, in := range w.instances {
			in.running = false
		}
	}
}

// The methods below let the conformance suite inspect the target directly
// (conformance.Inspector).

// Leftovers lists the artifact, the secret file, and every instance of ref.
func (p *Provider) Leftovers(_ context.Context, ref provider.ResourceRef) ([]string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	w, ok := p.workloads[ref]
	if !ok {
		return nil, nil
	}
	out := []string{"artifact", "secrets.env"}
	for _, in := range w.instances {
		out = append(out, "instance/"+in.name)
	}
	return out, nil
}

// Instances counts running instances.
func (p *Provider) Instances(_ context.Context, ref provider.ResourceRef) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := 0
	if w, ok := p.workloads[ref]; ok {
		for _, in := range w.instances {
			if in.running {
				n++
			}
		}
	}
	return n, nil
}

// Artifacts returns the readable artifact.
func (p *Provider) Artifacts(_ context.Context, ref provider.ResourceRef) (map[string][]byte, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	w, ok := p.workloads[ref]
	if !ok {
		return nil, nil
	}
	return map[string][]byte{"compose.yaml": []byte(w.artifact)}, nil
}

// SecretFiles returns the declared secret file.
func (p *Provider) SecretFiles(_ context.Context, ref provider.ResourceRef) (map[string][]byte, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	w, ok := p.workloads[ref]
	if !ok {
		return nil, nil
	}
	var b strings.Builder
	keys := make([]string, 0, len(w.secrets))
	for k := range w.secrets {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		b.WriteString(k + "=" + w.secrets[k] + "\n")
	}
	return map[string][]byte{"secrets.env": []byte(b.String())}, nil
}
