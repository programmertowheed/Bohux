# ADR-0022: Credential format, tenant-first lookup, and idempotency records

- **Status:** proposed (owner authorized building on it before acceptance, 2026-10-21)
- **Date:** 2026-10-21
- **Deciders:** Towheed (pending)

## Context
S2 made row-level security load-bearing: every table is `FORCE`d, and a transaction
sees nothing until the store sets its tenant (data-model.md, "Schema rules"). But
authentication happens *before* the tenant is known: a request arrives with an API
token, an agent arrives with a bootstrap token or a client certificate, and the
control plane must find the matching row to learn the tenant. Under the S2 rules that
first read returns nothing.

Two more contract details are owed before the API is built (S3):

- Every mutation takes an `idempotency_key` (api-contract.md, convention 4). A retry
  must return the original outcome, but some responses carry a secret exactly once
  (`CreateApiTokenResponse.secret`, `CreateBootstrapTokenResponse.token`), and rule 3
  forbids storing secret values in Postgres.
- Token secrets need a storage form. Stored hashes must be useless to someone who
  reads the database.

These choices are hard to reverse: token strings end up in users' CI systems and
scripts, and the idempotency semantics are part of the public API.

## Decision
**Credential format.** Every Bohux-issued bearer credential is
`<prefix>_<tenant>_<id>_<secret>`:
`prefix` is `bxa` (API token) or `bxb` (bootstrap token); `tenant` and `id` are UUIDs
as 32 lowercase hex digits; `secret` is 32 random bytes, base64url without padding.
The prefix makes leaked tokens recognizable to secret scanners. The tenant and ID are
not secret.

**Tenant-first lookup.** The control plane parses the credential, scopes a
transaction to the embedded tenant, reads the row by `id`, and compares
`SHA-256(secret)` to the stored hash in constant time. A malformed token, an unknown
tenant, an unknown ID, a hash mismatch, an expired, used, or revoked token all return
the same `UNAUTHENTICATED` error with no detail. Editing the tenant part only moves
the lookup into a tenant where that ID does not exist. Agents on an established mTLS
stream are identified by their client certificate, whose URI SAN
`bohux://tenant/<tenant>/node/<node>` is set by the control plane's CA at registration
and is therefore trusted the same way.

**Hashing.** Token secrets are stored as `SHA-256(secret)`. They are 256 bits of
randomness, so a slow password hash adds cost without adding security.

**Idempotency records.** A new table `idempotency_keys` (tenant-scoped, row-level
security like every table) keyed by `(tenant_id, principal_id, idempotency_key)`
stores: the RPC name, `SHA-256` of the request with every `debug_redact` field cleared
and deterministic serialization, the resulting resource reference and revision ID, and
an expiry (24 hours). It is written in the same transaction as the mutation, so it
exists exactly when the mutation committed. A retry with the same key and the same
request hash returns the resource as of that revision. The same key with a different
hash fails with `FAILED_PRECONDITION`. Responses are never stored; a retried
`CreateApiToken` or `CreateBootstrapToken` returns the metadata without the secret,
which is shown once only (the API documents this).

## Alternatives considered
- **Opaque random tokens with a cross-tenant lookup table (`token_hash → tenant_id`)
  outside row-level security.** Shorter tokens, no tenant in the string. Lost because it
  is the one table without row-level security, and "every table, no exceptions" is the
  rule that makes S2's isolation checkable by a grep.
- **A second connection pool or role with `BYPASSRLS` for authentication only.**
  Keeps tokens opaque. Lost because the control plane would hold a credential that
  reads every tenant, which is what rule 10 and S2's `Open` check exist to prevent.
- **A `SECURITY DEFINER` lookup function owned by a privileged role.** Narrower than a
  second pool. Lost because roles are cluster-global: migrations would need
  `CREATEROLE`, and installs on managed Postgres often cannot grant it.
- **JWTs (signed, self-contained).** No database read per request. Lost because
  revocation then needs a deny list anyway (`RevokeApiToken` must take effect at once),
  and a signing-key compromise mints tokens for any tenant.
- **Argon2id or bcrypt for token hashes.** Standard for passwords. Lost because the
  secrets are 256-bit random values; a slow hash only adds latency to every request.
- **Store the full response for idempotent replay.** Byte-exact replays. Lost on rule 3:
  two responses contain secrets, and responses could later grow more.
- **Hash the raw request.** Simpler. Lost because `SetSecretRequest.value` would be
  hashed, and a low-entropy secret value can be recovered from an unsalted hash.

## Consequences
- Tokens are long (about 115 characters) and reveal their tenant and token IDs to
  anyone who sees them. Those IDs are not secret, and the token is useless without the
  secret part.
- `CreateApiTokenResponse.secret` is truly shown once, including across retries. A
  client that loses the first response must revoke and create again.
- One more table and a migration in S3. Expired idempotency rows need periodic
  deletion. That cleanup is a scheduled job, not part of any deploy path.
- Changing the format later means supporting both formats until old tokens expire;
  the prefix makes that possible (`bxa2_…`).
- The agent CA's URI SAN format becomes part of the agent protocol (ADR-0007) and needs
  the same N-1 care.

## Revisit when
A user needs tokens issued by an external identity provider (SSO, v1.0): then add
OIDC token validation alongside, keeping this format for Bohux-issued tokens.
