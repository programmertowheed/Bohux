# Upgrade the infra: Postgres, Temporal, OpenBao

**Severity:** routine
**Expected duration:** 5–10 minutes per service
**Blast radius:** while a service restarts, the control plane cannot serve what needs
it: Postgres down → the API answers `503` on `/healthz` and nothing works; Temporal down
→ deploys wait (in-flight ones resume afterwards); OpenBao down → secrets cannot be
read or set. Running Apps keep serving throughout (rule 1).
**Prerequisites:** root on the host; a backup taken in the last hour
(`backup-and-restore.md` Section B or N, step N3); the new image tags, from the Bohux
release notes or the service's own release page.

The infra is one Compose project, `bohux-infra`, defined in
`/etc/bohux/infra/compose.yaml` (ADR-0030). `scripts/install.sh` copies that file from
its bundle on every run, so an edit by hand is overwritten by the next install run:
upgrade through the bundle (Section A) when a Bohux release ships new tags, by hand
(Section H) otherwise, and put the same tag into the bundle before the next install run.

## Symptoms
- A Bohux release note names new infra versions.
- A security advisory for Postgres, Temporal, or OpenBao.

## Decision tree
- A Bohux release ships the new tags → **Section A**.
- One service, a patch or minor version, not yet in a Bohux release → **Section H**.
- Postgres **major** version (16 → 17) → **Section P**.
- Temporal: more than one minor version ahead (1.25 → 1.27) → do Section H once per
  minor version, in order. Temporal applies its schema one minor version at a time.

## A — Upgrade with the install bundle
1. Take a backup (`backup-and-restore.md` N3). Expected: a new archive.
2. In the new release's checkout, re-run the installer with the image you run now:
   ```
   BOHUX_IMAGE=$(sudo docker inspect -f '{{.Config.Image}}' $(sudo docker ps -q --filter name=controlplane)) sudo -E scripts/install.sh
   ```
   Expected: `starting Postgres, Temporal, OpenBao`, then `already installed; the
   control plane answers on http://127.0.0.1:7400`. Compose re-creates only the services
   whose definition changed.
3. **Verification** below.

## H — One service by hand
1. Take a backup (`backup-and-restore.md` N3).
2. Edit the service's `image:` line in `/etc/bohux/infra/compose.yaml`:
   ```
   sudoedit /etc/bohux/infra/compose.yaml
   ```
3. Pull and re-create that service (`postgres`, `temporal` or `openbao`):
   ```
   sudo docker compose -p bohux-infra -f /etc/bohux/infra/compose.yaml --env-file /etc/bohux/infra/.env up -d --wait <service>
   ```
   Expected: `Container bohux-infra-<service>-1 Healthy` or `Started`.
4. Make the same edit in `deploy/install/compose.yaml` of the checkout you install from,
   so the next install run keeps it.
5. **Verification** below.

## P — Postgres major version
A new major version cannot open the old data directory. Bohux's archive is a logical
copy (ADR-0029), so it moves across major versions:
1. Take a backup (`backup-and-restore.md` N3) and copy it off the host.
2. On a fresh host, install with a bundle whose `compose.yaml` names the new major, and
   restore the archive: `backup-and-restore.md` Section R.
3. Point DNS or the agents at the new host. Agents reconnect without re-registering
   (the node CA is in the archive).

## Verification
- `curl -s http://127.0.0.1:7400/healthz` → `ok`.
- `sudo docker exec -e BAO_ADDR=http://127.0.0.1:8200 bohux-infra-openbao-1 bao status`
  → `Sealed false`.
- `sudo docker exec bohux-infra-temporal-1 temporal --address temporal:7233 operator cluster health`
  → `SERVING`.
- `bohux rollback <app> -p <project> -e <env>` of any App succeeds: a deploy touches all
  three.

## Rollback
Put the previous `image:` tag back and repeat H3. Exception: a Temporal or Postgres
version that migrated its schema may not start on the older image; then restore the
backup from step 1 onto the old version (`backup-and-restore.md` Section R).

## Escalation
Escalate to the maintainers when a service does not turn healthy on the new tag and
does not come back on the old one: running Apps are unaffected, so this is not a reason
to touch them.
