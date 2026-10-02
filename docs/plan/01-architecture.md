# Architecture

## Shape

```
                    ┌──────────────────────────────────────────┐
   CLI ───┐         │            CONTROL PLANE                 │
   UI  ───┼─ gRPC ─▶│                                          │
   API ───┘ (Connect)│  API  ──▶ Postgres (desired state,       │
                    │           revisions, audit)              │
                    │   │                                      │
                    │   ├──▶ Temporal (deploy sagas)            │
                    │   └──▶ OpenBao / Vault (secret refs)      │
                    └───────────────▲──────────────────────────┘
                                    │ mTLS, agent dials OUT
                    ┌───────────────┴──────────────────────────┐
                    │              DATA PLANE                  │
                    │  agent ──▶ container runtime             │
                    │  agent ──▶ Envoy / Caddy (ingress)       │
                    │  agent ──▶ OTEL collector                │
                    │  BuildKit (builder nodes when >1 node)   │
                    └──────────────────────────────────────────┘
```

## Components

### API layer
gRPC core served over ConnectRPC so browsers speak it directly without a translating
proxy, with a generated REST/OpenAPI gateway alongside. `proto/` is the source of truth;
the CLI, UI, and SDKs are all generated clients. See ADR-0006.

The web UI is a React SPA built with Vite and embedded in the Go binary; no Node process
runs in any deployment (ADR-0012). Components: shadcn/ui + Tailwind; data via
Connect-Query generated from `proto/` (ADR-0013).

### Desired state: Postgres
Every mutation creates an immutable revision row: actor, timestamp, diff, parent revision.
Audit log, rollback, and change history fall out of the data model rather than being
features added later. See `docs/specs/data-model.md` and ADR-0004.

### Orchestration: Temporal
A deploy is a long-running saga: build, scan, sign, push, provision dependencies, run
migrations, shift traffic, gate on health, finalize or compensate. Durable workflow
execution survives control plane restarts mid-deploy and gives retries and compensation
for free. See ADR-0002.

### Eventing: none (agent streams + Postgres)
No message bus. Agents hold one outbound gRPC stream each: assignments go down, status and
log tails come up. Work distribution is Temporal task queues. Cross-replica notification
uses Postgres `LISTEN/NOTIFY`, with the row as the truth. See ADR-0009.

### Agent (data plane)
Same binary as the control plane, agent mode. One per node. **Pull-based**: the node dials
out over mTLS, so managed nodes need zero inbound ports and work behind NAT. It owns
container lifecycle, local health checks, log shipping, and metric scraping.

The load-bearing property: **the agent keeps reconciling its last known good state when
the control plane is unreachable.** Control plane down must never mean apps down, and must
never mean apps cannot self-heal. See ADR-0003.

### Providers
The extension boundary. The same app spec targets single-host Docker, Swarm, Nomad,
Kubernetes, ECS/Fargate, Lambda/Cloud Run, systemd on bare metal, or static-to-CDN. A
provider implements `Describe` plus seven verbs (rollback is a deploy, ADR-0010). See `docs/specs/provider-interface.md` and ADR-0005.

Providers run inside the agent, never in the control plane. Deploy workflows write an
assignment and wait for the agent to report convergence (ADR-0007).

### Build layer: BuildKit
Content-hash-keyed remote cache. Auto-detection via Cloud Native Buildpacks and Nixpacks,
Dockerfile passthrough, Nix/Bazel escape hatch for monorepos. With more than one node,
builds run on dedicated builder nodes, never on a node serving traffic; a single-node
install builds locally under CPU/memory limits (ADR-0011). Every artifact gets an SBOM, SLSA
provenance, and a cosign signature at build time — retrofitting supply chain security is
miserable.

### Ingress: Envoy via xDS
Canary and blue/green are traffic splits at the proxy, programmed by the same workflow
that runs the deploy. Caddy as the lightweight option for single-node installs that do
not want Envoy's footprint.

### Observability: OpenTelemetry, no storage of our own
Everything emits OTLP to the user's backend. What Kiln owns is **correlation**: every
deploy emits a marker, and the health gate queries real SLO burn rate before promoting.
Automatic rollback on SLO violation is the feature; the dashboard is not.

### Secrets
Never rolled by hand. Interface to Vault/OpenBao/cloud KMS, with embedded OpenBao as the
zero-config default. Envelope encryption, per-environment keys, age/SOPS for the GitOps
path.

## Resource hierarchy

```
Tenant ─▶ Project ─▶ Environment ─▶ Service ─▶ Release ─▶ Deployment
                                   └─ Resource (db, cache, bucket, queue)
```

A `Release` is an immutable artifact digest plus config revision. A `Deployment` is one
attempt to reconcile a Release onto an Environment. Promotion between environments reuses
the same digest — build once, deploy everywhere.

## Failure model (design against these explicitly)

| Failure | Required behaviour |
|---|---|
| Control plane down | Apps serve; agents keep reconciling and self-healing |
| Agent down | Workloads keep running; node marked stale, no silent drift |
| Postgres down | API read-only; no deploys; running workloads unaffected |
| Temporal down | In-flight deploys resume on recovery, never half-applied |
| Provider API down | Deploy fails closed, compensates, previous release stays live |
| Network partition | Agent reconciles locally; no split-brain writes to desired state |
