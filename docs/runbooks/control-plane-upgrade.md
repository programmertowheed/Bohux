# Upgrade the control plane, and recover from a bad upgrade

**Severity:** routine (upgrade) · sev2 (bad upgrade)
**Expected duration:** 1–2 minutes; up to health timeout + 2 minutes when a version
cannot start (7 minutes by default)
**Blast radius:** the API, deploys, logs and exec are unavailable during the recreate
(about a second) and while a broken version is being replaced. Running Apps keep
serving throughout (rule 1).
**Prerequisites:** the owner token (`/etc/bohux/owner.token` on the host), the new image
pinned by digest (`<repo>@sha256:…`, from the release notes), shell on the host.

The control plane is an App of itself, `bohux/system/controlplane` (ADR-0030). Upgrading
it is a deploy; rolling it back is a rollback.

## Symptoms
- Routine: a new Bohux release.
- Bad upgrade: `bohux deploy controlplane …` reports `control plane unavailable … still
  following` for minutes, or ends `rolled_back`.

## Decision tree
- Upgrading → **Section U**.
- The new version is up but misbehaving → **Section B** (roll back).
- The API has been unreachable for more than health timeout + 2 minutes → **Section M**
  (manual recovery).

## U — Upgrade
1. On the host:
   ```
   export BOHUX_TOKEN=$(sudo cat /etc/bohux/owner.token) BOHUX_API_URL=http://127.0.0.1:7400
   ```
2. Deploy the new version:
   ```
   bohux deploy controlplane -p bohux -e system --image <repo>@sha256:<digest> --reason "upgrade to x.y.z"
   ```
   Expected: `control plane unavailable (…); still following` once (the recreate), then
   `health_gate`, then `succeeded`. Exit status 0.
3. If it ends `rolled_back`, the previous version is running again; report the failure
   reason it printed and stop.

## B — Roll back a running but bad version
1. ```
   bohux rollback controlplane -p bohux -e system --reason "<why>"
   ```
   Expected: `rolling back to release …`, then `succeeded`.

## M — Manual recovery (the agent's fallback did not bring the API back)
The agent falls back to the previous version on its own after the health timeout plus
2 minutes (ADR-0031). If the API is still down after that:
1. Check the agent is running:
   ```
   systemctl status bohux-agent
   ```
   Expected: `active (running)`. If not: `sudo systemctl start bohux-agent`, wait 2
   minutes, recheck the API.
2. Look for the fallback in its log:
   ```
   journalctl -u bohux-agent | grep -i "falling back" | tail -1
   ```
3. If there is no fallback line, start the previous version by hand. The Compose file
   is plain (rule 2); put the previous digest back and bring it up:
   ```
   cd /var/lib/bohux-agent/workloads/bohux-controlplane-system-*/
   sudo sed -i 's#image: .*#image: <previous repo>@sha256:<previous digest>#' compose.yaml
   sudo docker compose up -d
   ```
   The previous digest is in `bohux` release history, or in the image list:
   `docker images --digests | grep bohux`.
4. Once `curl -s http://127.0.0.1:7400/healthz` prints `ok`, deploy the previous
   release properly so Bohux's history matches (Section B). The agent re-applies its own
   assignment on its next pass; until then do not edit the file again.

## Verification
- `curl -s http://127.0.0.1:7400/healthz` → `ok`.
- `docker inspect -f '{{.Config.Image}}' $(docker ps -q --filter name=controlplane)`
  shows the expected digest.

## Rollback
Section B is the rollback of Section U. Section M's step 3 is undone by Section B.

## Escalation
Escalate when the agent itself does not stay running, or when no previous image is on
the host (retention keeps the last five Releases, ADR-0020 item 3).
