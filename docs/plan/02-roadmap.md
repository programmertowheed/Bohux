# Roadmap

Phased so that the plugin boundary and the workflow model land early. Those two are what
let v2 through v8 happen without a rewrite, and the rewrite is what kills these projects —
not the missing features.

## v0.1 — Walking skeleton (weeks 1–12)
**Goal: one service, one VPS, deployed and rolled back from a CLI — obeying every hard
rule.** Scope is fixed by ADR-0011, extended by ADR-0020.

- [ ] **Week 1:** footprint spike on a 2GB VPS, published before feature work (ADR-0020)
- [ ] `proto/` contract for Tenant/Project/Environment/App/Release/Deployment, plus
      the agent protocol (ADR-0007); `tenant_id` on every resource
- [ ] Postgres schema: revisions, audit events in the same transaction, row-level security
- [ ] Control plane API over ConnectRPC
- [ ] API tokens, fixed roles (`viewer`/`deployer`/`owner`), permission check on every
      RPC, audit event on every mutation
- [ ] Agent registration, mTLS, outbound stream, sealed secret cache (ADR-0008), offline
      reconcile loop
- [ ] Docker provider: `Describe` + all seven verbs, conformance suite green
- [ ] Temporal deploy workflow: build → push → assign → wait healthy → finalize, with
      compensation; rollback is the same workflow (ADR-0010)
- [ ] BuildKit, Dockerfile only; builds may run on the serving node in single-node mode,
      resource-limited
- [ ] OpenBao as the one secrets backend, shipped in the install bundle
- [ ] `bohux deploy`, `bohux logs`, `bohux rollback`, `bohux exec`, `bohux scale`
- [ ] Compensation/rollback tested in CI before forward path merges (CLAUDE.md rule 5)
- [ ] Footprint re-measured at the end of v0.1; <1GB left for workloads reopens ADR-0002
- [ ] No registry in single-node mode: BuildKit exports to the local image store
- [ ] Image retention (current + last 5 Releases), BuildKit cache cap, log rotation,
      disk-pressure fails closed (ADR-0020)
- [ ] `bohux admin backup` / `restore` for Bohux's own state, restore tested in CI
- [ ] OpenBao auto-unseal; Docker `live-restore` checked at agent registration
- [ ] Agent/control-plane version skew handled in the agent protocol (N-1 minor)
- [ ] Host-port deploy downtime measured and printed by the CLI
- [ ] `.claude/skills/new-workflow/` and `deploy/dev/compose.yaml` exist

**Exit criterion:** Bohux deploys Bohux, and rolls itself back.

## v0.2 — Usable by someone who is not us (weeks 13–20)
- [ ] Buildpacks + Nixpacks auto-detection
- [ ] Secrets: envelope encryption, per-env keys
- [ ] Managed Postgres/Redis as Resources, with backups
- [ ] Ingress: Caddy via admin API, automatic TLS, DNS, zero-downtime deploys (ADR-0019)
- [ ] Web UI (read-only first, then mutations)
- [ ] Audit log and RBAC surfaced in the UI
- [ ] Git push and webhook triggers
- [ ] MCP server: `bohux mcp` over stdio, tools generated from `proto/` (ADR-0018)
- [ ] Notifications on deploy/failure/rollback (Slack, Discord, email, webhook)
- [ ] Team invites and member management (CLI + UI)
- [ ] Web terminal into a running container (UI over the existing `Exec` verb)
- [ ] Private registry credentials, SSH key and Git source management
- [ ] Basic server and app resource view (CPU/memory/disk) from node agents — current
      values only; history goes to the user's OTLP backend (non-goal: metrics storage)

## v0.3 — The reason to choose this (weeks 21–30)
- [ ] Preview environments per PR, with seeded data
- [ ] Blue/green and canary via Caddy weighted upstreams (ADR-0019)
- [ ] OTEL correlation, deploy markers, SLO burn-rate health gate
- [ ] Automatic rollback on SLO violation
- [ ] Schema migrations as a gated workflow step
- [ ] **Scheduled automated restore drills** — a backup you have not restored is a feeling
- [ ] Second provider (Swarm) — proves the boundary is real
- [ ] Scheduled jobs (cron) per app, run as Temporal schedules (rule 6) — needs workflow design
- [ ] Docker Compose apps (multi-container) as an App source — needs an ADR (changes App)
- [ ] Volume management and volume backups to S3-compatible storage
- [ ] Monorepo watch paths: only deploy when files under given paths change

## v1.0 — Trustworthy
- [ ] Kubernetes provider
- [ ] SSO (OIDC/SAML), SCIM
- [ ] SBOM, SLSA provenance, cosign signing, scan policy gates
- [ ] Break-glass prod access with session recording
- [ ] Self-upgrade, node drain/cordon, cert rotation
- [ ] Cost attribution per service
- [ ] gRPC provider plugin protocol opened to third parties (ADR-0016)
- [ ] More managed resources: MySQL/MariaDB, MongoDB, ClickHouse, with backups to S3
- [ ] One-click app templates, implemented as Compose import (no bespoke template format)

## v1.x — Parity (ordered by user demand, not committed dates)
Competitor features (Coolify, Dokploy, Vercel docs, checked 2026-10-02) not needed to win
first users. Each still needs the hard rules: tenant scope, permission check, audit event,
CLI and API before UI.
- [ ] Preview deployment protection (password / SSO in front of preview URLs)
- [ ] Custom roles beyond `viewer`/`deployer`/`owner`
- [ ] Remote MCP endpoint with OAuth (ADR-0018 "revisit when")
- [ ] Deploy/ownership hand-off links for agencies (Vercel "claim deployments" analogue)

**Covered by integration, not built (non-goals or out of category)**
CDN, image optimisation, ISR, edge middleware, WAF/bot management, AI gateway, code
sandboxes, web analytics, built-in CI, metrics/log storage, whitelabeling. Users get these
through documented integrations (e.g. Cloudflare in front of Caddy, Grafana via OTLP).

## Deliberately deferred
Multi-region traffic steering, FaaS providers, continuous profiling, compliance evidence
export, marketplace. All are v1.x+. Writing them down here is how we stop arguing about
them in v0.1.
