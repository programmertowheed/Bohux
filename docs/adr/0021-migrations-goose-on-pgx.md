# ADR-0021: Schema migrations with goose, embedded in the binary, on pgx

- **Status:** proposed (owner authorized building on it before acceptance, 2026-10-21)
- **Date:** 2026-10-21
- **Deciders:** Towheed (pending)

## Context
S2 creates the first Postgres schema (ADR-0004, ADR-0017). Every later slice adds
tables, so the migration mechanism is hard to change once real installs exist: their
recorded migration history is in its format. Constraints:

- CLAUDE.md: forward-only, one file per change, each reversible or explicitly marked
  irreversible with a reason.
- Bohux ships as one binary (ADR-0001) that a user runs on one VPS. Migrations must
  travel inside it, with no second tool to install.
- More than one control-plane process can start at once (an upgrade overlapping the old
  process, or HA later). Two migrators must not race.
- Some Postgres changes cannot run in a transaction (`CREATE INDEX CONCURRENTLY`), and
  the large append-only tables (`revisions`, `audit_events`) will need them.
- `bohux admin restore` (ADR-0020 item 7) needs to know exactly which migrations an
  archive was taken at.
- The module is pinned to Go 1.23 (S0), which limits dependency versions.

A Postgres driver is needed underneath whatever runs migrations, so it is decided here
too.

## Decision
Use **`github.com/pressly/goose/v3`** (v3.26.0, the last release supporting Go 1.23)
through its `Provider` API, on **`github.com/jackc/pgx/v5`** (v5.7.6) via its
`database/sql` adapter. Migrations are plain SQL files in `internal/store/migrations/`,
named `NNNNN_short_name.sql`, embedded with `embed.FS`, and applied by the binary (at
control-plane start, and by an explicit `bohux admin migrate`). The provider uses goose's
Postgres advisory-lock session locker, so concurrent starts serialize. Every file has a
`-- +goose Down` section: either the reversing SQL, or only a comment beginning
`-- irreversible:` that says why. Bohux never runs Down automatically; the section exists
so a reviewer can see what reversing would mean. Application queries use pgx directly
(`pgxpool`), not `database/sql`.

## Alternatives considered
- **golang-migrate.** The most common choice. Lost on two specifics: separate `.up.sql`
  and `.down.sql` files per change break "one file per change", and a failed migration
  leaves a global `dirty` flag that an operator must clear by hand with `force`, which
  is a poor failure mode for a single-VPS user with no DBA.
- **jackc/tern.** Postgres-only and from the pgx author, so the smallest dependency
  footprint. Lost because it runs every SQL file through Go `text/template` by default,
  so any `{{` in a function body or JSON literal is silently reinterpreted, and because
  its version table keeps only the current version, not a per-migration history with
  timestamps, which restore verification (ADR-0020) wants.
- **Atlas (declarative or versioned).** Strong diffing and linting. Lost because it is
  a separate binary or a large library, part of its feature set sits behind a paid
  plan or account login, and declarative mode
  hides the exact SQL that runs, against "one readable file per change".
- **Hand-rolled runner (~200 lines over embed.FS).** No dependency. Lost because the
  parts that matter, advisory locking, transaction-per-file with a no-transaction
  escape, gap and out-of-order detection, and a history table, are each small but are
  exactly where a homegrown runner gets subtle bugs, and they are already done and used
  widely in goose.
- **Driver: `lib/pq`.** In maintenance mode by its own README, which recommends pgx.
  Lost on that.

## Consequences
- One binary, no external migration tool. `goose` CLI is optional for contributors and
  reads the same files.
- goose's module declares many database drivers. Only the Postgres pieces are linked
  into the binary, but `go.sum` grows. Accepted.
- goose's history table (`goose_db_version`) becomes part of every install's state and
  of backups. Moving off goose later means translating that table.
- Two access paths exist: `database/sql` (goose only) and `pgxpool` (everything else).
  Both are opened from the same DSN.
- The Go 1.23 pin holds goose at v3.26 and pgx at v5.7. Raising the Go version is a
  separate, cheap change that unlocks newer releases.
- Down sections are review documentation, not an automated rollback path. A bad
  migration is fixed by a new forward migration (CLAUDE.md, ADR-0004).

## Revisit when
A migration in a real install fails in a way goose's locking or history cannot recover
without manual SQL, or goose stops publishing releases for the Go version the module
targets for more than six months.
