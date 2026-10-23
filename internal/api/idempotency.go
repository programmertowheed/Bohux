package api

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"google.golang.org/protobuf/proto"

	"github.com/bohumix/bohux/internal/auth"
	"github.com/bohumix/bohux/internal/secrets"
	"github.com/bohumix/bohux/internal/store"
)

// idempotencyTTL is how long a key is remembered (ADR-0022).
const idempotencyTTL = 24 * time.Hour

// replay is what an earlier request with the same key produced.
type replay struct {
	ResourceID uuid.UUID
	RevisionID uuid.UUID
}

// idempotency checks and records idempotency keys inside the mutation's transaction
// (ADR-0022). Begin it before the mutation; if it returns a replay, answer from that
// and write nothing. Otherwise call record after the mutation, in the same tx.
type idempotency struct {
	rpc  string
	key  string
	hash []byte
	p    auth.Principal
}

func newIdempotency(ctx context.Context, rpc, key string, req proto.Message) (*idempotency, error) {
	if key == "" {
		return nil, invalidArgument("idempotency_key", "REQUIRED", "idempotency_key is required")
	}
	if len(key) > 128 {
		return nil, invalidArgument("idempotency_key", "TOO_LONG", "idempotency_key is longer than 128 bytes")
	}
	p, ok := auth.FromContext(ctx)
	if !ok {
		return nil, auth.ErrUnauthenticated
	}
	h, err := requestHash(req)
	if err != nil {
		return nil, err
	}
	return &idempotency{rpc: rpc, key: key, hash: h, p: p}, nil
}

// lookup returns the earlier outcome for this key, or nil if there is none.
func (i *idempotency) lookup(ctx context.Context, tx *store.Tx) (*replay, error) {
	var (
		rpc  string
		hash []byte
		r    replay
	)
	err := tx.QueryRow(ctx, `SELECT rpc, request_hash, resource_id, revision_id
		FROM idempotency_keys
		WHERE principal_id = $1 AND idempotency_key = $2 AND expires_at > now()`,
		i.p.ID, i.key).Scan(&rpc, &hash, &r.ResourceID, &r.RevisionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("look up idempotency key: %w", err)
	}
	if rpc != i.rpc || string(hash) != string(i.hash) {
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			errors.New("idempotency_key was already used for a different request"))
	}
	return &r, nil
}

// record stores the outcome. A concurrent request with the same key loses on the
// primary key and is told to retry, which then replays.
func (i *idempotency) record(ctx context.Context, tx *store.Tx, resourceType string, resourceID, revisionID uuid.UUID) error {
	err := tx.Exec(ctx, `DELETE FROM idempotency_keys
		WHERE principal_id = $1 AND idempotency_key = $2 AND expires_at <= now()`, i.p.ID, i.key)
	if err != nil {
		return fmt.Errorf("clear expired idempotency key: %w", err)
	}
	err = tx.Exec(ctx, `INSERT INTO idempotency_keys
		(tenant_id, principal_id, idempotency_key, rpc, request_hash, resource_type,
		 resource_id, revision_id, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, now() + $9::interval)`,
		tx.Tenant(), i.p.ID, i.key, i.rpc, i.hash, resourceType, resourceID, revisionID,
		fmt.Sprintf("%d seconds", int(idempotencyTTL.Seconds())))
	if errors.Is(err, store.ErrAlreadyExists) {
		return connect.NewError(connect.CodeAborted,
			errors.New("a request with this idempotency_key is in flight; retry"))
	}
	if err != nil {
		return fmt.Errorf("record idempotency key: %w", err)
	}
	return nil
}

// requestHash is SHA-256 of the request, deterministically serialized, with every
// debug_redact field cleared, so a secret value never feeds a stored hash (ADR-0022).
func requestHash(req proto.Message) ([]byte, error) {
	b, err := proto.MarshalOptions{Deterministic: true}.Marshal(secrets.Redacted(req))
	if err != nil {
		return nil, fmt.Errorf("hash request: %w", err)
	}
	h := sha256.Sum256(b)
	return h[:], nil
}
