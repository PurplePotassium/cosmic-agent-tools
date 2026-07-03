# Cosmo Canyon — how the system works (READ THIS before touching any wiring)

> **What it is, in one paragraph.** Cosmo Canyon is an autonomous, **spec-authority-driven** game-dev loop with
> **Claude Code as the orchestrator**. The loop's north-star is the **set of Ready Spec assets** (§15b, phase 7):
> a deterministic `spec-compile.mjs` compiles them → `control/spec-doc.md`. **There is NO GDD, no document import,
> no splitter — the Ready Spec set is the ONLY authority** (create/curate specs directly in the Asset Browser). A
> thin **supervisor** spawns
> ONE `claude -p` process per "tick"; each tick reads the compiled spec authority + a task backlog, then either
> **plans** (opus invents the next tasks) or **works** ONE task — routing headless logic to **agy** (free Gemini)
> and feel/visual to **Claude** (headless screenshot + *looks* at it) — and a **deterministic script gates +
> commits or reverts**. It builds a fresh survivorslike in `game/`. It is the CC-native successor to **Fort
> Condor** (`ralph/fortcondor/`). **Status: the §15/§15g build is COMPLETE — phases 0–8 landed + verified. Phase 8
> (PARALLEL validation) is FLIPPED: `control/config.json` mode=parallel, maxConcurrency=2 — a cycle claims a disjoint
> set of assets, each worker gates in an isolated `C:/Vibes-cc-wt/<id>` worktree (`bookkeep --gate-only`, commits
> nothing) and a single-committer merge lands each green onto HEAD serialized (re-gate + acceptance + derive at the
> seam). Set `mode:"serial"` to fall back to the byte-for-byte N=1 path.** Design + per-phase log: [`PLAN.md`](PLAN.md).
> Spike decisions: [`SPIKE-RESULTS.md`](SPIKE-RESULTS.md).

> ⭐ **"Drive Cosmo Canyon" = drive it from THIS Claude Code session by spawning `Agent`-tool SUBAGENTS** (the
> recommended default — mode A under "Three ways to drive it"), NOT the in-app Workflow and NOT the detached
> `claude -p` supervisor. The present agent orchestrates with its own judgment + vision; the deterministic
> `bookkeep.mjs` still owns every gate/commit. Playbook: [`docs/DRIVE.md`](docs/DRIVE.md).

