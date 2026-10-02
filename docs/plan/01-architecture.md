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
                    │   ├──▶ NATS JetStream (events, log fanout)│
                    │   └──▶ OpenBao / Vault (secret refs)      │
                    └───────────────▲──────────────────────────┘
                                    │ mTLS, agent dials OUT
                    ┌───────────────┴──────────────────────────┐
                    │              DATA PLANE                  │
                    │  agent ──▶ container runtime             │
                    │  agent ──▶ Envoy / Caddy (ingress)       │
                    │  agent ──▶ OTEL collector                │
                    │  BuildKit on dedicated builder nodes     │
                    └──────────────────────────────────────────┘
```

## Components

### API layer
gRPC core served over ConnectRPC so browsers speak it directly without a translating
proxy, with a generated REST/OpenAPI gateway alongside. `proto/` is the source of truth;
the CLI, UI, and SDKs are all generated clients. See ADR-0006.

### Desired state: Postgres
Every mutation creates an immutable revision row: actor, timestamp, diff, parent revision.
Audit log, rollback, and change history fall out of the data model rather than being
features added later. See `docs/specs/data-model.md` and ADR-0004.

### Orchestration: Temporal
A deploy is a long-running saga: build, scan, sign, push, provision dependencies, run
migrations, shift traffic, gate on health, finalize or compensate. Durable workflow
execution survives control plane restarts mid-deploy and gives retries and compensation
for free. See ADR-0002.

### Eventing: NATS JetStream
Pub/sub, KV, and object store in one binary. Carries log streaming, agent fan-out, and
work distribution. Avoids running Redis plus Kafka plus a separate object store.

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
provider implements eight verbs. See `docs/specs/provider-interface.md` and ADR-0005.

### Build layer: BuildKit
Content-hash-keyed remote cache. Auto-detection via Cloud Native Buildpacks and Nixpacks,
Dockerfile passthrough, Nix/Bazel escape hatch for monorepos. Builds run on dedicated
builder nodes, never on a node serving traffic. Every artifact gets an SBOM, SLSA
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
