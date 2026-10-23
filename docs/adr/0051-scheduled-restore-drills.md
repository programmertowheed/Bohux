# ADR-0051: Scheduled restore drills — a weekly Temporal schedule restoring the newest archive into throwaway state

- **Status:** proposed
- **Date:** 2026-10-23
- **Deciders:** Towheed

## Context
The vision promises "backups are proven by scheduled restore drills" (`00-vision.md`,
positioning), and ADR-0020 item 7 makes that promise part of the operational baseline.
Today it is half kept. ADR-0033 writes a nightly archive and ADR-0029 fixed its format,
but the only thing that ever *restores* one is `TestRestoreDrill` in CI: it builds a
synthetic two-Release stack, backs it up, and restores it. A green CI run says the
restore **code** works. It says nothing about whether *this install's* archives restore —
the ones with this install's migration version, row volumes, secret version history and
node CA.

The failures a drill exists to catch are exactly the ones CI cannot see: an archive
encrypted to a recipient nobody holds the key for, a migration recorded in the manifest
that this binary can no longer reach, table data that no longer satisfies a constraint
added by a later migration, a secret whose version numbers cannot be rebuilt, a
`pki/` directory that was empty when the backup ran.

Restoring is destructive by construction: `backup.Restore` refuses a database with
tables, an OpenBao path that exists, or a `pki/` that holds a CA. So a drill on a live
install cannot restore into anything the install is using, and four things are hard to
undo once installs exist:

