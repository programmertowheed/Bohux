# Docker provider

Runs workloads on one Docker host, driving `docker compose` against a Compose file it
writes for each workload (ADR-0025). Source: `providers/docker/`.

## Prerequisites

- Docker Engine 25 or later, with `live-restore` on (`bohux agent register` refuses
  otherwise; ADR-0020 item 9).
- The Docker CLI and the Compose plugin v2.24 or later on the agent's `PATH`.
- The agent can reach containers on their Compose network from the host, for health
  checks (true for an agent running on the Linux host).

`Describe` checks the versions and fails with the reason if they are too old.

## What it writes

One directory per workload under `<agent state dir>/workloads/`, named after the
Compose project: `bohux-<app slug>-<environment slug>-<8 hex>`.

| File | Mode | Contents |
|---|---|---|
| `compose.yaml` | 0644 | The whole workload: image, replicas, ports, limits, labels, log rotation. No secret values. |
| `secrets.env` | 0600 | Secret values, `KEY=value` per line, referenced by `env_file`. The only place values exist on disk besides the agent's sealed cache (ADR-0008). |
| `.bohux-state.json` | 0644 | Desired replicas and the health check, for `Status`. |

Without Bohux, the workload runs with:

```
docker compose -p <project> -f compose.yaml up -d
```

Every container carries the labels `io.bohux.app`, `io.bohux.environment`,
`io.bohux.release`, and `io.bohux.spec-hash`.

## Declared capabilities

All seven verbs: `plan`, `apply`, `status`, `logs`, `exec`, `scale`, `destroy`.

| Verb | How |
|---|---|
| Apply | Write the files, then `docker compose up --detach --remove-orphans`. A no-op when the target already matches (no container churn). |
| Status | `docker compose ps --all --format json`, the spec-hash label, and an HTTP `GET` of the health path from the host. |
| Destroy | `docker compose down --volumes --remove-orphans`, then remove the directory. |
| Logs | `docker compose logs --timestamps [--follow]`. |
| Exec | `docker compose exec -T`. |
| Scale | Rewrite `replicas` in `compose.yaml` and run `up` again, so the file stays the truth. |

## Duties beside the verbs (ADR-0020)

These run in the agent, not in `Apply` (ADR-0025).

- **Image retention.** Images carrying `io.bohux.app` and `io.bohux.release` labels
  are grouped per App. The current Release (from the cached assignment) and the 5
  newest other Releases are kept; Release IDs are UUIDv7, so ID order is creation
  order. Older images are removed with `docker image rm` (no force) unless any
  container, running or stopped, uses them. Nothing is removed while no assignment is
  cached. Images without the labels are never touched: Bohux labels the images it
  builds (S7), so images pulled by digest are not pruned yet.
- **Disk pressure.** The agent checks free space on its state dir and on Docker's data
  root (`docker info` `DockerRootDir`) every 30 s. Below 10% free the node reports
  `degraded` with reason `DISK_PRESSURE` in every StatusReport and new builds are
  refused. Running workloads are never stopped because of it.

## Builds (ADR-0028)

The agent builds Releases from Git on its own node: `git fetch --depth 1` of the ref,
then `docker buildx build --load` on a buildx builder named `bohux` (`docker-container`
driver, `moby/buildkit`), so the image lands in the local daemon with labels
`io.bohux.app` and `io.bohux.release`. The agent creates the builder on first use with
a 1 GB memory limit, one CPU, and a 10 GB cache cap (`gckeepstorage`). To use a proxy,
a registry mirror, a CA, or other limits, create the builder yourself first, e.g.
`docker buildx create --name bohux --driver docker-container --driver-opt memory=2g
--buildkitd-config /etc/bohux/buildkitd.toml`; the agent uses an existing `bohux`
builder as it is. Builds are refused while the node reports disk pressure.
`bohux agent register` refuses a node without the buildx plugin.

