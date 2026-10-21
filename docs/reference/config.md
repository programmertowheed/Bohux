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
| `--openbao-address` | `BOHUX_OPENBAO_ADDR` | empty | OpenBao URL holding secret values (ADR-0008). Empty disables secrets: `SetSecret` answers `UNAVAILABLE` and assignments that reference secrets cannot be sent. |
| `--openbao-token-file` | `BOHUX_OPENBAO_TOKEN_FILE` | none, required with an address | File holding the OpenBao token. A file, so the token stays out of process listings and shell history. Read once at start. |
| (none) | `BOHUX_OPENBAO_TOKEN` | none | The OpenBao token itself, used when no token file is given. For the control plane running as an App, which receives it as a secret (ADR-0032). |
| `--openbao-kv-mount` | `BOHUX_OPENBAO_KV_MOUNT` | `secret` | KV **v2** mount for values. Each secret is one path, `bohux/<tenant>/<secret id>`; Postgres records which version is current, and only that version is ever sent to a node. |
| `--deploy-health-timeout` | `BOHUX_DEPLOY_HEALTH_TIMEOUT` | `5m` | How long a deployed Release has to report healthy before the deploy rolls back to the last healthy Release (ADR-0026). Go duration syntax. |
| `--backup-recipient` | `BOHUX_BACKUP_RECIPIENT` | empty | age public key (`age1…`) nightly backups are encrypted to (ADR-0029, ADR-0033). Empty disables them and removes a schedule an earlier start created. Keep the private key off the host. |
| `--backup-database-url` | `BOHUX_BACKUP_DATABASE_URL` | none, required with a recipient | DSN of a role with `BYPASSRLS` and `SELECT` only (the installer's `bohux_backup`). Never a superuser. |
| `--backup-dir` | `BOHUX_BACKUP_DIR` | `/var/lib/bohux-backups` | Where archives go, as `bohux-<UTC time>.backup`, mode 0600. Its own volume in the install. |
| `--backup-schedule` | `BOHUX_BACKUP_SCHEDULE` | `17 3 * * *` | When, as a 5-field cron expression in UTC. |
| `--backup-keep` | `BOHUX_BACKUP_KEEP` | `7` | How many archives stay; older ones are deleted after each successful backup, never after a failed one. |

On start it applies pending migrations, then serves. Temporal does not have to be up: the
API serves without it, deploys are refused with `UNAVAILABLE` until it is reachable, and
the worker keeps retrying every 5 seconds. With a backup recipient, the control plane
also creates or updates the Temporal schedule `bohux-backup-nightly` and serves it on
task queue `bohux-backup`. `SIGINT`/`SIGTERM` stop it, giving
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
| `--ingress-admin-socket` | `BOHUX_INGRESS_ADMIN_SOCKET` | empty | Caddy's admin API Unix socket for this node's ingress (ADR-0034). The agent loads the config of every App with routes there and writes it to `<state dir>/ingress/caddy.json`, which Caddy starts from. Empty: no ingress, and workloads with routes report `INGRESS_FAILED`. The install bundle sets `/run/bohux-caddy/admin.sock`. |
| `--acme-email` | `BOHUX_ACME_EMAIL` | empty | ACME account e-mail for the ingress's certificates. Hosts under `.localhost` use Caddy's internal CA instead. |

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

## `bohux admin backup` and `bohux admin restore` (ADR-0029)

| Flag | Environment | Meaning |
|---|---|---|
| `--admin-database-url` | `BOHUX_ADMIN_DATABASE_URL` | Postgres DSN of a **superuser or `BYPASSRLS`** role on Bohux's database. Row-level security hides every row from Bohux's own role outside a tenant, so backup and restore need one that bypasses it. Never give it to `bohux controlplane`, which refuses such roles. |
| `--data-dir` | `BOHUX_DATA_DIR` | The control plane's data directory; its `pki/` (node CA, signing key) is in the archive. |
| `--openbao-address`, `--openbao-token-file`, `--openbao-kv-mount` | as for `bohux controlplane` | Where secret values are read from (backup) or written to (restore). Leave the address empty when no secrets are configured. |
| `--recipient` (backup) | | age public key (`age1…`) the archive is encrypted to. Only its private key can restore it; keep that key away from the host. |
| `--out` (backup) | | Archive to create, mode 0600. Never overwritten. |
| `--database-url` (restore) | `BOHUX_DATABASE_URL` | The role Bohux runs as; it creates and owns the restored schema. |
| `--identity` (restore) | | age identity file (`AGE-SECRET-KEY-…`). |
| `--in` (restore) | | Archive to restore. |

Restore needs an empty target: a fresh database, no node CA in `--data-dir`, no secret
paths in the mount. It recreates the schema at the archive's migration version, copies
every row back, rewrites every secret version so version numbers match, and restores
the node CA, so registered agents reconnect without re-registering. Start
`bohux controlplane` afterwards to finish migrating. The archive holds every secret
value: store it, and the key, accordingly.

## CLI: `bohux deploy | rollback | scale | logs | exec`

The CLI is a client of the public API (CLAUDE.md rule 8). It has no config file in
v0.1; it reads:

| Environment | Default | Meaning |
|---|---|---|
| `BOHUX_API_URL` | `http://127.0.0.1:7400` | Control plane API. `http://` speaks cleartext HTTP/2 (h2c), as `bohux controlplane` serves; `https://` uses TLS. |
| `BOHUX_TOKEN` | none, required | API token (`bxa_…`). Never put it in a flag: flags show up in process listings. |
| `BOHUX_PROJECT` | none | Project slug, the default for `-p`. |

Apps are named by slug: `bohux deploy api -p shop -e prod`.

- `bohux deploy <app> -e <env> [-p <project>] [--ref <git ref> | --image <repo>@sha256:<digest>] [--reason <text>]`
  creates a Release (built from Git, an image, or the App's own source) and deploys it,
  printing each state until it ends. It exits non-zero unless the deploy succeeded.
  When the App publishes a host port it first says the deploy has a short outage
  (ADR-0020 item 5).
- `bohux rollback <app> -e <env> [--to <release id>] [--reason <text>]` deploys the
  last healthy Release before the current one, or the one named.
- `bohux scale <app> -e <env> --replicas <n>` changes replicas; the node follows at once
  (ADR-0027).
- `bohux logs <app> -e <env> [-f] [--tail <n>] [--since <duration>] [--instance <name>]`
  prints the App's log lines, relayed from its node; `-f` keeps following.
- `bohux exec <app> -e <env> [--instance <name>] -- <command> [args…]` runs a command in
  one of the App's instances and exits with its status. v0.1 forwards no standard input
  and has no TTY. Every session is audited; its input and output are not recorded.

## CLI: resource commands (ADR-0036)

`bohux <noun> <verb>`, with `-p` (or `BOHUX_PROJECT`) and `-e` as above, `-o json` for
the API's own JSON, and `--idempotency-key` for safe retries:

- `project list|create`, `env list|create --node <slug>`, `node list|token <slug>`
- `app list|show|create` (`--image` or `--git`, `--port C[:H]`, `--health PATH[:PORT]`,
  `--route HOST[:PORT]`, `--replicas`)
- `route list|add|remove <app> <host[:port]>`: takes effect without a deploy
- `secret list|set <KEY> [--app <app>]`: the value is read from stdin, never an argument
- `webhook list|create|remove <app>` (`--branch`, `--flavor github|generic`): create
  prints the URL and the signing secret, once (ADR-0037)
- `channel list|create|remove` (`--kind slack|discord|webhook`, `--state …`): the URL
  is read from stdin (ADR-0038)

## `bohux mcp` (ADR-0018)

| Environment | Default | Meaning |
|---|---|---|
| `BOHUX_API_URL` | `http://127.0.0.1:7400` | The API, as for the CLI. |
| `BOHUX_TOKEN` | none, required | The token every tool call uses. |

Serves the Model Context Protocol on stdin/stdout for AI agents (Claude Code, Cursor,
…): one tool per API RPC, named after it (`ListApps`, `Rollback`, …), with its request
message as the input schema. Each RPC's `ToolPolicy` in `proto/` sets the hints:
read-only tools say so, destructive ones (deploy, rollback, scale, delete, revoke) ask
the client to confirm with a human. RPCs that carry a credential (`SetSecret`, token,
webhook, and channel creation) and streams (`StreamLogs`, `Exec`, `WatchDeployment`)
are never tools: secret values never pass through a model's context.

**Give the agent its own token, scoped to one project and the lowest role that does
the job, with an expiry** (`viewer` to look, `deployer` to deploy). Everything it does
is checked and audited as that token, like any other client (rules 8 and 10).

```json
{"mcpServers": {"bohux": {"command": "bohux", "args": ["mcp"],
  "env": {"BOHUX_API_URL": "https://bohux.example.com", "BOHUX_TOKEN": "bxa_…"}}}}
```
