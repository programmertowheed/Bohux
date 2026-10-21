package agentapi

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/nacl/box"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	agentv1 "github.com/bohumix/bohux/gen/bohux/agent/v1"
	bohuxv1 "github.com/bohumix/bohux/gen/bohux/v1"
	"github.com/bohumix/bohux/internal/store"
)

const assignmentType = "bohux.agent.v1.Assignment"

// SecretResolver turns secret handles into values. The OpenBao implementation comes
// with S7. Values it returns are sealed immediately and never stored or logged (rule 3).
type SecretResolver interface {
	Resolve(ctx context.Context, tenant uuid.UUID, handles []string) (map[string]string, error)
}

var (
	// ErrRevokedNode, ErrUnknownApp, and ErrWrongEnv are refusals that retrying cannot
	// fix.
	ErrRevokedNode     = errors.New("node is revoked")
	ErrUnknownApp      = errors.New("workload names an app that does not exist in this tenant")
	ErrWrongEnv        = errors.New("workload environment does not match its app's environment")
	errNoResolver      = errors.New("workloads reference secrets but no secret resolver is configured")
	errSealedInRequest = errors.New("workloads must carry secret handles, not sealed values")
)

// Assign writes the node's next generation: the full desired state, every workload
// (ADR-0007). Workloads carry secret handles only. Each workload's App must exist in
// the tenant; Assign fills its environment ID and the App and environment slugs that
// name the provider's artifacts (ADR-0025). An open stream for the node is woken to
// send it. It returns the new generation.
func (s *Server) Assign(ctx context.Context, tenant, node uuid.UUID, workloads []*agentv1.Workload, actor store.Actor) (uint64, error) {
	named := make([]*agentv1.Workload, len(workloads))
	for i, w := range workloads {
		if len(w.GetSealedSecrets()) > 0 {
			return 0, errSealedInRequest
		}
		named[i] = proto.Clone(w).(*agentv1.Workload)
	}
	return s.write(ctx, tenant, node, actor, func(tx *store.Tx, _ []*agentv1.Workload) ([]*agentv1.Workload, bool, error) {
		for _, w := range named {
			if err := nameWorkload(ctx, tx, w); err != nil {
				return nil, false, err
			}
		}
		return named, true, nil
	})
}

// AssignApp changes one App in the node's latest generation (ADR-0026): it replaces
// the App's workload with w, adds it if absent, or removes it when w is nil. Other
// workloads are kept as they are. If the latest generation already carries w's
// Deployment and Release for the App, or, for a removal, no workload for the App, it
// writes nothing and returns that generation, so retries are idempotent.
//
// With a non-zero fallbackAfter, a w that replaces another Release carries the
// workload it replaces as its fallback, which the agent applies on its own if w is
// never healthy by then (ADR-0031). Compensation passes zero.
func (s *Server) AssignApp(ctx context.Context, tenant, node, app uuid.UUID, w *agentv1.Workload, fallbackAfter time.Time, actor store.Actor) (uint64, error) {
	if w != nil {
		if len(w.GetSealedSecrets()) > 0 {
			return 0, errSealedInRequest
		}
		if w.GetAppId() != app.String() {
			return 0, fmt.Errorf("workload app %q is not %s: %w", w.GetAppId(), app, ErrUnknownApp)
		}
		w = proto.Clone(w).(*agentv1.Workload)
	}
	return s.write(ctx, tenant, node, actor, func(tx *store.Tx, current []*agentv1.Workload) ([]*agentv1.Workload, bool, error) {
		next := make([]*agentv1.Workload, 0, len(current)+1)
		found := false
		for _, c := range current {
			if c.GetAppId() != app.String() {
				next = append(next, c)
				continue
			}
			found = true
			if w != nil && c.GetDeploymentId() == w.GetDeploymentId() && c.GetReleaseId() == w.GetReleaseId() {
				return nil, false, nil
			}
			if w != nil && !fallbackAfter.IsZero() && c.GetReleaseId() != w.GetReleaseId() {
				fb := proto.Clone(c).(*agentv1.Workload)
				fb.Fallback, fb.FallbackAfter, fb.SealedSecrets = nil, nil, nil
				w.Fallback, w.FallbackAfter = fb, timestamppb.New(fallbackAfter)
			}
		}
		if w == nil {
			return next, found, nil
		}
		if err := nameWorkload(ctx, tx, w); err != nil {
			return nil, false, err
		}
		return append(next, w), true, nil
	})
}

