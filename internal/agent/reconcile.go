package agent

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	agentv1 "github.com/bohumix/bohux/gen/bohux/agent/v1"
	bohuxv1 "github.com/bohumix/bohux/gen/bohux/v1"
	"github.com/bohumix/bohux/internal/provider"
)

const (
	defaultReconcileInterval = 10 * time.Second
	// appliedFile records every workload this agent has applied and not destroyed.
	// Providers have no list verb (rule 4), so this is how removals are found.
	appliedFile = "applied.json"
)

// Reconciler converges the node to the cached assignment, with or without the control
// plane (rule 1, ADR-0007). It is the only apply path: deploys, rollbacks, and
// self-healing all arrive as a cached assignment.
type Reconciler struct {
	State    *State
	Cache    *Cache
	Provider provider.Provider
	// Interval between passes; zero means 10 seconds. A new assignment triggers a
	// pass at once.
	Interval time.Duration

	mu      sync.Mutex
	latest  *agentv1.StatusReport
	updated chan struct{}
}

// Latest is the most recent pass's report, or nil before the first one. It satisfies
// ReportSource for the stream.
func (r *Reconciler) Latest() *agentv1.StatusReport {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.latest
}

// Updated is signaled after each pass that produced a report.
func (r *Reconciler) Updated() <-chan struct{} {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.updated == nil {
		r.updated = make(chan struct{}, 1)
	}
	return r.updated
}

func (r *Reconciler) publish(rep *agentv1.StatusReport) {
	r.mu.Lock()
	r.latest = rep
	if r.updated == nil {
		r.updated = make(chan struct{}, 1)
	}
	ch := r.updated
	r.mu.Unlock()
	select {
	case ch <- struct{}{}:
	default:
	}
}

// Run reconciles until ctx ends.
func (r *Reconciler) Run(ctx context.Context) error {
	interval := r.Interval
	if interval <= 0 {
		interval = defaultReconcileInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if rep := r.Once(ctx); rep != nil {
			r.publish(rep)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		case <-r.Cache.Changed():
		}
	}
}

// Once runs one pass and returns the resulting report, or nil when there is no
// assignment yet. With no assignment it changes nothing: an empty cache must never
// read as "remove every workload".
func (r *Reconciler) Once(ctx context.Context) *agentv1.StatusReport {
	a := r.Cache.Current()
	if a == nil {
		return nil
	}
	desc, err := r.Provider.Describe(ctx)
	if err != nil {
		slog.ErrorContext(ctx, "provider describe failed; skipping this pass", "err", err)
		return nil
	}
	applied, err := r.loadApplied()
	if err != nil {
		slog.ErrorContext(ctx, "read applied workloads; skipping this pass", "err", err)
		return nil
	}

	report := &agentv1.StatusReport{Generation: a.GetGeneration()}
	desired := map[provider.ResourceRef]bool{}
	for _, w := range a.GetWorkloads() {
		ref := provider.ResourceRef{Environment: w.GetEnvironmentId(), App: w.GetAppId()}
		desired[ref] = true
		ws := &agentv1.WorkloadStatus{AppId: w.GetAppId(), ReleaseId: w.GetReleaseId()}
		report.Workloads = append(report.Workloads, ws)
		if w.GetProvider() != desc.Name {
			fail(ws, "PROVIDER_MISSING")
			slog.ErrorContext(ctx, "workload needs a provider this node does not run",
				"app", w.GetAppId(), "wants", w.GetProvider(), "have", desc.Name)
			continue
		}
		if !applied[ref] {
			// Recorded before Apply, so a crash mid-apply still leaves a trace to
			// clean up if the workload is later removed.
			applied[ref] = true
			if err := r.saveApplied(applied); err != nil {
				fail(ws, "STATE_WRITE_FAILED")
				slog.ErrorContext(ctx, "record applied workload", "app", w.GetAppId(), "err", err)
				continue
			}
		}
		r.converge(ctx, ref, w, ws)
	}

	for ref := range applied {
		if desired[ref] {
			continue
		}
		if err := r.Provider.Destroy(ctx, ref); err != nil {
			slog.ErrorContext(ctx, "destroy removed workload; will retry", "app", ref.App, "err", err)
			continue
		}
		delete(applied, ref)
		if err := r.saveApplied(applied); err != nil {
			slog.ErrorContext(ctx, "record destroyed workload", "app", ref.App, "err", err)
		}
		slog.InfoContext(ctx, "workload removed", "app", ref.App)
	}
	return report
}

