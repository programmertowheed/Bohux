---
name: write-runbook
description: Use when adding or changing an operational procedure for Bohux — incident response, upgrades, restores, node drain, cert rotation, break-glass access. Also use when a failure mode in docs/plan/01-architecture.md gains a handling procedure. Produces a runbook in docs/runbooks/.
---

# Writing a runbook

A runbook is read at 3am by someone who did not write it and is not thinking clearly.
Optimise for that reader. Prose is the enemy.

## Structure

```markdown
# <Procedure name>

**Severity:** sev1 | sev2 | routine
**Expected duration:** <minutes>
**Blast radius:** what breaks if this goes wrong
**Prerequisites:** access, tools, approvals needed before starting

## Symptoms
What the operator is seeing that brought them here.

## Decision tree
If A → go to section 1. If B → go to section 2. Do not make them read linearly.

## Steps
1. One action per step. Exact command. Expected output shown.
2. ...

## Verification
How to confirm it worked. A command, not a feeling.

## Rollback
What to do if the procedure made things worse.

## Escalation
Who to wake, and the specific signal that it is time.
```

## Rules

- **Exact commands, copy-pasteable.** No `<your-value-here>` without saying where to find
  the value.
- **Show expected output.** The operator needs to know whether the step worked.
- **One action per numbered step.** Two actions in one step means one gets skipped.
- **Never "simply" or "just".** If it were simple there would be no runbook.
- **Destructive steps get a confirmation line** stating what is about to be lost.
- Every runbook that touches production must have a Rollback section. No exceptions.

## After writing

- Link from the relevant alert, so it arrives with the page.
- If the procedure can be tested, add it to the drill schedule. Untested runbooks rot.
  This is the same principle as the restore drills in the roadmap: a procedure you have
  not rehearsed is a hope, not a plan.
