# Roadmap

Phased so that the plugin boundary and the workflow model land early. Those two are what
let v2 through v8 happen without a rewrite, and the rewrite is what kills these projects —
not the missing features.

## v0.1 — Walking skeleton (weeks 1–6)
**Goal: one service, one VPS, deployed and rolled back from a CLI.**

- [ ] `proto/` contract for Project/Environment/Service/Release/Deployment
- [ ] Postgres schema with revision table and audit events
- [ ] Control plane API over ConnectRPC
- [ ] Agent registration, mTLS, outbound dial, heartbeat
- [ ] Docker provider: `plan`, `apply`, `status`, `logs`, `destroy`
- [ ] Temporal deploy workflow: build → push → apply → health gate → finalize
- [ ] BuildKit integration, Dockerfile only
- [ ] `kiln deploy`, `kiln logs`, `kiln rollback`
- [ ] Rollback tested in CI before forward path merges (see CLAUDE.md rule 5)

**Exit criterion:** Kiln deploys Kiln.

## v0.2 — Usable by someone who is not us (weeks 7–14)
- [ ] Remaining provider verbs: `exec`, `scale`, `rollback`
- [ ] Buildpacks + Nixpacks auto-detection
- [ ] Secrets: embedded OpenBao, envelope encryption, per-env keys
- [ ] Managed Postgres/Redis as Resources, with backups
- [ ] Ingress: Caddy, automatic TLS, DNS
- [ ] Web UI (read-only first, then mutations)
- [ ] RBAC and audit log surfaced in API and UI
- [ ] Git push and webhook triggers

## v0.3 — The reason to choose this (weeks 15–24)
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