// converge applies w if the target does not match it or is not running, then fills
// ws from the observed state.
func (r *Reconciler) converge(ctx context.Context, ref provider.ResourceRef, w *agentv1.Workload, ws *agentv1.WorkloadStatus) {
	spec, err := r.spec(ref, w)
	if err != nil {
		fail(ws, "SPEC_INVALID")
		slog.ErrorContext(ctx, "build spec", "app", w.GetAppId(), "err", err)
		return
	}
	st, err := r.Provider.Status(ctx, ref)
	if err != nil {
		fail(ws, "STATUS_FAILED")
		slog.ErrorContext(ctx, "provider status", "app", w.GetAppId(), "err", err)
		return
	}
	if !st.Exists || st.Hash != spec.Hash || !st.Running {
		if _, err := r.Provider.Apply(ctx, provider.ApplyRequest{Spec: spec}); err != nil {
			fail(ws, "APPLY_FAILED")
			slog.ErrorContext(ctx, "provider apply; will retry", "app", w.GetAppId(), "err", err)
			return
		}
		if st, err = r.Provider.Status(ctx, ref); err != nil {
			fail(ws, "STATUS_FAILED")
			slog.ErrorContext(ctx, "provider status", "app", w.GetAppId(), "err", err)
			return
		}
	}
	ws.ReadyReplicas = st.ReadyReplicas
	switch {
	case st.Running && st.Healthy:
		ws.State = agentv1.WorkloadState_WORKLOAD_STATE_HEALTHY
	case st.Running:
		ws.State = agentv1.WorkloadState_WORKLOAD_STATE_UNHEALTHY
	default:
		ws.State = agentv1.WorkloadState_WORKLOAD_STATE_APPLYING
	}
}

// fail marks the workload failed with a stable reason; details go to the agent's log.
func fail(ws *agentv1.WorkloadStatus, reason string) {
	ws.State = agentv1.WorkloadState_WORKLOAD_STATE_FAILED
	ws.Failure = &bohuxv1.ErrorDetail{Reason: reason}
}

// workloadSecrets opens a workload's sealed values only when the provider asks.
type workloadSecrets struct {
	st *State
	w  *agentv1.Workload
}

func (s workloadSecrets) Open() (map[string]string, error) { return s.st.OpenSecrets(s.w) }

// spec builds the provider Spec. Its hash covers the workload minus fields that do
// not change what runs (the per-send seal, the deployment id) plus a keyed digest of
// the secret values, so a rotated secret re-applies without the hash revealing it.
func (r *Reconciler) spec(ref provider.ResourceRef, w *agentv1.Workload) (provider.Spec, error) {
	values, err := r.State.OpenSecrets(w)
	if err != nil {
		return provider.Spec{}, err
	}
	shape := proto.Clone(w).(*agentv1.Workload)
	shape.SealedSecrets, shape.DeploymentId = nil, ""
	b, err := proto.MarshalOptions{Deterministic: true}.Marshal(shape)
	if err != nil {
		return provider.Spec{}, fmt.Errorf("encode workload: %w", err)
	}
	h := sha256.New()
	h.Write(b)
	h.Write(r.secretDigest(values))
	spec := provider.Spec{
		Ref: ref, Release: w.GetReleaseId(), Image: w.GetImage(), Runtime: w.GetRuntime(),
		Hash: hex.EncodeToString(h.Sum(nil)),
	}
	if len(w.GetSealedSecrets()) > 0 {
		spec.Secrets = workloadSecrets{st: r.State, w: w}
	}
	return spec, nil
}

// secretDigest is an HMAC of the values under a key derived from the node's sealing
// key: it changes when a value changes but cannot be used to guess one.
func (r *Reconciler) secretDigest(values map[string]string) []byte {
	key := sha256.Sum256(append([]byte("bohux-spec-hash\x00"), r.State.sealingKey.Bytes()...))
	mac := hmac.New(sha256.New, key[:])
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		mac.Write([]byte(k))
		mac.Write([]byte{0})
		mac.Write([]byte(values[k]))
		mac.Write([]byte{0})
	}
	return mac.Sum(nil)
}

func (r *Reconciler) loadApplied() (map[provider.ResourceRef]bool, error) {
	out := map[provider.ResourceRef]bool{}
	raw, err := os.ReadFile(filepath.Join(r.State.dir, appliedFile))
	if errors.Is(err, fs.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", appliedFile, err)
	}
	var refs []provider.ResourceRef
	if err := json.Unmarshal(raw, &refs); err != nil {
		return nil, fmt.Errorf("decode %s: %w", appliedFile, err)
	}
	for _, ref := range refs {
		out[ref] = true
	}
	return out, nil
}

func (r *Reconciler) saveApplied(applied map[provider.ResourceRef]bool) error {
	refs := make([]provider.ResourceRef, 0, len(applied))
	for ref := range applied {
		refs = append(refs, ref)
	}
	sort.Slice(refs, func(i, j int) bool {
		if refs[i].Environment != refs[j].Environment {
			return refs[i].Environment < refs[j].Environment
		}
		return refs[i].App < refs[j].App
	})
	b, err := json.Marshal(refs)
	if err != nil {
		return fmt.Errorf("encode %s: %w", appliedFile, err)
	}
	return writeFile(r.State.dir, appliedFile, b)
}
