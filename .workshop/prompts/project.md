# Project rules — workshop editing workshop

You are improving the very tool that is running you. The binary executing this
loop is an older build; you edit the SOURCE only. Discipline matters more here
than in a normal repo.

## Scope

- Edit **only** files under `workshop/`.
- `.workshop/**` is **OFF-LIMITS** — never edit the config, this file, GOAL.md,
  or anything else in that directory, even if a task seems to ask for it. If a
  task requires it, report the task blocked instead.
- `ralph/`, `skills/`, `docs/`, and the repo-root README are read-only context.

## Required reading before you touch code

- `workshop/AGENTS.md` before any driver/process change (agy's blind-driver
  facts and Windows console/job-object behavior live there).
- The package you're editing, end to end — this codebase is small enough.

## The gate

The verify command builds the binary, runs all unit/integration tests, and
runs the e2e suite (`go test -tags e2e ./e2e`) that drives the REAL binary
against scaffolded repos.

- **Never weaken the gate to get green**: do not delete, skip, tag-out, or
  loosen an existing test or assertion so a failing pass can succeed. Fix the
  code. If you believe a test itself is wrong, report the task blocked and say
  why in your self-report.
- New behavior needs a test in the same pass, in the style of the surrounding
  suite. Engine behavior belongs in the fake-agent harness; orchestrator-level
  behavior belongs in `e2e/`.
- Keep the e2e suite hermetic: temp state dirs, temp git config, no real
  agents, no network.

## Style

- Match the existing idiom: table-lite tests, terse doc comments explaining
  WHY, errors that tell the operator what to do next.
- One small verified increment per pass. If a task is too big for one pass,
  do the first coherent slice and propose the rest as follow-up tasks.
