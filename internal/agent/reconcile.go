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
	"google.golang.org/protobuf/types/known/timestamppb"

	agentv1 "github.com/bohumix/bohux/gen/bohux/agent/v1"
	bohuxv1 "github.com/bohumix/bohux/gen/bohux/v1"
	"github.com/bohumix/bohux/internal/ingress"
	"github.com/bohumix/bohux/internal/provider"
	"github.com/bohumix/bohux/internal/slo"
)

const (
	defaultReconcileInterval = 10 * time.Second
	// appliedFile records every workload this agent has applied and not destroyed.
	// Providers have no list verb (rule 4), so this is how removals are found.
	appliedFile = "applied.json"
	// fallbackFile records, per deployment, whether its workload turned healthy or the
	// agent fell back to the workload it replaced (ADR-0031), so a restart keeps it.
	fallbackFile = "fallback.json"

	fallbackHealthy = "healthy"
	fallbackApplied = "applied"
)

// Reconciler converges the node to the cached assignment, with or without the control
// plane (rule 1, ADR-0007). It is the only apply path: deploys, rollbacks, and
// self-healing all arrive as a cached assignment.
type Reconciler struct {
	State *State
	Cache *Cache
	// Provider is the node's primary provider (Docker); workloads recorded before
	// records named their provider belong to it.
	Provider provider.Provider
	// Extra are further providers on the same node by name, e.g. "swarm" on a swarm
	// manager (ADR-0045). Each workload names the one it runs on.
	Extra map[string]provider.Provider
	// Interval between passes; zero means 10 seconds. A new assignment triggers a
	// pass at once.
	Interval time.Duration
	// Now is the clock for fallback deadlines; nil means time.Now.
	Now func() time.Time
	// Ingress is the node's ingress (ADR-0034); nil when the node has none, and then
	// workloads with routes report INGRESS_FAILED.
	Ingress Ingress
	// Usage supplies the latest resource sample for reports (ADR-0040); nil reports
	// none.
	Usage UsageSource
	// Traffic counts routed workloads' requests at the ingress for the SLO gate
	// (ADR-0050); nil reports none.
	Traffic TrafficSource
	// Link, if set, says whether the control plane rejects this agent. A new release
	// healthy while it does cannot be seen healthy there (a control plane that refuses
	// its own agent), so it does not cancel its fallback (ADR-0042). An unreachable
	// control plane is not a rejection (rule 1). Nil: never rejected.
	Link interface{ Rejected() bool }

	// endpoints remembers each routed port's last reported endpoint, so a replica
	// restarting does not drop its host from the ingress for a pass.
	endpoints map[string]string

	mu      sync.Mutex
	latest  *agentv1.StatusReport
	updated chan struct{}
}

// UsageSource is the latest resource sample; Sampler implements it.
type UsageSource interface {
	Latest() (*bohuxv1.NodeUsage, map[provider.ResourceRef]provider.Usage, time.Time)
}

// Ingress serves hosts from endpoints; ingress.Caddy implements it.
type Ingress interface {
	Load(ctx context.Context, sites []ingress.Site) error
}

