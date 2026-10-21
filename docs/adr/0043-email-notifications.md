# ADR-0043: Email notifications — a `mailto:` channel through one SMTP relay per install

- **Status:** proposed
- **Date:** 2026-10-21
- **Deciders:** Towheed
- **Extends:** ADR-0038

## Context
ADR-0038 left email out of deploy notifications because it needs an SMTP relay and its
credentials in the install. v0.2 promises email. Choices that are costly to change once
installs configure them:

1. **How a channel names its recipients**, given `NotificationChannel` already keeps
   its destination as a write-only URL in OpenBao.
2. **Where the relay and its password are configured**, and who may change them.
3. **How mail reaches the relay** without a password crossing the network in clear.

## Decision
1. **A new channel kind, `EMAIL`, whose `url` is a `mailto:` URL** (RFC 6068) with one to
   ten plain addresses: `mailto:ops@example.com,dev@example.com`. No display names, no
   `?subject=` or other headers. It is stored and shown like any channel URL: in
   OpenBao, write-only; the API shows the recipients' domains. Addresses are personal
   data, so keeping them out of Postgres is the right default anyway.
2. **One relay per install, set by the operator, not per project**: control-plane flags
   `--smtp-addr host:port`, `--smtp-from`, `--smtp-username`, and the password from
   `--smtp-password-file` or `BOHUX_SMTP_PASSWORD` (as for OpenBao's token). The
   installer passes them as the control plane App's secrets. Without `--smtp-addr`,
   creating an `EMAIL` channel fails with `FAILED_PRECONDITION`. A project owner chooses
   recipients, never the relay: they cannot aim the control plane at another host.
3. **TLS always**: port 465 is implicit TLS; any other port must offer `STARTTLS`, or
   the send fails before authenticating. The relay's certificate is verified against
   the system roots.
4. **Delivery as ADR-0038**: one activity per channel after the terminal state, the same
   bounded retry, and a failed send never changes the deployment. The message is plain
   text: subject `[Bohux] <project>/<environment>/<app>: deployment <state>`, body the
   same summary as chat channels. Names are slugs and failure reasons are stable codes,
   so nothing secret or user-typed reaches a header.

## Alternatives considered
- **Recipients as a new field in Postgres.** Searchable, but a second storage path for
  a channel's destination and personal data in the database. Lost to reuse.
- **SMTP settings per project, stored in OpenBao.** Flexible, but a project owner could
  then make the control plane connect anywhere on its network. Lost on blast radius.
- **An email API (SES, Postmark) instead of SMTP.** Better deliverability, but one
  vendor; every one of them also speaks SMTP. Lost to the open protocol.
- **Plain SMTP on 25 without TLS.** Some internal relays need it. Lost: credentials and
  failure reasons would cross the network in clear. Revisit on demand.

## Consequences
- Easy: `bohux channel create --kind email` with `mailto:` on stdin; the UI gains the
  kind. Rotating the SMTP password is redeploying the control plane with a new secret.
- Hard: the proto gains `NOTIFICATION_KIND_EMAIL`; the control plane gains four settings.
- Messages can be read by every mailbox they reach, as chat messages already are.

## Revisit when
Someone needs an internal relay without TLS, HTML mail, per-App recipients, or a relay
per project.
