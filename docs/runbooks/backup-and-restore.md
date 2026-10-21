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
- Taking a backup → **Section B**.
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

## B — Take a backup (on the Bohux host)
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
- Agents of the old host reconnect without re-registering (the node CA is the same).

## Rollback
A failed restore leaves a half-filled database: repeat step R3, then R5. The archive is
never modified by a restore.

## Escalation
Escalate to the maintainers if the archive does not decrypt with the right identity, or
if restore refuses the archive's migration with the same Bohux version.
