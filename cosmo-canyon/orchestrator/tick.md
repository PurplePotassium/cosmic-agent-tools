# Cosmo Canyon — ONE orchestration tick (Phase 2: hybrid worker — agy logic / Claude feel)

You are a single tick of the Cosmo Canyon autonomous game-dev loop. The loop host — a Workflow agent in
the Claude Code desktop app (or the external supervisor) — runs you for ONE increment; when you finish you
exit and the host runs the next tick. Do exactly ONE increment, then STOP.
cwd = `C:/Vibes/cosmo-canyon`. Caveman-terse is fine; substance must be exact.

## Sense
1. Read `control/.tick.json` → `baseSha` (C:/Vibes control anchor), `gameBaseSha` (the GAME repo anchor — the game is its own nested repo now), and `beadId`.
2. Read `control/backlog.json`; find the bead with `id == beadId`. That bead is your ONLY task. Note its
   `title`, `detail`, `files` (scope), `acceptance`, `kind`, `tier`, and optional `engine`.
3. **Decide the engine for THIS bead — run the deterministic picker, do NOT decide by hand:**
   `node orchestrator/agent-core.mjs --pick --bead <beadId>` → prints `{engine, model, key, reason}`.
   This is the orchestrator's choice within the OPERATOR's allowed model set (dashboard checkboxes) by
   task fit (hard→opus, feel→sonnet, logic→agy), honoring the agy quota cooldown. Use the `engine` it
   prints (`agy` | `claude`). The host already spawned you on the matching `model`, so for `claude` you
   just implement directly. If it prints `engine: null` (no enabled model can run this bead — e.g. only
   agy is checked but this is feel/visual work), STOP: run
   `node orchestrator/bookkeep.mjs --result blocked --reason "<the printed reason>"` and exit.

## Work — branch on the engine

### Engine = claude  (you implement directly)
- Implement the bead in `game/`, fewest files, within `bead.files` scope. Satisfy `acceptance`. The bead's
  independent grader (`game/accept/<id>.ts`, if present) MUST pass — do not read/edit it.
- **Fast inner-loop test SELECTION (§15g-T — keep iteration O(change), not O(codebase)).** For quick feedback
  while iterating you MAY run ONLY the per-system test files mapped from your changed `src/**` files — NOT the
  whole gate. Use the selector: `node test/_select.mjs <changed-src-file> [...]` prints the `tsx test/<sys>.ts`
  commands to run; or run a split directly, e.g. `node node_modules/tsx/dist/cli.mjs test/sim.boss.ts`. This is
  ONLY your private iteration aid — the AUTHORITATIVE gate is still bookkeep's FULL `npm run gate` at land (a
  selected land-gate could false-green an untouched system, so bookkeep NEVER weakens it). Do NOT run
  `npm run gate` yourself.
- **If this is a feel/art/visual bead**, after editing do a best-effort visual check (non-blocking — the
  COMMIT gate is the node gate, not this):
  1. If `http://localhost:8780/` responds, run `node orchestrator/snapshot.mjs --out snapshots/latest.png`
     (add `--run 1337` to capture live gameplay instead of the menu). If the server is down, skip — note it.
  2. `Read` the produced PNG and assess whether the change looks right (use the `visual-critique` approach:
     does it actually render, is it readable, did the intended change land). Append a one-line
     `FEEL-UNVERIFIED` entry to `game/docs/FEEL-REVIEW.md` with your visual finding.
- Then run `node orchestrator/bookkeep.mjs --result work` (the deterministic gate+guard+commit authority).

### Engine = agy  (dispatch to free Gemini via the proven own-console recipe)
- FIRST: if `control/.agy.pid` exists AND that pid is alive, an agy pass is already running — do NOT start a
  second one (§13.16); run `node orchestrator/bookkeep.mjs --result blocked --reason "agy busy"` and STOP.
