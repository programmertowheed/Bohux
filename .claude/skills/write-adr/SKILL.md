---
name: write-adr
description: Use before implementing any hard-to-reverse decision in Kiln — a new dependency, a data model shape, a protocol or interface change, a storage engine, an orchestration mechanism. Also use when a task's design is being re-litigated and the reasoning needs to be recorded. Produces a numbered, immutable ADR in docs/adr/.
---

# Writing an ADR

ADRs exist so the same argument is not had three times a year. They are **immutable**:
once accepted, never edited. Changed your mind? Write a new one that supersedes it.

## When

Write one before implementing if the decision is expensive to reverse:
- a new runtime dependency or service
- a data model shape others will build on
- an interface or wire protocol
- anything that touches the rules in CLAUDE.md

Do **not** write one for reversible choices. A library you can swap in an afternoon is a
code comment, not an ADR.

## Steps

1. `ls docs/adr/` and take the next number. Never reuse one.
2. Copy `docs/adr/0000-template.md`.
3. Filename: `NNNN-short-kebab-title.md`.
4. Fill it in. Status starts `proposed`, becomes `accepted` on merge.
5. **Alternatives considered is the whole point.** For each rejected option, name the
   specific reason it lost. "We preferred X" is not a reason. "Rejected on the resource
   budget" is. If you cannot articulate why an alternative lost, you have not finished
   thinking.
6. Be honest in Consequences about what gets harder. An ADR with only upsides is marketing.
7. Fill in **Revisit when** with a concrete, observable signal — a measurement, a count,
   a user report. "If it becomes a problem" is not a signal.
8. If this supersedes an earlier ADR, update the old one's Status line only. Change
   nothing else in it.
9. Link it from `docs/plan/01-architecture.md` if it affects the architecture.

## Smells

- Writing the ADR after the code is merged — it is then a justification, not a decision.
- An alternatives section with one entry.
- Burying an architectural decision inside a feature PR (CLAUDE.md forbids this).
