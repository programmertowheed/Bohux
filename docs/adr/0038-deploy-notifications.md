# ADR-0038: Deploy notifications — project channels, posted by an activity at the end of the run

- **Status:** proposed; item 3 amended by ADR-0048 (notifications run in their own workflow)
- **Date:** 2026-10-21
- **Deciders:** Towheed

## Context
v0.2 promises notifications on deploy, failure, and rollback (Slack, Discord, email,
webhook). Deploys are Temporal workflows (rule 6, ADR-0026); their end states are
`SUCCEEDED`, `FAILED`, `ROLLED_BACK`, and `CANCELLED`. Choices that are costly to change
once users configure channels:

1. **Where channels live** and who may change them (rule 10).
2. **Where the URL is kept.** A Slack or Discord incoming-webhook URL is itself a
   credential: whoever has it can post to the channel (rule 3).
3. **Who sends, and what a failed send does to the deploy.**

## Decision
1. **A `NotificationChannel` resource per project**: `kind` (`SLACK`, `DISCORD`,
   `WEBHOOK`), the deployment end states it wants (default: failed, rolled back,
   cancelled — the ones that need a human), revisions and audit events like any
   resource. Creating, changing, and deleting one needs the **owner** role: a channel
   receives every App's deploy outcomes in the project.
2. **The URL is a secret**: written to OpenBao, referenced by handle, never returned by
   the API after creation (`url` is input-only). The API shows its host only.
3. **The deploy workflow sends**, after the Deployment's terminal state is written, on a
   disconnected context: one activity lists the project's channels that want the
   state, then one activity per channel renders the message (App, environment,
   Release, state, failure reason, who started it) and posts it, each with a bounded
   retry (5 attempts over about 75 s), so one failing channel is retried alone. A send
   that still fails is logged and dropped: **a notification never changes a
   deployment's outcome**, and the workflow ends regardless.
4. **Formats:** Slack and Discord get their incoming-webhook JSON (`text` / `content`);
   `WEBHOOK` gets a JSON summary (deployment, project, environment, App, Release,
   state, failure reason, who started it), signed with
   `X-Bohux-Signature: sha256=<hmac>` using a per-channel secret shown once, as
   webhooks in (ADR-0037).
5. **Email is not in this ADR.** It needs an SMTP relay and its credentials in the
   install; it gets its own ADR when someone needs it.

## Alternatives considered
- **Send from the API when it reads a terminal state.** Nothing reads every state, and
  a control-plane restart would drop sends. Lost on rule 6.
- **A separate notification workflow per deployment, signalled.** Survives the deploy
  run's end, but adds a second workflow for what one activity at the end does. Lost.
- **Channels per App.** Finer, but most teams want one channel per project. Can be
  added later as a filter on the channel.
- **Fail the deploy when a notification fails.** Couples a deploy's correctness to a
  chat service's uptime. Lost.

## Consequences
- Easy: one owner call wires a project to Slack; every outcome that needs a human is
  posted, and history shows who changed the channel.
- Hard: a new resource with its OpenBao paths; the deploy workflow gains a final
  activity (a workflow change with runs possibly in flight, guarded with
  `workflow.GetVersion`).
- Messages contain App and environment names and failure reasons, never secret values
  (rule 3; failure reasons are stable codes).
- Channel URLs must be `https://`: a project owner cannot aim the control plane at the
  plain-HTTP services beside it (OpenBao, Temporal).

## Revisit when
Someone needs email, per-App channels, or notifications for builds and node events.
