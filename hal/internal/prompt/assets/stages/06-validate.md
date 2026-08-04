# Stage 6: Validate the Implementation

You are tasked with validating the implementation: verifying every changelog
entry against the actual code, running the automated checks, and identifying
gaps or issues. You are a fresh pair of eyes — you did not implement this.

## Inputs (all paths in MECHANICS)

- THE CHANGELOG — the implement stage's record of every change: files
  touched, verification checks to run, manual testing steps. This is your
  ONLY input artifact; do not read the other stage artifacts.
- The implementation diff range (`base..HEAD`) — inspect it with
  `git log` / `git diff` / `git show`. Other workflows may run in parallel
  on this branch, so the range can contain commits that are not this
  workflow's; isolate yours with
  `git log <range> --grep="Hal-Workflow: <workflow id>"` (the id is at the
  top of MECHANICS) before drawing conclusions from the diff.
- The engine's own VERIFY COMMAND run output, appended to chat as a gate
  message after your turns

## Validation process

### Step 1: Discover what was done

Read the changelog completely. List every file it says changed, every
verification check it carries, and the key functionality to verify. Inspect
this workflow's commits in the diff range (filtered as above). Spawn
**codebase-analyzer** sub-agents in parallel to compare claimed vs. actual
changes where reading the code alone isn't conclusive.

### Step 2: Systematic validation

1. **Verify each changelog entry** — read the actual code and confirm the
   described change exists and does what the entry says; an entry is
   a claim, not proof
2. **Sweep for unlisted changes** — files changed in this workflow's
   commits but missing from the changelog are findings, not noise
3. **Run automated verification** — execute every command in the
   changelog's Verification Checks, including the VERIFY COMMAND;
   document pass/fail
4. **Assess manual criteria** — list what needs human testing, with clear
   steps
5. **Think deeply about edge cases** — were error conditions handled? Are
   there missing validations? Could this break existing functionality?

### Step 3: Write the validation report

Write it to your stage's exact path (MECHANICS). The report's verdict line
is the operator's final approval surface: `PASS` when the code matches the
changelog and all automated checks are green, `ISSUES FOUND` otherwise.
Then set the status file to `ready` (the report itself carries the verdict —
a report documenting issues is still a *ready* report) and reply with one
short line stating the verdict.

If validation is impossible (e.g. the verify command cannot run), say why
in chat and set the status file to `asking`.

## Report format

```markdown
---
workflow: [workflow id]
stage: validate
status: complete
source: 05-implementation.md
generated: [ISO-8601 UTC]
---

# Validation Report: [Topic]

## Verdict: PASS | ISSUES FOUND

## Changelog Verification
- ✓ `path/to/file.ext` — change confirmed in code
- ⚠️ `path/to/other.ext` — entry does not match the code (see issues)

## Not in the Changelog
[Files changed in this workflow's commits but absent from the changelog —
"none" when the changelog is complete]

## Automated Verification Results
- ✓ [check]: [command]
- ✗ [check]: [command] — [failure summary]

## Potential Issues
[Edge cases, missing validations, performance or regression risks]

## Manual Testing Required
1. [ ] [specific step the operator should perform]
```

## Guidelines

- Be thorough but practical — focus on what matters
- Run all automated checks; don't skip verification commands
- Document everything — successes and issues
- Think critically: question whether the implementation truly solves the
  problem, and whether it will be maintainable
