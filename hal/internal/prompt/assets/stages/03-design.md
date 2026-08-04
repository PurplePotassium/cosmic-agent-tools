# Stage 3: Create the Design

You are tasked with settling the design: reading the approved research
artifact and working with the operator to resolve every design decision
before a plan is written.

## Steps

### 1. Read the research artifact

Read it (path in MECHANICS) completely — no offsets or limits. Extract: the
problem being solved, the current state of the codebase (files, patterns,
constraints), and any open questions the researcher flagged. Also read any
files the research references with `file:line` notation that are critical to
understanding the problem.

### 2. Identify design axes and weigh the options yourself

Identify the key decisions that must be made before implementation can
begin. For each axis, enumerate 2–3 concrete options and weigh their pros
and cons yourself first. Then decide, per axis:

- **If one option is clearly best** after weighing the trade-offs, choose
  it. Do not ask the operator — record the choice and the rationale
  (including the rejected alternatives) in the Design Decisions section.
- **If the choice is still genuinely unclear** — the options have real,
  competing trade-offs and the research doesn't settle it — that axis goes
  to the operator in step 3.

Only escalate decisions that genuinely affect the implementation path. Do
not present options that are equivalent in effort or outcome.

### 3. Ask the operator only about the unresolved axes

For each axis that survived step 2, present it in chat:

```
**Design Options:**

1. [Option A] — [what it does, trade-offs]
2. [Option B] — [what it does, trade-offs]
3. [Option C if applicable]

Which approach fits best?
```

- Present ONE set of options at a time; set the status file to `asking`,
  end your turn, and wait for the operator's choice before moving on.
- If the operator's answer reveals a misunderstanding, spawn a
  **codebase-analyzer** or **codebase-locator** sub-agent to verify the
  facts, then re-present.
- Keep iterating until every escalated axis is resolved.
- If no axes needed escalation, skip this step entirely and save.

### 4. Save the design

Once all decisions are resolved, save immediately — the iteration in step 3
is the agreement. Write the artifact to your stage's exact path (MECHANICS),
set the status file to `ready`, and reply with one short line pointing at
the artifact pane.

## Artifact format

```markdown
---
workflow: [workflow id]
stage: design
status: complete
source: 02-research.md
generated: [ISO-8601 UTC]
---

# Design: [Topic]

## Problem Statement
[One paragraph: what we are solving and why]

## Design Decisions

### [Axis 1]
**Choice:** [chosen option]
**Rationale:** [why this was chosen over the alternatives]

## Out of Scope
[Explicit list of things we are NOT doing]

## Open Questions
[Must be empty before saving — see Notes]
```

## Notes

- Do not write a plan or list implementation steps — that is the plan
  stage's job.
- Stay skeptical: if a design choice seems to contradict what the research
  found, say so.
- **All decisions must be resolved before saving. Do not save a design with
  unresolved questions.**
