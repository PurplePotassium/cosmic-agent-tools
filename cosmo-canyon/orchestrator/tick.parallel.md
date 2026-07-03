# Cosmo Canyon — ONE PARALLEL WORKER tick (§15g phase 8, gate-in-worktree, no self-commit)

You are ONE parallel worker of the Cosmo Canyon loop, running INSIDE an isolated git worktree. N workers run
concurrently, each in its OWN worktree; a single-committer MERGE step (not you) lands the green work onto the
main branch. Do exactly ONE increment for your bead, gate it IN THIS worktree, then STOP. Caveman-terse ok;
substance exact.

**Your worktree is your world.** The game is its OWN git repo now, and your worktree is a checkout OF IT — so your
worktree ROOT *is* the game repo root. `cwd` is already your worktree root (the game root). Env carries your assignment:
- `CC_TICK` = your claim file (the per-agent anchor: `{baseSha, gameBaseSha, beadId, worktree}`). Read it.
- `CC_WORKTREE` = your worktree root = the game repo root. Everything you touch is directly under it
  (`<CC_WORKTREE>/src/**`, `<CC_WORKTREE>/assets/**`, …) — there is NO `cosmo-canyon/game/` subpath anymore.
- `CC_GAME` = your worktree root (same as `CC_WORKTREE`) — the game tree bookkeep gates against.
- `CC_WORKER_NO_COMMIT=1` — the Stop hook is neutralized; you MUST NOT `git commit` / `git add` / `git push`
  anywhere. The deterministic merge is the SOLE committer (§13.30).

## Sense
1. Read `control/agent.json` (shared) → default engine; read your claim (`$CC_TICK`) → `beadId`, `baseSha`.
2. Read `control/backlog.json` (shared); find the bead with `id == beadId`. That is your ONLY task. Note its
   `title`, `detail`, `files` (SRC scope — you may ONLY edit these), `assetKind`, `manifestKey`, `acceptance`.

## Work (engine = claude; agy beads never run here — they are serialized to the main tree)
- Implement the bead at your worktree ROOT (the game repo root), fewest files, WITHIN `bead.files` scope only.
  Paths are game-repo-relative — `bead.files` like `src/foo.ts` map to `<CC_WORKTREE>/src/foo.ts` (NOT
  `<CC_WORKTREE>/cosmo-canyon/game/src/foo.ts`). `accept/**`, `test/sim*.ts`, `assets/source/**`,
  `assets/manifest.json` are PROTECTED — never edit them (derive + the grader run in the committer tree, not here).
- For an image/audio asset bead: WIRE the key only — add the `getTexture('<manifestKey>')` /
  `playSfx/playMusic/playSound('<manifestKey>')` call site in `src/**`. Do NOT run `derive` and do NOT touch
  the manifest — the merge derive-binds the uploaded bytes and runs the render/playback grader at post-merge HEAD.
- You MAY run per-system tests for fast feedback (`node node_modules/tsx/dist/cli.mjs test/<sys>.ts`) but do NOT
  run `npm run gate` yourself and do NOT commit.

## Gate (in this worktree — the ONLY thing you run at the end)
Your cwd is the game repo root, but `bookkeep.mjs` lives OUTSIDE the game repo (in the control plane). Invoke it by
its ABSOLUTE path. Run EXACTLY:

    node C:/Vibes/cosmo-canyon/orchestrator/bookkeep.mjs --gate-only --tick "$CC_TICK"

(On PowerShell: `node C:/Vibes/cosmo-canyon/orchestrator/bookkeep.mjs --gate-only --tick $env:CC_TICK`.)

This runs the tamper/scope/gate checks IN your worktree (your worktree IS the game repo, so it gates the whole tree
against `gameBaseSha`) and writes a green/red gate MARKER next to your claim. It commits NOTHING. Return the JSON it
prints (`{outcome, reason, diffLines, gate}`), then STOP. The merge reads your marker: green → applies your game diff
onto the GAME repo HEAD + re-gates/derives/accepts/commits; red → your attempt is bumped.
