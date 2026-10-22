package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"

	agentv1 "github.com/bohumix/bohux/gen/bohux/agent/v1"
	"github.com/bohumix/bohux/internal/provider"
)

// slotsFile records, per App, which slot serves and the spec hash it runs (ADR-0035).
// The hash is kept here because a provider reports none for a workload that is not
// running, and a crashed live slot must restart in place, not switch.
const slotsFile = "slots.json"

type slotEntry struct {
	Slot string `json:"slot"`
	Hash string `json:"hash"`
}

// cutover is a switch ready to happen: once the ingress serves to's endpoints, to
// becomes live and from is destroyed.
type cutover struct {
	key      string
	from, to provider.ResourceRef
	hash     string
}

func slotKey(ref provider.ResourceRef) string { return ref.Environment + "/" + ref.App }

// otherSlot is the slot a switch away from s goes to.
func otherSlot(s string) string {
	if s == "blue" {
		return "green"
	}
	return "blue"
}

// switches reports whether w deploys by switching slots: it is served by this node's
// ingress and holds no host port, which two copies could not share (ADR-0035 item 1).
func (r *Reconciler) switches(w *agentv1.Workload) bool {
	if r.Ingress == nil || len(w.GetRoutes()) == 0 {
		return false
	}
	for _, p := range w.GetRuntime().GetPorts() {
		if p.GetHostPort() != 0 {
			return false
		}
	}
	return true
}

// convergeSlots converges w held in slots: in place when the live slot already runs
// it (or nothing runs yet), else into the other slot. It returns the status whose
// endpoints the ingress should serve now, whether that is w itself, the cut-over to
// make once the ingress serves it, and the refs it keeps.
func (r *Reconciler) convergeSlots(ctx context.Context, base provider.ResourceRef, w *agentv1.Workload,
	ws *agentv1.WorkloadStatus, slots map[string]slotEntry, applied appliedSet,
) (st provider.Status, current bool, cut *cutover, keep []provider.ResourceRef) {
	key := slotKey(base)
	e := slots[key]
	live, pending := base, base
	live.Slot, pending.Slot = e.Slot, otherSlot(e.Slot)
	spec, err := r.spec(live, w)
	if err != nil {
		fail(ws, "SPEC_INVALID")
		slog.ErrorContext(ctx, "build spec", "app", w.GetAppId(), "err", err)
		return provider.Status{}, false, nil, []provider.ResourceRef{live}
	}
	stLive, err := r.providerNamed(w.GetProvider()).Status(ctx, live)
	if err != nil {
		fail(ws, "STATUS_FAILED")
		slog.ErrorContext(ctx, "provider status", "app", w.GetAppId(), "err", err)
		return provider.Status{}, false, nil, []provider.ResourceRef{live, pending}
	}
	if !stLive.Exists || e.Hash == spec.Hash || (e.Hash == "" && stLive.Hash == spec.Hash) {
		// The live slot runs w (or nothing runs): converge in place, and drop a
		// pending slot a compensation or fallback left behind.
		if err := r.record(applied, live, w.GetProvider()); err != nil {
			fail(ws, "STATE_WRITE_FAILED")
			return provider.Status{}, false, nil, []provider.ResourceRef{live, pending}
		}
		st := r.converge(ctx, live, w, ws)
		slots[key] = slotEntry{Slot: e.Slot, Hash: spec.Hash}
		return st, true, nil, []provider.ResourceRef{live}
	}
	// A new spec: bring it up beside the live slot, which keeps serving.
	if err := r.record(applied, pending, w.GetProvider()); err != nil {
		fail(ws, "STATE_WRITE_FAILED")
		return stLive, false, nil, []provider.ResourceRef{live, pending}
	}
	st = r.converge(ctx, pending, w, ws)
	if st.Running && st.Healthy && len(st.Endpoints) > 0 {
		return st, true, &cutover{key: key, from: live, to: pending, hash: spec.Hash},
			[]provider.ResourceRef{live, pending}
	}
	// Not ready: report the new Release as it is, serve the old one.
	if ws.GetState() == agentv1.WorkloadState_WORKLOAD_STATE_HEALTHY {
		ws.State = agentv1.WorkloadState_WORKLOAD_STATE_APPLYING
	}
	return stLive, false, nil, []provider.ResourceRef{live, pending}
}

// finish makes a cut-over permanent once the ingress serves its new slot: record it
// first, so a crash before the old slot is gone re-destroys rather than serving a
// destroyed slot, then destroy the old slot.
func (r *Reconciler) finish(ctx context.Context, c *cutover, slots map[string]slotEntry, applied appliedSet) {
	slots[c.key] = slotEntry{Slot: c.to.Slot, Hash: c.hash}
	if err := r.saveSlots(slots); err != nil {
		slog.ErrorContext(ctx, "record slot switch", "app", c.to.App, "err", err)
		return
	}
	slog.InfoContext(ctx, "switched slots", "app", c.to.App, "from", c.from.Slot, "to", c.to.Slot)
	r.destroy(ctx, c.from, applied)
}

// record notes ref as applied, with the provider that owns it, before anything is
// applied to it.
func (r *Reconciler) record(applied appliedSet, ref provider.ResourceRef, owner string) error {
	if cur, ok := applied[ref]; ok && cur == owner {
		return nil
	}
	applied[ref] = owner
	return r.saveApplied(applied)
}

// destroy removes ref through the provider that applied it and forgets it; a failure
// is retried on a later pass.
func (r *Reconciler) destroy(ctx context.Context, ref provider.ResourceRef, applied appliedSet) {
	owner, ok := applied[ref]
	if !ok {
		return
	}
	if err := r.providerNamed(owner).Destroy(ctx, ref); err != nil {
		slog.ErrorContext(ctx, "destroy workload; will retry", "app", ref.App, "slot", ref.Slot, "err", err)
		return
	}
	delete(applied, ref)
	if err := r.saveApplied(applied); err != nil {
		slog.ErrorContext(ctx, "record destroyed workload", "app", ref.App, "err", err)
	}
	slog.InfoContext(ctx, "workload removed", "app", ref.App, "slot", ref.Slot)
}

func (r *Reconciler) loadSlots() (map[string]slotEntry, error) {
	out := map[string]slotEntry{}
	raw, err := os.ReadFile(filepath.Join(r.State.dir, slotsFile))
	if errors.Is(err, fs.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", slotsFile, err)
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("decode %s: %w", slotsFile, err)
	}
	return out, nil
}

func (r *Reconciler) saveSlots(slots map[string]slotEntry) error {
	b, err := json.Marshal(slots)
	if err != nil {
		return fmt.Errorf("encode %s: %w", slotsFile, err)
	}
	return writeFile(r.State.dir, slotsFile, b)
}
