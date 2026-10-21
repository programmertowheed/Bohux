# ADR-0041: Private images and Git sources — credentials are environment secrets, used once and dropped

- **Status:** proposed
- **Date:** 2026-10-21
- **Deciders:** Towheed

## Context
v0.2 promises private registry credentials and SSH keys for Git sources. Today an App
can run only a public image or build from a public repository (ADR-0028). Credentials
must obey rule 3 (never in Postgres, logs, or errors), reach only the node that needs
them, and must not leak into the App's own environment, where any process in the
container could read them. Choices that are hard to change once users store them:

1. **Where a credential lives** and how an App names it.
2. **How it reaches the node** and the tool that uses it (`docker compose pull`,
   `buildx`).
3. **How long it exists on the node.**

## Decision
1. **A credential is an ordinary environment secret** (ADR-0008: OpenBao, by handle),
   set with `bohux secret set` like any other. An App names it by key:
   - `App.image_pull_secret`: a secret holding a Docker `config.json` (the
     `{"auths": {...}}` document `docker login` writes), the format every registry
     documents;
   - `GitSource.ssh_key_secret`: a secret holding a private deploy key, for `ssh://` or
     `git@` repository URLs.
2. **It travels sealed, apart from the App's environment variables**: the control plane
   resolves it with the workload's other secrets and seals it to the node's key in its
   own field of `SecretValues` (`image_pull_config`, `ssh_key`), never in the map the
   provider turns into the container's environment (ADR-0024).
3. **It exists on the node only while used**: the Docker provider writes the config to
   a fresh `0700` directory, runs `docker compose` with `DOCKER_CONFIG` pointing at it,
   and removes the directory when the command ends; the agent's build passes the key to
   `buildx --ssh default=<file>` the same way. Nothing is written to the daemon's or
   root's Docker config, and the artifact on disk (rule 2) names the image, not the
   credential.
4. **Validation**: the referenced key must be a secret of the App's environment
   (`UNKNOWN_SECRET` otherwise), as for environment variables today.

## Alternatives considered
- **A `RegistryCredential` resource** (server, username, password). More structure, but
  a second secret store path, and still a `config.json` on the node. Lost to reuse.
- **`docker login` on the node.** Simple, but the credential persists in root's config
  for every workload and every later pull. Lost on blast radius.
- **Inject as environment variables.** What the existing secret map does; the container
  would read its own registry password. Lost.

## Consequences
- Easy: one familiar format, set once per environment, rotated like any secret (a new
  value reaches the node with the next generation).
- Hard: proto changes (`App.image_pull_secret`, `GitSource.ssh_key_secret`,
  `Workload.image_pull_secret`, `SecretValues.image_pull_config`/`ssh_key`,
  `BuildRequest` key); the provider gains a credential input (`Spec.PullConfig`).
- After a node restart, a private image the node no longer holds is pulled with the
  credential sealed in the cached assignment (rule 1 holds).

## Revisit when
A registry needs short-lived tokens (ECR, GCR) that must be refreshed on the node, or
users want one credential across environments.
