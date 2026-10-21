# ADR-0037: Git push webhooks — a Webhook resource, HMAC-verified, deploying as itself

- **Status:** proposed
- **Date:** 2026-10-21
- **Deciders:** Towheed

## Context
v0.2 promises deploys on `git push`. An App with a Git source can be built and deployed
from the CLI (ADR-0028), but only by someone holding a token. A Git host (GitHub,
Gitea, GitLab, or a script) must be able to start a deploy with no Bohux token, which
raises questions that are expensive to change once hosts are configured with URLs:

1. **The URL and how the control plane finds the tenant** without authentication,
   while every table is under row-level security.
2. **How a request is proved genuine.** The secret that proves it must not land in
   Postgres, logs, or errors (rule 3).
3. **Who the deploy runs as**, for RBAC and audit (rule 10).
4. **What is trusted from the payload.**

## Decision
1. **A `Webhook` resource** (`bohux.v1.Webhook`): tenant-scoped, belongs to an App with
   a Git source, has a `branch` and a `flavor` (`GITHUB` or `GENERIC`), revisions and
   audit events like any resource. `WebhookService`: `CreateWebhook` (deployer role;
   returns the URL path and the signing secret **once**), `ListWebhooks`,
   `DeleteWebhook`.
2. **URL:** `POST /hooks/v1/<tenant id hex>/<webhook id hex>` on the API listener,
   plain HTTP (not ConnectRPC), like token strings carry their tenant (ADR-0022): the
   tenant in the path selects the RLS scope, the ID the row. Unknown, deleted, and
   badly signed requests all answer `404`, so the endpoint reveals nothing.
3. **Signature:** HMAC-SHA256 of the raw body with the webhook's secret, compared in
   constant time. GitHub: `X-Hub-Signature-256: sha256=<hex>`. Generic:
   `X-Bohux-Signature: sha256=<hex>`. The secret is stored in OpenBao by handle like an
   App secret (ADR-0008); webhooks need OpenBao, as secrets do. Bodies over 1 MiB are
   refused.
4. **Payload:** GitHub `push` events (a `ping` answers `204`; other events `204` and
   nothing happens); generic `{"ref": "<branch or commit>"}`. Only the ref and commit
   are read: a push to `refs/heads/<branch>` deploys its `after` commit. The repository
   URL is the App's, never the payload's.
5. **The deploy runs as the webhook**: a principal of a new actor kind `webhook` (ID =
   the webhook's), role `deployer` in the App's project, calling the same
   `CreateRelease` and `CreateDeployment` code as the API, so RBAC, audit, and
   idempotency apply unchanged. The idempotency keys are `webhook/<id>/<commit>`: a
   redelivered push starts nothing new. Deleting the webhook revokes it.
6. **A push while the App is deploying** answers `409` (ADR-0026: one run per App); the
   Git host shows the failed delivery, and the next push deploys the newest commit.

## Alternatives considered
- **Use an API token in the URL or a header.** No new resource, but a long-lived
  deployer token sits in the Git host's settings and in its delivery logs. Lost.
- **Poll the repository.** No inbound endpoint, but a cron poller over a status column
  is what rule 6 bans, and latency is the poll interval. Lost.
- **Store the secret's hash.** HMAC verification needs the secret itself. Lost.
- **Trust the payload's repository URL.** Anyone with the secret could build any
  repository as the App. Lost.

## Consequences
- Easy: `git push` deploys; deliveries are audited as the webhook; revoking is one
  delete; the API's checks are reused, not copied.
- Hard: a new resource with its migration, proto service, and actor kind; the API
  listener accepts unauthenticated POSTs on one path prefix, which must stay minimal.
- The control plane's API port must be reachable from the Git host: through the
  node's ingress with a route to the control plane, which install leaves to the owner.

## Revisit when
A Git host needs another signature scheme, or users want deploys on tags or pull
requests (preview environments, v0.3).
