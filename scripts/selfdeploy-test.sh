#!/usr/bin/env bash
# v0.1's exit criterion, end to end on a real host (ADR-0030, ADR-0031): install Bohux,
# let it deploy a new version of itself, roll itself back, and survive a version of
# itself that cannot start. Destroys nothing it did not create, but it does install
# Bohux on this host: run it on a throwaway machine (CI).
#
#   sudo -E scripts/selfdeploy-test.sh
#
# Needs: Docker with live-restore and buildx, jq, curl, openssl, and systemd (or
# BOHUX_SYSTEMD=0). BOHUX_SELFDEPLOY_V1 names an already pushed v1 image instead of
# building one from this tree.
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
root="$here/.."
registry=localhost:5000
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

log() { printf 'selfdeploy: %s\n' "$*" >&2; }
die() { log "FAIL: $*"; exit 1; }

# --- Images: v1 from this tree, v2 on top of it, and one that cannot start ---------
if ! curl -fsS "http://$registry/v2/" >/dev/null 2>&1; then
  docker run -d --name bohux-selfdeploy-registry -p "127.0.0.1:5000:5000" registry:2 >/dev/null
  until curl -fsS "http://$registry/v2/" >/dev/null 2>&1; do sleep 1; done
fi
push() { # push TAG DOCKERFILE_DIR -> prints repository@digest
  docker build -q -t "$registry/bohux:$1" "$2" >/dev/null
  docker push -q "$registry/bohux:$1" >/dev/null
  docker inspect --format '{{range .RepoDigests}}{{println .}}{{end}}' "$registry/bohux:$1" | grep "^$registry/"
}
if [[ -n "${BOHUX_SELFDEPLOY_V1:-}" ]]; then
  v1="$BOHUX_SELFDEPLOY_V1" # an image already pushed (hosts that cannot build it)
else
  log "building the control plane image"
  v1="$(push v1 "$root")"
fi
printf 'FROM %s\nLABEL io.bohux.selfdeploy=v2\n' "$v1" >"$work/Dockerfile.v2"
mkdir -p "$work/v2" "$work/broken" && mv "$work/Dockerfile.v2" "$work/v2/Dockerfile"
v2="$(push v2 "$work/v2")"
printf 'FROM %s\nCMD ["no-such-command"]\n' "$v1" >"$work/broken/Dockerfile"
broken="$(push broken "$work/broken")"
mkdir -p "$work/hello"
printf 'FROM busybox:1.36\nRUN mkdir -p /www && echo "hello from bohux" > /www/index.html\nCMD ["httpd", "-f", "-p", "8080", "-h", "/www"]\n' \
  >"$work/hello/Dockerfile"
hello="$(push hello "$work/hello")"
mkdir -p "$work/hello2"
sed 's/hello from bohux/hello again/' "$work/hello/Dockerfile" >"$work/hello2/Dockerfile"
hello2="$(push hello2 "$work/hello2")"

# --- Install -------------------------------------------------------------------------
log "installing $v1"
# Nightly backups to a throwaway key: this test checks an archive is written, the
# restore drill (TestRestoreDrill) checks archives restore.
BOHUX_IMAGE="$v1" BOHUX_DEPLOY_HEALTH_TIMEOUT="${BOHUX_DEPLOY_HEALTH_TIMEOUT:-60s}" \
  BOHUX_BACKUP_RECIPIENT=age1y5p7wtgkha9u87lrqynxv0mkpfqgvr86d8gyyumpqazqg8y9hedq8mrjq7 "$here/install.sh"
etc="${BOHUX_ETC:-/etc/bohux}"
bin="${BOHUX_BIN:-/usr/local/bin/bohux}"
export BOHUX_TOKEN="$(cat "$etc/owner.token")" BOHUX_API_URL=http://127.0.0.1:7400 BOHUX_PROJECT=bohux

running() { docker inspect --format '{{.Config.Image}}' "$(docker ps -q --filter label=io.bohux.app --filter name=controlplane)"; }