// TrafficSource returns the ingress's counters for the upstreams dialled as endpoints;
// Traffic implements it.
type TrafficSource interface {
	Counts(endpoints ...string) slo.Counts
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

	fallbacks, err := r.loadFallbacks()
	if err != nil {
		slog.ErrorContext(ctx, "read fallback state; skipping this pass", "err", err)
		return nil
	}
	slots, err := r.loadSlots()
	if err != nil {
		slog.ErrorContext(ctx, "read slot state; skipping this pass", "err", err)
		return nil
	}
	report := &agentv1.StatusReport{Generation: a.GetGeneration()}
	desired := map[provider.ResourceRef]bool{}
	seen := map[string]bool{}
	var routed []routedWorkload
	inAssignment := map[string]bool{}
	for _, w := range a.GetWorkloads() {
		ref := provider.ResourceRef{Environment: w.GetEnvironmentId(), App: w.GetAppId(),
			EnvironmentSlug: w.GetEnvironmentSlug(), AppSlug: w.GetAppSlug()}
		inAssignment[slotKey(ref)] = true
		seen[w.GetDeploymentId()] = true
		w, fellBack := r.effective(ctx, w, fallbacks)
		ws := &agentv1.WorkloadStatus{AppId: w.GetAppId(), ReleaseId: w.GetReleaseId()}
		report.Workloads = append(report.Workloads, ws)
		if w.GetProvider() != desc.Name && r.Extra[w.GetProvider()] == nil {
			desired[ref] = true
			fail(ws, "PROVIDER_MISSING")
			slog.ErrorContext(ctx, "workload needs a provider this node does not run",
				"app", w.GetAppId(), "wants", w.GetProvider(), "have", desc.Name)
			continue
		}
		var (
			st      provider.Status
			current = true
			cut     *cutover
			split   *canarySplit
		)
		if r.switches(w) {
			var keep []provider.ResourceRef
			st, current, cut, split, keep = r.convergeSlots(ctx, ref, w, ws, slots, applied)
			for _, k := range keep {
				desired[k] = true
			}
		} else {
			// A workload that does not switch stays in the slot it lives in.
			ref.Slot = slots[slotKey(ref)].Slot
			desired[ref] = true
			if err := r.record(applied, ref, w.GetProvider()); err != nil {
				// Recorded before Apply, so a crash mid-apply still leaves a trace to
				// clean up if the workload is later removed.
				fail(ws, "STATE_WRITE_FAILED")
				slog.ErrorContext(ctx, "record applied workload", "app", w.GetAppId(), "err", err)
				continue
			}
			st = r.converge(ctx, ref, w, ws)
		}
		if len(w.GetRoutes()) > 0 {
			routed = append(routed, routedWorkload{w: w, ws: ws, endpoints: st.Endpoints, current: current, cut: cut,
				split: split})
		}
		if fellBack {
			if ws.Failure == nil {
				ws.Failure = &bohuxv1.ErrorDetail{Reason: "FALLBACK_APPLIED"}
			}
		} else if w.GetFallback() != nil && ws.GetState() == agentv1.WorkloadState_WORKLOAD_STATE_HEALTHY &&
			fallbacks[w.GetDeploymentId()] == "" && (r.Link == nil || !r.Link.Rejected()) {
			fallbacks[w.GetDeploymentId()] = fallbackHealthy
		}
	}
	r.saveFallbacks(ctx, fallbacks, seen)
	r.serve(ctx, routed, slots, applied)
	for key := range slots {
		if !inAssignment[key] {
			delete(slots, key)
		}
	}
	if err := r.saveSlots(slots); err != nil {
		slog.ErrorContext(ctx, "record slot state", "err", err)
	}

	for ref := range applied {
		if !desired[ref] {
			r.destroy(ctx, ref, applied)
		}
	}
	r.attachUsage(report)
	return report
}

type routedWorkload struct {
	w         *agentv1.Workload
	ws        *agentv1.WorkloadStatus
	endpoints map[int32]string
	// current is false while the endpoints serve an older spec than w (a switch in
	// progress, ADR-0035); cut is the switch to finish once the ingress serves w.
	current bool
	cut     *cutover
	// split, during a canary, is the live slot's endpoints and the new Release's share
	// of the traffic (ADR-0049).
	split *canarySplit
}

// canarySplit serves a canary beside the live slot.
type canarySplit struct {
	live   map[int32]string
	weight uint32
}

// serve loads the node's ingress with every routed workload's hosts (ADR-0034). A
// workload is marked ingress_loaded once the loaded config serves all its routes; a
// refused or impossible load marks the routed workloads INGRESS_FAILED, retried next
// pass, while the ingress keeps serving what it had.
func (r *Reconciler) serve(ctx context.Context, routed []routedWorkload, slots map[string]slotEntry, applied appliedSet) {
	if r.Ingress == nil {
		for _, rw := range routed {
			failIfOK(rw.ws, "INGRESS_FAILED")
		}
		if len(routed) > 0 {
			slog.ErrorContext(ctx, "workloads have routes but this node runs no ingress", "workloads", len(routed))
		}
		return
	}
	// Only routed ports carry over to the next pass.
	known := r.endpoints
	r.endpoints = map[string]string{}
	var sites []ingress.Site
	complete := make([]bool, len(routed))
	for i, rw := range routed {
		complete[i] = true
		for _, rt := range rw.w.GetRoutes() {
			key := fmt.Sprintf("%s/%d", rw.w.GetAppId(), rt.GetPort())
			ep := rw.endpoints[rt.GetPort()]
			if ep == "" {
				complete[i] = false
				if ep = known[key]; ep == "" {
					continue
				}
			}
			r.endpoints[key] = ep
			site := ingress.Site{Host: rt.GetHost(), Upstreams: []string{ep}}
			if live := rw.split.liveEndpoint(rt.GetPort()); live != "" && live != ep {
				share := int(rw.split.weight)
				site.Upstreams, site.Weights = []string{live, ep}, []int{100 - share, share}
			}
			sites = append(sites, site)
		}
	}
	if err := r.Ingress.Load(ctx, sites); err != nil {
		for _, rw := range routed {
			failIfOK(rw.ws, "INGRESS_FAILED")
		}
		slog.ErrorContext(ctx, "load ingress config; will retry", "err", err)
		return
	}
	for i, rw := range routed {
		rw.ws.IngressLoaded = complete[i] && rw.current
		if rw.cut != nil {
			r.finish(ctx, rw.cut, slots, applied)
		}
		r.attachTraffic(rw)
	}
}