// write locks the node, hands build the workloads of its latest generation, and
// stores what build returns as the next generation, unless build says nothing
// changed. It returns the generation now in force.
func (s *Server) write(ctx context.Context, tenant, node uuid.UUID, actor store.Actor,
	build func(tx *store.Tx, current []*agentv1.Workload) (next []*agentv1.Workload, changed bool, err error)) (uint64, error) {
	var (
		gen     uint64
		written bool
	)
	err := s.Store.InTenant(ctx, tenant, func(tx *store.Tx) error {
		var err error
		gen, written, err = s.writeTx(ctx, tx, node, actor, build)
		return err
	})
	if err != nil {
		return 0, fmt.Errorf("assign generation to node %s: %w", node, err)
	}
	if written {
		s.wake(node)
	}
	return gen, nil
}

// writeTx is write inside the caller's transaction. The caller wakes the node after
// commit if it reports written.
func (s *Server) writeTx(ctx context.Context, tx *store.Tx, node uuid.UUID, actor store.Actor,
	build func(tx *store.Tx, current []*agentv1.Workload) (next []*agentv1.Workload, changed bool, err error)) (uint64, bool, error) {
	// Locking the node serializes generations per node. The head is locked alone and
	// its state read afterwards: with a join, a concurrent state change (the node
	// coming online) moves the head to a new revision, and Postgres's recheck of the
	// locked row against the old revision row would drop it as not found.
	var revision uuid.UUID
	err := tx.QueryRow(ctx, `SELECT revision_id FROM nodes_heads
		WHERE id = $1 AND NOT deleted FOR UPDATE`, node).Scan(&revision)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, store.ErrNotFound
	}
	if err != nil {
		return 0, false, fmt.Errorf("lock node: %w", err)
	}
	var state string
	if err := tx.QueryRow(ctx, `SELECT state FROM nodes_revisions WHERE id = $1`,
		revision).Scan(&state); err != nil {
		return 0, false, fmt.Errorf("read node state: %w", err)
	}
	if state == "revoked" {
		return 0, false, ErrRevokedNode
	}
	var (
		last int64
		raw  []byte
	)
	err = tx.QueryRow(ctx, `SELECT generation, payload FROM assignments
		WHERE node_id = $1 ORDER BY generation DESC LIMIT 1`, node).Scan(&last, &raw)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return 0, false, fmt.Errorf("read generation: %w", err)
	}
	current := &agentv1.AssignmentPayload{}
	if err := proto.Unmarshal(raw, current); err != nil {
		return 0, false, fmt.Errorf("decode stored assignment: %w", err)
	}
	workloads, changed, err := build(tx, current.GetWorkloads())
	if err != nil {
		return 0, false, err
	}
	gen := uint64(last)
	if !changed {
		return gen, false, nil
	}
	gen++
	payload, err := proto.MarshalOptions{Deterministic: true}.Marshal(&agentv1.AssignmentPayload{
		NodeId: node.String(), Generation: gen, IssuedAt: timestamppb.New(s.now()), Workloads: workloads,
	})
	if err != nil {
		return 0, false, fmt.Errorf("encode assignment: %w", err)
	}
	if err := tx.Exec(ctx, `INSERT INTO assignments (tenant_id, node_id, generation, payload, created_by)
		VALUES ($1, $2, $3, $4, $5)`, tx.Tenant(), node, int64(gen), payload, actor.ID); err != nil {
		return 0, false, fmt.Errorf("store assignment: %w", err)
	}
	if err := tx.AuditOnly(ctx, store.Audit{Actor: actor, Action: "bohux.agent.v1/Assign",
		ResourceType: assignmentType}, node); err != nil {
		return 0, false, err
	}
	return gen, true, nil
}

