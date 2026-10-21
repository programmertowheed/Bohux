#!/usr/bin/env bash
# Installs Bohux on one VPS (ADR-0030): infra (Postgres, Temporal, OpenBao) as a Compose
# project, the agent as a systemd unit, and the control plane as a Bohux App of itself,
# started through a short-lived seed. Safe to run again: every step checks first.
#
#   BOHUX_IMAGE=ghcr.io/bohumix/bohux@sha256:<digest> sudo -E scripts/install.sh
#
# Settings (environment):
#   BOHUX_IMAGE        control plane image, pinned by digest (required)
#   BOHUX_TENANT       tenant slug (default: default)
#   BOHUX_ETC          config and secrets directory (default: /etc/bohux)
#   BOHUX_STATE_DIR    agent state directory (default: /var/lib/bohux-agent)
#   BOHUX_BIN          where the bohux binary goes (default: /usr/local/bin/bohux)
#   BOHUX_SYSTEMD      1 to run the agent as a systemd unit (default), 0 to run it in
#                      the background without systemd (test hosts only)
#   BOHUX_DEPLOY_HEALTH_TIMEOUT
#                      the control plane's --deploy-health-timeout (default: its own,
#                      5m); tests shorten it
#   BOHUX_BACKUP_RECIPIENT
#                      age public key (age1…) for nightly backups (ADR-0033); keep
#                      its private key off this host (docs/runbooks/backup-and-restore.md).
#                      Empty: no nightly backups. Archives land in the
#                      bohux-backups volume (/var/lib/docker/volumes/bohux-backups/_data)
#
# It writes, all readable (rule 2): $BOHUX_ETC/infra/compose.yaml and friends, the
# agent unit, and, through the agent, the control plane App's Compose file. Secrets go
# to $BOHUX_ETC with mode 0600 and are never printed except the owner token's path.
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
bundle="$here/../deploy/install"

image="${BOHUX_IMAGE:?set BOHUX_IMAGE to the control plane image, as repository@sha256:<digest>}"
tenant="${BOHUX_TENANT:-default}"
etc="${BOHUX_ETC:-/etc/bohux}"
state="${BOHUX_STATE_DIR:-/var/lib/bohux-agent}"
bin="${BOHUX_BIN:-/usr/local/bin/bohux}"
systemd="${BOHUX_SYSTEMD:-1}"
api="http://127.0.0.1:7400"
agent_addr="127.0.0.1:7443"

log() { printf 'bohux-install: %s\n' "$*" >&2; }
die() { log "$*"; exit 1; }

repo="${image%@*}"
digest="${image#*@}"
[[ "$repo" != "$image" && "$digest" == sha256:* ]] || die "BOHUX_IMAGE must be repository@sha256:<digest>, never a tag"

# --- 0. The host ------------------------------------------------------------------
for tool in docker jq curl openssl; do
  command -v "$tool" >/dev/null || die "$tool is required"
done
[[ "$(docker info --format '{{.LiveRestoreEnabled}}')" == true ]] ||
  die 'Docker live-restore is off: set "live-restore": true in /etc/docker/daemon.json and reload dockerd'
docker buildx version >/dev/null 2>&1 || die "docker buildx is required (docker-buildx-plugin)"

umask 077
mkdir -p "$etc/infra"
chmod 700 "$etc"

# --- 1. Secrets, generated once ---------------------------------------------------
env_file="$etc/infra/.env"
if [[ ! -f "$env_file" ]]; then
  log "generating infra passwords"
  {
    echo "POSTGRES_PASSWORD=$(openssl rand -hex 24)"
    echo "BOHUX_APP_PASSWORD=$(openssl rand -hex 24)"
    echo "TEMPORAL_PASSWORD=$(openssl rand -hex 24)"
    echo "BOHUX_UNSEAL_KEY=$etc/openbao-unseal.key"
  } >"$env_file"
fi
grep -q '^BOHUX_BACKUP_PASSWORD=' "$env_file" || echo "BOHUX_BACKUP_PASSWORD=$(openssl rand -hex 24)" >>"$env_file"
# shellcheck disable=SC1090
source "$env_file"
if [[ ! -f "$etc/openbao-unseal.key" ]]; then
  log "generating the OpenBao unseal key"
  openssl rand 32 >"$etc/openbao-unseal.key"
