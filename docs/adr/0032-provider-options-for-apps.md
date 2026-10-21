# ADR-0032: Provider options on Apps, and how the control plane runs as one

- **Status:** proposed
- **Date:** 2026-10-21
- **Deciders:** Towheed

## Context
ADR-0030 makes the control plane an App of itself. As a container it needs three things
a v0.1 App cannot express:

1. **Persistent state across redeploys**: its data directory holds the node CA and the
   assignment signing key (ADR-0024). A recreate must not lose them.
2. **Reaching the infra layer**: Postgres, Temporal, and OpenBao run in their own
   Compose project (ADR-0030). An App's containers sit on their own Compose network and
   cannot reach them by name.
3. **Configuration**: database URL, OpenBao token, Temporal address.

The provider interface already reserves the place for target-specific features:
`Spec.Raw`, "a passthrough blob the provider interprets; never new interface methods"
(provider-interface.md rule 5). It is not wired from the API yet. Volumes as a
first-class, cross-provider concept belong with managed resources (v0.3) and are not
needed by any other v0.1 App.

## Decision
1. **`App.provider_options`** (`google.protobuf.Struct`, public API, proto first) is
   passed through the Release's captured App revision to `Workload.provider_options`
   (JSON bytes, agent protocol) and to the provider as `Spec.Raw`. The API checks only
   that it is a JSON object; the provider validates it and fails the workload with
   `SPEC_INVALID` on anything it does not accept. Changing it is part of the App
   revision, so it is versioned and audited like the rest (rule 10).
2. **Setting non-empty provider options requires the `owner` role.** Options can reach
   outside the workload (other networks, existing volumes), which is close to root on
   the node; a deployer must not get that by editing an App.
3. **The Docker provider accepts two keys**, both naming things that already exist and
   that Bohux never creates or deletes:
   - `networks`: external Docker networks to attach the App's containers to, in
     addition to its own.
   - `volumes`: `{"source": <external named volume>, "target": <path in the
     container>}`; no host paths.
   Unknown keys are refused. The rendered Compose file shows them (rule 2).
4. **The control plane App** uses `networks: ["bohux-infra"]` and a volume
   `bohux-controlplane-data` → `/var/lib/bohux`, both created by the installer, and
   gets its database URL, Temporal address, and OpenBao token as App secrets (the open
   question O5 — where non-secret App config lives — is not decided here; secrets work
   for both, at the cost of holding a non-secret in OpenBao). The OpenBao token reaches
   the control plane as the env var `BOHUX_OPENBAO_TOKEN`, accepted when no token file
   is given.

## Alternatives considered
- **First-class volumes in `Runtime`.** Cross-provider and discoverable, but it is the
  managed-resources design (v0.3) done early for one App, and Destroy semantics
  (delete the data or not) need that design. Lost on scope.
- **Host-path bind mounts.** Simplest to implement; any path on the node becomes
  readable and writable by a deployer's container. Lost on security, even behind the
  owner role: named volumes and networks the installer made are the narrower grant.
- **The node CA as App secrets** written into a tmpfs data dir at start. No volume
  needed, but the CA key would travel through every assignment for the control plane
  and live in OpenBao, which the control plane itself fronts. Lost on blast radius.
- **Host networking for the control plane** (`network_mode: host`). Reaches infra on
  127.0.0.1 and needs no port publishing, but grants the container the whole host
  network stack. Lost on blast radius.

## Consequences
- Easy: target-specific needs have one, owner-gated place; the control plane is an
  ordinary App with two options.
- Hard: options are opaque to the API, so a bad option is caught at apply time on the
  node, not at the API. Every provider must document what it accepts.
- The API grows one field on App, the agent protocol one on Workload, the schema one
  column on `apps_revisions`.

## Revisit when
A second App needs persistent data (design first-class volumes with managed resources),
or a provider's options start to look the same as another's (promote them to the API).
