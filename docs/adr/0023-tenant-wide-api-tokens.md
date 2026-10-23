# ADR-0023: API tokens may be tenant-wide; authorization is two-step

- **Status:** proposed (owner authorized building on it before acceptance, 2026-10-21)
- **Date:** 2026-10-21
- **Deciders:** Towheed (pending)

## Context
S1a made an API token's scope exactly one project and one role (ADR-0011: fixed roles
per project). Several v0.1 RPCs have no project: `CreateProject`, `ListProjects`,
`GetTenant`, and all of `NodeService` (nodes are tenant-scoped). With project-only
tokens nobody can call `CreateProject`, so a fresh install cannot be used. v0.1 has no
user login (SSO is v1.0), so tokens are the only principals.

Separately, the permission check needs a home. An interceptor sees the RPC name before
the handler runs, but not which project an `UpdateApp` touches: that needs a lookup
(app → environment → project).

## Decision
1. **An API token with no `project_id` is tenant-wide.** Its role applies to every
   project in the tenant and to tenant-level RPCs. `ApiToken.project_id` and
   `CreateApiTokenRequest.project_id` lose `REQUIRED`; empty means tenant-wide. The
   tenant bootstrap step issues one tenant-wide owner token.
2. **No escalation.** A caller can create a token only with a scope inside its own (a
   project token can create tokens for its project only) and a role no higher than its
   own.
3. **Authorization is two-step.** An interceptor authenticates every call and checks a
   static per-RPC table: the minimum role, and whether the RPC needs a tenant-wide
   token. An RPC missing from the table is denied (fail closed), and a test fails if any
   `bohux.v1` RPC lacks an entry. Handlers then check the resource's project against
   the principal's scope with one helper, before reading or writing it.

## Alternatives considered
- **Make tenant-level RPCs owner-of-any-project.** No contract change. Lost because a
  project owner could then create projects and register nodes for the whole tenant,
  which is tenant-wide power without saying so.
- **A separate tenant-admin token kind.** Explicit. Lost because it adds a fourth
  role-like concept beside ADR-0011's fixed three, when "owner, no project" already
  says the same thing.
- **Users and RoleBindings in v0.1.** The eventual model. Lost on scope: ADR-0011
  keeps SSO and user accounts in v1.0; RoleBinding stays in the API for that.
- **All checks in the interceptor, resolving each request's project there.** One
  place. Lost because the interceptor would need per-RPC knowledge of every request
  shape and an extra query that the handler repeats anyway.

## Consequences
- A leaked tenant-wide owner token is full control of the tenant. The bootstrap token
  is shown once, and the CLI should encourage project-scoped tokens for CI.
- Every handler must call the project check. The static table catches a missing RPC,
  not a handler that forgets the second step, so handler tests must cover a token from
  another project.
- Contract change in `identity.proto` before v0.1 freezes; a nullable column in
  `api_tokens_heads`.

## Revisit when
User accounts and SSO land (v1.0): then RoleBindings carry scope and tenant-wide
tokens become a narrow automation feature, or are superseded.
