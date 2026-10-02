# ADR-0018: MCP server is a generated client of the public API

- **Status:** accepted
- **Date:** 2026-10-02
- **Deciders:** Md Towheedul Islam

## Context
Teams increasingly drive deploys from AI coding agents (Claude Code, Cursor, etc.) via
the Model Context Protocol. Competing platforms expose MCP servers. Bohux has no plan for
one. Any agent-facing surface must obey rule 8 (no capability beyond the public API),
rule 3 (no secret values leak), and rule 10 (every action authorised and audited).

## Decision
Bohux ships an MCP server in v0.2 as the `bohux mcp` subcommand of the CLI binary,
speaking MCP over stdio and authenticating to the control plane with the user's existing
CLI token. It is purely a client of the public ConnectRPC API, exactly like the CLI and UI.
MCP tools are generated from `proto/` RPCs (rule 9), not hand-written; the token's role
(`viewer`/`deployer`/`owner`) limits what succeeds, enforced server-side as for any client.
Mutating and destructive RPCs are annotated in `proto/` so the generated tools carry MCP
`destructiveHint`/`readOnlyHint`, letting clients ask the human before acting. Secret
values are never returned; `SetSecret` is not exposed as an MCP tool, so values never pass
through model context. A remote HTTP MCP endpoint on the control plane is deferred.

## Alternatives considered
- **Remote HTTP endpoint on the control plane first.** Requires the MCP OAuth
  authorization flow and a new public listener; more surface on the control plane for v0.2.
  Rejected for now; stdio reuses existing token auth with zero server changes.
- **Separate `bohux-mcp` binary.** Another artifact to version, sign and install for no
  capability gain. Rejected.
- **Hand-curated tool set.** Drifts from the API and becomes a second contract to
  maintain; violates the spirit of rule 9. Rejected. Tool descriptions may be refined via
  proto comments.
- **Read-only tools only.** Safer, but agents that cannot deploy or roll back give little
  value; server-side RBAC and audit already bound the risk. Rejected.
- **Ship in v0.1.** Reopens ADR-0011's fixed scope. Rejected.

## Consequences
- No new server-side code path: audit events record the token's principal, tagged with
  client `mcp` via the user-agent, so agent actions are attributable.
- An AI agent with a `deployer` token can deploy and roll back production. Docs must
  recommend scoped, short-lived tokens for agent use.
- Generator must map proto annotations to MCP hints; `proto/` gains a read-only/
  destructive annotation (contract change, done in proto first).
- Excluding `SetSecret` means secret entry stays a human action via CLI/UI.
- We take a dependency on an MCP Go SDK; it is swappable (protocol is the contract).

## Revisit when
A user needs MCP from a context without the CLI installed (hosted agents, browser-based
assistants), which triggers the remote HTTP endpoint with OAuth.
