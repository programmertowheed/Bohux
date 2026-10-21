# ADR-0024: Node PKI, assignment signing, and secret sealing

- **Status:** proposed (owner authorized building on it before acceptance, 2026-10-21)
- **Date:** 2026-10-21
- **Deciders:** Towheed (pending)

## Context
S4 builds the agent. Three accepted ADRs need key material that does not exist yet:

- ADR-0003: agents dial out over **mTLS**. Something must issue node client
  certificates and a server certificate the agent can trust.
- ADR-0007 / api-contract.md: assignments are **signed by the control plane**, and the
  agent verifies before caching.
- ADR-0008: secret values travel inside the assignment **sealed to the node's public
  key**, and stay sealed in the node's cache.

ADR-0022 fixed the client-certificate identity (URI SAN
`bohux://tenant/<tenant>/node/<node>`). Constraints:

- Rule 3: private keys are secrets and never go in Postgres, logs, or errors.
- Rule 1: nothing here may make a running workload depend on the control plane.
- ADR-0020: a control-plane disk loss must be recoverable from `bohux admin backup`,
  which already has to carry "the node CA".
- v0.1 is one control-plane process on one VPS; Go 1.23 standard library plus
  `golang.org/x/crypto`, which is already in the module graph.
- A node's cache and sealed values outlive control-plane upgrades, so their formats
  are a compatibility promise like the agent protocol.

## Decision
**Where keys live.** The control plane has a data directory (`--data-dir`, env
`BOHUX_DATA_DIR`, default `/var/lib/bohux`). On first start it creates, under
`pki/`, mode 0600, owned by the Bohux user:
`node-ca.key` / `node-ca.crt` (ECDSA P-256, self-signed, 10 years) and
`assignment-signing.key` (Ed25519). `bohux admin backup` includes `pki/` inside its
encrypted archive (ADR-0020); restore puts it back before the control plane starts.

**Agent endpoint and trust bootstrap.** The agent API (`bohux.agent.v1`) is served on
its own TLS listener with a server certificate issued by the node CA. `Register`
needs only server TLS; `Connect` requires a client certificate from the node CA.
`CreateBootstrapTokenResponse` gains `ca_cert_sha256` (additive proto change): the
installer passes it with the token, and the agent pins the CA by that hash on its
first call, then stores the CA certificate it receives (the kubeadm pattern).

**Node certificates.** The agent generates an ECDSA P-256 key and a CSR; `Register`
returns a client certificate valid 90 days whose only identity is the URI SAN. The
agent renews over the `Connect` stream once less than 30 days remain (a later additive
protocol message). Revocation needs no CRL: the control plane is the only verifier
and checks the node's state on every `Connect`. An expired certificate stops only
control-plane contact; workloads keep running (rule 1), and the node re-registers.

**Assignment signatures.** Ed25519 over `"bohux-assignment-v1\x00" || payload`, where
`payload` is the serialized `AssignmentPayload` bytes that travel unchanged.
`key_id` is the first 16 hex characters of SHA-256 of the public key; the agent
receives the public key in `RegisterResponse`.

**Secret sealing.** The agent generates a separate X25519 key pair; its public key is
`RegisterRequest.sealing_public_key`. `Workload.sealed_secrets` is
`nacl/box.SealAnonymous` (X25519, XSalsa20-Poly1305) of a serialized message holding
that workload's key → value map. The private key is a 0600 file in the agent's data
directory. The cache keeps values sealed at rest; they are opened only to render the
provider's secret file (ADR-0008).

## Alternatives considered
- **OpenBao's PKI and Transit engines hold the CA and signing key.** Keys never touch
  disk in the clear, and HA control planes share them for free. Lost for v0.1 because
  every registration and every assignment would then need OpenBao unsealed and
  reachable, OpenBao has no backup story yet (ADR-0020 open item), and the signing
  call would sit on the deploy path. Reconsider with HA (see below).
- **Keys in Postgres, encrypted with a key from config.** Backed up with everything
  else. Lost on rule 3 as written ("never in Postgres") and because the wrapping key
  is then the same 0600 file problem, one level removed.
- **Trust on first use for the agent's first connection.** Simpler install command.
  Lost because the first connection carries the bootstrap token; a man in the middle
  on that one call would own the node.
- **Public WebPKI certificates for the agent endpoint.** No pinning needed. Lost
  because v0.1 may have no public DNS name (ADR-0011 item 9), and client certificates
  still need a private CA.
- **RSA keys.** Universally supported. Lost on size and speed; P-256 and Ed25519 are
  supported by every TLS stack Bohux uses.
- **HPKE (RFC 9180) for sealing.** The modern standard. Lost only because Go 1.23's
  standard library lacks it and adding a third-party HPKE library is more surface than
  `nacl/box`, already vetted and in the graph. Revisit when the module's Go version
  includes `crypto/hpke`.
- **Short-lived node certificates (hours).** Smaller window after theft. Lost because
  an offline control plane would then strand nodes within hours; 90 days with renewal
  keeps rule 1 comfortable.

## Consequences
- The data directory is now state that must be backed up and restored with Postgres;
  losing `pki/` means re-registering every node. The backup runbook must say so.
- Two more config keys (`--data-dir`, and `--agent-listen` for the agent TLS listener)
  and one additive field in `node.proto`.
- Root on the control-plane VPS can read the CA and signing keys, as it can already
  read OpenBao's unseal key (ADR-0020). Stated, not solved.
- Key rotation (signing key, CA) is designed for (`key_id`, CA bundle in
  `RegisterResponse`) but not built in v0.1.
- The sealed-secret format and the signature prefix are part of the agent protocol's
  N-1 promise.

## Revisit when
A second control-plane instance is needed (HA): then the CA and signing key move to
OpenBao (PKI, Transit) so instances share them, by a superseding ADR.
