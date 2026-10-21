# ADR-0036: CLI resource commands — `bohux <noun> <verb>`, JSON from the API's own messages

- **Status:** proposed
- **Date:** 2026-10-21
- **Deciders:** Towheed

## Context
The CLI has the deploy loop (`deploy`, `rollback`, `scale`, `logs`, `exec`) but cannot
create a project, an environment, an App, a route, or a secret: the installer and the
self-deploy test call the API with `curl`. Rule 8 says the UI (v0.2) gets nothing the
CLI lacks, so the CLI must cover the API first. Command names, flags, and output are a
contract scripts depend on, and costly to change once people use them.

## Decision
1. **Resources are `bohux <noun> <verb>`**: `project`, `env`, `app`, `route`, `secret`,
   `node`, each with `list` and the verbs its API has (`create`, `show`, `add`,
   `remove`, `set`, `token`). The deploy-loop verbs stay top-level, as today. Nouns are
   singular; `list` is a verb, never a plural noun.
2. **Scope flags are the deploy loop's**: `-p` project (or `BOHUX_PROJECT`), `-e`
   environment; an App is the one positional argument after the verb.
3. **Output:** a plain, aligned table for people; `-o json` prints the API response
   message as protojson (proto field names), so the machine format is the API's own
   contract (`proto/` is the source of truth, rule 9) and needs no second schema.
4. **Secret values come only from stdin** (`bohux secret set KEY -p -e < file`), never
   from an argument or an environment variable, so they stay out of shell history and
   process listings (rule 3). The CLI prints the handle, never the value.
5. **Every mutation sends a fresh idempotency key**; `--idempotency-key` sets it, so a
   script can retry a call safely (ADR-0022).
6. **No interactive prompts.** A missing required flag is a usage error with exit
   status 2, as today.

## Alternatives considered
- **`bohux <verb> <noun>`** (`bohux create app`). Reads as English, but groups help by
  verb, and the nouns' verbs differ (`route add`, `node token`). Lost on discoverability:
  `bohux app` lists what an App can do.
- **A config file of desired state (`bohux apply -f`).** Declarative and reviewable, but
  it is a second model beside the API with its own diff semantics. Not lost forever:
  it can be built on these commands later.
- **Secrets from `--value`.** Convenient, but leaks into history and `ps`. Lost.
- **A CLI-specific JSON shape.** Friendlier field names, but a second schema to keep in
  step with `proto/`. Lost.

## Consequences
- Easy: everything the installer and tests do with `curl` becomes a CLI command; the
  JSON output is stable exactly as long as the API is.
- Hard: the CLI grows a command per API verb, each with a test; table columns are a
  soft contract (scripts should use `-o json`).

## Revisit when
The UI or a user asks for a declarative `apply`, or a noun needs a verb its API lacks.