1. **What runs it**, given rule 6 forbids a cron poller.
2. **Which credentials create and own the throwaway database**, given ADR-0021 makes
   `store.MigrateTo` refuse a superuser or a `BYPASSRLS` role, while `COPY` into the
   restored tables needs `BYPASSRLS` because every table is `FORCE ROW LEVEL SECURITY`
   (the schema owner cannot `COPY` into one: Postgres answers "COPY FROM not supported
   with row-level security"). Production solves this with two roles; a drill needs the
   same split, and ADR-0033 refused to give the control plane a superuser DSN.
3. **Where the decrypting key lives.** The backup needs only a public key. A drill needs
   a private one, and the backup runbook currently tells the operator to `shred` the
   identity off the host after a restore.
4. **Where the verdict goes**, given ADR-0033 item 6 gave backups no API surface and no
   tenant scope.

## Decision
1. **A second Temporal schedule, `bohux-restore-drill-weekly`**, on the backup's task
   queue and served by the same worker in the control plane (ADR-0033 item 1). Default
   cron `47 4 * * 0` — Sunday, after the nightly archive has landed. Overlaps are
   skipped and the catch-up window is one hour, as for backups: a drill missed while the
   host was down is not worth running late.
2. **Opt-in, through a drill identity file.** `BOHUX_DRILL_IDENTITY_FILE` holds an age
   identity (0600). Drills are off unless it and a backup recipient are both configured.
   Its **public key is added as a second recipient on every archive**, so the operator's
   own key never has to be on the host: dropping the drill recipient turns drills off
   without touching the key the install's survival depends on.
3. **Throwaway state, created per run and destroyed after it, pass or fail:**
   - **A fresh database** `bohux_drill_<UTC time>`, created and owned by a dedicated
     `bohux_drill` role (`LOGIN CREATEDB`, no `BYPASSRLS`, so `MigrateTo` accepts it),
     which also runs the migrations. It is dropped at the end of the run.
   - **A second role, `bohux_drill_admin`** (`LOGIN BYPASSRLS`, plus a cluster-wide
     `GRANT SET ON PARAMETER session_replication_role`, which restore needs to load rows
     past triggers and foreign keys), is the restore target's admin DSN. After migrating,
     the drill grants it privileges on the scratch schema. **Neither role holds a single
     grant in the live database**, so neither can read or write live rows.
   - **A temporary data directory** for the archive's `pki/`, removed after the run.
   - **A dedicated OpenBao KV v2 mount** (`BOHUX_DRILL_KV_MOUNT`), refused if it equals
     the live mount, wiped before and after each run. A separate mount rather than a
     path prefix because restore writes the archive's own paths, which would collide
     with live secrets; a mount of its own keeps the real `importSecrets` path — version
     rebuilding and all — exercised against nothing that matters.
4. **The drill checks more than restore does.** Restore proves the archive loads; the
   drill then proves what loaded is a usable install: the manifest's format and
   migration, every table the manifest names present, a row count per table, at least
   one tenant and at least one owner token (an install nobody can log into is not
   restored), the node CA files present and parseable, and every secret path in the
   archive readable back at the version number Postgres records for it.
5. **The verdict is a `Verdict` returned by the workflow**, logged (names, counts and
   sizes only — rule 3), and written as JSON to `<backup dir>/drills/<UTC time>.json`
   under the same retention count as archives. A drill that fails a check **fails the
   workflow**, so Temporal holds the history and the failure is visible where backup
   failures already are.
6. **No API surface and no tenant scope**, as ADR-0033 item 6 decided for backups: a
   drill is instance-level. `bohux admin drill` runs one by hand against a given archive
   and prints the verdict, so the runbook has a one-command proof and the schedule is
   not the only way to get one.
7. **Config, following ADR-0033 item 5:** `BOHUX_DRILL_IDENTITY_FILE` (the age identity;
   empty disables drills), `BOHUX_DRILL_DATABASE_URL` and
   `BOHUX_DRILL_ADMIN_DATABASE_URL` (the two roles of item 3),
   `BOHUX_DRILL_KV_MOUNT` (the drill's own mount; required once OpenBao is configured,
   and refused if it is the live mount), and `BOHUX_DRILL_SCHEDULE` (cron, UTC, default
   `47 4 * * 0`).

## Alternatives considered
- **Leave the drill in CI (the status quo).** No new code, no new credentials, no
  private key on the host. Lost because it drills a stack the test built: a two-Release
  synthetic install, one migration behind nothing, no real secret history, no real row
  volume. It proves the restore code, which is worth having, and says nothing about the
  archives an install is actually producing — which is the whole claim in the vision.
- **A systemd timer or host cron running `bohux admin restore` into a scratch
  database.** No scheduler code at all. Lost on rule 6, and for the reasons ADR-0033
  rejected the same shape for backups: the identity and a privileged DSN end up in a
  host script, and retries, overlap and failure history are hand-written.
- **A throwaway Postgres container per run.** The most faithful target — a fresh
  cluster, a superuser available, no new roles, no grants to arrange. Lost on two
  counts: the memory budget ADR-0020 gates on a 2 GB VPS, and the provider boundary —
  containers are the data plane's business (ADR-0007), and a control plane that starts
  its own would be the first code to cross that line.
- **Verify the archive without restoring it**: decrypt, walk the tar, check the manifest
  and that every table entry parses. Cheap, needs no roles, no scratch database and no
  second mount. Lost because it proves only that the file is readable. It cannot catch a
  manifest migration this binary can no longer reach, table data that violates a
  constraint a later migration added, or secret versions that will not rebuild — the
  three failures that would actually ruin a restore.
- **Give the existing `bohux_backup` role `CREATEDB` and `CREATEROLE`.** One credential
  instead of two new ones. Lost on blast radius: the control plane would then hold a
  role that reads every tenant's rows *and* creates databases and roles. ADR-0033 made
  that role `SELECT`-only on purpose, and the two drill roles improve on it — they have
  no grants in the live database whatsoever.
- **Keep the operator's backup key on the host and drill with that.** One key to manage.
  Lost because the key an install's survival depends on would live on the host it is
  meant to survive, where it can be neither rotated nor revoked without re-encrypting
  the archive history. A separate drill recipient is removable.
- **Drill into the live OpenBao mount.** Nothing new to configure. Lost outright:
  restore requires empty secret paths, so it would either refuse or, worse, write the
  archive's secret versions over the live ones.
- **Make the drill the last step of the nightly backup workflow.** Fewer moving parts
  and no second schedule. Lost twice: a failed drill would fail the backup workflow,
  which inverts the priority between them, and a nightly full restore is the heaviest
  thing Bohux would do to a 2 GB VPS. The cadences belong apart.

## Consequences
- Easy: a backup that cannot be restored is found within a week by the install itself,
  with a verdict naming the check that failed, instead of during the incident that needed
  it. `bohux admin drill` makes the runbook's "prove it" step one command.
- Hard: the install grows two Postgres roles, a cluster-wide `GRANT SET ON PARAMETER
  session_replication_role`, and a second KV mount. The installer creates them; an
  install upgraded from an earlier version has none of them, so its drills stay off
  until an operator adds them. That is a documented upgrade step, not a migration.
- **Enabling drills puts an age private key on the host.** Anyone with root there can
  decrypt every archive that lists the drill recipient. The off-host copy encrypted to
  the operator's key remains the disaster-recovery copy, and the drill key can be
  rotated or dropped without touching it. An install that cannot accept this leaves
  drills off and keeps the CI drill plus a hand-run `bohux admin drill`.
- Archives written before drills were enabled do not list the drill recipient, so the
  first drill fails until the next nightly archive exists. The verdict says exactly that
  rather than reading like a corrupt archive.
- A drill costs a full restore: disk for a second copy of the database for the duration,
  plus the CPU and IO to load it. Weekly, off-hours, overlaps skipped.
- The verdict's row counts describe the archive, not live state. A drill cannot detect
  that a backup *missed* rows written after its snapshot began; that is ADR-0029's
  snapshot semantics, and a drill is not the place to relitigate it.
- `Activities.Recipient` becomes a list of recipients, which changes the backup
  activity's input shape. Runs in flight across the upgrade are nightly and short, so
  the worker is versioned rather than drained.

## Revisit when
A drill's restore outlasts its activity timeout on a real install, or an install's
database grows large enough that a weekly second copy breaks the disk budget (then the
drill restores a sample of tables rather than all of them), or at least three users
report that they cannot put any age private key on the host (then the drill moves
off-host and the schedule's job becomes reminding them to run one).
