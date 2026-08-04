# Stage 4: Create the Plan

You are tasked with turning the approved design into an actionable,
code-level implementation plan with phased checkboxes.

## Steps

### 1. Read the design artifact

Read it (path in MECHANICS) completely — no offsets or limits. Extract the
agreed design decisions, the out-of-scope list, and any open questions.

**If open questions remain in the design, stop**: set the status file to
`asking`, list the open questions in chat, and wait for the operator to
resolve them (they may answer in chat or edit the design artifact — re-read
it when you resume).

### 2. Decide phases

Decompose the work into phases — do not ask the operator to iterate on the
phase structure. Each phase should be a thin vertical slice that compiles.
For each phase decide a name and a one-sentence goal. Keep the phase list
tight; merge anything that does not stand alone.

### 3. Research implementation details

For each phase, spawn parallel sub-agents to gather the specific file paths
and code patterns needed:

- **codebase-locator** — find the exact files that need changing
- **codebase-pattern-finder** — find existing patterns to model the new
  code after
- **codebase-analyzer** — understand the current implementation at the
  specific spots that change

Wait for all sub-agents to complete before writing the plan.

### 4. Write the plan

Write it to your stage's exact path (MECHANICS) with the Write tool. Every
phase's Automated Verification MUST include the workflow's VERIFY COMMAND
(MECHANICS) when one is configured. Do not pause to summarize or ask for
confirmation. Then set the status file to `ready` and reply with one short
line pointing at the artifact pane.

## Plan template

````markdown
---
workflow: [workflow id]
stage: plan
status: complete
source: 03-design.md
generated: [ISO-8601 UTC]
---

# [Feature/Task Name] Implementation Plan

## Overview
[1–2 sentences: what this plan delivers and why]

## Current State
[What exists now, key constraints — with file:line references]

## Desired End State
[Specification of the final state]

## What We Are NOT Doing
[Explicit out-of-scope list, carried from the design]

---

## Phase 1: [Name]

### Goal
[What this phase accomplishes]

### Changes

#### [Component / File Group]
**File:** `path/to/file.ext`
**Change:** [summary]

```language
// specific code to add or modify
```

### Automated Verification
- [ ] Project compiles without errors
- [ ] VERIFY COMMAND passes
- [ ] [other automated check]

---

## Phase 2: [Name]

[same structure — Goal, Changes, Automated Verification only]

---

## Manual Verification (run after ALL phases are complete)

- [ ] [Specific behavior check covering Phase 1]
- [ ] [End-to-end behavior matching the design's success criteria]

## Manual Testing Steps
1. [step]

## References
- Related patterns: [file:line]
````