fi
# OpenBao drops to its own user; only that user may read the key.
bao_uid="$(docker run --rm --entrypoint id openbao/openbao:2.4.1 -u openbao)"
chown "$bao_uid" "$etc/openbao-unseal.key"
chmod 400 "$etc/openbao-unseal.key"

# --- 2. Infra ---------------------------------------------------------------------
cp "$bundle/compose.yaml" "$bundle/openbao.hcl" "$etc/infra/"
rm -rf "$etc/infra/postgres-init" && cp -r "$bundle/postgres-init" "$etc/infra/"
chmod 644 "$etc/infra/compose.yaml" "$etc/infra/openbao.hcl"
chmod 755 "$etc/infra" "$etc/infra/postgres-init" "$etc/infra/postgres-init/"*
compose=(docker compose -p bohux-infra -f "$etc/infra/compose.yaml" --env-file "$env_file")
log "starting Postgres, Temporal, OpenBao"
"${compose[@]}" up -d --wait postgres openbao
"${compose[@]}" up -d temporal

# pg_isready answers during the first start's init script too; wait for its database.
until "${compose[@]}" exec -T postgres psql -q -U postgres -d bohux_app -c 'SELECT 1' >/dev/null 2>&1; do sleep 1; done
# The nightly backup's role (ADR-0033): reads every table past RLS, writes nothing.
# Default privileges cover the tables bohux_app's migrations create later.
"${compose[@]}" exec -T postgres psql -q -v ON_ERROR_STOP=1 -U postgres -d bohux_app \
  -v pass="$BOHUX_BACKUP_PASSWORD" >/dev/null <<'SQL'
SELECT format('CREATE ROLE bohux_backup LOGIN BYPASSRLS PASSWORD %L', :'pass')
  WHERE NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'bohux_backup') \gexec
ALTER ROLE bohux_backup PASSWORD :'pass';
GRANT SELECT ON ALL TABLES IN SCHEMA public TO bohux_backup;
ALTER DEFAULT PRIVILEGES FOR ROLE bohux_app IN SCHEMA public GRANT SELECT ON TABLES TO bohux_backup;
SQL

