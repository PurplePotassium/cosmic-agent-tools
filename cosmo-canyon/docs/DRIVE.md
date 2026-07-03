# Drive Cosmo Canyon — from a Claude Code session, with SUBAGENTS (the default mode)

> **This is what "Drive Cosmo Canyon" means for a Claude Code agent.** You (the present agent) ARE the
> orchestrator. You drive the loop with your own judgment + vision, spawning **`Agent`-tool subagents** to do the
> implementation. Do **NOT** launch the in-app Workflow (`cc-loop.workflow.js`) and do **NOT** start the detached
> `claude -p` supervisor — those are the *other* two hosts (see [`../AGENTS.md`](../AGENTS.md) "Three ways to
> drive it"), for hands-off / unattended runs. Here, a human is present, so use it: adapt, look at the screen,
> skip dead ends. That is exactly what the autonomous hosts can't do.

## Why this mode (read once)
The deterministic rails — `preflight` / `sense` / `tick-prep` / `bookkeep` (the SOLE gate/commit authority) —
stay in charge, so there is still no false green: a subagent can't self-attest a pass, because **you** run
`bookkeep`, not the subagent. What changes is the *orchestration + acceptance judgment*: instead of an opus
planner inventing tasks and a headless worker grinding, **you** decide what to do next and **you** verify
feel/visual results by looking at a screenshot. This sidesteps the failure modes an autonomous run hits
(planner↔worker churn, specs mis-routed to the preview-blind engine, specs describing already-built features
that no grader can close) — you just *see* those and handle them.

## Preconditions
- On the `cosmo-canyon` git branch, clean tree. `cd cosmo-canyon` for all commands below (paths are repo-relative).
- ⚠️ **Do NOT run an autonomous host (Workflow / supervisor) at the same time** — the mutex refuses it, and two
  orchestrators on one control plane corrupt it. If one is running, stop it first.
- ⚠️ `bookkeep` and the revert path do `git reset --hard` scoped to `cosmo-canyon/game` — **commit any
  hand-edits to tracked orchestrator/game files before driving** or they get wiped (see AGENTS.md gotcha).

## The loop (repeat until idle/done)

### 1. Preflight — ONCE, at the start
```
node orchestrator/preflight.mjs
```
It prints `{ok,reason}`. If `ok:false`, stop and report the reason (wrong branch / rival loop / etc.).

### 2. Sense — every iteration
```
node orchestrator/sense.mjs --cap 200 --audit-hours 6
```
Parse the SNAPSHOT JSON. The fields you branch on:
- `authorityEmpty:true` → no Ready specs. Tell the user to mark a Spec asset **Ready** in the Asset Browser, then stop.
- `headReadyBead` / `readyCount` → the next ready bead to work (and how many remain). `null` + nothing else → done.
- `blockedIds` → beads the autonomous planner would replan; here YOU judge them (usually: rescope by hand, or
  mark needs-operator, or close if already satisfied).
- `trigger` (`diff`/`blocked`/`topup`/`audit`) → the autonomous hosts would spawn a planner. You usually DON'T
  need one for asset work (asset beads are auto-minted by sense). Only plan when you genuinely need NEW tasks
  toward the spec authority — then either add beads yourself (GUI "Add Task" / append to `control/backlog.json`)
  or spawn ONE `Plan` subagent to propose them. Never loop an opus planner against a dead-end bead.
- `completion.idleBlockedOnHuman` / `toSpec` → honest stop reasons (surface them).

### 3. Work each ready bead — spawn ONE subagent per bead
For a bead with id `<id>`:
```
node orchestrator/tick-prep.mjs --bead <id>      # persists control/.tick.json base anchor — REQUIRED before edits
```
Then spawn a subagent (use `web-game-dev` for game code, else `general-purpose`) with a prompt like:

> Caveman-terse. You are ONE Cosmo Canyon work tick. cwd = `C:/Vibes/cosmo-canyon`. Your ONLY bead is `<id>`
> ("<title>"). Read `orchestrator/tick.md` and follow it EXACTLY for this bead: pick the engine per its rules,
> implement ONE increment within the bead's `files` scope, honor every scope / protected-file / determinism rule,
> then run `node orchestrator/bookkeep.mjs --result work` (the deterministic gate/commit — do NOT run raw git or
> the gate yourself). If the bead is un-actionable by a code edit (feature already implemented / confirm-only /
> mis-classified), run `node orchestrator/bookkeep.mjs --result blocked --needs-operator --reason "<why>"` instead.
> Return the JSON `bookkeep` printed.

You can run several bead-subagents **in parallel** only if their `files` scopes are disjoint; otherwise do them
one at a time (serial is the safe default — it matches `control/config.json mode:serial`). After each, read
`control/status.json` for the outcome, or re-`sense`.

### 4. Verify + close acceptance — YOUR judgment (this is the point of the mode)
- **Image / audio / sim beads** gate deterministically inside `bookkeep` (render-reachability / reached-by-playback
  / a confirmed+mutation-checked sim grader). You do nothing — a green land already flipped them Implemented.
- **Feel/visual specs, or a spec whose feature already exists:** VERIFY it yourself, then CLOSE it:
  1. Capture the screen: `node orchestrator/snapshot.mjs --out snapshots/verify.png` (add `--run 1337` for
     live gameplay instead of the menu). Requires the game dev server on `:8780` (the standalone app's
     `ccEnsureVite`, or `cd game && npm run dev`).
  2. `Read` the PNG and judge honestly (does it render, is it readable, did the intended change land?). Use the
     `visual-critique` approach for anything non-trivial.
  3. If it's good → close it: `node orchestrator/close-satisfied.mjs --id <assetId>` (for a spec satisfied by
     existing code) — this writes the honest operator-attested provenance and commits, flipping it Implemented.
     For a *fresh* feel land that went through the FEEL-REVIEW queue, use the dashboard's feel-confirm instead.
  4. **Surface what you closed** to the user in your reply (which asset, why you judged it satisfied, the
     screenshot) so they can veto. You verified + closed — you didn't rubber-stamp.

### 5. Loop
Re-`sense`. Keep working ready beads. Stop when `headReadyBead` is null and there's no trigger you should act on
(`completion` tells you if it's a clean to-spec finish or idle-blocked-on-human). Report a short summary: what
landed, what you closed, what needs the user.

## Do / don't
- ✅ Use judgment: skip a bead whose feature already exists (close it satisfied), rescope a vague bead before
  spawning a subagent, pick the right subagent for feel vs logic.
- ✅ Let `bookkeep` own every gate/commit/revert. You orchestrate; it decides pass/fail.
- ❌ Don't launch `cc-loop.workflow.js` or `supervisor.mjs` — that's a *different* host and will fight you on the
  mutex. "Drive Cosmo Canyon" is THIS mode.
- ❌ Don't hand-edit `control/` provenance to fake Implemented, and don't touch `game/accept/**`,
  `game/test/{canary,determinism,sim-purity,budget,sim*}.ts`, `game/assets/source/**`, or the gate script —
  the tamper guard reverts it.
- ❌ Don't run an opus planner in a loop against a bead the worker keeps blocking — that's the churn this mode
  exists to avoid; mark it `--needs-operator` or close it satisfied.
