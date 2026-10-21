# ADR-0044: Watch paths — a push webhook deploys only when files under its paths changed

- **Status:** proposed
- **Date:** 2026-10-21
- **Deciders:** Towheed
- **Extends:** ADR-0037

## Context
v0.3 lists "monorepo watch paths: only deploy when files under given paths change". In
a monorepo, several Apps build from one repository (each with its own `subpath`); today
every push to the branch redeploys every one of them. ADR-0037 item 4 has the webhook
read only the ref and the commit. Choices that are costly to change once users set
paths:

1. **What a path means**: prefixes, globs, or regular expressions.
2. **Where the list of changed files comes from**, and what happens when it is
   incomplete.
3. **Which webhook flavors** can have paths.

## Decision
1. **`Webhook.paths`: up to 20 repository-relative path prefixes**, fixed at creation
   like the branch. `services/api` matches `services/api` itself and everything under
   `services/api/`; `go.mod` matches that file. No globs: a prefix is what a monorepo
   directory is, and it cannot be misread. Leading `./` and trailing `/` are dropped;
   absolute paths, `..`, and empty entries are refused. No paths: every push deploys, as
   today.
2. **The changed files come from the push payload**: the union of every commit's
   `added`, `modified`, and `removed`. The push deploys when one of them is under a
   watched path; otherwise the delivery is answered `204` with the reason, like a push
   to another branch.
3. **When the payload cannot be trusted to list every change, it deploys** (fails
   open): no commits listed (a new branch at an existing commit, a force push to an old
   one), a list at GitHub's cap of 2048 commits, or Gitea's `total_commits` larger than
   the list. A redundant deploy is cheap and idempotent; a missed one ships stale code.
4. **Only the `github` flavor** (GitHub and Gitea, whose payloads carry the lists) takes
   paths. A `generic` webhook sends a ref and no file list, so paths on it are refused.

## Alternatives considered
- **Glob patterns** (`services/*/api/**`). More expressive, but `*` versus `**`
  semantics differ between Git hosts, CI systems, and Go's `path.Match`. Lost to the
  plain prefix; globs can be added later as entries containing `*`.
- **Diff the commits in the agent's checkout.** Exact, but the decision would move from
  the API to a build already started. Lost.
- **Ask the Git host's API for the changed files.** Exact even past the caps, but needs
  a host credential and a call per push. Lost; failing open covers the gap.
- **Fail closed on an incomplete list.** Fewer builds, but a large merge could ship
  nothing. Lost.

## Consequences
- Easy: `bohux webhook create api --path services/api --path go.mod`; one repository,
  many Apps, each rebuilt only when its part changes.
- Hard: the proto (`Webhook.paths`), migration 00023, and the receiver now reads the
  commits' file lists (still never anything else of the payload).

## Revisit when
Users need globs or exclusions, or a Git host whose payload lacks file lists.