## Three repos (⚠️ the GAME is now its OWN repo — §SPLIT 2026-07-03)
- **The GAME** lives in its **own nested git repo**: `C:\Vibes\cosmo-canyon\game\` (own `.git`, branch `main`,
  **gitignored from the C:\Vibes parent**). It is the artifact the loop builds — independent history, shareable/
  publishable on its own. `bookkeep.commitGame()` is its sole committer in serial; the single-committer
  `merge.mjs` in parallel. History was carried over via `git subtree split` at the split.
- **Orchestrator + control plane** live in `C:\Vibes\cosmo-canyon\` on the dedicated **`cosmo-canyon` git
  branch** of the shared `C:\Vibes` repo. ⚠️ Never run the loop on another branch (the supervisor asserts it + refuses).
- **The GUI is a STANDALONE app** (§15g phase 3): `cosmo-canyon\server.mjs` (Node `http`, **:7788**,
  single-instance) serves `cosmo-canyon\ui\index.html` + ALL `/api/cosmocanyon/*` endpoints + the loop-host
  controls (`cc-start`/`cc-stop` spawn/kill the detached supervisor) + `ccEnsureVite`(:8780) + auto-snapshot +
  asset upload + rollback, each carrying its §15i guard. The **Launcher** (`D:\Ag\launcher\`,
  separate repo) keeps ONLY a launch-or-open button (probe :7788 → open-if-up / spawn-if-down).

**The two-repo tick (§SPLIT — the load-bearing change):** every tick now spans TWO repos. Game gate/guard/
revert/commit → the **GAME repo** (anchored on `.tick.json.gameBaseSha`, via the `GAME`/`ggit` seam in
state/bookkeep/supervisor). Control bookkeeping (backlog, completions, status, asset store) + the
`.claude/settings.json` tamper guard → **C:\Vibes** (anchored on `baseSha`). A green land = a `ralph <id>:`
commit in the game repo (its provenance sha) **plus** a control commit in C:\Vibes. `cc-known-good` is tagged in
BOTH repos; **rollback resets the GAME repo only** (control is never auto-reset — it holds asset uploads). The
game is gitignored from C:\Vibes, so the repo-wide Stop hook (`git add -A cosmo-canyon`) naturally skips it.

## Three ways to drive it (same control plane, same deterministic rails) — DEFAULT = this Claude Code session
All three read/write the same `control/` plane and delegate every gate/commit/revert to the same
`bookkeep.mjs`/`plan-apply.mjs` — **the deterministic rails never move; only WHO runs the ticks differs.**

> ⭐ **When a user tells a Claude Code agent to "Drive Cosmo Canyon", that means MODE A below: the present agent
> orchestrates by spawning Claude Code SUBAGENTS (the `Agent` tool) — NOT the in-app Workflow, and NOT the
> detached `claude -p` supervisor.** Full step-by-step playbook: [`docs/DRIVE.md`](docs/DRIVE.md).

Prefer **A** (present, fast, judicious — recommended); **B** for a hands-off in-app run; **C** for
unattended/overnight only (last resort).

### A. This Claude Code session — spawn SUBAGENTS (DEFAULT; the meaning of "Drive Cosmo Canyon") ⭐
The present Claude Code agent IS the orchestrator and drives the loop with its OWN judgment + vision, spawning
`Agent`-tool subagents to do the implementation. NO external process, NO `claude -p`, NO Workflow. Each iteration:
`node orchestrator/preflight.mjs` (once) → `node orchestrator/sense.mjs` (Bash) → for each ready bead:
`node orchestrator/tick-prep.mjs --bead <id>` then **spawn ONE subagent** that follows `orchestrator/tick.md` for
that bead (implement the increment + run `node orchestrator/bookkeep.mjs --result work` — the deterministic
gate/commit authority; the subagent NEVER decides pass/fail or runs raw git). For a feel/visual or
already-satisfied spec, the **driving agent verifies visually** (`node orchestrator/snapshot.mjs` → `Read` the
PNG) and **closes acceptance itself** (`node orchestrator/close-satisfied.mjs --id <assetId>` for an
already-satisfied spec, or feel-confirm for a fresh feel land), surfacing the call so the user can veto.
Deterministic image/audio/sim graders still gate automatically — the agent never fakes those. **This mode
sidesteps the autonomous failure modes** (planner↔worker churn, mis-classified specs, already-built dead-ends)
because a present agent adapts instead of grinding. Exact per-bead subagent prompt + the loop → [`docs/DRIVE.md`](docs/DRIVE.md).

### B. In-app Workflow — **the Claude Code desktop client IS the orchestrator** (you-present, hands-off)
The loop runs as a **Workflow** launched from a desktop chat session (no external process, no `claude -p`).
Loop state (breaker/replan) lives in the Workflow script; each tick is a **spawned agent** (sense=haiku,
work=sonnet, planner=opus) that runs the deterministic glue scripts. Launch it from chat:
`Workflow({scriptPath:"cosmo-canyon/orchestrator/cc-loop.workflow.js", args:{ticks:1, noPlanner:true}})`.
- Glue scripts (agents run these via Bash; they are the deterministic rails, NOT model judgment):
  `preflight.mjs` (boot: branch+mutex+reconcile), `sense.mjs` (→SNAPSHOT), `tick-prep.mjs --bead <id>`
  (persist `.tick.json` base anchor + bump usage), `bookkeep.mjs` (gate/commit/revert), `post-tick.mjs`
  (outcome + known-good tag + agy failover + cleanup), `plan-prep.mjs --mode <m>` + `plan-apply.mjs`.
  Shared core: `state.mjs` (fs/git primitives) + `spec-core.mjs` (`computeSnapshot`+`computeTrigger`, the
  SINGLE source both hosts branch on — 15.22). `sense.mjs` emits the trigger INTO the SNAPSHOT; the
  workflow's `decideTrigger` is a passthrough that adds only the breaker/replan/noPlanner host gates.
- `args` keys: `ticks, cap, breaker, maxReplan, agyFailover, auditHours, noPlanner`. **⚠️ the harness passes
  `args` as a JSON STRING** — `cc-loop.workflow.js` parses it; if you fork the script, keep that parse or it
  silently runs 50-tick defaults.
- **Liveness:** runs only while the desktop app/session is alive (closing it stops the loop). For unattended
  overnight, use B. Stop = set `control/.paused` (the loop breaks at the next sense); `cc-known-good` tracks
  the last green commit; roll back with `git reset --hard cc-known-good`.

### C. External supervisor — detached/unattended (overnight; last resort)
- **From the app (normal):** run `node cosmo-canyon\server.mjs` (or click the Launcher's Cosmo Canyon button,
  which probes :7788 and spawns the standalone app if down) → open `http://localhost:7788/` → in the Asset
  Browser create/curate Spec assets + mark them **Ready** → **Start**. `cc-start` spawns the supervisor detached; `cc-stop` pauses + kills it.
  A running fallback supervisor is flagged by a LOUD pinned banner on the dashboard (§7/§15d).
- **From the CLI (dev/debug):** `node cosmo-canyon/orchestrator/supervisor.mjs [flags]`
  - `--ticks N` (default ∞), `--timeout-min 45`, `--cap 200` (daily tick cap), `--breaker 5`,
    `--model claude-sonnet-4-6` (work-tick driver), `--planner-model claude-opus-4-8`, `--max-replan 2`,
    `--agy-failover 2`, `--no-planner` (Phase-1/2 mode), `--reconcile-only` (just clean up a killed tick).
- Spawns a headless `claude -p < tick.md` per tick (the original design). Same rails as A.

## The architecture (supervisor → tick → deterministic bookkeep)
```
supervisor.mjs (the dumb repeater; loop lives HERE, not in any one process — SPIKE A)
  each cycle: preflight (branch assert 13.24 · cross-system mutex 13.23 · reconcile a killed tick 13.28)
              compute SNAPSHOT (readyCount, blocked, authorityChanged[debounced], readySpecCount, authorityEmpty, auditDue, wipKeywords)
              trigger? (precedence diff>blocked>topup>audit, one/fire, per-mode latch, max-replan≤2 — 13.31)
                YES → spawn PLANNER tick  (claude -p < planner.md  --model opus)  → plan-apply.mjs
                NO  → spawn WORK   tick   (claude -p < tick.md     --model sonnet)
              persist control/.tick.json {pid,startEpoch,baseSha,beadId} BEFORE spawn (13.28)
              wait for exit; read control/status.json for the outcome (NEVER the tick's stdout)
              tag cc-known-good on a green commit (13.40); track breaker/stalled; repeat
```
**The deterministic split is load-bearing (§13.30/13.42):** the model (the `claude -p` tick) ONLY senses +
implements + calls `node orchestrator/bookkeep.mjs --result work|blocked|agy-noop`. **`bookkeep.mjs` is the
SOLE authority** for the gate, the guards, the per-bead acceptance check, and the commit/revert. No model
judgment ever runs git or decides pass/fail — that's why a worker can't self-attest a false green.

## The commit model (the trickiest thing — get this right)
> **§SPLIT (2026-07-03): TWO repos, TWO anchors.** The GAME is its own nested repo now, so bookkeep anchors the
> game against **`gameBaseSha`** (game repo) and the control/settings guard against **`baseSha`** (C:\Vibes).
> `git`/`gitArgs` = C:\Vibes (control); `ggit`/`ggitArgs` = the game repo. `commitGame()` lands the game increment
> (its provenance sha); `commit()` lands the control bookkeeping. The discipline below is unchanged, now per-repo.

This repo's `.claude/settings.json` has a **`Stop` hook** that runs `git add -A && git commit` at the END of
every `claude -p` session. So:
- Each tick is spawned with env **`RALPH_PASS=<bead-id>`** → a clean labeled `ralph <id>:` commit (never amended).
- `bookkeep.mjs` commits explicitly (game repo + control) and leaves **both trees clean**, so the Stop hook then
  finds nothing to add under `cosmo-canyon` — and the game is gitignored, so it can't stage it anyway (no-op).
- **Anchor to the persisted base** (`control/.tick.json`): the game side to **`gameBaseSha`** (via `ggit diff/
  reset --hard $gameBaseSha` in the game repo), the control side to **`baseSha`** (C:\Vibes) — **NEVER**
  working-tree-dirty, **NEVER `HEAD`**. PASS → `commitGame()` the game + `commit()` the control. FAIL/blocked →
  `ggit reset --hard $gameBaseSha` + `ggit clean -fd` (whole game repo, NEVER -x) + surgically revert any
  out-of-allowlist C:\Vibes path, then commit only the attempts/blocked bookkeeping.

## ⚠️ THE operational gotcha that will bite you (cost ~4 redo cycles to learn)
**NEVER run the supervisor (or bookkeep) with uncommitted edits to a TRACKED orchestrator/game file.** The
supervisor's `reconcile` and bookkeep's revert do `git reset --hard` — which **silently wipes your
uncommitted edits**. Always **commit a loop-script change BEFORE running the loop**. Corollaries:
- `git clean` is always **scoped to `cosmo-canyon`** — an unscoped clean would nuke untracked files anywhere
  in the shared `C:\Vibes` tree.
- Runtime markers (`.tick.json`, `.supervisor.pid`, `.plan-*`, `.agy-strikes`, `.stalled`, `.paused`,
  `.authority-consumed`, …) are **gitignored** — if you add a new one, gitignore it, or it makes the tree "dirty"
  every start (spurious reset) and `clean -fd` deletes it.

## Engines (hybrid worker)
- **agy** (free Gemini, `gemini-3.5-flash`) — headless logic/systems/balance. Dispatched via the **own-console
  recipe** (`agy-pass.ps1`, launched with `Start-Process … -File`, **no stdout redirect** or it hangs). Verify
  agy's work by **`git diff $BASE_SHA` only** (its print stdout is uncapturable). It runs in its OWN console
  (NOT the tick's child tree) → its pid is in `control/.agy.pid` so the supervisor/cc-stop can kill it (13.16),
  and a zero-diff pass = quota/auth signal → no attempt bump + strike → auto-failover to claude (13.38).
  (agy "not logged into Antigravity" log lines are a *secondary* service; the model backend auths fine.)
- **claude** (sonnet for work, opus for the planner) — feel/art/visual ALWAYS routes here (agy is
  preview-blind). Feel-verify = `snapshot.mjs` (puppeteer headless shot of :8780) → Claude `Read`s the PNG +
  visual-critique. Pick the default engine in `control/agent.json` (the GUI hot-swaps it).

## What NOT to touch (deterministic guards will revert you)
- `game/test/{canary,determinism,sim-purity,budget,sim}.ts`, `game/accept/**`, the `package.json` gate script
  → **tamper guard** reverts + blocks the bead (workers keep trying to edit `test/sim.ts` to fake a green —
  the guard catches it every time).
- `game/assets/source/**` → **source-art lockout** (13.41); full-res source is sacred (derive regenerates it).
- Anything outside `cosmo-canyon/game/` during a work tick → **scope guard**.
- `src/sim/**` must stay deterministic: NO `Math.random`/`Date.now`/`requestAnimationFrame`/`performance.now`
  (the `sim-purity` gate bans them); use the seeded `Rng`.
- Don't run Cosmo Canyon **and** Fort Condor/Workshop/fleet at once (shared tree). The mutex (13.23) refuses
  cc-start while a rival LOOP is alive. **Known issue:** the Launcher's FC *watchdog* fires FC planners
  independent of FC's worker — stop the Launcher's FC side before a hands-off cosmo run.

## How to add or steer work
- **Add an asset (§15g phase 4 — the PRIMARY input surface):** the dashboard's **Asset Browser** — drop an
  image / audio / spec file → mints a folder-per-asset (`control/assets/<id>/meta.json`, via
  `orchestrator/assets.mjs`). Give it **Instructions** (autosaved, rev-guarded), toggle it **Ready** when it's
  implementable, answer a worker's **Questions**, or replace/tombstone it. `state` is `not_ready|ready` ONLY
  (Implemented is a DERIVED projection computed by `orchestrator/assets-core.mjs` — bead in completions ∧
  acceptance PASS ∧ img/audio manifest real, bound to contentHash+rev; `/assets/list` returns it as `implemented`).
  The endpoints are `/api/cosmocanyon/assets/{list,file,get,create,replace,instructions,answer,state}` +
  `DELETE /assets` + `GET /active`. **The asset→bead scan/projection (§15g phase 5) is LIVE:** a `Ready+dirty`
  asset auto-mints a bead (`asset-<id>-r<rev>`) the loop works; a worker that can't proceed runs `ask.mjs` +
  `bookkeep --result unsure` → the asset gets a **Questions** badge (state STAYS ready, no attempts++); on a
  green land bookkeep flips the DERIVED Implemented. **Image/audio now reach REAL Implemented (§15g phase 6):**
  bookkeep derive-binds the upload (`derive.mjs`/`derive-audio.mjs` copy the bytes → game source + manifest and
  flip it `real`, serialized on the manifest lock) then runs the auto-minted deterministic grader — image =
  render-reachability (`getTexture('<key>')` referenced in `src/**` + manifest real + atlas frame + flipbook
  frames differ), audio = reached-by-playback (`playSfx/playMusic('<key>')` referenced + audio-manifest real +
  decodable). A no-op/still-placeholder or never-wired asset FAILS acceptance (no false green). A fresh upload
  gets a deterministic `manifestKey` at create (`parse-instructions.mjs`), so it can flip Implemented end-to-end.
  **Spec assets:** sim-checkable → a planner grader that lands DISABLED until you confirm (`/grader-confirm`) +
  passes a mutation-check (must FAIL on the unimplemented BASE) + emits an `ACCEPT-PASS <bead>` token; feel/visual
  → the **FEEL-REVIEW queue** (critic verdict is ADVISORY only — a model never lands a green; you confirm via the
  dashboard's Feel-review card / `/assets/feel-confirm`). Re-opening an Implemented asset (`state`→`not_ready`)
  runs the full cross-authority invalidation (`asset-reconcile.mjs`): clears provenance, supersedes the
  completion, bumps rev (the regenerated grader binds the new rev), flags `placeholderStale` — and NEVER
  auto-deletes shipped code (the rework bead overwrites/re-derives the asset directly; no code-review suggestion
  is minted — see the NO code-review suggestions rule below).
- **Add a task by hand:** GUI "Add Task" box, or append a bead to `control/backlog.json` (schema in
  [`PLAN.md`](PLAN.md) §2). For independent acceptance (13.42), add `acceptanceCmd` pointing at a grader under
  `game/accept/<id>.ts` (you write it as the operator — the worker can't read/edit it).
- **Authority = the Ready Spec set (§15b, phase 7).** Mark a Spec asset **Ready** in the Asset Browser to make it
  authoritative (`spec-compile.mjs` → `control/spec-doc.md` = the planner north-star; a Not-Ready spec is EXCLUDED
  = the WIP wall). No Ready specs → `authorityEmpty` → the loop idles (never invents junk). A Ready-toggle/edit
  fires a **debounced** `diff` plan (~90s settle, so a curation burst = ONE replan). Retiring the last Ready spec
  needs confirm (drains authority → the loop idles).
- **Steer priority:** the GUI "Steer" box → `control/focus.md` (a hint atop the authority; never overrides it).
- **Design changes** (new mechanics) go to the **suggestions queue** (the only human gate) → accept→a spec / reject→memory.
- **⛔ NO code-review suggestions (system rule).** The operator does NOT review code. No agent may mint a suggestion
  that asks a human to **review / audit / remove already-shipped code** ("review shipped code", "code review",
  "remove shipped code", etc.). This is enforced in THREE places: (1) `asset-reconcile.mjs` reopen no longer mints a
  "review shipped code" suggestion (it just supersedes stale beads); (2) `planner.md` instructs the planner to emit
  only NEW-design/content `suggestionOps` and never a code-review/removal one; (3) `plan-apply.mjs` has a defensive
  `isCodeReviewSuggestion` filter that DROPS any such suggestion (logged as `code-review-dropped`) even if a planner
  ignores the prompt. Genuine new-design suggestions ("author a shop spec", "add a dash", even "remove button") pass —
  only requests to review/remove *code* are dropped. Obsoleted work is handled by `cancel`-ing beads + letting rework
  overwrite the asset, never by a human code-review gate.
- **NO GDD / document import.** There is no `/gdd` endpoint, no splitter, no `HUMAN_GDD.md` seed. The Ready Spec set
  is the sole authority; author specs directly in the Asset Browser (drop a spec file or create one, edit
  Instructions, mark Ready). WIP `(WIP, DO NOT IMPLEMENT)` keywords stay a secondary forbidden filter.

## The gate (the commit bar)
`cd game && npm run gate` = `tsc --noEmit && tsx test/{canary,determinism,sim-purity,sim,budget}.ts`.
Render-free, fast, deterministic. This is THE commit gate for both engines. Green before anything lands.

## Quick file map → [`README.md`](README.md). Full design + per-phase build log → [`PLAN.md`](PLAN.md).
Cross-harness pointer (Antigravity etc.): `D:\Ag\knowledge\cosmo_canyon_system\`.
