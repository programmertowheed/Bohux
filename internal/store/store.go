package store

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Errors returned by writes. The API layer maps them to Connect codes.
var (
	// ErrNotFound: the resource does not exist, is deleted, or belongs to another
	// tenant. The three are indistinguishable on purpose.
	ErrNotFound = errors.New("not found")
	// ErrConflict: expected_revision_id is not the current revision (ADR-0017).
	ErrConflict = errors.New("revision conflict")
	// ErrAlreadyExists: a unique key, such as a slug, is taken.
	ErrAlreadyExists = errors.New("already exists")
)

// Versioned resources, by table base name (<name>_heads, <name>_revisions). Writes are
// accepted only for these, so table names in generated SQL never come from input.
var versioned = map[string]bool{
	"tenants": true, "projects": true, "role_bindings": true, "api_tokens": true,
	"nodes": true, "environments": true, "secret_refs": true, "apps": true,
	"releases": true, "deployments": true,
}

var columnName = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// Store is the Postgres store. All access goes through tenant-scoped transactions.
type Store struct {
	pool *pgxpool.Pool
}

// Open connects to Postgres. It refuses roles that bypass row-level security, like
// Migrate.
func Open(ctx context.Context, dsn string) (*Store, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("open postgres pool: %w", err)
	}
	var super, bypass bool
	err = pool.QueryRow(ctx,
		`SELECT rolsuper, rolbypassrls FROM pg_roles WHERE rolname = current_user`,
	).Scan(&super, &bypass)
	if err != nil {
		pool.Close()
		return nil, fmt.Errorf("check database role: %w", err)
	}
	if super || bypass {
		pool.Close()
		return nil, ErrPrivilegedRole
	}
	return &Store{pool: pool}, nil
}

// Close releases every connection.
func (s *Store) Close() { s.pool.Close() }

// InTenant runs fn in one transaction scoped to tenant: row-level security shows and
// accepts only that tenant's rows. The transaction commits if fn returns nil and rolls
// back otherwise, so a write and its audit event land together or not at all.
func (s *Store) InTenant(ctx context.Context, tenant uuid.UUID, fn func(*Tx) error) error {
	if tenant == uuid.Nil {
		return errors.New("store: tenant is required")
	}
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx,
			`SELECT set_config('bohux.tenant_id', $1, true)`, tenant.String()); err != nil {
			return fmt.Errorf("set tenant: %w", err)
		}
		return fn(&Tx{tx: tx, tenant: tenant})
	})
	if err != nil {
		return fmt.Errorf("tenant transaction: %w", err)
	}
	return nil
}

// Tx is a tenant-scoped transaction.
type Tx struct {
	tx     pgx.Tx
	tenant uuid.UUID
}

// Query runs a read in the transaction.
func (t *Tx) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	return t.tx.Query(ctx, sql, args...)
}

// QueryRow runs a single-row read in the transaction.
func (t *Tx) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	return t.tx.QueryRow(ctx, sql, args...)
}

// ActorKind matches audit_events.actor_kind.
type ActorKind string

const (
	ActorUser     ActorKind = "user"
	ActorAPIToken ActorKind = "api_token"
	ActorNode     ActorKind = "node"
	ActorSystem   ActorKind = "system"
)

// Actor is who makes a change. It comes from authentication, never a request
// (rule 10).
type Actor struct {
	ID   uuid.UUID
	Kind ActorKind
}

// Col is one column and its value. Names are code constants, never input.
type Col struct {
	Name  string
	Value any
}

// Audit describes the audit event a write records.
type Audit struct {
	Actor Actor
	// The RPC or operation, e.g. "bohux.v1.ProjectService/CreateProject".
	Action string
	// Fully qualified proto message name, e.g. "bohux.v1.Project".
	ResourceType string
	// Project the resource belongs to, if any.
	ProjectID uuid.UUID
}

// Create inserts a new resource: its head, its first revision, and the audit event.
// Head holds the identity columns, Revision the mutable ones (data-model.md, "Schema
// rules"). It returns the new resource ID and revision ID.
func (t *Tx) Create(ctx context.Context, resource string, head, revision []Col, a Audit) (id, rev uuid.UUID, err error) {
	if resource == "tenants" {
		id = t.tenant
	} else if id, err = uuid.NewV7(); err != nil {
		return uuid.Nil, uuid.Nil, fmt.Errorf("new id: %w", err)
	}
	rev, err = t.CreateWithID(ctx, resource, id, head, revision, a)
	return id, rev, err
}

