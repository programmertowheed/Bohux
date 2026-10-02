# Roadmap

Phased so that the plugin boundary and the workflow model land early. Those two are what
let v2 through v8 happen without a rewrite, and the rewrite is what kills these projects —
not the missing features.

## v0.1 — Walking skeleton (weeks 1–10)
**Goal: one service, one VPS, deployed and rolled back from a CLI — obeying every hard
rule.** Scope is fixed by ADR-0011.

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
- [ ] Footprint measured on a 2GB VPS and published; <1GB left for workloads reopens
      ADR-0002
- [ ] `.claude/skills/new-workflow/` and `deploy/dev/compose.yaml` exist

**Exit criterion:** Bohux deploys Bohux, and rolls itself back.

## v0.2 — Usable by someone who is not us (weeks 11–18)
- [ ] Buildpacks + Nixpacks auto-detection
- [ ] Secrets: envelope encryption, per-env keys
- [ ] Managed Postgres/Redis as Resources, with backups
- [ ] Ingress: Caddy, automatic TLS, DNS
- [ ] Web UI (read-only first, then mutations)
- [ ] Audit log and RBAC surfaced in the UI
- [ ] Git push and webhook triggers

## v0.3 — The reason to choose this (weeks 19–28)
- [ ] Preview environments per PR, with seeded data
- [ ] Blue/green and canary via Envoy xDS
- [ ] OTEL correlation, deploy markers, SLO burn-rate health gate
- [ ] Automatic rollback on SLO violation
- [ ] Schema migrations as a gated workflow step
- [ ] **Scheduled automated restore drills** — a backup you have not restored is a feeling
- [ ] Second provider (Swarm) — proves the boundary is real

## v1.0 — Trustworthy
- [ ] Kubernetes provider
- [ ] SSO (OIDC/SAML), SCIM
- [ ] SBOM, SLSA provenance, cosign signing, scan policy gates
- [ ] Break-glass prod access with session recording
- [ ] Self-upgrade, node drain/cordon, cert rotation
- [ ] Cost attribution per service
- [ ] WASM plugin runtime opened to third parties

## Deliberately deferred
Multi-region traffic steering, FaaS providers, continuous profiling, compliance evidence
export, marketplace. All are v1.x+. Writing them down here is how we stop arguing about
them in v0.1.
