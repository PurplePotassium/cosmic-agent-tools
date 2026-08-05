# Stage 2: Research the Codebase

You are tasked with researching the codebase to answer the approved question
artifact. This stage is normally non-interactive: read, investigate,
document, then signal ready. The operator may still interject with steering
messages — treat them as course corrections.

## Steps

### 1. Read the question artifact

Read the question artifact (path in MECHANICS) in full — no offsets or
limits. Extract: the refined question, the research areas, clarifications,
edge cases, and any `Files Provided by User` (read those fully too, before
spawning sub-agents).

### 2. Investigate efficiently

Research directly by default. Use a sub-agent
only when a clearly independent research area is large enough to benefit,
and use the smallest number needed. Useful roles are:

- **codebase-locator** — find where relevant files and components live
- **codebase-analyzer** — understand how specific code works
- **codebase-pattern-finder** — find examples of existing patterns
- **web-search-researcher** — only if the question explicitly requests web research; include the links in the final report

Start with locator agents to find what exists, then dispatch analyzer agents
on the most promising findings. Run independent agents in parallel. Tell
each agent what to look for — don't prescribe how to search. Instruct all
agents to describe what exists without recommendations or critique.

**Historical context:** also check prior workflows' research artifacts under
the workflow artifact root (MECHANICS) — earlier `02-research.md` files are
supplementary historical context. Live codebase findings are the primary
source of truth.

Sub-agents are read-only researchers in the same checkout. They must not
create branches or worktrees or edit repository files.

### 3. Synthesize

If you used sub-agents, wait for them before writing. Then compile all findings, connect
them across components, address each research area from the question file,
and **explicitly check each edge case listed there**. Note anything that
remains unresolved.

### 4. Write the research artifact

Write it to your stage's exact path (MECHANICS) with the Write tool. Do not
pause to summarize or ask for confirmation. Then set the status file to
`ready` and reply with one short line pointing at the artifact pane.

## Artifact format

```markdown
---
workflow: [workflow id]
stage: research
status: complete
source: 01-question.md
generated: [ISO-8601 UTC]
topic: "[refined question topic]"
---

# Research: [Topic]

## Research Question
[Refined question from the question artifact]

## Summary
[High-level description of what was found]

## Detailed Findings

### [Research Area 1]
- What exists (`file.ext:line`)
- How it connects to other components
- Current implementation details

## Edge Cases Addressed
[Each edge case from the question artifact, with findings]

## Code References
- `path/to/file.go:123` — description

## Architecture Documentation
[Current patterns, conventions, and design implementations found]

## Historical Context
[Relevant insights from prior workflow artifacts, with paths]

## Open Questions
[Anything that needs further investigation]
```

## Notes

- **Document what IS — describe current state without recommendations,
  critique, or suggestions.** Design opinions belong to the design stage.
- Keep yourself focused on synthesis; sub-agents do the deep reading.
- Prefer direct reading. If sub-agents were used, wait for all of them before synthesizing.
- Always read referenced files fully before spawning sub-agents.
