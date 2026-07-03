# Cosmo Canyon — Phase -1 Spike Results (2026-06-30)

Gate question (§13.1): how should the Claude-Code orchestrator loop be hosted?

## Tests (empirical, this box)
- **A — detached `claude -p` background-task lifetime.** Spawned `claude -p` (sonnet) instructed to start
  a 25s background Bash task then immediately end its turn. Result: claude **exited on its own after 21s**;
  the task's side-effect file **never appeared** (not at exit, not +45s later). → a detached `claude -p`
  does NOT keep background work alive; when the model's turn ends, the process exits and kills background
  tasks. A fire-and-forget background Workflow inside `claude -p` would die.
- **B — preview MCP in headless `claude -p`.** Asked a `claude -p` child which preview tools it could call.
  Reply: **NONE**. → the `preview_*` MCP is app/session-scoped; a headless CLI child does not have it.

## Decisions
1. **Host = external thin supervisor + per-tick `claude -p` (NOT background-Workflow-as-loop).**
   A minimal **single-flight supervisor** — a `setInterval` in the Launcher's Node server (same pattern as
   FC's watchdog; no new PowerShell) — spawns ONE `claude -p < tick.md` per tick. Each tick does a full
   orchestration pass (sense → plan|work → gate → bookkeep) for ONE increment using **inline blocking tool
   calls only** (Agent/Bash — never `run_in_background`), then exits. Supervisor repeats, honoring
   `.paused` + a daily/max-ticks cap, never two ticks at once. Robust: bounded sessions (no context bloat),
   natural restart, survives a Launcher restart. **Claude makes every decision → "CC as orchestrator" holds;
   the supervisor is a dumb repeater.** The Workflow tool stays useful WITHIN a tick for parallel fan-out
   (parallel planner candidates; a future parallel-lane fleet) — just not as the long-lived loop host.
2. **Feel/visual verification = headless snapshot + Claude vision critique (NOT preview MCP).** Reuse FC's
   `fc-snapshot.mjs` puppeteer pattern: headless screenshot of the vite dev server → Claude `Read`s the PNG
   + runs `visual-critique`. Image analysis needs no preview MCP, so it works inside a headless tick.
   (preview MCP only when an operator runs a foreground/interactive CC — optional, not in the autonomous loop.)

## Knock-on simplifications
- **§13.3 dissolved** — no long-lived Workflow to resume → `resumeFromRunId` irrelevant; restart = supervisor
  spawns the next tick; the on-disk control plane is the only source of truth (each tick idempotent).
- **§13.14 resolved** — feel-verify never needs preview MCP in the tick.
- **§13.4 simplified** — cost cap = supervisor-enforced daily counter + max-ticks/run; don't rely on the
  `budget` object (a per-tick `-p` doesn't carry it across ticks anyway).

## Net
Both risky unknowns resolved; architecture is robust. **Proceed to Phase 0.**
