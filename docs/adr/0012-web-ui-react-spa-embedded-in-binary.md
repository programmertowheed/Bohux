# ADR-0012: Web UI is a React SPA (Vite), embedded in the Go binary

- **Status:** proposed
- **Date:** 2026-10-02
- **Deciders:** Towheed (pending review)

## Context
The dashboard arrives in v0.2. The choice was never recorded. Constraints that decide it:

- CLAUDE.md rule 8 / ADR-0006: the UI gets no capability the CLI lacks; both are clients
  of the same public API generated from `proto/`.
- ADR-0001: one static binary, sized for a 2GB VPS that also runs workloads.
- The dashboard sits behind authentication. It has no public pages and no SEO needs.

## Decision
The web UI is a client-side single-page app: **TypeScript + React, built with Vite**,
calling the API only through the ConnectRPC TypeScript client generated from `proto/`.
The build output (static HTML/JS/CSS) is embedded into the Go binary with `go:embed` and
served by the control plane. There is no Node.js process in any Kiln deployment.

## Alternatives considered
- **Next.js (or any SSR/full-stack JS framework: Remix, Nuxt, SvelteKit server mode).**
  Its value is server rendering, server actions, and API routes. Lost on rule 8: server
  actions and API routes are a second backend where UI-only capabilities appear. Also
  lost on ADR-0001: it needs a Node runtime (~100–200MB RAM) on the smallest target box.
  SSR and SEO buy nothing behind a login.
- **Next.js in static-export mode.** Removes the server, but then most of what Next adds
  is disabled and its routing/data conventions fight a pure API client. Lost: Vite gives
  the same output with less framework.
- **Server-rendered Go templates + htmx.** Smallest footprint and no JS build. Lost
  because the UI would call internal Go handlers instead of the public API, which breaks
  rule 8 by construction, and live views (log tails, deploy progress) are harder.
- **Svelte/SolidJS instead of React.** Smaller bundles. Lost on contributor pool and
  component ecosystem; bundle size is irrelevant for an authenticated dashboard served
  from the same host.

## Consequences
- Users still install one file; the UI is versioned with the API it calls.
- A Node toolchain is a **build-time** dependency only (CI and contributors).
- Every UI feature needs a public API endpoint first — slower for UI work, which is the
  intended cost of rule 8.
- Binary size grows by the bundle size (a few MB). Acceptable.
- A public marketing/docs site is out of scope here; it may use any framework, Next.js
  included, as a separate project.

## Revisit when
A required UI feature cannot be built as a pure API client without adding a
UI-only server endpoint — that would mean rule 8 itself is under question, not just this ADR.
