# ADR-0015: Product name is Bohux, published by Bohumix

- **Status:** accepted
- **Date:** 2026-10-02
- **Deciders:** Towheed

## Context
"Kiln" was a placeholder. The name goes into the proto packages, the Go module path,
the CLI binary, and every generated client, so changing it after `proto/` ships breaks
users. A check on 2026-10-02 found "Kiln" already used by a self-hosted deployment tool
(github.com/OthmanHaba/kiln), Kiln AI, Fog Creek's Kiln, and kiln.fi, with the GitHub
names and most domains taken.

## Decision
The product is **Bohux** (from Bangla বহু, *bohu*, "many" — deploy many kinds of app,
anywhere), published by **Bohumix** (bohumix.com).

- GitHub org `bohumix`, repo `github.com/bohumix/bohux`; Go module the same.
- Proto packages `bohux.v1` and `bohux.agent.v1`.
- CLI binary `bohux`; `cmd/bohux/`.

ADRs 0001–0014 say "Kiln"; they are immutable and that name means Bohux.

## Alternatives considered
- **Kiln.** Lost on collision: a directly competing product uses it.
- **CWAOS.** Descriptive ("cloud web application operating system"), but hard to
  pronounce and the GitHub username is taken. Rejected by the owner.
- **Bohuploy / Bohuship / Bohuflow / Bohudock.** All free on GitHub and npm. Lost on
  length (CLI typed constantly), "bohuploy deploy" reading twice, or tying the brand to
  Docker or CI when the product goes beyond both.
- **Plain Bangla words (nouka, bondor, jatra, ghat, tori).** Taken on GitHub, npm, or
  domains.

## Consequences
- Short CLI (`bohux deploy api -e prod`) and a name tied to the company's Bangla root.
- Availability was checked by GitHub/npm lookups and DNS only, not registrar or
  trademark search. Those are owner actions before a public launch.
- The `bohumix` GitHub org must be created before code is pushed.

## Revisit when
A trademark search or a registrar check shows a conflict before public launch.
