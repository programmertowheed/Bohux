# OpenBao unseal key: custody, a vault that will not unseal, and a lost key

**Severity:** routine (custody) · sev2 (OpenBao down) · sev1 (key lost)
**Expected duration:** custody 5 minutes; OpenBao down 5–10 minutes; key lost: a full
restore (`backup-and-restore.md`, 20–30 minutes)
**Blast radius:** while OpenBao is down, the control plane cannot read or write secret
values: deploys of Apps with secrets, setting secrets, and backups fail. Running Apps
keep serving; the agent restarts them from its sealed cache (rule 1). A lost unseal key
makes every stored secret value unreadable on this host, for good.
**Prerequisites:** root on the host; for custody, the backup public key (`age1…`,
`backup-and-restore.md` K3) and `age` on the host.

OpenBao uses the static seal (ADR-0020 item 6, ADR-0030): the key in
`/etc/bohux/openbao-unseal.key` unseals it after every restart. `/etc/bohux/openbao-init.json`
holds the root token (the control plane's `BOHUX_OPENBAO_TOKEN`) and one recovery key.
Both files exist only on the host until you copy them off (Section K).

## Symptoms
- `bohux deploy` ends `failed` with a secret resolution error, or `SetSecret` returns
  `unavailable`.
- `docker ps --filter name=bohux-infra-openbao` shows the container restarting or absent.
- A new install: nothing is wrong yet, the key has not been copied off the host.

## Decision tree
- New install, or the key has never been copied off the host → **Section K**.
- OpenBao is restarting or reports sealed, and `/etc/bohux/openbao-unseal.key` exists →
  **Section S**.
- `/etc/bohux/openbao-unseal.key` is gone or changed and no copy from Section K exists →
  **Section L**.
- The key file is gone but you have the copy from Section K → **Section S**, step S3.

## K — Copy the key and the init file off the host
1. Encrypt both to the backup public key (from `backup-and-restore.md` K3):
   ```
   sudo tar -C /etc/bohux -c openbao-unseal.key openbao-init.json | age -r age1… > bohux-openbao-keys.age
   ```
   Expected: no output; `bohux-openbao-keys.age` written.
2. Copy `bohux-openbao-keys.age` off the host, next to the backup archives.
3. Delete the local copy: `rm bohux-openbao-keys.age`.
4. Repeat after any reinstall: a fresh install generates a new key.

## S — OpenBao does not unseal
1. Look at its log:
   ```
   sudo docker logs --tail 20 bohux-infra-openbao-1
   ```
   `permission denied` on `unseal.key` → step S2. `no such file` → step S3. Anything else
   → **Escalation**.
2. Give the key back to the container's `openbao` user, read-only:
   ```
   sudo chown "$(sudo docker run --rm --entrypoint id openbao/openbao:2.4.1 -u openbao)" /etc/bohux/openbao-unseal.key
   sudo chmod 400 /etc/bohux/openbao-unseal.key
   ```
   Go to step S4.
3. Put the key back from Section K's copy (needs the backup identity; copy it onto the
   host only for this step):
   ```
   age -d -i bohux-backup.key bohux-openbao-keys.age | sudo tar -C /etc/bohux -x openbao-unseal.key
   shred -u bohux-backup.key
   ```
   Then step S2.
4. Restart OpenBao:
   ```
   sudo docker compose -p bohux-infra -f /etc/bohux/infra/compose.yaml --env-file /etc/bohux/infra/.env restart openbao
   ```
5. Check it is unsealed:
   ```
   sudo docker exec -e BAO_ADDR=http://127.0.0.1:8200 bohux-infra-openbao-1 bao status -format=json | jq .sealed
   ```
   Expected: `false`. Then **Verification**.

## L — The key is lost
Without the key the vault's data cannot be decrypted. The values live on only in the
latest backup archive (ADR-0029 stores every OpenBao version) and, for running Apps, in
each agent's sealed cache, which cannot be exported.
1. Take note: **every secret value stored after the latest backup is lost.** Collect the
   list now so their owners can set them again (the refs live in Postgres and still
   list; the CLI has no secret command yet, so this calls the API):
   ```
   export BOHUX_TOKEN=$(sudo cat /etc/bohux/owner.token)
   curl -s -H "Authorization: Bearer $BOHUX_TOKEN" -H 'Content-Type: application/json' \
     -d '{"environmentId": "<environment id>", "pageSize": 100}' \
     http://127.0.0.1:7400/bohux.v1.SecretService/ListSecretRefs | jq -r '.secrets[].key' > lost-secrets.txt
   ```
   for each environment (`EnvironmentService/ListEnvironments` lists their IDs).
2. Restore the latest backup onto a fresh install, following `backup-and-restore.md`
   Section R. The fresh install generates a new unseal key.
3. Run Section K for the new key.
4. Have the owners of the secrets from step 1 set them again
   (`SecretService/SetSecret`), then redeploy their Apps.

## Verification
- `bohux deploy` of an App with a secret ends `succeeded`.
- `sudo docker exec -e BAO_ADDR=http://127.0.0.1:8200 bohux-infra-openbao-1 bao status`
  shows `Sealed false`.

## Rollback
Section K and S change no data: a wrong `chown` is fixed by repeating S2. Section L is a
restore; its rollback is in `backup-and-restore.md`.

## Escalation
Escalate to the maintainers when OpenBao stays sealed with the right key and owner, or
logs a storage error (`/openbao/file` corruption): do not delete the `bohux-infra_openbao`
volume, it is the only copy of the values written since the last backup.
