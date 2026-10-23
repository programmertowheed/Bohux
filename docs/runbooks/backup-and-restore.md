# Backup and restore of Bohux's own state

**Severity:** sev1 (restore) · routine (backup, key custody)
**Expected duration:** backup 2 minutes; restore 20–30 minutes on a fresh host
**Blast radius:** a restore onto the wrong host or database overwrites nothing (restore
refuses any target that is not empty), but restoring an old archive loses every change
made since it was taken: deploys, secrets, tokens, audit events.
**Prerequisites:** root on the host; the age **identity** (private key) for restores;
the archive file; for restore, a host installed with `scripts/install.sh` whose
control plane has **not** been started on its own data (see step R3).

Archives hold every secret value and the node CA (ADR-0029). Treat the archive and the
identity like the root password of every App.

## Symptoms
- Routine: setting up backups on a new install, or rotating the backup key.
- Restore: the control plane's disk is gone, Postgres is corrupt, or the host is lost.
  Running Apps keep serving meanwhile (rule 1); deploys, rollbacks, logs and exec do not.

## Decision tree
- First time, or rotating the key → **Section K** (key custody).
- Checking the nightly backup → **Section N**.
- Taking a backup by hand → **Section B**.
- Checking the weekly drill, or proving an archive restores now → **Section D** (drill).
- Control plane state lost → **Section R** (restore).
- Archive will not decrypt → check you have the identity that matches the recipient the
  backup was made for (Section K, step K3). Without it the archive is unreadable: there
  is no recovery. Escalate.

## K — Key custody
1. On a machine that is **not** the Bohux host (a laptop or a vault), create the key:
   ```
   age-keygen -o bohux-backup.key
   ```
   Expected: `Public key: age1…` printed; `bohux-backup.key` written.
2. Store `bohux-backup.key` in two places outside the host (password manager, offline
   copy). Never copy it onto the Bohux host.
3. Record the public key (`age1…`, from step 1 or `age-keygen -y bohux-backup.key`). Only
   the public key goes on the host.

## N — The nightly backup
Installed with `BOHUX_BACKUP_RECIPIENT=age1…`, the control plane writes an archive
every night at 03:17 UTC and keeps the last seven (ADR-0033).
1. List the archives:
   ```
   sudo ls -l /var/lib/docker/volumes/bohux-backups/_data
   ```
   Expected: `bohux-YYYYMMDDTHHMMSSZ.backup` files, the newest from last night.
2. If last night's is missing, see why in Temporal:
   ```
   sudo docker exec bohux-infra-temporal-1 temporal --address temporal:7233 workflow list --query 'WorkflowType="BackupWorkflow"' --limit 3
   ```
   then `workflow show --workflow-id <id>` on the failed one. A `BadRecipient` failure
   means the configured key is not an age public key.
3. Take one now:
   ```
   sudo docker exec bohux-infra-temporal-1 temporal --address temporal:7233 schedule trigger --schedule-id bohux-backup-nightly
   ```
   Expected: a new archive in step 1's list within a minute.
4. Copy archives off the host: the volume is on the same disk as the data.

## D — Drill an archive (prove it restores)
A backup nobody has restored is a hope. A drill restores an archive into throwaway state
— a scratch database, a temporary data directory, a KV mount of its own — checks what
came back, and destroys all of it (ADR-0051). It never touches live state, and an App
keeps serving throughout.

**This section needs the identity on the host.** Prefer a key that exists only for
drills, so the operator's key from Section K stays off the host. Install with
`BOHUX_DRILL_IDENTITY=AGE-SECRET-KEY-1…` and its public half is added as a second
recipient on every archive from then on; archives written before that cannot be
drilled, and a drill of one fails to decrypt rather than passing quietly.

### The weekly schedule
Installed with a drill identity, the control plane drills the newest archive every
Sunday at 04:47 UTC and keeps as many verdicts as there are archives (ADR-0051).
1. Read the last verdicts, newest last:
   ```
   sudo ls -l /var/lib/docker/volumes/bohux-backups/_data/drills
   sudo cat "$(sudo ls -1 /var/lib/docker/volumes/bohux-backups/_data/drills/*.json | tail -1)"
   ```
   Expected: `"checks"` with every `"ok": true`. A failed check names itself; read it by
   name in step D3 below.
2. If last Sunday's verdict is missing, see why in Temporal:
   ```
   sudo docker exec bohux-infra-temporal-1 temporal --address temporal:7233 workflow list --query 'WorkflowType="DrillWorkflow"' --limit 3
   ```
   then `workflow show --workflow-id <id>` on the failed one. `NoArchive` means there
   was nothing to drill yet; `DrillFailed` means the archive did not pass, and the
   verdict file says which check.
