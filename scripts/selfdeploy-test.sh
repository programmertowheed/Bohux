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

# --- Install -------------------------------------------------------------------------
log "installing $v1"
BOHUX_IMAGE="$v1" BOHUX_DEPLOY_HEALTH_TIMEOUT="${BOHUX_DEPLOY_HEALTH_TIMEOUT:-60s}" "$here/install.sh"
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

log "PASS: Bohux deployed itself, rolled itself back, and recovered from a broken version of itself"
