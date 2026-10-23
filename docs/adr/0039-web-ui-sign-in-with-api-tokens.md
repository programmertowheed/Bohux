# ADR-0039: The web UI signs in with an API token kept in the tab, behind a strict CSP

- **Status:** proposed
- **Date:** 2026-10-21
- **Deciders:** Towheed

## Context
ADR-0012 makes the UI a React SPA embedded in the binary and calling only the public
API; ADR-0013 picks its stack. Neither says how a browser authenticates. The API knows
one credential, the API token (ADR-0022, ADR-0023), sent as a bearer header. There are
no user accounts, passwords, or SSO yet. Choices that are costly to change once people
use the UI:

1. **What the browser holds**, and how long it survives.
2. **Whether the API gains a browser-only path** (cookies, sessions), which rule 8
   frowns on and which brings CSRF with it.
3. **How a script injected into the page is kept from reading the credential.**

## Decision
1. **The user signs in by pasting an API token**, the same credential the CLI uses. The
   UI calls `WhoAmI` to check it and shows the principal and role.
2. **The token lives in `sessionStorage`** for that tab: closing the tab signs out; it
   is never written to `localStorage`, a cookie, or a URL. "Sign out" clears it.
3. **No new server-side auth path**: the UI sends `Authorization: Bearer`, so the API
   is unchanged and there is no cookie to forge (no CSRF surface).
4. **The control plane serves the UI with a strict Content-Security-Policy**:
   `default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:;
   connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'`,
   plus `X-Content-Type-Options: nosniff` and `Referrer-Policy: no-referrer`. No inline
   scripts, no third-party origins: the bundle is the only script that can run.
5. **The first UI is read-only**: projects, environments, Apps, Releases, Deployments
   with their states, and nodes. Mutations come after, each through the same API the
   CLI uses (rule 8).
6. The UI is served at `/` on the API listener; `/bohux.*` (the API), `/hooks/`, and
   `/healthz` keep their paths. The SPA's routes are under `/ui/`.

## Alternatives considered
- **Session cookies from a login RPC.** HttpOnly cookies hide the credential from
  scripts, but bring CSRF, a session table, and an auth path only browsers use. Right
  once there are user accounts or SSO; premature with API tokens only. Revisit then.
- **`localStorage`.** Survives restarts, which is the problem: a token left in a shared
  browser. Lost.
- **OAuth device flow against the CLI.** No identity provider exists yet. Lost for now.

## Consequences
- Easy: the UI needs nothing from the API it does not already have; a token's role
  bounds what the UI can do, exactly as for the CLI.
- Hard: an XSS bug would expose the tab's token. The CSP, the absence of third-party
  scripts, and React's escaping are the defence; the UI never renders HTML from data.
- Users must create a token to sign in. The docs recommend a `viewer` token with an
  expiry for browsing.

## Revisit when
Bohux gains user accounts or SSO (then: HttpOnly session cookies and CSRF tokens), or a
feature needs the UI to act while no tab is open.