3. Drill now rather than waiting for Sunday:
   ```
   sudo docker exec bohux-infra-temporal-1 temporal --address temporal:7233 schedule trigger --schedule-id bohux-restore-drill-weekly
   ```
   Expected: a new verdict in step 1's list within a few minutes.

### By hand, against an archive you choose
1. Check the roles and the mount exist (the installer creates them; an install upgraded
   from before ADR-0051 has neither):
   ```
   sudo bash -c 'source /etc/bohux/infra/.env
     PGPASSWORD=$POSTGRES_PASSWORD psql -h 127.0.0.1 -U postgres -At \
       -c "SELECT rolname, rolcreatedb, rolbypassrls FROM pg_roles WHERE rolname LIKE '"'"'bohux_drill%'"'"'"'
   ```
   Expected: `bohux_drill|t|f` and `bohux_drill_admin|f|t`. If either is missing, see
   step D4.
2. Drill the newest archive:
   ```
   sudo bash -c 'source /etc/bohux/infra/.env
     bao_ip=$(docker inspect -f "{{.NetworkSettings.Networks.bohux-infra.IPAddress}}" bohux-infra-openbao-1)
     newest=$(ls -1 /var/lib/docker/volumes/bohux-backups/_data/bohux-*.backup | tail -1)
     bohux admin drill --in "$newest" \
       --identity /etc/bohux/drill.key \
       --drill-database-url "postgres://bohux_drill:${BOHUX_DRILL_PASSWORD}@127.0.0.1:5432/postgres?sslmode=disable" \
       --drill-admin-database-url "postgres://bohux_drill_admin:${BOHUX_DRILL_ADMIN_PASSWORD}@127.0.0.1:5432/postgres?sslmode=disable" \
       --openbao-address "http://${bao_ip}:8200" --openbao-token-file /etc/bohux/openbao.token \
       --drill-kv-mount bohux-drill'
   ```
   Expected: `Restore drill PASSED`, then one `[ok  ]` line per check and a summary.
   The exit status is non-zero if any check failed.
3. Read a failure by its check name:
   - `restore` — the archive did not decrypt or did not load. Wrong identity, or a
     truncated archive. Try the previous archive; if that also fails, escalate.
   - `node-ca` — the archive carries no node CA, so restoring it would make every agent
     re-register. Usually a backup taken before the control plane first started. Take a
     fresh one (Section B) and drill that.
   - `owner-token` — nothing in the archive could administer the restored install.
     Create an owner token (`bohux token create`) and take a fresh backup.
   - `secrets` — Postgres records a secret version that is not in the archive. Almost
     always a backup taken with no OpenBao configured: check `--openbao-address` is set
     wherever backups run, then take a fresh one.
   - `tables`, `tenants` — the archive is incomplete. Escalate.
   - `cleanup` — the drill could not remove its own scratch database or mount. Drop the
     `bohux_drill_*` database by hand before the next drill; it is not live state.
4. If the roles or the mount are missing, create them once (values from
   `/etc/bohux/infra/.env`):
   ```
   sudo bash -c 'source /etc/bohux/infra/.env
     PGPASSWORD=$POSTGRES_PASSWORD psql -h 127.0.0.1 -U postgres \
       -c "CREATE ROLE bohux_drill LOGIN PASSWORD '"'"'$BOHUX_DRILL_PASSWORD'"'"' CREATEDB" \
       -c "CREATE ROLE bohux_drill_admin LOGIN PASSWORD '"'"'$BOHUX_DRILL_ADMIN_PASSWORD'"'"' BYPASSRLS" \
       -c "GRANT SET ON PARAMETER session_replication_role TO bohux_drill_admin"
     bao_ip=$(docker inspect -f "{{.NetworkSettings.Networks.bohux-infra.IPAddress}}" bohux-infra-openbao-1)
     curl -s -X POST -H "X-Vault-Token: $(cat /etc/bohux/openbao.token)" \
       -d "{\"type\":\"kv\",\"options\":{\"version\":\"2\"}}" \
       "http://${bao_ip}:8200/v1/sys/mounts/bohux-drill"'
   ```
   Neither role is granted anything in Bohux's own database, so neither can read or
   write live rows.

## B — Take a backup by hand (on the Bohux host)
1. Load the admin password:
   ```
   sudo bash -c 'source /etc/bohux/infra/.env; echo "postgres://postgres:${POSTGRES_PASSWORD}@127.0.0.1:5432/bohux_app?sslmode=disable"' > /tmp/admin.url
   ```