// attachTraffic reports the ingress's counters for the slot that runs the workload's
// Release: its own endpoints, which during a canary are the new slot's (ADR-0050).
func (r *Reconciler) attachTraffic(rw routedWorkload) {
	if r.Traffic == nil || len(rw.endpoints) == 0 {
		return
	}
	seen := map[string]bool{}
	var eps []string
	for _, ep := range rw.endpoints {
		if !seen[ep] {
			seen[ep] = true
			eps = append(eps, ep)
		}
	}
	c := r.Traffic.Counts(eps...)
	rw.ws.Traffic = &agentv1.TrafficStats{Requests: c.Requests, ServerErrors: c.ServerErrors, LatencyBuckets: c.Latency}
}

// attachUsage adds the latest sample to the report: the node's, and each workload's
// summed over its slots (ADR-0040, ADR-0035).
func (r *Reconciler) attachUsage(report *agentv1.StatusReport) {
	if r.Usage == nil {
		return
	}
	node, workloads, at := r.Usage.Latest()
	if node == nil {
		return
	}
	report.Usage = node
	byApp := map[string]*bohuxv1.Usage{}
	for ref, u := range workloads {
		sum := byApp[ref.App]
		if sum == nil {
			sum = &bohuxv1.Usage{SampledAt: timestamppb.New(at)}
			byApp[ref.App] = sum
		}
		sum.CpuMillicores += u.CPUMillicores
		sum.MemoryBytes += u.MemoryBytes
		sum.MemoryLimitBytes += u.MemoryLimitBytes
	}
	for _, ws := range report.GetWorkloads() {
		ws.Usage = byApp[ws.GetAppId()]
	}
}

// failIfOK marks ws failed with reason unless it already failed for another one.
func failIfOK(ws *agentv1.WorkloadStatus, reason string) {
	if ws.GetState() != agentv1.WorkloadState_WORKLOAD_STATE_FAILED {
		fail(ws, reason)
	}
}

// effective returns the workload to converge to: w, or the workload it replaced once
// the agent has fallen back (ADR-0031). It falls back when w carries a fallback whose
// deadline has passed and w was never seen healthy while the control plane did not
// reject this agent (ADR-0042); a newer assignment (another deployment) is a different key
// and starts clean.
func (r *Reconciler) effective(ctx context.Context, w *agentv1.Workload, fallbacks map[string]string) (*agentv1.Workload, bool) {
	fb := w.GetFallback()
	if fb == nil || w.GetDeploymentId() == "" {
		return w, false
	}
	switch fallbacks[w.GetDeploymentId()] {
	case fallbackApplied:
		return fb, true
	case fallbackHealthy:
		return w, false
	}
	if r.now().Before(w.GetFallbackAfter().AsTime()) {
		return w, false
	}
	fallbacks[w.GetDeploymentId()] = fallbackApplied
	slog.WarnContext(ctx, "release never turned healthy and no newer assignment came; falling back",
		"app", w.GetAppId(), "release", w.GetReleaseId(), "fallback_release", fb.GetReleaseId())
	return fb, true
}

func (r *Reconciler) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *Reconciler) loadFallbacks() (map[string]string, error) {
	out := map[string]string{}
	raw, err := os.ReadFile(filepath.Join(r.State.dir, fallbackFile))
	if errors.Is(err, fs.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", fallbackFile, err)
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("decode %s: %w", fallbackFile, err)
	}
	return out, nil
}

// saveFallbacks keeps the entries of deployments still assigned and writes them.
func (r *Reconciler) saveFallbacks(ctx context.Context, fallbacks map[string]string, assigned map[string]bool) {
	for id := range fallbacks {
		if !assigned[id] {
			delete(fallbacks, id)
		}
	}
	b, err := json.Marshal(fallbacks)
	if err == nil {
		err = writeFile(r.State.dir, fallbackFile, b)
	}
	if err != nil {
		slog.ErrorContext(ctx, "record fallback state", "err", err)
	}
}

