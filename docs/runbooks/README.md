# Runbooks

Operational procedures. Written for someone who did not write them, at 3am, under stress.

Use `.claude/skills/write-runbook/SKILL.md` to add one.

## Required before v1.0

Each of these maps to a failure mode in `docs/plan/01-architecture.md`.

- [ ] Control plane down — confirming apps are unaffected
- [ ] Postgres failover and read-only mode
- [ ] Temporal outage with in-flight deploys
- [ ] Agent upgrade and staged rollback
- [ ] Node drain and cordon
- [ ] Certificate rotation
- [x] Database restore (and the scheduled drill that exercises it) — `backup-and-restore.md`;
      the drill is `TestRestoreDrill` in CI
- [ ] Break-glass production access
- [ ] Stuck deployment — diagnosis via workflow history

## Written

- `backup-and-restore.md` — backup key custody, taking a backup, restoring onto a fresh host
- `control-plane-upgrade.md` — upgrading the control plane with itself, and recovering
  from a bad upgrade
- `openbao-unseal-key.md` — unseal key custody, a vault that will not unseal, a lost key
