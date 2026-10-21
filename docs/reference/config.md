# Configuration

Every setting Bohux reads, in one place. A flag wins over its environment variable.
Adding a setting means adding it here in the same commit.

## `bohux controlplane`

| Flag | Environment | Default | Meaning |
|---|---|---|---|
| `--listen` | `BOHUX_LISTEN_ADDR` | `127.0.0.1:7400` | TCP address for the API (ConnectRPC over h2c) and `GET /healthz`. |
| `--database-url` | `BOHUX_DATABASE_URL` | none, required | Postgres DSN. |

On start it applies pending migrations, then serves. `SIGINT`/`SIGTERM` stop it, giving
in-flight requests up to 10 seconds.

`GET /healthz` returns `200 ok` when Postgres answers, `503` otherwise.

## `bohux admin migrate`

| Flag | Environment | Default | Meaning |
|---|---|---|---|
| `--database-url` | `BOHUX_DATABASE_URL` | none, required | Postgres DSN. |

Applies pending migrations (ADR-0021) and exits. Safe to run while a control plane is
starting: migrators serialize on an advisory lock.

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