// FollowAppTx sets the replicas (ADR-0027) and routes (ADR-0034) of the App's workload
// in the node's next generation, inside the caller's transaction: App state that is not
// part of a Release. An App with no workload on the node, or already at both, writes
// nothing. When it reports written, the caller calls Wake after commit.
func (s *Server) FollowAppTx(ctx context.Context, tx *store.Tx, node, app uuid.UUID, replicas int32, routes []*bohuxv1.Route, actor store.Actor) (bool, error) {
	_, written, err := s.writeTx(ctx, tx, node, actor, func(_ *store.Tx, current []*agentv1.Workload) ([]*agentv1.Workload, bool, error) {
		next := make([]*agentv1.Workload, len(current))
		changed := false
		for i, c := range current {
			next[i] = c
			if c.GetAppId() != app.String() ||
				(c.GetRuntime().GetReplicas() == replicas && sameRoutes(c.GetRoutes(), routes)) {
				continue
			}
			w := proto.Clone(c).(*agentv1.Workload)
			if w.Runtime == nil {
				w.Runtime = &bohuxv1.Runtime{}
			}
			w.Runtime.Replicas = replicas
			w.Routes = cloneRoutes(routes)
			next[i], changed = w, true
		}
		return next, changed, nil
	})
	if err != nil {
		return false, fmt.Errorf("follow app %s on node %s: %w", app, node, err)
	}
	return written, nil
}

func sameRoutes(a, b []*bohuxv1.Route) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !proto.Equal(a[i], b[i]) {
			return false
		}
	}
	return true
}

func cloneRoutes(rs []*bohuxv1.Route) []*bohuxv1.Route {
	out := make([]*bohuxv1.Route, len(rs))
	for i, r := range rs {
		out[i] = proto.Clone(r).(*bohuxv1.Route)
	}
	return out
}

// RotateTx writes a new generation, with the same workloads, for every node whose
// latest assignment references handle, inside the caller's transaction. Values are
// resolved and sealed when a generation is sent, so the new generation carries the
// rotated value and the agent re-applies (ADR-0008). It returns the nodes to wake
// after commit.
func (s *Server) RotateTx(ctx context.Context, tx *store.Tx, handle string, actor store.Actor) ([]uuid.UUID, error) {
	rows, err := tx.Query(ctx, `SELECT a.node_id, a.payload FROM assignments a
		JOIN nodes_heads n ON n.id = a.node_id AND NOT n.deleted
		WHERE a.generation = (SELECT max(generation) FROM assignments WHERE node_id = a.node_id)`)
	if err != nil {
		return nil, fmt.Errorf("read latest assignments: %w", err)
	}
	var nodes []uuid.UUID
	for rows.Next() {
		var (
			node uuid.UUID
			raw  []byte
		)
		if err := rows.Scan(&node, &raw); err != nil {
			rows.Close()
			return nil, fmt.Errorf("read latest assignments: %w", err)
		}
		p := &agentv1.AssignmentPayload{}
		if err := proto.Unmarshal(raw, p); err != nil {
			rows.Close()
			return nil, fmt.Errorf("decode stored assignment: %w", err)
		}
		if references(p.GetWorkloads(), handle) {
			nodes = append(nodes, node)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read latest assignments: %w", err)
	}
	var woken []uuid.UUID
	for _, node := range nodes {
		_, written, err := s.writeTx(ctx, tx, node, actor, func(_ *store.Tx, current []*agentv1.Workload) ([]*agentv1.Workload, bool, error) {
			// Re-check under the node lock: a concurrent write may have moved on.
			return current, references(current, handle), nil
		})
		if errors.Is(err, ErrRevokedNode) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("rotate secret on node %s: %w", node, err)
		}
		if written {
			woken = append(woken, node)
		}
	}
	return woken, nil
}

func references(workloads []*agentv1.Workload, handle string) bool {
	for _, w := range workloads {
		for _, ref := range append(w.GetSecrets(), w.GetFallback().GetSecrets()...) {
			if ref.GetHandle() == handle {
				return true
			}
		}
	}
	return false
}

// Wake tells the node's open stream, if any, that a new generation is waiting.
func (s *Server) Wake(node uuid.UUID) { s.wake(node) }

// nameWorkload fills w's environment ID and slugs from its App's head row.
func nameWorkload(ctx context.Context, tx *store.Tx, w *agentv1.Workload) error {
	app, err := uuid.Parse(w.GetAppId())
	if err != nil {
		return fmt.Errorf("app %q: %w", w.GetAppId(), ErrUnknownApp)
	}
	var (
		appSlug, envSlug string
		env              uuid.UUID
	)
	err = tx.QueryRow(ctx, `SELECT a.slug, e.slug, e.id FROM apps_heads a
		JOIN environments_heads e ON e.id = a.environment_id
		WHERE a.id = $1 AND NOT a.deleted`, app).Scan(&appSlug, &envSlug, &env)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("app %s: %w", app, ErrUnknownApp)
	}
	if err != nil {
		return fmt.Errorf("look up app %s: %w", app, err)
	}
	if w.GetEnvironmentId() != "" && w.GetEnvironmentId() != env.String() {
		return fmt.Errorf("app %s: %w", app, ErrWrongEnv)
	}
	w.EnvironmentId, w.AppSlug, w.EnvironmentSlug = env.String(), appSlug, envSlug
	return nil
}

