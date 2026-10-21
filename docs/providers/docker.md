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

## Known gaps (v0.1)

- **Deploys have downtime.** Compose recreates containers stop-old/start-new; a
  published host port cannot be held by two containers (ADR-0020 item 5).
- **Replicas above 1 cannot publish a host port.** Docker refuses the second bind.
- **Exec has no TTY yet.** Non-interactive commands work; an interactive shell needs a
  pseudo-terminal on the agent, planned later.
- **Logs do not separate stdout from stderr** (`LogLine.Stderr` is always false).
- Secret values cannot contain newlines (env-file format).

## `Spec.Raw`

Not used yet. Target-specific options will be accepted here, never as new interface
methods (provider-interface.md rule 5).