// CreateWithID is Create with a caller-chosen ID, for resources whose ID must be known
// before the row exists (a token embeds its own ID, ADR-0022). Use NewID for it.
func (t *Tx) CreateWithID(ctx context.Context, resource string, id uuid.UUID, head, revision []Col, a Audit) (uuid.UUID, error) {
	if err := checkWrite(resource, a, head, revision); err != nil {
		return uuid.Nil, err
	}
	if id == uuid.Nil {
		return uuid.Nil, errors.New("store: id is required")
	}
	rev, err := uuid.NewV7()
	if err != nil {
		return uuid.Nil, fmt.Errorf("new revision id: %w", err)
	}

	headCols := append([]Col{
		{"id", id}, {"tenant_id", t.tenant}, {"revision_id", rev}, {"created_by", a.Actor.ID},
	}, head...)
	if err := t.insert(ctx, resource+"_heads", headCols); err != nil {
		return uuid.Nil, fmt.Errorf("create %s: %w", resource, err)
	}
	revCols := append([]Col{
		{"id", rev}, {"tenant_id", t.tenant}, {"resource_id", id}, {"actor_id", a.Actor.ID},
	}, revision...)
	if err := t.insert(ctx, resource+"_revisions", revCols); err != nil {
		return uuid.Nil, fmt.Errorf("create %s revision: %w", resource, err)
	}
	if err := t.audit(ctx, a, id, rev); err != nil {
		return uuid.Nil, err
	}
	return rev, nil
}

// NewID returns a server-generated UUIDv7 (api-contract.md, convention 3).
func NewID() (uuid.UUID, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return uuid.Nil, fmt.Errorf("new id: %w", err)
	}
	return id, nil
}

// Exec runs a statement in the transaction. Writes to versioned resources go through
// Create, Update, and Delete; Exec is for non-versioned rows such as observations.
func (t *Tx) Exec(ctx context.Context, sql string, args ...any) error {
	if _, err := t.tx.Exec(ctx, sql, args...); err != nil {
		return mapError(err)
	}
	return nil
}

// Tenant is the tenant this transaction is scoped to.
func (t *Tx) Tenant() uuid.UUID { return t.tenant }

// Update writes a new revision with the full mutable state, moves the head to it with
// a compare-and-swap on expected, and records the audit event (ADR-0017). A stale
// expected returns ErrConflict.
func (t *Tx) Update(ctx context.Context, resource string, id, expected uuid.UUID, revision []Col, a Audit) (uuid.UUID, error) {
	if err := checkWrite(resource, a, nil, revision); err != nil {
		return uuid.Nil, err
	}
	rev, err := t.moveHead(ctx, resource, id, expected, false)
	if err != nil {
		return uuid.Nil, err
	}
	revCols := append([]Col{
		{"id", rev}, {"tenant_id", t.tenant}, {"resource_id", id},
		{"parent_revision_id", expected}, {"actor_id", a.Actor.ID},
	}, revision...)
	if err := t.insert(ctx, resource+"_revisions", revCols); err != nil {
		return uuid.Nil, fmt.Errorf("update %s revision: %w", resource, err)
	}
	if err := t.audit(ctx, a, id, rev); err != nil {
		return uuid.Nil, err
	}
	return rev, nil
}

// Delete writes a deletion revision (a copy of the current snapshot with deleted set),
// marks the head deleted with a compare-and-swap on expected, and records the audit
// event. Rows are never removed (ADR-0004).
func (t *Tx) Delete(ctx context.Context, resource string, id, expected uuid.UUID, a Audit) (uuid.UUID, error) {
	if err := checkWrite(resource, a, nil, nil); err != nil {
		return uuid.Nil, err
	}
	rev, err := t.moveHead(ctx, resource, id, expected, true)
	if err != nil {
		return uuid.Nil, err
	}
	table := resource + "_revisions"
	// Copy every column of the previous revision, overriding the revision fields, so
	// the deletion is a full snapshot like any other revision.
	_, err = t.tx.Exec(ctx, `INSERT INTO `+table+`
		SELECT (jsonb_populate_record(NULL::`+table+`, to_jsonb(prev) || jsonb_build_object(
			'id', $1::uuid, 'parent_revision_id', prev.id, 'actor_id', $2::uuid,
			'created_at', now(), 'deleted', true))).*
		FROM `+table+` prev WHERE prev.id = $3`, rev, a.Actor.ID, expected)
	if err != nil {
		return uuid.Nil, fmt.Errorf("delete %s revision: %w", resource, mapError(err))
	}
	if err := t.audit(ctx, a, id, rev); err != nil {
		return uuid.Nil, err
	}
	return rev, nil
}