// signedLatest builds the latest assignment for sending: resolves and seals each
// workload's secrets to the node's key, then signs the exact payload bytes
// (ADR-0024). It returns nil if the node has no assignment newer than after.
func (s *Server) signedLatest(ctx context.Context, tenant, node uuid.UUID, after uint64) (*agentv1.Assignment, uint64, error) {
	var (
		raw        []byte
		gen        int64
		sealingKey []byte
	)
	err := s.Store.InTenant(ctx, tenant, func(tx *store.Tx) error {
		err := tx.QueryRow(ctx, `SELECT a.generation, a.payload, h.sealing_public_key
			FROM assignments a JOIN nodes_heads h ON h.id = a.node_id
			WHERE a.node_id = $1 ORDER BY a.generation DESC LIMIT 1`, node).Scan(&gen, &raw, &sealingKey)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return err
	})
	if err != nil {
		return nil, 0, fmt.Errorf("load assignment: %w", err)
	}
	if raw == nil || uint64(gen) <= after {
		return nil, 0, nil
	}
	payload := &agentv1.AssignmentPayload{}
	if err := proto.Unmarshal(raw, payload); err != nil {
		return nil, 0, fmt.Errorf("decode stored assignment: %w", err)
	}
	if len(sealingKey) != 32 {
		return nil, 0, errors.New("node has no valid sealing key")
	}
	var recipient [32]byte
	copy(recipient[:], sealingKey)
	for _, w := range payload.GetWorkloads() {
		// A fallback runs with its own secrets, so they travel sealed too (ADR-0031).
		for _, sw := range []*agentv1.Workload{w, w.GetFallback()} {
			if sw == nil {
				continue
			}
			if err := s.seal(ctx, tenant, sw, &recipient); err != nil {
				return nil, 0, err
			}
		}
	}
	signed, err := proto.MarshalOptions{Deterministic: true}.Marshal(payload)
	if err != nil {
		return nil, 0, fmt.Errorf("encode assignment: %w", err)
	}
	sig, keyID := s.PKI.SignAssignment(signed)
	return &agentv1.Assignment{Payload: signed, Signature: sig, KeyId: keyID}, uint64(gen), nil
}

// seal resolves w's secret handles and seals the values to the node's key.
func (s *Server) seal(ctx context.Context, tenant uuid.UUID, w *agentv1.Workload, recipient *[32]byte) error {
	if len(w.GetSecrets()) == 0 {
		return nil
	}
	if s.Secrets == nil {
		return errNoResolver
	}
	handles := make([]string, len(w.GetSecrets()))
	for i, ref := range w.GetSecrets() {
		handles[i] = ref.GetHandle()
	}
	byHandle, err := s.Secrets.Resolve(ctx, tenant, handles)
	if err != nil {
		return fmt.Errorf("resolve secrets for app %s: %w", w.GetAppId(), err)
	}
	values := &agentv1.SecretValues{Values: map[string]string{}}
	for _, ref := range w.GetSecrets() {
		v, ok := byHandle[ref.GetHandle()]
		if !ok {
			return fmt.Errorf("secret %s for app %s did not resolve", ref.GetKey(), w.GetAppId())
		}
		values.Values[ref.GetKey()] = v
	}
	plain, err := proto.Marshal(values)
	if err != nil {
		return fmt.Errorf("encode secret values: %w", err)
	}
	if w.SealedSecrets, err = box.SealAnonymous(nil, plain, recipient, rand.Reader); err != nil {
		return fmt.Errorf("seal secret values: %w", err)
	}
	return nil
}

// recordAck stores the highest generation the agent acknowledged, ignoring any
// generation that was never assigned.
func (s *Server) recordAck(ctx context.Context, tenant, node uuid.UUID, gen uint64) error {
	return s.Store.InTenant(ctx, tenant, func(tx *store.Tx) error {
		return tx.Exec(ctx, `UPDATE nodes_heads SET acked_generation = $1
			WHERE id = $2 AND COALESCE(acked_generation, 0) < $1
			  AND EXISTS (SELECT 1 FROM assignments WHERE node_id = $2 AND generation = $1)`,
			int64(gen), node)
	})
}
