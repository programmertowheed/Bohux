# ADR-0029: Backup archive — logical Postgres copy, OpenBao versions, node CA, encrypted with age

- **Status:** proposed
- **Date:** 2026-10-21
- **Deciders:** Towheed

## Context
ADR-0020 item 7: `bohux admin backup` writes one archive with Bohux's own state —
Postgres, OpenBao's secret values, and the node CA — encrypted at rest with a key the
operator holds, and `bohux admin restore` brings it back on a fresh host; CI proves it
with backup → wipe → restore → rollback. What goes in the archive and how is open, and
some choices are hard to change once operators hold archives:

1. **Postgres.** `pg_dump` must be at least the server's major version, so the host
   (and CI) needs a matching client. Row-level security is forced on every tenant table
   (data-model.md), so the application role sees no rows outside a tenant; a dump needs
   a role that bypasses it.
2. **OpenBao.** Its native snapshot (`sys/storage/raft/snapshot`) exists only for raft
   storage; the storage backend is chosen with the install bundle (S9). Postgres
   records, per secret, the **KV version** that is current (S7a): a restore must keep
   those version numbers, or every secret resolves to the wrong value or none.
3. **Encryption.** The archive holds every secret value. It must be encrypted with a
   key the control plane never stores, and a lost or wrong key must fail loudly.

## Decision
1. **The archive is a tar stream encrypted with age** (`filippo.io/age` v1.2.1, X25519
   recipients). `bohux admin backup --recipient age1…` encrypts to the operator's
   public key; `restore --identity <file>` decrypts with the private key. Nothing on
   the host can read an archive. Inside: `manifest.json` (format version, Bohux
   version, migration version, time), `postgres/<table>.copy` per table, `openbao.json`,
   and `pki/` (the data directory's node CA and signing key).
2. **Postgres is copied logically in Go**, table by table with `COPY … TO STDOUT`
   (text format), over a **privileged DSN** (a superuser or `BYPASSRLS` role) given to
   the admin command only. Restore needs an empty database: it runs the migrations up
   to the manifest's version as the application role, then `COPY … FROM STDIN` every
   table with triggers and foreign-key checks off (`session_replication_role =
   replica`), as the privileged role. No `pg_dump`, no client-version coupling; the
   archive is tied to a migration version, and restore refuses a binary that does not
   have it.
3. **OpenBao values are exported per path, every version, in order**, for paths under
   `bohux/`; destroyed or deleted versions are kept as placeholders. Restore writes them
   back in order to an empty mount, so version *n* is again version *n*, then destroys
   the placeholders. This works for any storage backend.
4. **Archives are never written unencrypted**, not even to a temp file; values exist
   in memory only while streaming. The command never logs a value (rule 3).

## Alternatives considered
- **`pg_dump` / `pg_restore`.** The standard tool, but it couples to the client
  version on every host and CI runner, and a plain dump replays RLS-checked inserts.
  Lost on operational coupling; the logical copy is ~200 lines over a schema Bohux owns.
- **OpenBao raft snapshot.** Exact and fast, but only for raft storage and it restores
  the whole OpenBao, including its own auth and unseal state. Lost on coupling to a
  backend not chosen yet; revisit when the install bundle fixes raft.
- **AES-GCM with a symmetric key file (stdlib only).** No dependency, but streaming
  encryption needs its own chunk framing (a home-made format), and a symmetric key must
  sit where backups are made. Lost to age: a reviewed format, streaming built in, and
  the host needs only the public key.
- **GPG.** Widely installed, but a large surface, an external binary, and keyrings.
  Lost on operational weight.

## Consequences
- Easy: one file to keep, readable only by the operator's identity; restore on a fresh
  host is two commands; CI runs the drill with no extra tools.
- Hard: backup and restore need a privileged Postgres DSN in addition to the app's.
  Archives are tied to migration versions (restore with the same Bohux version, then
  upgrade). Losing the age identity loses every backup: the runbook (S9) covers key
  custody.
- A new dependency: `filippo.io/age` (and `filippo.io/edwards25519`).

## Revisit when
The install bundle settles on raft storage for OpenBao (switch to native snapshots), or
a backup takes longer than the nightly window on a real install.