**Private repositories (ADR-0041).** A Git source may name an environment secret
holding a private SSH deploy key, with an `ssh://` or `user@host:path` URL:

```sh
bohux secret set DEPLOY_KEY -e prod < deploy_key        # the private key file
bohux app create site -e prod --git git@github.com:acme/site.git --ssh-key-secret DEPLOY_KEY
bohux app deploy-key site -e prod --secret DEPLOY_KEY   # or --clear
```

The key reaches the node sealed to its key, inside the build request; it is never
stored in the control plane's database. For the build only, the agent writes it to a
`0700` directory under its work dir, fetches with `GIT_SSH_COMMAND` (that key only,
`BatchMode`), passes it to `buildx --ssh default=…` for `RUN --mount=type=ssh` steps,
and removes it when the build ends. The node needs an `ssh` client. A Git host's key
is trusted on first use and pinned in `<state dir>/builds/known_hosts`; to pin it
beforehand, write that file yourself (`ssh-keyscan github.com >> …`, checked against
the host's published fingerprints).

## Private images (ADR-0041)

An App names an environment secret that holds a Docker `config.json` (the
`{"auths": {...}}` document `docker login` writes):

```sh
docker login ghcr.io                     # or write the JSON by hand
bohux secret set REGISTRY -e prod < ~/.docker/config.json
bohux app create api -e prod --image ghcr.io/acme/api@sha256:… --image-pull-secret REGISTRY
bohux app pull-secret api -e prod --secret REGISTRY   # or --clear; used by the next deploy
```

The control plane seals the value to the node apart from the App's environment
variables, so the container never sees it. For each `docker compose up`, the provider
writes it to a fresh `0700` directory, points `DOCKER_CONFIG` at it (per-user CLI
plugins are linked in), and removes the directory when the command ends. Nothing is
written to root's or the daemon's Docker config, and `compose.yaml` names only the
image. A rotated credential is used by the next pull; it does not restart anything,
since the image is pinned by digest. Short-lived registry tokens (ECR, GCR) are not
refreshed on the node yet.

## Exec

`docker compose exec -T` for plain sessions. A TTY session runs the docker CLI on a
pseudo-terminal the agent opens (Linux), as session leader, so compose allocates a
terminal in the container too; resizes set the agent's terminal size, which the CLI
passes on. A TTY session's output is all stdout, and closing its input types end of
file (^D).

## Known gaps (v0.1)

- **Deploys have downtime.** Compose recreates containers stop-old/start-new; a
  published host port cannot be held by two containers (ADR-0020 item 5). Measured in
  the end-to-end test (`cmd/bohux/e2e_test.go`, busybox httpd, local daemon): the host
  port refused connections for 0.35–1.5 s across runs. The old container keeps serving through
  its stop timeout, so a process that ignores SIGTERM makes the deploy slower (10 s),
  not the outage longer.
- **Replicas above 1 cannot publish a host port.** Docker refuses the second bind.
- **Logs do not separate stdout from stderr** (`LogLine.Stderr` is always false).
- Secret values cannot contain newlines (env-file format).
- Build logs stay in the agent's log.

## `Spec.Raw`

An App's `provider_options` reach this provider as `Spec.Raw` (ADR-0032). Setting them
needs the owner role. Accepted, as JSON:

```json
{
  "networks": ["bohux-infra"],
  "volumes": [{"source": "bohux-controlplane-data", "target": "/var/lib/bohux"}]
}
```

- `networks`: existing Docker networks the containers join, besides the workload's
  own. Declared `external` in the Compose file.
- `volumes`: existing **named** volumes mounted at `target` (a clean absolute path).
  Host paths are refused. Declared `external`.
- `publish_address`: the host IP every published port binds to (default: all
  interfaces), e.g. `127.0.0.1` to keep a port off the network.

Both must exist; the provider never creates or removes them, and Destroy leaves them.
Any other key, or a malformed value, fails the workload with `SPEC_INVALID`.
