# ADR-0033: Nightly backup — a Temporal schedule in the control plane, a read-only BYPASSRLS role, a local directory

- **Status:** proposed
- **Date:** 2026-10-21
- **Deciders:** Towheed

## Context
ADR-0020 item 7 promises a scheduled nightly backup to a user-configured path or
S3-compatible bucket; ADR-0029 fixed the archive. `bohux admin backup` exists but runs
only when someone types it. Rule 6 forbids a cron poller; rule 3 keeps values out of
logs. Three choices are hard to undo once installs exist:

1. **What runs it.** A host cron job, a systemd timer, or a Temporal schedule.
2. **Which Postgres role.** ADR-0029 needs a role that bypasses row-level security.
   The control plane's own role must not (ADR-0021: `store.Migrate` refuses
   superusers), and putting a superuser DSN into the control plane would hand every
   tenant's rows to any bug in it.
3. **Where archives go, and how many stay.**

## Decision
1. **A Temporal schedule, `bohux-backup-nightly`, owned by the control plane.** On
   start, when a backup recipient is configured, the control plane creates the schedule
   or updates its spec to match its config; with no recipient it deletes the schedule.
   Overlaps are skipped; a run missed while Temporal or the control plane was down
   catches up within one hour, else waits for the next night. The schedule starts
   `BackupWorkflow` on task queue `bohux-backup`, served by a worker in the control
   plane next to the deploy worker (ADR-0026 item 1).
2. **`BackupWorkflow` is two activities.** `WriteBackup` streams the archive
   (`backup.Write`, ADR-0029) to `<name>.partial` (created exclusively, mode 0600),
   syncs it and renames it to `bohux-<UTC time>.backup`; any failure removes the
   partial file. It heartbeats; three attempts. `PruneBackups` then deletes the oldest
   finished archives beyond the retention count (default 7), touching only files that
   match the archive name pattern. A failed write never prunes.
3. **A dedicated role, `bohux_backup`: `LOGIN BYPASSRLS`, `SELECT` only**, on every
   table in `public` now and later (default privileges granted for tables `bohux_app`
   creates). The archive is read in a read-only transaction, so `SELECT` suffices. The
   install bundle creates it. The control plane gets its DSN as
   `BOHUX_BACKUP_DATABASE_URL`; it never holds a role that can write past RLS.
4. **Target: a local directory** (`BOHUX_BACKUP_DIR`, default `/var/lib/bohux-backups`),
   on its own named volume in the install (`bohux-backups`), so a host directory copy
   or the operator's own sync tool takes it off the host. S3 waits for a user who
   needs it (ADR-0020 item 7 stays open on that half).
5. **Config, all control plane flags with env vars:** `BOHUX_BACKUP_RECIPIENT` (age
   public key; empty disables the schedule), `BOHUX_BACKUP_DATABASE_URL`,
   `BOHUX_BACKUP_DIR`, `BOHUX_BACKUP_SCHEDULE` (cron, UTC, default `17 3 * * *`),
   `BOHUX_BACKUP_KEEP` (default 7).
6. **No API surface, no tenant scope.** A backup is instance-level state, not a tenant
   resource; nothing is created through the API, so there is no RBAC check to add.
   Each run is recorded in Temporal history and logged (`backup written`, file name,
   size, migration; never values). An instance-level audit trail is not part of v0.1.

## Alternatives considered
- **systemd timer running `bohux admin backup`.** No new code, but it needs the
  superuser DSN and the OpenBao root token in a host file, retries and failure history
  are hand-written, and it is a second scheduler beside Temporal. Lost on rule 6's
  spirit and on credentials.
- **A goroutine with a ticker in the control plane.** Lost on rule 6, and on missed
  runs across restarts.
- **Reuse the installer's superuser DSN.** Simplest, but the control plane would hold a
  credential that writes past RLS in every tenant. Lost on blast radius.
- **Back up to the control plane's data volume.** One volume fewer, but a lost volume
  takes the backups with the data. Lost.

## Consequences
- Easy: backups run without anyone typing, survive restarts, and show their failures
  in Temporal; the role that reads them cannot write anything.
- Hard: a new migration that adds a table relies on the default privileges, which only
  cover tables created by `bohux_app` (the only role that runs migrations).
- Archives pile up on the host unless retention prunes them; seven nights of archives
  is about seven times the database plus the secrets.
- The control plane's configuration grows by five settings; the installer sets them.

## Revisit when
A user needs S3 or another off-host target, or a backup outlasts its one-hour attempt
on a real install.