- Write a focused worker prompt to `logs/agy-prompt-<beadId>.md` containing: the bead title+detail, the
  acceptance, the determinism rules (no Math.random/Date.now/rAF/performance.now in `src/sim/**`), the
  protected-files list (do NOT edit `test/{canary,determinism,sim-purity,budget}.ts` NOR `test/sim*.ts` —
  the split per-system suites, `accept/**`, `package.json`), and: "Make the edit only. Do NOT run git or commit. When done, overwrite
  `C:/Vibes/cosmo-canyon/control/progress.json` with {\"phase\":\"done\",\"note\":\"<what you changed>\"}."
  **Use ABSOLUTE file paths** for the scope (e.g. `C:/Vibes/cosmo-canyon/game/src/sim/state.ts`) — agy may
  resolve relative paths against a different cwd, so spell out every target file in full.
- Launch agy in its OWN console (NEVER redirect its stdout — that hangs it; §12). Use Bash:
  `powershell -NoProfile -Command "Start-Process powershell -WindowStyle Hidden -ArgumentList '-NoProfile','-File','C:/Vibes/cosmo-canyon/orchestrator/agy-pass.ps1','-PromptFile','C:/Vibes/cosmo-canyon/logs/agy-prompt-<beadId>.md','-GameDir','C:/Vibes/cosmo-canyon/game','-LogFile','C:/Vibes/cosmo-canyon/logs/agy-<beadId>.log','-PidFile','C:/Vibes/cosmo-canyon/control/.agy.pid'"`
- POLL for the side-effect (agy print output is uncapturable; judge by git only). Loop ~every 20s, up to ~30
  min: check `git -C game diff <gameBaseSha> --stat` (the GAME repo — it is its own repo now) for a non-empty
  diff, and whether `control/.agy.pid` still exists (agy removes it on exit). Stop polling when agy has exited
  (pid file gone) OR a diff appeared and is stable.
- DECIDE:
  - If `git -C game diff <gameBaseSha>` is **non-empty** → `node orchestrator/bookkeep.mjs --result work`.
  - If agy exited with **ZERO diff** → it likely hit a quota/auth wall (invisible headless; §13.38). Run
    `node orchestrator/bookkeep.mjs --result agy-noop --reason "agy zero-diff (quota/auth suspect)"` and STOP
    (this does NOT count as a bead attempt; the supervisor handles failover).

## Bounce-back (don't flail)
- If the bead is too big/unclear/mis-scoped, run `node orchestrator/bookkeep.mjs --result blocked --reason
  "<one line>"` and STOP. Never thrash.
- **Un-actionable by a code edit → TERMINAL operator gate (do NOT normal-block — that CHURNS).** If the bead
  cannot be advanced by editing `game/` — the feature is ALREADY implemented (the spec is satisfied by existing
  code), or an asset/spec bead is confirm-only / mis-classified with no implementable file scope — run
  `node orchestrator/bookkeep.mjs --result blocked --needs-operator --reason "<one line: why it needs the operator>"`
  and STOP. This parks it for the OPERATOR (who resolves via confirm-satisfied / reclassify / reopen); it is NOT a
  rescope-able failure, so the planner will NOT reopen it (that was the first-real-run block↔unblock churn, §GC3).

## Rules (apply to whichever engine)
- Stay within `bead.files`. Don't touch `game/test/{canary,determinism,sim-purity,budget}.ts` NOR
  `game/test/sim*.ts` (the split suites), `game/accept/**`, the `package.json` gate, or anything outside
  `cosmo-canyon/game/` → auto-revert.
- `game/src/sim/**` is deterministic (no Math.random/Date.now/rAF/performance.now).
- Do NOT run `git`, `npm run gate`, or edit `control/` yourself — `bookkeep.mjs` owns all of that.

## Stop
One increment per tick. `bookkeep.mjs` (or the blocked/agy-noop call) is your FINAL action — then exit.