# --- Bohux deploys Bohux -------------------------------------------------------------
log "deploying v2 of the control plane with itself"
"$bin" deploy controlplane -e system --image "$v2" --reason selfdeploy || die "deploying v2"
[[ "$(running)" == "$v2" ]] || die "after deploying v2 the control plane runs $(running)"

log "rolling the control plane back with itself"
"$bin" rollback controlplane -e system --reason selfdeploy || die "rolling back"
[[ "$(running)" == "$v1" ]] || die "after rollback the control plane runs $(running)"

# --- A version that cannot start: nothing is left to compensate, the agent falls back -
log "deploying a control plane that cannot start (expect the agent to fall back)"
start=$(date +%s)
if "$bin" deploy controlplane -e system --image "$broken" --reason selfdeploy; then
  die "the broken version was reported healthy"
fi
[[ "$(running)" == "$v1" ]] || die "after the broken deploy the control plane runs $(running)"
log "recovered in $(( $(date +%s) - start ))s"

# --- The nightly backup, run now ------------------------------------------------------
log "triggering the nightly backup schedule"
archives() { docker run --rm -v bohux-backups:/b busybox:1.36 ls /b | grep -c '^bohux-.*\.backup$' || true; }
before="$(archives)"
temporal() { docker exec bohux-infra-temporal-1 temporal --address temporal:7233 "$@"; }
until temporal schedule describe --schedule-id bohux-backup-nightly >/dev/null 2>&1; do sleep 2; done
temporal schedule trigger --schedule-id bohux-backup-nightly >/dev/null
for _ in $(seq 60); do
  [[ "$(archives)" -gt "$before" ]] && break
  sleep 2
done
[[ "$(archives)" -gt "$before" ]] || die "the triggered backup wrote no archive"

# --- An App on a route, through the node's ingress (ADR-0034) ------------------------
log "serving an App on https://hello.localhost through the ingress"
# Set up with the CLI (ADR-0036), as a user would.
node="$("$bin" node list -o json | jq -r '.nodes[0].slug')"
"$bin" project create demo --name Demo >/dev/null
BOHUX_PROJECT=demo "$bin" env create prod --node "$node" >/dev/null
BOHUX_PROJECT=demo "$bin" app create hello -e prod --image "$hello" --port 8080 --health / \
  --route hello.localhost >/dev/null
BOHUX_PROJECT=demo "$bin" deploy hello -e prod --image "$hello" --reason selfdeploy || die "deploying an App with a route"
https_port="${BOHUX_HTTPS_PORT:-443}"
body="$(curl -fsSk --resolve "hello.localhost:$https_port:127.0.0.1" "https://hello.localhost:$https_port/")" ||
  die "the ingress did not serve hello.localhost"
[[ "$body" == "hello from bohux" ]] || die "hello.localhost served \"$body\""

# --- A second deploy of it, with a client polling throughout (ADR-0035) --------------
log "deploying hello again while a client polls it: no request may fail"
poll="$work/poll.log"
( while :; do
    curl -fsSk --max-time 2 --resolve "hello.localhost:$https_port:127.0.0.1" "https://hello.localhost:$https_port/" >>"$poll" 2>&1 ||
      echo "FAILED" >>"$poll"
    sleep 0.1
  done ) &
poller=$!
BOHUX_PROJECT=demo "$bin" deploy hello -e prod --image "$hello2" --reason selfdeploy || die "redeploying hello"
sleep 2
kill "$poller"
wait "$poller" 2>/dev/null || true
failed="$(grep -c FAILED "$poll" || true)"
served="$(grep -c '^hello' "$poll" || true)"
grep -q '^hello again$' "$poll" || die "the new release was never served"
[[ "$failed" == 0 ]] || die "$failed of $((failed + served)) requests failed during the deploy"
log "zero downtime: $served requests, none failed"

log "PASS: Bohux deployed itself, rolled itself back, recovered from a broken version of itself, backed itself up, and served an App on a route through a deploy without downtime"