2. Load the OpenBao token into a file only root reads:
   ```
   sudo jq -r .root_token /etc/bohux/openbao-init.json | sudo tee /etc/bohux/openbao.token >/dev/null
   sudo chmod 600 /etc/bohux/openbao.token
   ```
3. Write the archive (replace `age1…` with the public key from K3):
   ```
   sudo bohux admin backup --admin-database-url "$(cat /tmp/admin.url)" \
     --data-dir /var/lib/docker/volumes/bohux-controlplane-data/_data \
     --openbao-address http://$(sudo docker inspect -f '{{.NetworkSettings.Networks.bohux-infra.IPAddress}}' bohux-infra-openbao-1):8200 \
     --openbao-token-file /etc/bohux/openbao.token \
     --recipient age1… --out /root/bohux-$(date +%F).backup
   ```
   Expected: `Backup written to /root/bohux-YYYY-MM-DD.backup (N tables, migration M).`
4. Remove the admin URL: `rm /tmp/admin.url`.
5. Copy the archive off the host (it is encrypted; any storage will do).

## R — Restore onto a fresh host
1. Install Bohux with the **same version** the archive was taken with (the manifest's
   Bohux version; restore refuses a binary that lacks the archive's migration):
   ```
   BOHUX_IMAGE=<repo>@sha256:<digest of that version> sudo -E scripts/install.sh
   ```
2. Stop the agent and the control plane App before restoring, so nothing writes:
   ```
   sudo systemctl stop bohux-agent
   sudo docker compose -f /var/lib/bohux-agent/workloads/bohux-controlplane-system-*/compose.yaml down
   ```
3. **Destructive — this deletes the fresh install's own state (tenant, token, node CA),
   which the archive replaces.** Empty the database, the CA volume, and the secrets mount:
   ```
   sudo bash -c 'source /etc/bohux/infra/.env; docker exec -i bohux-infra-postgres-1 psql -U postgres -c "DROP DATABASE bohux_app" -c "CREATE DATABASE bohux_app OWNER bohux_app"'
   sudo docker run --rm -v bohux-controlplane-data:/d busybox:1.36 sh -c 'rm -rf /d/pki'
   sudo docker exec -e BAO_TOKEN=$(sudo jq -r .root_token /etc/bohux/openbao-init.json) -e BAO_ADDR=http://127.0.0.1:8200 bohux-infra-openbao-1 bao kv metadata delete -mount=secret bohux
   ```
4. Copy the archive and the identity onto the host (the identity only for this step).
5. Restore (the passwords come from `/etc/bohux/infra/.env`, written by the installer):
   ```
   sudo bash -c 'source /etc/bohux/infra/.env
     bao_ip=$(docker inspect -f "{{.NetworkSettings.Networks.bohux-infra.IPAddress}}" bohux-infra-openbao-1)
     bohux admin restore \
       --admin-database-url "postgres://postgres:${POSTGRES_PASSWORD}@127.0.0.1:5432/bohux_app?sslmode=disable" \
       --database-url "postgres://bohux_app:${BOHUX_APP_PASSWORD}@127.0.0.1:5432/bohux_app?sslmode=disable" \
       --data-dir /var/lib/docker/volumes/bohux-controlplane-data/_data \
       --openbao-address "http://${bao_ip}:8200" --openbao-token-file /etc/bohux/openbao.token \
       --identity bohux-backup.key --in bohux-YYYY-MM-DD.backup'
   ```
   (`/etc/bohux/openbao.token` as in B2; `bohux-YYYY-MM-DD.backup` is the archive from
   step R4.) Expected: `Restored a backup from … (Bohux x.y.z, migration M).`
6. Delete the identity from the host: `shred -u bohux-backup.key`.
7. Start the control plane and the agent:
   ```
   sudo docker compose -f /var/lib/bohux-agent/workloads/bohux-controlplane-system-*/compose.yaml up -d
   sudo systemctl start bohux-agent
   ```

## Verification
- `curl -s http://127.0.0.1:7400/healthz` → `ok`.
- With the **restored** owner token (`BOHUX_TOKEN`), `bohux rollback <app> -p <project> -e <env>`
  succeeds: this is the drill CI runs (`TestRestoreDrill`, ADR-0020 item 7).
- Without doing a restore at all: `bohux admin drill` on the newest archive reports
  `PASSED` (Section D).
- Agents of the old host reconnect without re-registering (the node CA is the same).

## Rollback
A failed restore leaves a half-filled database: repeat step R3, then R5. The archive is
never modified by a restore.

## Escalation
Escalate to the maintainers if the archive does not decrypt with the right identity, or
if restore refuses the archive's migration with the same Bohux version.
