<!-- ============================================================================
  FLEET ORCHESTRATOR pointer block.
  COPY this file to your REPO ROOT as `AGENTS.md` (Antigravity) AND `CLAUDE.md`
  (Claude Code) -- or, if you already have those, append the block below to them.
  It tells any agent that lands in this repo that the parallel-agent fleet exists
  and how to drive it. (`repo-root-AGENTS.md` and `repo-root-CLAUDE.md` in ralph/
  are identical -- use whichever name your harness reads; both is fine.)
============================================================================ -->

# Parallel-agent fleet (Ralph orchestrator)

This repo has a **fleet orchestrator** in `ralph/` — many coding-agent loops grind on the codebase in
parallel, each in its own git worktree with a hard file scope, while a **refinery** merges their
branches under a gate and a **planner** keeps the backlog file-disjoint.

**Before you run, configure, or reason about the fleet, READ [`ralph/SETUP.md`](ralph/SETUP.md).** It is
the bring-up checklist (config, prompt, lanes) and explains every script. Also see `ralph/README.md`
(single loop + flags) and `ralph/HYBRID.md` (full fleet rationale + troubleshooting).

Key entry points (run from the repo root, PowerShell):
- `./ralph/start-fleet.ps1 -LaneIterations 3 -RefineryIterations 12` — bounded test run.
- `./ralph/start-fleet.ps1 -WithPlanner` — open-ended fleet (Ctrl-C / `stop-fleet.ps1` to stop).
- `./ralph/watch-fleet.ps1` — live dashboard. `./ralph/ralph-status.ps1` — is a loop alive?
- `./ralph/ralph.ps1 -Random` — just the single loop (no fan-out).

The only file with project-specifics is `ralph/fleet.config.ps1` (Root, Base, GateDir, GateCmd, Agent).

## ⚠️ Sacred files — do not destroy
- `ralph/PROMPT.md` and `ralph/PLAN-PROMPT.md` are the user's REAL task files and are **gitignored —
  overwriting or deleting them is unrecoverable**. NEVER `cp`/`Write`/`rm` them to smoke-test. Point any
  test at a throwaway prompt: `./ralph/ralph.ps1 -Prompt "$env:TEMP/ralph-test.md" -Iterations 1 -SkipPermissions:$false`.
- The fleet runs agents UNATTENDED (`--dangerously-skip-permissions`) across multiple worktrees. Only
  run it where you can fully revert via git. Start bounded (`-LaneIterations`) and keep the gate honest.
