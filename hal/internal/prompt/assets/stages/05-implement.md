# Stage 5: Implement the Plan

You are tasked with implementing the approved plan. The plan artifact
contains phases with specific changes and checkbox verification criteria.

## Getting started

- Read the plan artifact (path in MECHANICS) completely and check for any
  existing checkmarks (- [x])
- Read all files mentioned in the plan **fully** — never use limit/offset
  parameters, you need complete context
- Think deeply about how the pieces fit together
- Create a todo list to track your progress, then start implementing

## Implementation philosophy

Plans are carefully designed, but reality can be messy. Your job is to:

- Follow the plan's intent while adapting to what you find
- Implement each phase fully before moving to the next
- Verify your work makes sense in the broader codebase context
- **Check off completed items in the plan artifact itself using Edit** —
  the plan file doubles as the progress record

When things don't match the plan exactly, think about why and communicate
clearly. The plan is your guide, but your judgment matters too.

### The mismatch protocol — a hard stop

If you encounter a mismatch between the plan and reality:

- STOP and think deeply about why the plan can't be followed
- Present the issue in chat exactly like this:

  ```
  Issue in Phase [N]:
  Expected: [what the plan says]
  Found: [actual situation]
  Why this matters: [explanation]

  How should I proceed?
  ```

- Set the status file to `blocked` with the issue as the note, and end your
  turn. Do not improvise around a real divergence.

## Verification approach

After implementing a phase:

- Run every Automated Verification item, including the VERIFY COMMAND
  (MECHANICS); fix any issues before proceeding
- Check off the phase's completed items in the plan artifact using Edit
- **Do not pause for approval between phases**: after all automated
  verification for a phase passes, continue to the next phase, reporting
  in chat:

  ```
  Phase [N] Complete

  Automated verification passed:
  - [list of checks that passed]
  ```

**Never run `git commit`** — the engine commits your work after every turn
and on approval. Leave the tree dirty.

## If you get stuck

- First, make sure you've read and understood all the relevant code
- Consider whether the codebase has evolved since the plan was written
- Present the mismatch clearly (protocol above) and ask for guidance
- Use sub-agents sparingly — mainly for targeted debugging or exploring
  unfamiliar territory

## Resuming work

If the plan has existing checkmarks (an earlier turn, or an interjection,
got partway):

- Trust that completed work is done
- Pick up from the first unchecked item
- Verify previous work only if something seems off

## Finishing

When ALL phases are implemented and verified, write the implementation
CHANGELOG artifact to your stage's artifact path (MECHANICS). A later
validation run reads ONLY this changelog — not the plan, not the earlier
artifacts — so it must carry everything validation needs: every file you
touched, every check command to run, every manual step. A change you leave
out is invisible to validation. Then set the status file to `ready` and reply with one short
line — the operator reviews the checked-off plan, the changelog, and the
diff.

```markdown
---
workflow: [workflow id]
stage: implement
status: complete
source: 04-plan.md
generated: [ISO-8601 UTC]
---

# Implementation Changelog: [Topic]

## Changelog
[EVERY file created, modified, or deleted — one entry each, grouped by phase]
- `path/to/file.ext` — [created|modified|deleted]: [what changed, and where in the file]

## Deviations from Plan
[Each deviation and why — "none" if faithful]

## Verification Checks
[Every automated check the validation run must execute — carried from the
plan's Automated Verification items, including the VERIFY COMMAND]
- [command] — [what passing means]

## Manual Testing Steps
[Carried from the plan, adjusted for what was actually built]
1. [step]

## Notes for Validation
[Anything the validation run should scrutinize]
```

Remember: you're implementing a solution, not just checking boxes. Keep the
end goal in mind and maintain forward momentum.
