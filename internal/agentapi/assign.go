package agentapi

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/nacl/box"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	agentv1 "github.com/bohumix/bohux/gen/bohux/agent/v1"
	"github.com/bohumix/bohux/internal/store"
)

const assignmentType = "bohux.agent.v1.Assignment"

// SecretResolver turns secret handles into values. The OpenBao implementation comes
// with S7. Values it returns are sealed immediately and never stored or logged (rule 3).
type SecretResolver interface {
	Resolve(ctx context.Context, tenant uuid.UUID, handles []string) (map[string]string, error)
}

var (
	errRevokedNode     = errors.New("node is revoked")
	errNoResolver      = errors.New("workloads reference secrets but no secret resolver is configured")
	errSealedInRequest = errors.New("workloads must carry secret handles, not sealed values")
	errUnknownApp      = errors.New("workload names an app that does not exist in this tenant")
	errWrongEnv        = errors.New("workload environment does not match its app's environment")
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
	workloads = named
	var gen uint64
	err := s.Store.InTenant(ctx, tenant, func(tx *store.Tx) error {
		// Locking the node serializes generations per node. The head is locked alone
		// and its state read afterwards: with a join, a concurrent state change (the
		// node coming online) moves the head to a new revision, and Postgres's recheck
		// of the locked row against the old revision row would drop it as not found.
		var revision uuid.UUID
		err := tx.QueryRow(ctx, `SELECT revision_id FROM nodes_heads
			WHERE id = $1 AND NOT deleted FOR UPDATE`, node).Scan(&revision)
		if errors.Is(err, pgx.ErrNoRows) {
			return store.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("lock node: %w", err)
		}
		var state string
		if err := tx.QueryRow(ctx, `SELECT state FROM nodes_revisions WHERE id = $1`,
			revision).Scan(&state); err != nil {
			return fmt.Errorf("read node state: %w", err)
		}
		if state == "revoked" {
			return errRevokedNode
		}
		var last int64
		if err := tx.QueryRow(ctx, `SELECT COALESCE(max(generation), 0) FROM assignments
			WHERE node_id = $1`, node).Scan(&last); err != nil {
			return fmt.Errorf("read generation: %w", err)
		}
		gen = uint64(last) + 1
		for _, w := range workloads {
			if err := nameWorkload(ctx, tx, w); err != nil {
				return err
			}
		}
		payload, err := proto.MarshalOptions{Deterministic: true}.Marshal(&agentv1.AssignmentPayload{
			NodeId: node.String(), Generation: gen, IssuedAt: timestamppb.New(s.now()), Workloads: workloads,
		})
		if err != nil {
			return fmt.Errorf("encode assignment: %w", err)
		}
		if err := tx.Exec(ctx, `INSERT INTO assignments (tenant_id, node_id, generation, payload, created_by)
			VALUES ($1, $2, $3, $4, $5)`, tenant, node, int64(gen), payload, actor.ID); err != nil {
			return fmt.Errorf("store assignment: %w", err)
		}
		return tx.AuditOnly(ctx, store.Audit{Actor: actor, Action: "bohux.agent.v1/Assign",
			ResourceType: assignmentType}, node)
	})
	if err != nil {
		return 0, fmt.Errorf("assign generation to node %s: %w", node, err)
	}
	s.wake(node)
	return gen, nil
}

// nameWorkload fills w's environment ID and slugs from its App's head row.
func nameWorkload(ctx context.Context, tx *store.Tx, w *agentv1.Workload) error {
	app, err := uuid.Parse(w.GetAppId())
	if err != nil {
		return fmt.Errorf("app %q: %w", w.GetAppId(), errUnknownApp)
	}
	var (
		appSlug, envSlug string
		env              uuid.UUID
	)
	err = tx.QueryRow(ctx, `SELECT a.slug, e.slug, e.id FROM apps_heads a
		JOIN environments_heads e ON e.id = a.environment_id
		WHERE a.id = $1 AND NOT a.deleted`, app).Scan(&appSlug, &envSlug, &env)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("app %s: %w", app, errUnknownApp)
	}
	if err != nil {
		return fmt.Errorf("look up app %s: %w", app, err)
	}
	if w.GetEnvironmentId() != "" && w.GetEnvironmentId() != env.String() {
		return fmt.Errorf("app %s: %w", app, errWrongEnv)
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
		if len(w.GetSecrets()) == 0 {
			continue
		}
		if s.Secrets == nil {
			return nil, 0, errNoResolver
		}
		handles := make([]string, len(w.GetSecrets()))
		for i, ref := range w.GetSecrets() {
			handles[i] = ref.GetHandle()
		}
		byHandle, err := s.Secrets.Resolve(ctx, tenant, handles)
		if err != nil {
			return nil, 0, fmt.Errorf("resolve secrets for app %s: %w", w.GetAppId(), err)
		}
		values := &agentv1.SecretValues{Values: map[string]string{}}
		for _, ref := range w.GetSecrets() {
			v, ok := byHandle[ref.GetHandle()]
			if !ok {
				return nil, 0, fmt.Errorf("secret %s for app %s did not resolve", ref.GetKey(), w.GetAppId())
			}
			values.Values[ref.GetKey()] = v
		}
		plain, err := proto.Marshal(values)
		if err != nil {
			return nil, 0, fmt.Errorf("encode secret values: %w", err)
		}
		if w.SealedSecrets, err = box.SealAnonymous(nil, plain, &recipient, rand.Reader); err != nil {
			return nil, 0, fmt.Errorf("seal secret values: %w", err)
		}
	}
	signed, err := proto.MarshalOptions{Deterministic: true}.Marshal(payload)
	if err != nil {
		return nil, 0, fmt.Errorf("encode assignment: %w", err)
	}
	sig, keyID := s.PKI.SignAssignment(signed)
	return &agentv1.Assignment{Payload: signed, Signature: sig, KeyId: keyID}, uint64(gen), nil
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