// converge applies w if the target does not match it or is not running, then fills
// ws from the observed state, which it returns (zero when it could not observe it).
func (r *Reconciler) converge(ctx context.Context, ref provider.ResourceRef, w *agentv1.Workload, ws *agentv1.WorkloadStatus) provider.Status {
	spec, err := r.spec(ref, w)
	if err != nil {
		fail(ws, "SPEC_INVALID")
		slog.ErrorContext(ctx, "build spec", "app", w.GetAppId(), "err", err)
		return provider.Status{}
	}
	p := r.providerNamed(w.GetProvider())
	st, err := p.Status(ctx, ref)
	if err != nil {
		fail(ws, "STATUS_FAILED")
		slog.ErrorContext(ctx, "provider status", "app", w.GetAppId(), "err", err)
		return provider.Status{}
	}
	if !st.Exists || st.Hash != spec.Hash || !st.Running {
		if _, err := p.Apply(ctx, provider.ApplyRequest{Spec: spec}); err != nil {
			fail(ws, "APPLY_FAILED")
			slog.ErrorContext(ctx, "provider apply; will retry", "app", w.GetAppId(), "err", err)
			return provider.Status{}
		}
		if st, err = p.Status(ctx, ref); err != nil {
			fail(ws, "STATUS_FAILED")
			slog.ErrorContext(ctx, "provider status", "app", w.GetAppId(), "err", err)
			return provider.Status{}
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
	return st
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

// workloadPullConfig opens the sealed registry credential only when the provider
// pulls (ADR-0041).
type workloadPullConfig struct {
	st *State
	w  *agentv1.Workload
}

func (p workloadPullConfig) Open() (string, error) { return p.st.OpenPullConfig(p.w) }

// spec builds the provider Spec. Its hash covers the workload minus fields that do
// not change what runs (the per-send seal, the deployment id, the fallback, the routes)
// plus a keyed digest of
// the secret values, so a rotated secret re-applies without the hash revealing it.
func (r *Reconciler) spec(ref provider.ResourceRef, w *agentv1.Workload) (provider.Spec, error) {
	values, err := r.State.OpenSecrets(w)
	if err != nil {
		return provider.Spec{}, err
	}
	shape := proto.Clone(w).(*agentv1.Workload)
	shape.SealedSecrets, shape.DeploymentId = nil, ""
	// The fallback changes what may run later, not what runs now; a canary's weight
	// changes what the ingress does (ADR-0049).
	shape.Fallback, shape.FallbackAfter, shape.CanaryWeight = nil, nil, 0
	// Routes reload the ingress, never the containers (ADR-0034); only whether there
	// are any changes what runs (the provider joins the workload to the ingress).
	ingress := len(shape.GetRoutes()) > 0
	shape.Routes = nil
	b, err := proto.MarshalOptions{Deterministic: true}.Marshal(shape)
	if err != nil {
		return provider.Spec{}, fmt.Errorf("encode workload: %w", err)
	}
	h := sha256.New()
	h.Write(b)
	if ingress {
		h.Write([]byte("\x00ingress"))
	}
	h.Write(r.secretDigest(values))
	spec := provider.Spec{
		Ref: ref, Release: w.GetReleaseId(), Image: w.GetImage(), Runtime: w.GetRuntime(),
		Raw: w.GetProviderOptions(), Hash: hex.EncodeToString(h.Sum(nil)), Ingress: ingress,
	}
	if len(w.GetSealedSecrets()) > 0 {
		spec.Secrets = workloadSecrets{st: r.State, w: w}
	}
	// The credential's value is not in the hash: the image is pinned by digest, so a
	// rotated credential is used by the next pull without restarting anything.
	if w.GetImagePullSecret() != nil {
		spec.PullConfig = workloadPullConfig{st: r.State, w: w}
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

// appliedSet maps every workload this agent applied and has not destroyed to the
// name of the provider that owns it; "" is the primary provider.
type appliedSet map[provider.ResourceRef]string

// appliedEntry is one record in appliedFile. Records written before providers were
// named have no provider and belong to the primary one.
type appliedEntry struct {
	provider.ResourceRef
	Provider string `json:",omitempty"`
}

func (r *Reconciler) loadApplied() (appliedSet, error) {
	out := appliedSet{}
	raw, err := os.ReadFile(filepath.Join(r.State.dir, appliedFile))
	if errors.Is(err, fs.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", appliedFile, err)
	}
	var entries []appliedEntry
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, fmt.Errorf("decode %s: %w", appliedFile, err)
	}
	for _, e := range entries {
		out[e.ResourceRef] = e.Provider
	}
	return out, nil
}

func (r *Reconciler) saveApplied(applied appliedSet) error {
	entries := make([]appliedEntry, 0, len(applied))
	for ref, name := range applied {
		entries = append(entries, appliedEntry{ResourceRef: ref, Provider: name})
	}
	sort.Slice(entries, func(i, j int) bool {
		a, b := entries[i].ResourceRef, entries[j].ResourceRef
		if a.Environment != b.Environment {
			return a.Environment < b.Environment
		}
		if a.App != b.App {
			return a.App < b.App
		}
		return a.Slot < b.Slot
	})
	b, err := json.Marshal(entries)
	if err != nil {
		return fmt.Errorf("encode %s: %w", appliedFile, err)
	}
	return writeFile(r.State.dir, appliedFile, b)
}

// providerNamed returns the provider a workload names: an extra one, else the primary
// (which also owns records without a name).
func (r *Reconciler) providerNamed(name string) provider.Provider {
	if p, ok := r.Extra[name]; ok {
		return p
	}
	return r.Provider
}
