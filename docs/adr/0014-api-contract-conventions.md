# ADR-0014: API contract — App not Service, secrets via API, field_behavior annotations

- **Status:** accepted
- **Date:** 2026-10-02
- **Deciders:** Towheed

## Context
`docs/specs/api-contract.md` left three contract choices open. All three are expensive to
change once `proto/` ships and clients are generated (ADR-0006).

## Decision
1. **The deployable resource is named `App`.** `AppService`, `kiln app …`, table `apps`.
2. **`SecretService.SetSecret` accepts secret values**, writes them to OpenBao, and stores
   only the handle (rule 3). A redacting interceptor runs before logging and tracing;
   a test asserts the value never appears in logs, traces, errors, or Postgres.
3. **Field rules are `google.api.field_behavior` annotations** (`REQUIRED`,
   `OUTPUT_ONLY`, `IMMUTABLE`), via a `buf` dependency on googleapis.

## Alternatives considered
- **Keep `Service` as the resource name.** Matches Compose/Kubernetes wording. Lost
  because it collides with the gRPC `service` keyword (`ServiceService`,
  `ServiceServiceClient`) in every generated client and makes docs ambiguous.
- **`Workload` instead of `App`.** Precise, but unfamiliar to the target user (teams of
  5–50 coming from Heroku-style platforms, where "app" is the norm). Lost on clarity.
- **Secrets by reference only (users write to OpenBao themselves).** Values never touch
  Kiln. Lost on usability: every user would need OpenBao CLI skills on day one, against
  the "easy for teams without a platform engineer" goal. The interceptor + test keeps
  rule 3 enforceable.
- **Field rules as comments.** No extra dependency. Lost because nothing enforces
  comments; annotations are checked by linters and visible to generated clients.

## Consequences
- Docs and the roadmap say App everywhere; "service" now means only a gRPC service.
- Secret values exist in control-plane memory during `SetSecret`. The interceptor and
  its test are part of the v0.1 definition of done, not optional hardening.
- `buf` now depends on googleapis; `buf.yaml` must pin it.

## Revisit when
A security review or customer requirement forbids secret values transiting the control
plane — then add reference-only mode alongside, not instead.
