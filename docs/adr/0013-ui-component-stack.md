# ADR-0013: UI component stack — shadcn/ui, Tailwind, TanStack, Connect-Query

- **Status:** accepted
- **Date:** 2026-10-02
- **Deciders:** Towheed

## Context
ADR-0012 fixed the shape: a React SPA built with Vite, embedded in the Go binary, calling
only the public API. It did not pick the component and data libraries. Constraints:

- The UI must feel simple and fast for teams without a platform engineer.
- Rule 2 (no lock-in) applies in spirit to our own UI: we should own our components, not
  depend on a library's release schedule for a fix.
- Rule 8: data access goes only through the generated `proto/` client.
- Accessibility (keyboard, screen readers) is required, not optional — enterprise buyers
  ask for it.
- Kiln stores no metrics (vision non-goal), so heavy charting is not a requirement.

## Decision
- **Components:** shadcn/ui (Radix primitives + Tailwind CSS). Components are copied into
  `web/` and owned by us.
- **Styling:** Tailwind CSS with design tokens as CSS variables; light and dark themes.
- **Data:** TanStack Query via Connect-Query, generated from `proto/`. No hand-written
  fetch calls.
- **Routing:** TanStack Router (type-safe routes and search params).
- **Tables:** TanStack Table (deployments, releases, audit log).
- **Forms:** React Hook Form + Zod.
- **Logs:** xterm.js for live log tails (ANSI colors, fast scrollback).
- **Command palette:** cmdk (⌘K), shipped with shadcn/ui.
- **Charts:** Recharts, only for deploy/health summaries.

UX rules the stack exists to serve: every mutating action shows its equivalent CLI
command; every resource page shows its revision history; rollback is one click from any
deployment.

## Alternatives considered
- **MUI (Material UI).** Mature and complete. Lost on bundle weight and a strong visual
  identity that is costly to restyle; fixes wait on upstream releases.
- **Ant Design.** Rich data components. Lost on the same weight/identity cost, and its
  theming fights Tailwind.
- **Mantine.** Good DX and a real component set. Lost because it is a runtime dependency
  we do not own; shadcn/ui gives comparable components as code in our repo.
- **Chakra UI.** Lost on churn: a major-version rewrite makes it a risky base for a
  multi-year project.
- **Headless Radix + custom CSS, no shadcn.** Maximum control. Lost on time: shadcn/ui is
  the same Radix base with styling we would otherwise write ourselves.
- **Plain fetch / SWR instead of Connect-Query.** Lost on rule 8 and type safety: the
  generated client keeps the UI exactly in step with `proto/`.

## Consequences
- We maintain the copied components ourselves; upstream shadcn improvements are pulled
  in deliberately, not automatically.
- Tailwind becomes a build-time dependency alongside Node (build-time only, ADR-0012).
- Exec is not in the UI (ConnectRPC cannot carry bidi streams from browsers;
  `api-contract.md`). xterm.js is used for read-only log tails.
- Contributors need React + Tailwind + TanStack familiarity; all three are mainstream.

## Revisit when
The bundled UI exceeds 2 MB gzipped, or a required accessibility audit fails on
components we cannot fix by editing our copies.