// moveHead points the head at a new revision ID if its current revision is expected.
// The revision row itself is inserted afterwards; the foreign key is deferred.
func (t *Tx) moveHead(ctx context.Context, resource string, id, expected uuid.UUID, deleted bool) (uuid.UUID, error) {
	rev, err := uuid.NewV7()
	if err != nil {
		return uuid.Nil, fmt.Errorf("new revision id: %w", err)
	}
	tag, err := t.tx.Exec(ctx, `UPDATE `+resource+`_heads
		SET revision_id = $1, deleted = $2
		WHERE id = $3 AND revision_id = $4 AND NOT deleted`, rev, deleted, id, expected)
	if err != nil {
		return uuid.Nil, fmt.Errorf("move %s head: %w", resource, mapError(err))
	}
	if tag.RowsAffected() == 1 {
		return rev, nil
	}
	var exists bool
	err = t.tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM `+resource+`_heads
		WHERE id = $1 AND NOT deleted)`, id).Scan(&exists)
	if err != nil {
		return uuid.Nil, fmt.Errorf("check %s head: %w", resource, err)
	}
	if exists {
		return uuid.Nil, fmt.Errorf("%s %s: %w", resource, id, ErrConflict)
	}
	return uuid.Nil, fmt.Errorf("%s %s: %w", resource, id, ErrNotFound)
}

func (t *Tx) audit(ctx context.Context, a Audit, resourceID, rev uuid.UUID) error {
	id, err := uuid.NewV7()
	if err != nil {
		return fmt.Errorf("new audit event id: %w", err)
	}
	var project *uuid.UUID
	if a.ProjectID != uuid.Nil {
		project = &a.ProjectID
	}
	_, err = t.tx.Exec(ctx, `INSERT INTO audit_events
		(id, tenant_id, actor_id, actor_kind, action, resource_type, resource_id,
		 revision_id, project_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		id, t.tenant, a.Actor.ID, string(a.Actor.Kind), a.Action, a.ResourceType,
		resourceID, rev, project)
	if err != nil {
		return fmt.Errorf("write audit event: %w", mapError(err))
	}
	return nil
}

func (t *Tx) insert(ctx context.Context, table string, cols []Col) error {
	names := make([]string, len(cols))
	marks := make([]string, len(cols))
	args := make([]any, len(cols))
	for i, c := range cols {
		names[i] = c.Name
		marks[i] = fmt.Sprintf("$%d", i+1)
		args[i] = c.Value
	}
	_, err := t.tx.Exec(ctx, `INSERT INTO `+table+` (`+strings.Join(names, ", ")+
		`) VALUES (`+strings.Join(marks, ", ")+`)`, args...)
	return mapError(err)
}

func checkWrite(resource string, a Audit, cols ...[]Col) error {
	if !versioned[resource] {
		return fmt.Errorf("store: %q is not a versioned resource", resource)
	}
	if a.Actor.ID == uuid.Nil || a.Actor.Kind == "" || a.Action == "" || a.ResourceType == "" {
		return errors.New("store: audit actor, action, and resource type are required")
	}
	for _, cs := range cols {
		for _, c := range cs {
			if !columnName.MatchString(c.Name) {
				return fmt.Errorf("store: invalid column name %q", c.Name)
			}
		}
	}
	return nil
}

// mapError turns unique violations into ErrAlreadyExists. Constraint names are kept;
// values are not, since error text must never carry secret values (rule 3).
func mapError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return fmt.Errorf("%s: %w", pgErr.ConstraintName, ErrAlreadyExists)
	}
	return err
}

// Ping checks that Postgres answers.
func (s *Store) Ping(ctx context.Context) error {
	if err := s.pool.Ping(ctx); err != nil {
		return fmt.Errorf("ping postgres: %w", err)
	}
	return nil
}
