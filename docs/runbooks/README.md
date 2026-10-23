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
      `TestRestoreDrill` in CI proves the code, and the weekly drill schedule plus
      `bohux admin drill` (Section D, ADR-0051) prove an install's own archives.
- [ ] Break-glass production access
- [ ] Stuck deployment — diagnosis via workflow history

## Written

- `backup-and-restore.md` — backup key custody, taking a backup, drilling an archive,
  restoring onto a fresh host
- `control-plane-upgrade.md` — upgrading the control plane with itself, and recovering
  from a bad upgrade
- `infra-upgrade.md` — upgrading Postgres, Temporal, and OpenBao
- `openbao-unseal-key.md` — unseal key custody, a vault that will not unseal, a lost key
