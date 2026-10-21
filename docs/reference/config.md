# Configuration

Every setting Bohux reads, in one place. A flag wins over its environment variable.
Adding a setting means adding it here in the same commit.

## `bohux controlplane`

| Flag | Environment | Default | Meaning |
|---|---|---|---|
| `--listen` | `BOHUX_LISTEN_ADDR` | `127.0.0.1:7400` | TCP address for the API (ConnectRPC over h2c) and `GET /healthz`. |
| `--agent-listen` | `BOHUX_AGENT_LISTEN_ADDR` | `127.0.0.1:7443` | TLS address agents connect to (`bohux.agent.v1`), with a certificate from the node CA (ADR-0024). Set it to an address remote nodes can reach. |
| `--database-url` | `BOHUX_DATABASE_URL` | none, required | Postgres DSN. |
| `--data-dir` | `BOHUX_DATA_DIR` | `/var/lib/bohux` | Holds `pki/`: the node CA and assignment signing key, created on first start, mode 0600 (ADR-0024). **Back it up with the database**; losing it means every node re-registers. |
| `--temporal-address` | `BOHUX_TEMPORAL_ADDRESS` | `127.0.0.1:7233` | Temporal frontend for deploy workflows (ADR-0002). The control plane also runs the deploy worker on task queue `bohux-deploy` (ADR-0026). |
| `--temporal-namespace` | `BOHUX_TEMPORAL_NAMESPACE` | `default` | Temporal namespace for Bohux's workflows. It must exist. |

On start it applies pending migrations, then serves. Temporal does not have to be up: the
API serves without it, deploys are refused with `UNAVAILABLE` until it is reachable, and
the worker keeps retrying every 5 seconds. `SIGINT`/`SIGTERM` stop it, giving
in-flight requests up to 10 seconds.

`GET /healthz` returns `200 ok` when Postgres answers, `503` otherwise.

## `bohux admin migrate`

| Flag | Environment | Default | Meaning |
|---|---|---|---|
| `--database-url` | `BOHUX_DATABASE_URL` | none, required | Postgres DSN. |

Applies pending migrations (ADR-0021) and exits. Safe to run while a control plane is
starting: migrators serialize on an advisory lock.

## `bohux admin bootstrap`

| Flag | Environment | Default | Meaning |
|---|---|---|---|
| `--database-url` | `BOHUX_DATABASE_URL` | none, required | Postgres DSN. |
| `--slug` | | none, required | Tenant slug. Fails if it exists. |
| `--display-name` | | empty | Tenant display name. |

Applies migrations, creates the tenant, and prints a tenant-wide owner API token
(ADR-0023) to stdout. That is the only time the token is shown; only its hash is
stored (ADR-0022). Use it to create project-scoped tokens for day-to-day use.

## `bohux agent register`

| Flag | Environment | Default | Meaning |
|---|---|---|---|
| `--control-plane` | | none, required | The control plane's agent address, `host:port` (its `--agent-listen`). |
| `--token` | `BOHUX_BOOTSTRAP_TOKEN` | none, required | The `bxb_` token from `NodeService.CreateBootstrapToken`. Prefer the env var so the token stays out of shell history. Single use. |
| `--ca-cert-sha256` | | none, required | The `ca_cert_sha256` returned with the token. The agent trusts only the CA with this hash (ADR-0024). |
| `--state-dir` | `BOHUX_AGENT_STATE_DIR` | `/var/lib/bohux-agent` | Where the node's keys, certificate, pinned CA, and identity are written (0700 directory, 0600 files). |

Refuses to run if Docker `live-restore` is off (ADR-0020 item 9), or if the directory
already holds a registration.

## `bohux agent run`

| Flag | Environment | Default | Meaning |
|---|---|---|---|
| `--state-dir` | `BOHUX_AGENT_STATE_DIR` | `/var/lib/bohux-agent` | The registered state. |

Connects to the control plane recorded at registration over mTLS and keeps the
stream open, reconnecting with backoff (1 s doubling to 1 min; 5 min after a
`VersionRejected`). It never exits because the control plane is away (rule 1).

Beside the stream it runs, with no further configuration:

- **The reconcile loop** with the Docker provider (v0.1 has no other), on the local
  daemon or `DOCKER_HOST` if set. Artifacts go to `<state dir>/workloads/`.
- **The disk-pressure monitor:** below 10% free on the state dir's or Docker's data
  root's filesystem, every StatusReport says the node is degraded and new builds are
  refused (ADR-0020 item 4). Running workloads are untouched.
- **Image retention,** hourly: per App, images of the current Release and the 5 newest
  others are kept; older ones go unless a container uses them (ADR-0020 item 3).

It refuses to start if the Docker provider's `Describe` fails (daemon down, Engine
older than 25, Compose older than 2.24).

## The database role

The DSN's role must own the Bohux schema and must be neither a superuser nor
`BYPASSRLS`: those ignore row-level security, so tenant isolation would be silently off.
Bohux refuses to start otherwise. The dev stack creates `bohux_app` (password
`bohux-dev`, database `bohux_app`) for this:

```
BOHUX_DATABASE_URL='postgres://bohux_app:bohux-dev@localhost:5432/bohux_app?sslmode=disable'
```

## Tests

| Environment | Used by | Meaning |
|---|---|---|
| `BOHUX_TEST_POSTGRES_URL` | `go test -tags=integration` | DSN of a role that can create roles and databases. Each test creates and drops its own. |
