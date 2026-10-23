# ADR-0019: One ingress proxy through v1 — Caddy

- **Status:** accepted
- **Date:** 2026-10-21
- **Deciders:** Towheed

## Context
`01-architecture.md` names Envoy via xDS as the ingress, with Caddy as "the lightweight
option". The roadmap ships Caddy in v0.2 and Envoy in v0.3 for blue/green and canary.
That means two proxy integrations to build, test against the conformance suite, upgrade,
and document — before v1 has proven itself on one VPS (vision non-goal: "It is Docker on
one VPS in v1").

Each proxy brings its own config model, TLS story, health checking, and failure modes.
Supporting both doubles that surface for a project with one maintainer.

What v1 actually needs from ingress:
- automatic TLS (ACME) and per-app domains (v0.2);
- zero-downtime swap between two upstreams (v0.2 deploys);
- weighted splits for canary and an instant flip for blue/green (v0.3);
- programmatic config from the agent, no file templating;
- a footprint that fits the 2GB box (ADR-0011).

## Decision
Bohux ships **one** ingress proxy through v1: **Caddy**, configured by the agent through
Caddy's admin API (JSON config), never through a generated Caddyfile. Canary uses
weighted upstreams; blue/green swaps the upstream set in one config load. The agent
also writes the active JSON config to disk next to the Compose artifacts, so rule 2
holds: deleting Bohux leaves a Caddy config that `caddy run --config` serves.

Envoy is not supported before v1. It may come back as an alternative *ingress provider*
behind the same interface, by its own ADR.

## Alternatives considered
- **Envoy via xDS as primary (current architecture doc).** The most capable traffic
  splitting. Lost on footprint and operational weight: an xDS control server to build
  and keep running, no built-in ACME (so cert-manager-like work lands on us), and a
  config that users who delete Bohux cannot read. Those cost more than the extra
  routing features earn on one VPS.
- **Both, Caddy single-node and Envoy multi-node (current roadmap).** Lost on surface
  area: two integrations, two test matrices, and canary semantics that must behave the
  same on both. Multi-node is not v1 scope.
- **Traefik.** Built-in ACME and weighted services. Lost because its dynamic config is
  driven by provider labels or file watching rather than one atomic API call, which
  makes the "swap upstreams in one step" deploy step harder to make transactional.
- **nginx.** Ubiquitous. Lost on ACME (external tooling) and reloads as the only
  config path; weighted canary needs config regeneration per step.

## Consequences
- One proxy to integrate, test, and document; the v0.3 canary work builds on the v0.2
  ingress instead of introducing a new component.
- Caddy's weighted load balancing is coarser than Envoy's (no request-level header
  routing policies or built-in outlier detection to the same degree). Canary health
  comes from the SLO gate (OTEL), not from proxy-side outlier detection.
- `01-architecture.md` and the roadmap change: "Envoy via xDS" becomes "Caddy via
  admin API".
- If Caddy's admin API is down or the config load fails, the deploy step fails closed and
  compensates; the previously loaded config keeps serving.

## Revisit when
A user with a multi-node install (post-v1) needs a traffic policy Caddy cannot express,
reported as a concrete issue — or Caddy's measured memory on the 2GB box exceeds 150MB
idle.
