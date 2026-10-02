# Vision

## The problem

Deploying software is a solved problem four or five times over, and solved badly each
time. Managed platforms (Vercel, Heroku, Railway) are excellent until the bill or the
constraints bite. Kubernetes is universal and miserable for teams under ~20 engineers.
The self-hosted PaaS tools (Coolify, Dokploy, Dokku, CapRover) are good at the happy path
and run out of road at the exact moment a company starts needing audit logs, canaries,
multi-region, or compliance evidence.

So teams migrate twice: off the managed platform onto a PaaS, then off the PaaS onto
Kubernetes. Both migrations are expensive and neither teaches them anything.

## What Bohux is

A reconciliation kernel with a provider boundary.

You declare desired state. A durable orchestrator reconciles it. A provider translates it
onto whatever compute you actually have — a single VPS today, Swarm next year, EKS when
you are acquired. The spec does not change. The migration becomes a config line.

The platform owns the lifecycle *concepts* (build, promote, deploy, observe, roll back,
audit) and owns almost none of the *implementations* (it does not store metrics, does not
implement a proxy, does not invent a secrets backend).

## Non-goals — read these twice

These are the things that kill projects in this space.

- **Not a Kubernetes replacement.** K8s is a provider target, not a competitor.
- **Not a metrics/logs/traces storage backend.** Emit OTLP, integrate, move on.
- **Not a secrets manager.** Interface to Vault/OpenBao/cloud KMS.
- **Not a CI system.** Integrate with GitHub Actions, GitLab CI, Buildkite. Bohux's build
  layer exists to produce a deployable artifact, not to run your test matrix.
- **Not a Git host, not an IdP, not an APM.**
- **Not "any language, any framework, any cloud" in v1.** It is Docker on one VPS in v1.
  Universality is earned through the provider boundary, over years, mostly by other people.

## Who it is for

The team at the inflection point: 5 to 50 engineers, past "push to Heroku", not yet
willing to hire two platform engineers to babysit a cluster. They have a compliance
questionnaire in their future and they know it.

## How we will know it worked

- A new provider can be written by an outside contributor in a weekend.
- A user can delete Bohux and still have a running, debuggable system.
- "Who changed prod, when, and what did it look like before" is answerable from the data
  model, not from a log grep.
- The platform deploys itself through its own pipeline. If it cannot, it is not ready.