bao() { "${compose[@]}" exec -T -e BAO_ADDR=http://127.0.0.1:8200 ${BAO_TOKEN:+-e BAO_TOKEN="$BAO_TOKEN"} openbao bao "$@"; }
until bao status -format=json >/dev/null 2>&1 || [[ $? -eq 2 ]]; do sleep 1; done
if [[ "$(bao status -format=json | jq -r .initialized)" != true ]]; then
  log "initializing OpenBao (static seal, one recovery key)"
  bao operator init -recovery-shares=1 -recovery-threshold=1 -format=json >"$etc/openbao-init.json"
fi
BAO_TOKEN="$(jq -r .root_token "$etc/openbao-init.json")"
if ! bao secrets list -format=json | jq -e '."secret/"' >/dev/null; then
  bao secrets enable -path=secret -version=2 kv >/dev/null
fi
until "${compose[@]}" exec -T temporal temporal operator cluster health --address temporal:7233 >/dev/null 2>&1; do
  sleep 2
done

# --- 3. The binary and the control plane's data volume ----------------------------
if [[ ! -x "$bin" ]] || [[ "$("$bin" version)" != "$(docker run --rm "$image" version)" ]]; then
  log "installing $bin from the image"
  cid="$(docker create "$image")"
  docker cp "$cid:/bohux" "$bin"
  docker rm "$cid" >/dev/null
  chmod 755 "$bin"
fi
docker volume create bohux-controlplane-data >/dev/null
docker volume create bohux-backups >/dev/null

db_url="postgres://bohux_app:${BOHUX_APP_PASSWORD}@postgres:5432/bohux_app?sslmode=disable"
cp_env=(-e "BOHUX_DATABASE_URL=$db_url" -e BOHUX_TEMPORAL_ADDRESS=temporal:7233
  -e BOHUX_OPENBAO_ADDR=http://openbao:8200 -e "BOHUX_OPENBAO_TOKEN=$BAO_TOKEN")
health_timeout="${BOHUX_DEPLOY_HEALTH_TIMEOUT:-}"
[[ -z "$health_timeout" ]] || cp_env+=(-e "BOHUX_DEPLOY_HEALTH_TIMEOUT=$health_timeout")
backup_recipient="${BOHUX_BACKUP_RECIPIENT:-}"
backup_db_url="postgres://bohux_backup:${BOHUX_BACKUP_PASSWORD}@postgres:5432/bohux_app?sslmode=disable"

if [[ -f "$etc/installed" ]] && curl -fsS "$api/healthz" >/dev/null 2>&1; then
  log "already installed; the control plane answers on $api"
  exit 0
fi

# --- 4. Tenant and owner token ----------------------------------------------------
if [[ ! -f "$etc/owner.token" ]]; then
  log "creating tenant $tenant"
  docker run --rm --network bohux-infra "${cp_env[@]}" "$image" admin bootstrap --slug "$tenant" |
    tail -n 1 >"$etc/owner.token"
fi
token="$(cat "$etc/owner.token")"

# --- 5. The seed control plane ----------------------------------------------------
if ! curl -fsS "$api/healthz" >/dev/null 2>&1; then
  log "starting the seed control plane"
  docker rm -f bohux-seed >/dev/null 2>&1 || true
  docker run -d --name bohux-seed --network bohux-infra -p 127.0.0.1:7400:7400 -p 127.0.0.1:7443:7443 \
    -v bohux-controlplane-data:/var/lib/bohux "${cp_env[@]}" "$image" >/dev/null
  until curl -fsS "$api/healthz" >/dev/null 2>&1; do sleep 1; done
fi

# call SERVICE/METHOD JSON: a Connect unary call with the owner token.
call() {
  curl -fsS -H "Authorization: Bearer $token" -H 'Content-Type: application/json' \
    -d "$2" "$api/bohux.v1.$1"
}

# --- 6. The agent -----------------------------------------------------------------
if [[ ! -f "$state/identity.json" ]]; then
  log "registering this node"
  bt="$(call NodeService/CreateBootstrapToken "{\"nodeSlug\": \"$(hostname -s | tr 'A-Z_.' 'a-z--')\", \"idempotencyKey\": \"install-node-$(date +%s)\"}")"
  BOHUX_BOOTSTRAP_TOKEN="$(jq -r .token <<<"$bt")" "$bin" agent register --state-dir "$state" \
    --control-plane "$agent_addr" --ca-cert-sha256 "$(jq -r .caCertSha256 <<<"$bt")"
fi
if [[ "$systemd" == 1 ]]; then
  sed "s#/usr/local/bin/bohux agent run --state-dir /var/lib/bohux-agent#$bin agent run --state-dir $state#" \
    "$bundle/bohux-agent.service" >/etc/systemd/system/bohux-agent.service
  chmod 644 /etc/systemd/system/bohux-agent.service
  systemctl daemon-reload
  systemctl enable --now bohux-agent >/dev/null
elif ! pgrep -f "$bin agent run --state-dir $state" >/dev/null; then
  log "starting the agent without systemd (test host)"
  nohup "$bin" agent run --state-dir "$state" >>"$etc/agent.log" 2>&1 &
fi
node=""
until [[ -n "$node" ]]; do
  node="$(call NodeService/ListNodes '{}' | jq -r '.nodes[]? | select(.state == "NODE_STATE_ONLINE") | .meta.id' | head -n 1)"
  [[ -n "$node" ]] || sleep 1
done

# --- 7. The control plane as an App of itself -------------------------------------
project="$(call ProjectService/GetProject '{"slug": "bohux"}' 2>/dev/null | jq -r .project.meta.id || true)"
if [[ -z "$project" || "$project" == null ]]; then
  project="$(call ProjectService/CreateProject '{"slug": "bohux", "displayName": "Bohux", "idempotencyKey": "install-project"}' | jq -r .project.meta.id)"
fi
env_id="$(call EnvironmentService/GetEnvironment "{\"slug\": {\"projectId\": \"$project\", \"slug\": \"system\"}}" 2>/dev/null | jq -r .environment.meta.id || true)"
if [[ -z "$env_id" || "$env_id" == null ]]; then
  env_id="$(call EnvironmentService/CreateEnvironment "{\"projectId\": \"$project\", \"slug\": \"system\",
    \"providerBinding\": {\"provider\": \"docker\", \"nodeId\": \"$node\"}, \"idempotencyKey\": \"install-env\"}" |
    jq -r .environment.meta.id)"
fi

# Its configuration travels as secrets (ADR-0032; O5 decides non-secret App config).
secret() {
  local key="$1" value="$2" existing
  existing="$(call SecretService/ListSecretRefs "{\"environmentId\": \"$env_id\", \"pageSize\": 100}" |
    jq -c --arg k "$key" '.secrets[]? | select(.key == $k)')"
  if [[ -z "$existing" ]]; then
    existing="$(call SecretService/SetSecret "$(jq -n --arg e "$env_id" --arg k "$key" --arg v "$value" \
      '{environmentId: $e, key: $k, value: $v, idempotencyKey: ("install-secret-" + $k)}')" | jq -c .secret)"
  fi
  jq -c '{key: .key, handle: .handle}' <<<"$existing"
}
refs="$(jq -s . <<<"$(secret BOHUX_DATABASE_URL "$db_url")
$(secret BOHUX_TEMPORAL_ADDRESS temporal:7233)
$(secret BOHUX_OPENBAO_ADDR http://openbao:8200)
$(secret BOHUX_OPENBAO_TOKEN "$BAO_TOKEN")
$([[ -z "$health_timeout" ]] || secret BOHUX_DEPLOY_HEALTH_TIMEOUT "$health_timeout")
$([[ -z "$backup_recipient" ]] || secret BOHUX_BACKUP_RECIPIENT "$backup_recipient")
$([[ -z "$backup_recipient" ]] || secret BOHUX_BACKUP_DATABASE_URL "$backup_db_url")")"

app="$(call AppService/GetApp "{\"slug\": {\"environmentId\": \"$env_id\", \"slug\": \"controlplane\"}}" 2>/dev/null | jq -r .app.meta.id || true)"
if [[ -z "$app" || "$app" == null ]]; then
  app="$(call AppService/CreateApp "$(jq -n --arg e "$env_id" --arg r "$repo" --arg d "$digest" --argjson s "$refs" '{
    environmentId: $e, slug: "controlplane", idempotencyKey: "install-app",
    source: {image: {repository: $r, digest: $d}},
    runtime: {replicas: 1, health: {path: "/healthz", port: 7400},
      ports: [{containerPort: 7400, hostPort: 7400}, {containerPort: 7443, hostPort: 7443}]},
    secrets: $s,
    providerOptions: {networks: ["bohux-infra"], publish_address: "127.0.0.1",
      volumes: [{source: "bohux-controlplane-data", target: "/var/lib/bohux"},
        {source: "bohux-backups", target: "/var/lib/bohux-backups"}]}}')" | jq -r .app.meta.id)"
fi
release="$(call ReleaseService/CreateRelease "{\"appId\": \"$app\", \"idempotencyKey\": \"install-release-$digest\"}" | jq -r .release.meta.id)"
deployment="$(call DeploymentService/CreateDeployment "{\"releaseId\": \"$release\", \"environmentId\": \"$env_id\",
  \"reason\": \"install\", \"idempotencyKey\": \"install-deploy-$digest\"}" | jq -r .deployment.meta.id)"

# The App needs the seed's ports. Once the deploy has assigned it, the seed goes; the
# agent starts the App on its next pass, and the App's worker finishes this very run.
state_of() { call DeploymentService/GetDeployment "{\"id\": \"$deployment\"}" | jq -r .deployment.state; }
until [[ "$(state_of)" == DEPLOYMENT_STATE_HEALTH_GATE ]]; do sleep 1; done
log "handing over from the seed to the control plane App"
docker rm -f bohux-seed >/dev/null
until curl -fsS "$api/healthz" >/dev/null 2>&1; do sleep 1; done
until s="$(state_of 2>/dev/null)" && [[ "$s" == DEPLOYMENT_STATE_SUCCEEDED || "$s" == DEPLOYMENT_STATE_FAILED || "$s" == DEPLOYMENT_STATE_ROLLED_BACK ]]; do
  sleep 2
done
[[ "$s" == DEPLOYMENT_STATE_SUCCEEDED ]] || die "the control plane's first deploy ended $s"

touch "$etc/installed"
log "done. The control plane runs as bohux/system/controlplane on $api (localhost only)."
log "owner token: $etc/owner.token. Upgrade with: bohux deploy controlplane -p bohux -e system --image <repo>@sha256:<digest>"
