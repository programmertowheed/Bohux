# ADR-0034: Routes on Apps, one Caddy per node programmed by the agent

- **Status:** proposed
- **Date:** 2026-10-21
- **Deciders:** Towheed

## Context
ADR-0019 fixes the proxy (Caddy, through its admin API, config also on disk) and v0.2
promises automatic TLS, per-App domains, and zero-downtime deploys. v0.1 publishes host
ports instead. What ADR-0019 leaves open, and is expensive to change once users have
domains in their databases:

1. **Where a domain lives in the model**, and how one tenant is kept from claiming
   another tenant's domain while every table is under row-level security.
2. **Whether changing a domain needs a deploy.** A Release is immutable (ADR-0004) and
   replicas are already App state outside it (ADR-0027).
3. **Who programs Caddy.** The control plane must not be on the serving path (rule 1),
   and assignments are whole-node (ADR-0007).
4. **How Caddy reaches a workload** without the ingress knowing provider internals.
5. **Where Caddy runs** and how the agent talks to it.

Zero-downtime swaps (two Releases live at once) need these first; they get their own
ADR.

## Decision
1. **`App.routes`: repeated `Route { host, port }`.** `host` is a lowercase DNS name (no
   wildcard, no port, no path in v0.2); `port` must be one of the App's container
   ports. A host belongs to at most one App across **all tenants**: a table
   `route_hosts (host PRIMARY KEY, tenant_id, app_id)` is written in the same
   transaction as the App revision. A primary key is enforced over rows that RLS hides,
   so a taken host fails with `ALREADY_EXISTS` ("host is in use") and reveals nothing
   else. Reads of `route_hosts` are tenant-scoped like every table.
2. **Routes are App state, not Release state** (as replicas, ADR-0027). Changing them
   writes an App revision and, when the App runs, re-assigns its current workload with
   the new routes in the same transaction — no deploy, no container restart. Setting
   routes needs the same role as updating the App (`deployer`); the audit event is the
   App update's.
3. **The agent owns its node's whole Caddy config.** `Workload.routes` carries them; the
   spec hash excludes them, so a route change never re-creates a container. Each pass the
   agent renders one JSON config from every workload's routes; when it differs from what
   it last loaded, it writes it to `ingress/caddy.json` in its state directory (rule 2:
   `caddy run --config` serves it without Bohux) and loads it with one `POST /load`
   (atomic in Caddy). A failed load leaves the old config serving and reports the
   workload `FAILED`/`INGRESS_FAILED`, retried every pass. Caddy keeps serving whether
   the agent or the control plane is up (rule 1).
4. **Providers report endpoints.** `provider.Status` gains `Endpoints`: container port →
   an address the node's ingress can dial. The Docker provider attaches every App with
   routes to the `bohux-ingress` network under the alias `<compose project>` and reports
   `<project>:<port>`; Docker's DNS spreads requests over the replicas. The agent never
   builds an address itself. The conformance suite checks `Endpoints` for every port.
5. **Caddy runs in the infra Compose project** (`caddy:2`, pinned by digest), publishing
   80 and 443, on network `bohux-ingress`, with its admin API on a Unix socket in a host
   directory the agent can reach (`/run/bohux-caddy/admin.sock`); the TCP admin listener
   is off. Automatic HTTPS with an ACME account e-mail set at install
   (`BOHUX_ACME_EMAIL`); names under `.localhost` get Caddy's internal CA (tests).
6. **Health includes the route.** A deploy of an App with routes is healthy only when the
   node reports the ingress config that carries this workload's routes as loaded
   (`WorkloadStatus.ingress_loaded`). A workload without routes is unaffected.
7. **Host ports stay** for non-HTTP Apps and for the control plane itself (localhost
   only); an App may have both.

## Alternatives considered
- **Domains as their own resource** (`Domain` with verification state). Needed once
  ownership is verified by DNS; in v0.2 nothing verifies, so a resource would be a
  second write path for the same fact. Revisit with DNS verification.
- **Routes in the Release.** A domain change would need a build-free deploy and would
  roll back with the code. Lost: rolling back code should not move a domain.
- **The control plane programs Caddy.** Puts the control plane on the serving path and
  needs a second channel to each node. Lost on rule 1 and ADR-0003.
- **Caddyfile with reloads.** Readable, but templated text and reloads are what ADR-0019
  rejected.
- **The agent addresses containers itself** (`docker inspect`). Couples the ingress to
  Docker; a second provider would need its own branch. Lost to a reported endpoint.
- **Uniqueness per tenant only.** Two tenants could then claim one domain and the node
  would route it to whichever loaded last. Lost.

## Consequences
- Easy: a domain change is one API call and takes effect within one agent pass; deleting
  Bohux leaves a Caddy config that still serves; rollbacks never move domains.
- Hard: the provider interface changes (a field, not a verb), so every provider reports
  endpoints; `route_hosts` must stay consistent with App revisions (one write path,
  `AppTx`, tested).
- Proto: `bohux.v1.Route`, `App.routes`, `Workload.routes`,
  `WorkloadStatus.ingress_loaded`, and a failure reason `INGRESS_FAILED`.
- The install bundle grows a fourth infra service and opens ports 80 and 443.

## Revisit when
DNS ownership verification arrives (domains become a resource), wildcard or path routes
are needed, or a second ingress provider is proposed.
