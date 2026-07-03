# Cosmo Canyon — Implementation Plan

> 🔀 GAME SPLIT INTO ITS OWN REPO (§SPLIT 2026-07-03): the game at `cosmo-canyon/game/` is now a **separate nested
> git repo** (own `.git`, branch `main`, gitignored from C:\Vibes; history carried via `git subtree split`). The
> tick now spans TWO repos — game gate/guard/revert/commit anchor on `.tick.json.gameBaseSha` via the `ggit`/`GAME`
> seam; control bookkeeping + the `.claude/settings.json` guard stay in C:\Vibes on `baseSha`. `bookkeep.commitGame()`
> lands the game, `commit()` lands control; `cc-known-good` tags both repos; **rollback resets the game repo only**.
> Any §1 topology below that says "game + orchestrator live in C:\Vibes" is PRE-split — the game's git home is now its own repo.

> ⛔ GDD REMOVED (2026-07-02): Cosmo Canyon has NO GDD, no import-splitter, no HUMAN_GDD seed. Authority = the Ready Spec set ONLY. Older entries below may mention a GDD — it no longer exists and must NEVER be reintroduced.

> Autonomous **Spec-driven** game-dev system with **Claude Code as the orchestrator**. A supervisor
> drives **per-tick `claude -p` orchestration passes** (planner + hybrid worker + deterministic gate)
> building a **fresh greenfield** survivorslike. **Authority = the set of Ready Spec assets** (§15b);
> the **Asset Browser** is the primary human surface (§7/§15d). **Cosmo Canyon is a STANDALONE app** (`cosmo-canyon\server.mjs`,
> a dedicated port 7788, own dashboard `ui\index.html`) — the Launcher keeps only a thin
> launch-or-open button (§1/§6). Concurrency is a **runtime toggle** (serial default / parallel
> worktree-isolated), the parallel-safe contracts first-class from phase 1 (§15c-2).
>
> Ported from Fort Condor's proven *design* (bead ledger, acceptance gates, failure-mode fixes,
> asset pipeline) — but **FC / Workshop / the ralph fleet are RETIRED (no longer in use)**; nothing
> shares the loop anymore beyond human WIP + other branches on the shared `C:\Vibes` repo (so the
> tree-wide safety guards still stand — §15.41 — and the cross-system mutex is RETAINED in code as belt-and-braces — §13.23, still enforced in preflight.mjs + supervisor.mjs — even though its FC/Workshop/fleet rivals are retired). The
> *mechanism* is CC-native: deterministic Node glue (`bookkeep.mjs` = SOLE gate/commit authority,
> BASE_SHA-anchored) + spawned agents, hosted by either a detached supervisor or an in-app Workflow.
> The proven agy-from-CC recipe: shared-knowledge `agy_cli_headless_gotchas` §6 (read first, §12).

Status (2026-07-01): the **§15/§15g redesign** (Asset Browser + Spec-authority + parallel toggle +
standalone app) is **COMPLETE — phases 0–8 landed + verified** on the `cosmo-canyon` branch. Parallel
validation is FLIPPED (`control/config.json` `concurrency.mode=parallel`, `maxConcurrency=2`;
worktree-isolated workers gate via `bookkeep --gate-only`, single-committer `merge.mjs` lands each
green serialized at HEAD; `mode:"serial"` is the fallback). The original GDD-authority loop (7 phases,
§14) preceded it. ⚠️ A 2026-07-01 whole-system giga-audit returned **VERDICT=RED / ship-blocked**
despite a green gate+build — see [`docs/AUDIT-2026-07-01.md`](docs/AUDIT-2026-07-01.md) (C1: parallel
mode can't land work — merge markers not gitignored+allowlisted; C2/C3: standalone :7788 binds all
interfaces with no auth/CSRF) — address those before relying on parallel or exposing the port.
Settled decisions in §0; open items in §11.

---

## 0. Settled design (from kickoff)

| Decision | Choice | Consequence |
|---|---|---|
| Game | **fresh greenfield** (new `cosmo-canyon/game/`) | adds a scaffold phase (Phase 0); zero collision with the retired FC/RS-v2 |
| Orchestrator / host | **Claude Code.** PRIMARY = the **desktop app IS the orchestrator** (in-app dynamic Workflow `cc-loop.workflow.js`, §3). FALLBACK = a detached `supervisor.mjs` running per-tick `claude -p` for LOCAL lights-out only | Same ticks + same deterministic rails either way. Prefer the app (live visibility in the tasks pane, one-click stop, no invisible process); the fallback supervisor MUST be flagged LOUDLY in the dashboard when alive (§7/§15d). Unattended → the app's native Routines / cloud sessions |
| Worker engine | **hybrid, selectable** | agy (free Gemini) for headless logic; Claude (sonnet/opus) for feel/visual (headless snapshot + visual-critique) |
| GUI | **STANDALONE app** in `cosmo-canyon\` (`server.mjs` :7788 + `ui\index.html`) | the Launcher keeps ONLY a launch-or-open button (§1/§6/§15d); the retired Fleet/Workshop/FC tabs are gone |
| Authority | **the set of Ready Spec assets** (§15b) | ONE authority — the Ready Spec set; no GDD (removed 2026-07-02) |
| Primary input | **Asset Browser** (§7/§15d) | backlog / suggestions / completions demoted to read-only monitoring |
| Concurrency | **runtime toggle** (`control/config.json`): serial default / parallel worktree-isolated | parallel-safe contracts (per-asset file-ownership, deadlock-free lock order, single-committer merge) are FIRST-CLASS from phase 1, NOT retrofitted (§15c-2) |
| Human gate | **design changes + destructive ops** | spec-retire / asset-delete need operator confirm; everything else autonomous |

**Why CC-as-orchestrator wins (the thesis):** FC hand-rolls in PowerShell what CC gives natively —
crash-resume (`resumeFromRunId` vs `.refinery-state.json`), cost cap (`budget` vs git/disk counter),
typed planner output (`schema` vs parse-or-fail), and **feel-verification** (preview MCP + visual-critique
skill, which agy *cannot* do). Net: less bespoke glue, an orchestrator that can *reason* about the loop,
and visual QA inside the loop for the first time.

---

## 1. Topology & file layout

```
D:\Ag\launcher\                         ← the Launcher (Express :3333) — ONLY a launch-or-open button now
  server.js                               one card: probe :7788 GET /api/cosmocanyon/status → UP: open UI URL;
                                          DOWN: spawn detached `node C:\Vibes\cosmo-canyon\server.mjs`, poll
                                          health until ready, open UI (real browser tab / window.open — NOT iframe)
  index.html                              one "Cosmo Canyon" button (+ optional health dot); NO cosmo tab/routes/ingest

C:\Vibes\cosmo-canyon\                   ← system home + STANDALONE app (NEW; CC-native, NOT under ralph/)
  server.mjs                              standalone server (Node http/Express, :7788): serves ui\index.html + ALL
                                          /api/cosmocanyon/* (status/backlog/suggestions/completions/agent/focus/
                                          assets/snapshot/aux/rollback + cc-start/cc-stop + §15 asset/active endpoints);
                                          owns ccEnsureVite(:8780) + auto-snapshot cadence; spawns/kills the detached
                                          supervisor.mjs; single-instance (port-in-use / pidfile → refuses a 2nd)
  ui\index.html                           standalone dashboard (Asset Browser primary; §7/§15d)
  PLAN.md                                 this doc
  README.md  AGENTS.md                    module map + gotchas (Phase 1)
  orchestrator\
    tick.md                               per-tick orchestrator prompt: sense→plan|work→gate→bookkeep (ONE increment, then exit)
    schemas.js                            JSON Schemas: SNAPSHOT, PLAN_RESULT, WORK_RESULT, GATE_RESULT (for in-tick subagents)
    prompts\                              agent prompt fragments (planner 3-modes, worker, bookkeeper)
    agy-pass.ps1                          PROVEN own-console agy dispatch (the recipe; §12)
    snapshot.mjs                          puppeteer headless screenshot of dev server (feel-verify input; FC fc-snapshot pattern)
    supervisor.mjs                        FALLBACK loop host (LOCAL lights-out only; PRIMARY = the desktop-app Workflow, §3) — a detached single-flight process spawned by server.mjs (survives a server.mjs restart; re-adopted via control/.supervisor.pid, §13.22); dashboard flags it LOUDLY when alive (§7)
    cc-loop.workflow.js                   PRIMARY loop host — the in-app dynamic Workflow the desktop app runs (no claude -p, no detached process; §3/§14)
  control\                               ← the on-disk control plane (durable; GUI + agents both r/w)
    backlog.json suggestions.json         task queue + design-proposal queue
    rejected.json accepted.md             don't-resuggest memory + accept log
    completions.json progress.json        completed log + current-pass self-report
    status.json                           orchestrator status (written each tick; GUI polls)
    focus.md                              operator priority steer
    agent.json                            {agent,model} worker pick (reuse WS_AGENT_OPTIONS)
    .paused .authority-consumed .lastaudit  flags/markers (mirror FC)
    .usage-YYYYMMDD.json                  daily cost counter
    runid.txt                             last Workflow runId (for resume)
    locks\                               atomic write-lock dir (single-writer helper)
  logs\                                   host log + per-pass agy logs + planner logs
  snapshots\latest.png                    periodic auto-screenshot for the dashboard

C:\Vibes\cosmo-canyon\game\             ← the fresh greenfield game (Phase 0)
  package.json vite.config.ts tsconfig.json  index.html  .claude\launch.json (port 8780)
  src\ sim\ (deterministic, render-free)  render\ (Pixi v8)  assets\  harness.ts (window.__cc)
  test\ canary.ts determinism.ts sim.ts budget.ts sim-purity.ts
  docs\ README.md DONE.md KNOWLEDGE.md FEEL-REVIEW.md adr\
  derive.mjs  assets\manifest.json source\ art\ atlas\
```

Mirrors FC's split (system home / control plane / game / docs) so FC's hard-won discipline transfers.
**Two knowledge tiers** unchanged: game-local → `game/docs/*`; durable cross-harness → `D:\Ag\knowledge\`.

---

## 2. Data contracts (reuse FC verbatim — the design is proven)

- **Task bead** (`backlog.json`): `{id,title,detail,files[],kind,tier,acceptance,source,status,blocked_reason,attempts,created,updated}` — identical to FC PLAN §2. `tier` (`light|heavy|structural`) now also drives **engine routing** (§3c).
- **`suggestions.json`** `[{id,title,body,kind:"design",created,status}]`; **`rejected.json`** `[{id,title,reason,rejected}]` (planner reads before proposing); **`accepted.md`** append-only.
- **Asset manifest** = positioning authority (FC §2). Same invariant: placeholder & real both render to declared `size`/`anchor`.
- **NEW — typed inter-stage contracts** (`schemas.js`), the CC-native upgrade over parsed JSON:
  - `SNAPSHOT` = `{readyBeads[],blockedBeads[],readyCount,authorityChanged,auditDue,counts,dirty}` (sense stage out)
  - `PLAN_RESULT` = `{mode,backlogOps[],suggestionOps[],note}` (planner out; applied atomically)
  - `WORK_RESULT` = `{beadId,engine,filesTouched[],committed,sha,note}` (worker out)
  - `GATE_RESULT` = `{pass,failedTest,diffLines,tamper}` (gate/guard out)
  Schema validation happens at the tool layer → the agent retries on mismatch (no parse-or-fail).

---

## 3. The orchestrator — two hosts, one control plane (desktop-app Workflow PRIMARY)

The loop advances **one verified increment at a time** (sense → work|plan → deterministic gate/commit).
That increment ("the tick") is **host-independent** — the deterministic rails (`bookkeep.mjs` = sole
gate/commit authority, `BASE_SHA` anchor, breakers) are identical no matter who hosts it. **Two hosts run
the same ticks against the same `control/` plane:**

- **PRIMARY — In-app Workflow (the Claude Code DESKTOP APP is the orchestrator).** Launch the loop as a
  **dynamic Workflow** from a desktop chat session (`orchestrator/cc-loop.workflow.js`); loop state lives in
  the script, each stage is a **spawned subagent** (sense=haiku, work=sonnet, planner=opus), and every
  fs/git/test step runs the deterministic glue via Bash. **No `claude -p`, no detached process.** Why
  primary: the app's **tasks pane shows the running Workflow + subagents + shell live** → full visibility,
  one-click stop, and NO invisible token-eating process (the exact failure the detached path spends
  §13.16/§13.17/§13.39 guarding against). Built + ran (§14).
- **FALLBACK — detached supervisor (LOCAL lights-out only).** A detached `supervisor.mjs` the standalone
  `server.mjs` spawns, running ONE `claude -p < tick.md` per tick (inline blocking calls only, then exit;
  repeat). Use ONLY for a pure-offline unattended run with no desktop session. Because it is an **invisible
  headless `claude -p` loop**, the dashboard MUST flag it running **LOUDLY + persistently** (§7/§15d) — an
  unmissable banner + pid + one-click Stop — so a forgotten supervisor cannot burn tokens unseen. For most
  unattended/overnight work prefer the app's native **Routines / cloud sessions** (scheduled, run with the
  app closed, still visible in the app) over this fallback.

> ✅ **The Phase -1 spike (2026-06-30 — `SPIKE-RESULTS.md`) only ruled out a HEADLESS `claude -p` hosting a
> long-lived background Workflow** (a detached `-p` exited at 21s and killed its background task). It did NOT
> rule out an interactive DESKTOP SESSION driving the Workflow tool — that distinction is exactly what makes
> the PRIMARY host valid. The fallback supervisor is a **dumb single-flight repeater** (Claude is the
> orchestrator; the supervisor just re-spawns the next tick). The tick logic below is the SAME under either
> host; only the host differs.

### 3a. The tick (one loop iteration)

```
tick logic (sketch) — the SUPERVISOR repeats this; each pass below = ONE `claude -p < tick.md` process
  while (host: !paused && under-daily-cap) {   // the loop lives in the HOST (Workflow script OR supervisor), not one long claude process
    const snap = await agent(SENSE_PROMPT,   {schema: SNAPSHOT, model:'haiku'})   // cheap read of control+git+Ready-Spec authority
    if (snap.authorityChanged || snap.blockedBeads.length || snap.readyCount<3 || snap.auditDue) {
      const plan = await agent(plannerPrompt(snap), {schema: PLAN_RESULT, model:'opus'})  // 3 modes (§3b)
      await agent(applyOpsPrompt(plan), {model:'haiku'})        // atomic write to backlog/suggestions
      continue                                                  // re-sense after planning
    }
    const bead = snap.readyBeads[0]
    if (!bead) { log('idle: no ready bead'); break }
    const work = await runWorker(bead)                          // hybrid dispatch (§3c)
    const gate = await agent(gatePrompt(work), {schema: GATE_RESULT})   // node gate + PostPassGuard
    if (!gate.pass || gate.tamper || gate.diffLines>800) {
      await agent(revertPrompt(bead, gate))                     // git reset; attempts++ / blocked
    } else {
      await agent(bookkeepPrompt(bead, work))                   // commit, DONE.md, completions, status.json, snapshot
    }
  }
```

- **No separate watchdog process** — FC's `server.js` trigger watchdog becomes the trigger `if(...)` at the top of the tick. Single locus of control. (Pause/flags it branches on come from the `sense` `SNAPSHOT`, not direct fs — the script has no fs; §13.2. The loop `break`s when `snap.paused` is set.)
- **Resume / restart (corrected — §13.3):** `resumeFromRunId` is **same-session only** → it covers an in-session pause/kill/script-edit, NOT a fresh-process host restart. **Cross-restart durability comes from the on-disk control plane** (idempotent ticks rebuild from `backlog.json`/markers), same as FC — not the journal. On (re)start: **reconcile first** (uncommitted edits from a killed mid-pass → `git reset --hard` + `clean -fd`), then resume from disk.
- **Cost governor (corrected — §13.4):** the always-on cap is the **daily `.usage-YYYYMMDD.json` counter + an explicit per-run cap** (args/config). `budget.total/remaining()` is a *bonus* ceiling only when a `+Nk` directive is present — it's usually `null`/Infinity in an autonomous run, so do NOT rely on it as the sole guard.
- **Commit model (CORRECTED — the Stop-hook reality, §13.21/§13.27/§13.29).** This repo's `.claude/settings.json` has a **`Stop` hook** that runs `git add -A && git commit` at the END of EVERY claude session/turn — a per-tick `claude -p` WILL commit the tree on exit; "leave it uncommitted" is not an option here. So: **(1)** spawn every tick with **`RALPH_PASS=<bead-id>`** → the hook makes a clean labeled `ralph <id>:` commit and **never `--amend`s** (it only amends an `auto-checkpoint:`-titled HEAD), keeping commits immutable. **(2)** the **supervisor** captures `BASE_SHA` and writes it to `control/.tick.json` **before spawning** (§13.28) — never in-tick (a killed tick's in-memory base is lost). **(3)** run gate + guard **inside the tick before exit**: fail → `git reset --hard $BASE_SHA` + `clean -fd` (clean tree → hook commits nothing); pass → leave the good tree → hook commits it as `ralph <id>:`. **(4)** anchor ALL success/revert/guard logic to **`git diff $BASE_SHA`** — never working-tree-dirty (§13.27), never `HEAD` (§13.21). **(5)** make reset/commit a **deterministic script** (§13.30), not a model judgment. **(6) HARDENED (revised §15i — must land before any hands-off run):** the guard is **tree-wide + allowlist-based** — `outOfScope = repoDirty.filter(p => !allowed(p))` over `git status --porcelain` for the WHOLE `C:/Vibes` tree (allow only `cosmo-canyon/game/**` + the specific `control/` files bookkeep writes), so an adversarial/injected worker writing OUTSIDE `cosmo-canyon/` (e.g. rewriting `.claude/settings.json`'s Stop hook for persistence/RCE) trips tamper-revert (**15.41**); on revert run a whole-worktree `git clean -fd` (assert `show-toplevel==C:/Vibes` first, never `-x`) so a stray root `evil.ps1` is removed; scope the serial Stop hook to `git add -A cosmo-canyon` (or env early-exit; bookkeep = sole committer); pre+post-tick assert `.claude/settings.json` byte-identical vs BASE. The serial revert is **narrowed to the game subtree** — `git checkout $BASE -- cosmo-canyon` (+ scoped clean) or a dirty-tracked-scope assert — so it never discards human WIP / other-branch work on the shared repo (**15.49**). ALL `C:/Vibes` committers (bookkeep, plan-apply, ingest, merge, Stop hook) serialize on ONE `git-tree` lock (**15.47/15.38**). bookkeep runs gate/commit via **argv-array `shell:false`** (commit msg via temp file; strip ``%&|<>`$`` from interpolated title/detail/note) and constrains `acceptanceCmd` to an allowlisted `node accept/<id>.ts` shape (**15.44**). Sole authority + BASE_SHA anchor + one-committer all intact.

### 3b. Planner (opus agent, conditional — never a hot-loop cost)

One `agent(..., {model:'opus', schema:PLAN_RESULT})` call, mode chosen by the tick:

| Mode | Trigger | Does (FC parity) |
|---|---|---|
| `diff` | `authoritySha` ≠ `.authority-consumed` (§15b) | classify authority (Ready-Spec) delta add/modify/**remove**; reconcile vs backlog+DONE (cancel obsoleted, emit removal-as-suggestion, never autonomous); ≤8 tasks; write `.authority-consumed` |
| `blocked` | any `status:"blocked"` beads | re-scope / split / drop; clear or update `blocked_reason` |
| `topup` | readyCount < 3 (fill to ~10) | invent highest-impact tasks toward the Ready-Spec authority; route design→suggestions, impl→backlog; dedup vs backlog+DONE+rejected+open |
| `audit` | `.lastaudit` older than cadence | diff the Ready-Spec authority vs what's built (DONE + `src/`); flag drift; emit reconciliation tasks; update `.lastaudit` |

Opus only fires when a trigger is live (hysteresis: topup at <3) → cheap. Planner **never edits `src/`** (plans only). Bounded input (**`spec-doc.md`** = the compiled Ready-Spec authority (§15b) + FOCUS + backlog titles + DONE tail + rejected + suggestions + optional authority diff) — not a repo crawl. **Trigger mechanics (§15b):** `authorityChanged`/`authoritySha` fire `diff` on a Ready-Spec-set change; `diff`/`audit` diff against the LAST-GREEN authority (`.authority-known-good`, 15.5), never the live set; an EMPTY Ready-Spec set → `authorityEmpty` is the FIRST `computeTrigger` branch → idle-blocked-on-human, never topup against an empty north-star (15.33, revised §15i).

### 3c. Worker — hybrid dispatch (`runWorker(bead)`)

Route by `bead.tier` + queued-work type:

- **headless logic/systems/balance → agy** (free Gemini): `agent()` spawns a thin Claude subagent that runs `orchestrator/agy-pass.ps1` via **Start-Process (own console, no stdout redirect)**, `--model gemini-3.5-flash --print-timeout 30m --dangerously-skip-permissions --log-file`, then **verifies by `git diff`** and returns `WORK_RESULT`. (The proven recipe — §12. agy can't be the subagent itself; the Claude wrapper is the bridge.)
- **feel/art/visual → Claude** (sonnet/opus): a subagent edits, then verifies via **headless snapshot + vision critique** — a puppeteer screenshot of the dev server (FC's `fc-snapshot.mjs` pattern) → Claude `Read`s the PNG + runs `visual-critique`. (Spike B: a headless `claude -p` has **no** preview MCP, so feel-verify uses image analysis, which needs none — §13.14. preview MCP only when an operator drives a foreground CC.) Still the capability FC's agy worker never had.
- `agent.json` picks the default engine/model; the UI warns "flash may struggle" when `structural` beads queue under a flash pick.
- **agy is main-tree-bound — a parallel caveat (15.42, §15i):** the default engine edits the SHARED `game/`, detects success via a main-tree `git diff $BASE`, and writes ONE global `control/.agy.pid`, so under the parallel toggle it is NOT worktree-isolated. Fix (before N>1): the scheduler routes ALL agy beads through one serial lane (image/audio already route to claude, so only claude beads parallelize), OR `agy-pass.ps1` runs worktree-aware (`-GameDir=<worktree>/game` + per-agent `.agy-<agentId>.pid`, diff scoped to the worktree). Parallel mode hard-refuses / auto-serializes agy until the full fix lands.

### 3d. Verify surfaces (two, same as FC)
1. **Node gate** (commit gate, both engines): `tsc --noEmit && tsx test/{canary,determinism,sim,budget,sim-purity}.ts` via Bash. Render-free, fast, deterministic.
2. **Visual/feel** (Claude-only): headless **puppeteer snapshot of the dev server → Claude reads the PNG + `visual-critique`** (NOT preview MCP — unavailable headless, §13.14). `window.__cc` via preview MCP only when an operator runs a foreground CC.

---

## 4. GDD ingest — REMOVED (2026-07-02)

**GDD REMOVED entirely.** There is no GDD, no doc import, no SPLITTER, no `POST /api/cosmocanyon/gdd`
endpoint (the endpoint + fetch/split helpers were deleted from `server.mjs`), no configured import doc,
no `control/gdd.json` / `control/gdd-snapshot.md` (deleted). **Authority = the set of Ready Spec assets
ONLY (§15b).** Specs are created/edited directly via the Asset Browser (§7/§15d); nothing bulk-imports
an external document. Historical note: a `POST /api/cosmocanyon/gdd` splitter that imported a Google-Doc
into Not-Ready Spec assets was built and then removed with the GDD concept — do NOT reintroduce it.

---

## 5. Asset pipeline (reuse FC Phase 6 verbatim)

`game/derive.mjs` (pure-JS **pngjs**): `source/<key>.png` → box-downsample to `size×renderScale` →
`art/<key>.png`, shelf-pack → `atlas/atlas.{png,json}`; idempotent (writes only on change). Registry
reads frame rects from atlas (never hardcodes UVs), falls back real→placeholder (game never breaks).
`test/budget.ts` caps atlas size/count/draw-calls. **Standalone server** (`server.mjs`, §1/§6): `GET
/api/cosmocanyon/assets` (manifest + wishlist) + `POST /assets/upload {key,file}` (magic-byte validate,
**PNG IHDR dimension cap + derive child timeout — 15.51 §15d**, atomic write, derive, flip status real).
Manifest = positioning authority. **The Asset Browser (§15a/§15d) supersedes this flat `{key,file}` model
with folder-per-asset + Instructions/Questions/State**; `derive.mjs` is reused underneath.

---

## 6. Standalone app + the Launcher's launch-or-open button

**Cosmo Canyon is a STANDALONE app** (§1). The dashboard UI + ALL `/api/cosmocanyon/*` endpoints +
loop-host control live in `cosmo-canyon\server.mjs` (Node http/Express, **:7788**) serving
`cosmo-canyon\ui\index.html`. The Launcher (`D:\Ag\launcher`) keeps ONLY a launch-or-open control.

**Standalone `server.mjs` owns (all `/api/cosmocanyon/*`):** `status,backlog,suggestions,completions,
agent,focus,assets,snapshot,aux,rollback` + `cc-start`/`cc-stop` + the §15 asset/active endpoints
(create/replace/instructions/answer/state/list/file/DELETE/active). It also owns `ccEnsureVite` (:8780) +
the auto-snapshot cadence, and spawns/kills the detached supervisor. (No `/gdd` ingest — GDD removed, §4.)

- **Single-instance + launch-or-open:** `server.mjs` refuses a 2nd instance (port-in-use / pidfile) so
  "spawn if down, else open" is always safe. Liveness probe = `GET /api/cosmocanyon/status`.
- **`cc-start` = arm the FALLBACK host only (§3).** The PRIMARY orchestrator is the desktop-app Workflow,
  which needs no server button — you launch it from a chat session. `cc-start` is for LOCAL lights-out runs:
  it spawns the detached **`supervisor.mjs`** (a SEPARATE process — NOT a setInterval inside the UI server, so
  the loop survives a UI-server restart; re-adopted via `control/.supervisor.pid`, §13.22) + clears `.paused`.
  The supervisor is the single-flight tick host (§13.5): each cycle, if not paused / not over the daily cap /
  no tick in flight, it spawns ONE `claude -p < tick.md --dangerously-skip-permissions`; `ccEnsureVite` keeps
  the :8780 dev server up for preview/snapshot. **Because this starts an invisible headless `claude -p` loop,
  the dashboard MUST render the LOUD persistent "fallback supervisor running" banner while `supervisor.alive`
  (§7/§15d) — the anti-invisible-process rail.** For unattended, prefer the app's native Routines/cloud.
- **`cc-stop`** = set `.paused` + kill the supervisor + the in-flight tick pid + its child claude/agy
  tree. Survivor-safe: the next `cc-start` reconciles any uncommitted tree from a killed tick.
- **Single-writer (15.23, revised §15i):** ALL control-plane writes — server, worker agent, planner-apply —
  go through the SAME `orchestrator/lock.mjs` + `CC_CONTROL/locks` named locks in the §15c-2 order.
  `server.mjs` MUST import that `lock.mjs` (never a private helper); RMW writes (backlog/completions/
  suggestions) take the mutex, blind overwrites (agent.json/focus.md) use atomic-rename. Restart-robust.

**The Launcher's role shrinks to one card/button** (§1/§15d): on click → probe
`http://localhost:7788/api/cosmocanyon/status`; **UP** → open the UI URL in a real browser tab (shell-open
/ `window.open`, NOT an embedded iframe — it is a separate app now); **DOWN** → spawn detached `node
C:\Vibes\cosmo-canyon\server.mjs`, poll health until ready, then open the URL. From `D:\Ag\launcher`
REMOVE: the `#view-cosmocanyon` tab markup + JS, ALL `/api/cosmocanyon/*` routes, cc-start/cc-stop,
asset upload, snapshot/aux/rollback, ccEnsureVite. Keep only the launch-or-open button (+ optional
status dot from the health probe).

**FC / Workshop / the ralph fleet are RETIRED** — nothing is left to shelve or coexist with; the
cross-system mutex (§13.23) is removed. Only human WIP / other branches on the shared `C:\Vibes` repo
remain, so the tree-wide git guards (15.41) + one-committer rule (15.47) stand.

---

## 7. Dashboard UI (standalone `ui\index.html`, served by `server.mjs` :7788, polls 3s)

**Hosted by the standalone app** (§1/§6) — its own page, NOT a Launcher tab. **The PRIMARY human surface
is the Asset Browser** (§15d): each Asset = File (image/audio/spec) + human **Instructions** + agent
**Questions** + **State** (`not_ready`/`ready`, with a `hasOpenQuestions` badge and a DERIVED `implemented`
projection — §15a/§15c), sortable/searchable + composable filters + drag-drop. Below it an **Active-tasks**
card (live in-flight tasks, `active.json`, §15c). **Backlog / Suggestions / Completions become read-only
monitoring** views (no longer the input surface). The **3s poll must be focus-safe** — diff-patch by `rev`,
never re-render a focused/dirty textarea, preserve caret+scroll (15.35, ship-blocker).

**⚠ LOUD host-state banner (top of page, always visible — the anti-invisible-process rail).** The dashboard
must show WHICH host drives the loop, unmissably. **Normal** (desktop-app Workflow is the orchestrator, or
idle) = a quiet status dot. **FALLBACK SUPERVISOR ALIVE** (`GET /api/cosmocanyon/status` → `supervisor.alive
== true` — a detached headless `claude -p` loop is running): render a **PERSISTENT high-contrast banner
pinned to the top, NOT collapsible** — "⚠ FALLBACK SUPERVISOR RUNNING — headless `claude -p` loop · pid N ·
started HH:MM · tick <bead> · N/cap today" — with a one-click **Stop** (cc-stop). Also flag it in the browser
tab title (`● supervisor running`) + an optional desktop notification on start. **Rationale (the operator's
core requirement):** the fallback is the ONLY path that can run a claude instance you cannot see in the app's
own tasks pane, so it must be the loudest thing on the page until stopped — no invisible token-eating loop.

Also (from the original layout): status banner + Start↔Stop + loud paused/stalled/guard alert; current-pass
self-report (`progress.json`); **planner state** (idle/mode/last-fire) + planner-log tail; **worker engine
picker** (agy-flash / sonnet / opus / auto) + "flash may struggle" warn; **Steer box** (`focus.md`);
FEEL-REVIEW queue (human-gated, §15e); live host log; **cost meter** (`budget` spent + daily count).
Preview pane: **`:8780` with a Popout button** (open a real browser window, §15d) + Reload/Fullscreen +
auto-snapshot `latest.png`. Live tick view (current stage, last runId) sourced from `status.json`.

---

## 8. Failure-mode fixes — FC → Cosmo Canyon (the headline: many become native)

> **FC is RETIRED** — this table is the historical port lineage (how each proven FC fix maps to the
> CC-native implementation), kept as design rationale. "Authority/GDD-compliance" rows now mean the
> Ready-Spec set (§15b), not the GDD; the §15/§15i hardening is the current authoritative form.

| FC fix | FC mechanism (ps1/node) | Cosmo Canyon (CC-native) |
|---|---|---|
| 1 worker→planner bounce-back | bead `blocked`+reason | same (worker agent sets it via WORK_RESULT) |
| 2 design-change invalidation | planner `diff` reconcile | same (opus planner, `diff` mode) |
| 3 harness integrity / false-green | `canary.ts` + gate-tamper guard | same tests; tamper check = **JS in the tick** (no postpass-guard.ps1) |
| 4 oversized-pass breaker | `-PostPassGuard` ps1, 800-line revert | **JS** `gate.diffLines>800 → revert` in the tick |
| 5 stuck-task demotion | `attempts≥3` in status.ps1/server | **JS counter** in the tick |
| 6 no-commit heartbeat (`stalled`) | status.ps1 + server slow-tick | tick tracks passes-since-git-change → status.json + alert |
| 7 determinism | seeded `rng.ts` + `sim-purity` grep-ban | same tests, run via Bash in gate |
| 8 cost governor | git/disk daily cap | **`budget.total/remaining()` native** + daily file for agy passes |
| 9 authority-compliance audit | planner `audit` mode | same (`audit` mode; audits vs the Ready-Spec authority, not a GDD) |
| 10 feel/art drift | FEEL-REVIEW + snapshot + visual-critique cadence | **stronger** — orchestrator runs preview MCP + visual-critique *in-loop* |
| 11 ~~GDD ingest validation~~ | ~~server reject login/empty~~ | **N/A — GDD ingest REMOVED (§4)**; no doc import exists |
| 12 queue write race | lockfile + atomic-rename | same (agents use Bash atomic write) |
| 13 suggestion hygiene | dedup + accepted.md | same (planner prompt) |
| 14 asset authority + budget | manifest + budget.ts | same (ported) |
| acceptance | per-bead, cited | same (WORK_RESULT cites it) |
| **resume (in-session)** | `.refinery-state.json` hand-rolled | `resumeFromRunId` journal replay — **same-session only**; cross-restart still leans on the on-disk control plane (§13.3) |
| **typed planner output** | parse-or-`.nothing-to-do` | **`schema:` validation, retry-on-mismatch native** |

**4 fixes collapse into native primitives** (4 oversized-pass, 5 stuck-task, typed output, in-session
resume); cost cap (8) is *partly* native (`budget`) but the always-on governor stays disk-based (§13.4).
**10 gets strictly better** (in-loop visual QA). Cross-restart durability = disk plane, same as FC.

---

## 9. Build phases (ordered, each ends verifiable)

-1. **SPIKE ✅ DONE (2026-06-30 — `SPIKE-RESULTS.md`).** Decided empirically: host = thin Launcher
   supervisor + per-tick `claude -p` (spike A: `-p` exited at 21s + killed its bg task); feel-verify =
   snapshot + vision-critique, NOT preview MCP (spike B: headless reported NONE). §13.1 resolved, §13.3
   dissolved, §13.14 confirmed. Lock-helper concurrency (§13.x) deferred to a Phase-4 unit test.
0. **Scaffold the greenfield game** (manual pre-req — breaks chicken-egg). `npm install` + commit lockfile;
   Vite+TS+Pixi v8, deterministic render-free sim (`rng/grid/pool` + systems), trivial playable loop on
   grey-boxes, `window.__cc` harness, all 5 tests (`canary/determinism/sim/budget/sim-purity`), asset
   registry+placeholder, `.claude/launch.json` (8780), context docs, `cosmo-canyon/.gitignore`
   (§13.9), empty committed control files. Work on a dedicated **`cosmo-canyon` git branch** (§13.8).
   **Node gate green before any loop runs.** (Borrow RS-v2 scaffold; rename to `__cc`.) *(Historical: the
   ORIGINAL scaffold also seeded a `game/docs/HUMAN_GDD.md` north-star — that GDD seed is REMOVED (2026-07-02);
   authority is now the Ready Spec set. §14.)*
1. **Core tick + supervisor, Claude-only** — `tick.md` (sense→work→gate→bookkeep, no planner yet) +
   `schemas.js` + the Launcher single-flight supervisor (`setInterval`, honors `.paused`, one tick at a
   time). Prove: one `claude -p < tick.md` tick lands one increment + writes `status.json`; the supervisor
   spawns the next tick only after the previous exits; kill mid-tick → next tick reconciles to the recorded
   base SHA (`git reset --hard $BASE_SHA`, never HEAD — §13.21) + continues from disk. Drive a seeded bead by hand.
2. **Hybrid worker dispatch** — `agy-pass.ps1` (the §12 recipe) + tier routing + feel→Claude(preview MCP).
   Prove: an agy bead lands a verified edit (git diff); a feel bead gets a visual-critique pass.
3. **Planner agent** — opus, 3 modes (`diff/topup/audit/blocked`) via PLAN_RESULT schema + tick trigger
   logic + suggestions queue + atomic apply. Prove: `topup` fills backlog from the Ready-Spec authority.
4. **Launcher integration** *(ORIGINAL build — superseded by the standalone extraction, §15g phase 3;
   the tab/shelving is retired)* — `#view-cosmocanyon` primary tab, `cc-start/stop` (detached host),
   status/queue/agent/focus endpoints, single-writer helper, shelve the other 3 tabs. Prove: start
   from GUI, watch a pass, stop.
5. ~~**GDD ingest**~~ — *(ORIGINAL step, since REMOVED — GDD deleted 2026-07-02, §4. No doc-ingest exists.)*
6. **Asset pipeline** — port `derive.mjs`/registry/budget + upload endpoint + Asset panel.
7. **Safety rails & drift** — PostPassGuard-as-JS (4/3), attempts/breaker (5), stalled (6), cost governor
   (8, budget+daily), determinism/sim-purity ban (7), auto-snapshot + visual-critique cadence (10).

Then: hands-off run; tune. **(Phases -1…7 are the ORIGINAL GDD-authority loop — BUILT + run, §14.)**

**Next-generation build order = §15g** (the Asset-Browser + Spec-authority + standalone-app + parallel
redesign). The parallel-safe contracts are CORE from phase 1, not a later add-on — this supersedes §11.4
("parallel lanes later — out of scope"). Each §15g phase ends verifiable, this same style.

---

## 10. What CC gives that FC hand-rolled (gains) + what's genuinely new risk

**Gains:** native crash-resume (journal), native cost cap (`budget`), typed stage I/O (`schema`),
in-loop feel/visual QA (preview MCP + visual-critique), single-locus control (tick vs separate
watchdog process), worktree isolation available for free if we later parallelize lanes.

**New risk / must validate early (post-spike):**
- **Per-tick Claude overhead** — every increment spins a fresh `claude -p` (sense+gate+bookkeep), even an
  agy one (§13.18). Mitigate: cheap `haiku` for sense/apply/bookkeep; opus only in the planner; keep ONE
  bead/tick (§13.25); the daily cost cap bounds the worst case.
- **Process hygiene** — a killed/timed-out tick can orphan its own-console agy (§13.16); a hung tick can
  wedge the single-flight guard (§13.17). Both wire into the supervisor (explicit pid tracking + tick timeout).
- **Feel-verify path** — headless `claude -p` has no preview MCP (spike B); feel uses a puppeteer snapshot +
  Claude PNG `visual-critique`, which needs `puppeteer-core` (§13.19).
- **Two writers (GUI + agents)** to control files — the lockfile/atomic helper is load-bearing; test it (§13.x).

---

## 11. Open decisions

1. **Game concept/name** — RESOLVED: **Realm Survivors** (mobile survivors-like). Phase 0 scaffold stays a
   generic survivorslike skeleton; the **Ready Spec set** drives specifics (there is no GDD — removed
   2026-07-02; the old `control/gdd.json` is deleted). Note: same IP as FC's RS-v2 but a SEPARATE fresh
   build under `cosmo-canyon/game/` (no collision).
2. **Tick-driver model** — ✅ **RESOLVED (settled by the built system, §14): a per-role split, NOT one global
   model.** The framing "opus **or** sonnet for THE tick driver" was the trap — it conflates three roles with
   opposite cost/capability needs: **(a) work-tick driver = `sonnet`** (implements one scoped bead, incl. the
   claude feel/visual path that edits + reads the snapshot — the capability floor; opus would blow the per-tick
   overhead §13.18 on EVERY increment, even a wrapped-agy one); **(b) planner tick = `opus`** (trigger-gated with
   hysteresis → never a hot-loop cost, §3b); **(c) sense/gate/commit/acceptance = deterministic Node scripts, NO
   model** on the supervisor path (`supervisor.mjs --model sonnet` work / `--planner-model opus`), or **cheap
   `haiku` wrappers that only EXECUTE those Node scripts** on the in-app Workflow path (`sense=haiku`). So the
   cheap tier never reasons (only runs Node), the capable tier (sonnet) never pays opus rates, and opus fires only
   on a planner trigger.
3. **GUI topology** — ✅ **RESOLVED: STANDALONE app** in `cosmo-canyon\` (`server.mjs` :7788 + `ui\index.html`);
   the Launcher only launches-or-opens it (§1/§6/§15d). Supersedes "extend the Launcher / primary tab" and the
   old "Legacy orchestrators" nav question (FC/Workshop/fleet retired — nothing to shelve).
4. **Parallel lanes** — ✅ **RESOLVED: first-class, toggle-selected** (§15c-2). `control/config.json`
   `concurrency.mode` = serial (default) / parallel; the parallel-safe contracts (per-asset file-ownership +
   claim, deadlock-free lock order, single-committer merge) are in the CORE from phase 1, NOT retrofitted. The
   toggle only selects runtime. Parallel stays GATED behind the §15i "must-fix-before-PARALLEL" set.
5. **Host mechanism** — ✅ **DECIDED: the desktop app is the PRIMARY orchestrator** (in-app dynamic Workflow
   `cc-loop.workflow.js`, §3/§14) — chosen for live visibility (tasks pane), one-click stop, and no invisible
   token-eating process. The detached `supervisor.mjs` + per-tick `claude -p` is a **demoted LOCAL lights-out
   FALLBACK only**; the spike ruled out a *headless* `claude -p` hosting a long-lived Workflow, NOT a desktop
   session driving the Workflow tool. **When the fallback supervisor is alive it MUST be flagged LOUDLY in the
   dashboard** (§7/§15d) so no headless `claude -p` loop runs unseen. Unattended/overnight → prefer the app's
   native **Routines / cloud sessions** over the fallback. See SPIKE-RESULTS.md.
6. **Git branch** — Cosmo Canyon commits to a dedicated **`cosmo-canyon`** branch of the shared `C:\Vibes` repo
   (FC/fleet retired, but human WIP + other branches may exist) → the branch isolates history and the tree-wide
   git guards (15.41) protect the rest of the repo. Merge to main when shippable.
7. **.gitignore** — ✅ **RESOLVED (§15h + 15.46/15.40):** COMMIT the authority + config — `control/config.json`
   (force-add / negation if a broad rule catches it), `control/assets/<id>/meta.json`, `control/assets.json`,
   Spec `file.md`, small images, `backlog/suggestions/rejected/accepted`. GITIGNORE runtime —
   `status.json`,`progress.json`,`.paused`,`.authority-consumed`,`.lastaudit`,`.usage-*`,`.supervisor.pid`,`.tick.json`,
   `.plan-*`,`.agy-*.pid`,`.stalled`,`.agy-strikes`, and the §15 paths `control/claims/`,`control/active.json`,
   `control/assets/*/history/`,`control/.trash/`,`control/.cc-host.lock`,`control/.authority-settle`,
   `control/.asset-scan-latch.json`, plus generated `game/art`,`game/atlas`,`game/node_modules`,`logs/`,`snapshots/`.
   Rationale: NOT gitignoring runtime lets `clean -fd cosmo-canyon` wipe live claims/active/history and makes
   every restart see a dirty tree → spurious reset (the Phase-4 `.supervisor.pid` bug class). Audio gitignored →
   `history/` retention MANDATORY (§15.9/15.40). Verify: a fresh clone has non-empty Spec authority + zero claims.
8. **Asset store / id / Implemented** — ✅ **SETTLED (§15a/§15e, 15.30/15.32):** ONE store (`meta.json` per-asset
   authority + DERIVED `assets.json` index — delete any `assets-state.json`/flat index); ONE id scheme
   `a-<base36>-<4rand>`; **Implemented is a DERIVED projection** (bead-done ∧ acceptance ∧ manifest-real, bound to
   `contentHash`+`rev`), NEVER a stored `state` flag — `bookkeep` records provenance (`implementedBy`) only.

---

## 12. The proven agy dispatch recipe (encode in `agy-pass.ps1`)

From shared-knowledge `agy_cli_headless_gotchas` §6 (verified 2026-06-30 on this box):

- **MUST launch in its own console — never pipe/redirect agy stdout** (CC's Bash/PS tool redirects child
  stdout → agy hangs at 0 CPU). Use `Start-Process powershell -File agy-pass.ps1` (no
  `-RedirectStandardOutput`).
- Inside the runner: `Set-Location <game>`; `agy -p $prompt --model gemini-3.5-flash --print-timeout 30m
  --log-file <f> --dangerously-skip-permissions`. **Default `--print-timeout` is 5m and kills a real pass
  → EXIT=1, no edit.** Duration is a Go string (`30m`), not ms.
- **Verify by side-effect only** (`git diff`/dirty tree/`progress.json`) — agy print stdout is dropped
  (non-TTY) and uncapturable; `--log-file` is operational only.
- The dispatching **Claude subagent** runs the runner, polls for the git side-effect, returns `WORK_RESULT`.
  agy cannot be the workflow subagent itself — the Claude wrapper is the bridge.
- agy has **no Stop hook** → nothing auto-commits; the worker leaves edits **uncommitted** and **only
  `bookkeep` commits, after the gate passes** (§13.11).
- **Per-pass hard kill:** the runner enforces a wall-clock timeout (`Stop-Process` at ~35m, just over the
  30m print-timeout) so a hung agy can't wedge the loop; a timeout = a failed attempt (revert + `attempts++`).

---

## 13. Review findings (once-over) — holes & resolutions

Second-pass audit against the actual CC tool semantics. Numbered for cross-reference above.

| # | Hole | Severity | Resolution |
|---|---|---|---|
| 13.1 | **Detached `claude -p` may exit and kill a background Workflow.** | **CRITICAL** | ✅ **RESOLVED (spike A: `-p` exited at 21s, killed its bg task).** Host = thin Launcher `setInterval` supervisor spawning one `claude -p < tick.md` per tick; inline blocking calls only; Workflow = in-tick fan-out only. See SPIKE-RESULTS.md, §3 intro, §11.5. |
| 13.2 | Sketch `while(!paused())` reads a flag the script can't (no fs). | high | Pause/flags ride in the `sense` `SNAPSHOT`; loop `break`s on `snap.paused`. §3a. |
| 13.3 | `resumeFromRunId` claimed for cross-restart; it's **same-session only**. | high | ✅ **DISSOLVED by the spike choice** — no long-lived Workflow to resume. Restart = supervisor spawns next tick; disk control-plane is the only truth (each tick idempotent). |
| 13.4 | Loop guarded on `budget.remaining()`; `budget.total` is often **null** → Infinity → no cap. | high | Always-on governor = daily `.usage` file + explicit per-run cap; `budget` is a bonus ceiling. §3a. |
| 13.5 | No **single-host guard** — two `cc-start`s = two hosts on one repo. | high | `cc-start` refuses if a host pid is alive (FC single-loop-guard parity). |
| 13.6 | Preview/iframe/feel-verify need a **running vite dev server**; omitted. | med | Port FC's `fcEnsureVite` → `ccEnsureVite` (detached, port-guarded, re-armed on boot). |
| 13.7 | Phases 1-3 planner needs an authority but **ingest was Phase 5**. | med | *(Historical: Phase 0 seeded `game/docs/HUMAN_GDD.md`. GDD REMOVED 2026-07-02 — authority = Ready Spec set; that seed no longer exists.)* |
| 13.8 | **Branch unspecified** — would commit onto `ralph-proto-iter` (shared w/ FC/fleet). | med | Dedicated `cosmo-canyon` branch. §11.6. |
| 13.9 | No **.gitignore** plan → runtime/generated files committed. | med | §11.7 list. |
| 13.10 | `PLAN_RESULT` ops undefined. | med | Verbs: `add`/`cancel`/`update`/`rescope`/`setStatus` keyed by bead id; apply stage writes atomically. |
| 13.11 | Commit timing ambiguous. | med | **Uncommitted until `bookkeep`; commit only after gate green; fail = `git reset --hard $BASE_SHA` (NOT HEAD — §13.21).** §3a/§12. |
| 13.12 | Hung agy/worker has **no kill**. | med | Per-pass wall-clock kill (~35m) in the runner; timeout = failed attempt. §12. |
| 13.13 | Phase 0 missing **`npm install`** → gate can't run. | low | Phase 0 installs + commits lockfile. |
| 13.14 | Detached host may **lack the preview MCP**. | high | ✅ **CONFIRMED (spike B: headless `claude -p` reported NONE).** Feel-verify = puppeteer snapshot → Claude reads PNG + `visual-critique` (needs no preview MCP). preview MCP only operator-side foreground. §3c/§3d. |
| 13.15 | `cc-stop` latency (pause seen only next `sense`) + mid-pass kill leaves half-edits. | low | Accept latency (FC parity); reconcile uncommitted tree on next start (13.3). |
| 13.16 | **Orphaned agy on tick kill.** agy launches detached in its OWN console (§12) → it is NOT in the `claude` tick's child tree; killing the tick (cc-stop / tick-timeout) can leave agy alive, still editing, racing the next tick. | **high** | `agy-pass.ps1` writes the agy pid to `control/.agy.pid`; supervisor + cc-stop kill THAT pid explicitly (not just the tick tree); next tick refuses to start while `.agy.pid` is alive. |
| 13.17 | **Hung tick wedges single-flight forever.** A stalled `claude -p` tick never exits → the supervisor's one-in-flight guard blocks all future ticks. | **high** | Supervisor stamps each tick start-epoch; a tick alive > ~45m is killed (+ its `.agy.pid`), logged as a failed pass; loop resumes. (Tick-level analog of FC's planner kill-timer.) |
| 13.18 | **Per-tick Claude cost erodes the "free agy" win.** Every increment — even agy — is wrapped in a full `claude -p` (sense+gate+bookkeep). The Gemini-quota saving is real but smaller than implied. | med | Name it honestly; cheap `haiku` for sense/apply/bookkeep, opus only in planner; **one bead/tick** (§13.25); daily cap bounds worst case. |
| 13.19 | **`puppeteer-core` dep unlisted.** Feel-verify + auto-snapshot need a headless browser. | low | Add `puppeteer-core` (→ system Chrome/Edge, no Chromium download — FC pattern) in Phase 0/2; `snapshot.mjs` uses it. |
| 13.20 | **Two-repo commit split.** Game/system land on the `cosmo-canyon` branch of C:\Vibes; the Launcher tab+supervisor land in the separate `D:\Ag` repo. | low | ✅ **RESOLVED (revised §15i — standalone decision): ONE repo now.** The UI + ALL `/api/cosmocanyon/*` + supervisor control move OUT of `D:\Ag\launcher` into `cosmo-canyon\server.mjs`+`ui\`; game + orchestrator + control plane + server + UI all live under `C:\Vibes\cosmo-canyon\`. The only `D:\Ag` change left is the tiny launch-or-open button. §1/§6. |
| 13.21 | **The auto-commit is a `Stop` HOOK, not a timer** (VERIFIED in `.claude/settings.json`): `git add -A && git commit` runs at the END of every claude session/turn → every per-tick `claude -p` commits the tree on exit, and an unpushed `auto-checkpoint:` HEAD gets `--amend`ed by the next session. The "leave edits uncommitted until bookkeep" model is impossible here. | **CRITICAL** | Spawn ticks with **`RALPH_PASS=<bead-id>`** → clean labeled `ralph <id>:` commit, never amended. Gate+revert INSIDE the tick before exit; fail → `git reset --hard $BASE_SHA` (clean tree → hook commits nothing); pass → hook commits the good tree. Anchor everything to `git diff $BASE_SHA`, never dirty-tree/HEAD. §3a. **(revised §15i — 15.41):** make the scope/tamper guard TREE-WIDE + allowlist-based (not `cosmo-canyon/`-prefixed) so an adversarial worker can't survive by writing OUTSIDE the game subtree (e.g. rewriting `.claude/settings.json`'s Stop hook for persistence/RCE); scope the serial Stop hook to `git add -A cosmo-canyon`; byte-identity-assert `.claude/settings.json` pre/post. §3a(6). |
| 13.22 | **Supervisor-in-Launcher = single point of failure.** The loop only advances while the Launcher (D:\Ag) Node process is up; a Launcher crash/restart halts ticks (regression vs FC's Launcher-independent detached loop). | med | ✅ **RESOLVED/UPDATED (revised §15i — standalone decision).** The supervisor is a SEPARATE detached process the standalone `server.mjs` spawns (NOT a setInterval inside any UI server) → it survives a UI-server restart; `server.mjs` re-adopts it via `control/.supervisor.pid`. The loop no longer depends on the Launcher being up. §1/§6. |
| 13.23 | **No hard cross-system mutex.** "Don't run FC/Workshop/fleet + CC at once" is prose, not enforced; one working tree + rival branches → chaos. | med | **OBSOLETE (revised §15i — FC/Workshop/ralph fleet RETIRED, nothing to conflict with): the cross-system mutex is REMOVED.** Only human WIP + other branches on the shared `C:\Vibes` repo remain → the single-host guard (§13.5), branch assert (§13.24), and tree-wide git guards (15.41) still stand. |
| 13.24 | **No branch assertion.** cc-start assumes the tree is on `cosmo-canyon`; on another branch, commits/ticks land wrong. | med | `cc-start` asserts `git branch --show-current` == `cosmo-canyon` (or checkout) before committing/looping. *(An ingest committer was a factor historically; GDD ingest is now REMOVED, §4.)* |
| 13.25 | **Self-inflicted contradiction: multi-bead-per-tick (the §13.18 mitigation) breaks gate attribution + revert.** Several beads, one fails → can't tell which, and a base-SHA revert discards the good ones. | low-med | Default **one bead per tick**; batch only when each sub-bead is independently gated+committed. §13.18 mitigation revised. |
| 13.26 | **SEED-cherry-pick cheat not explicitly guarded.** FC Phase 1 caught a worker editing the `test/sim.ts` SEED to fake a green winnable gate. Tamper-guard must catch SEED edits, not just test-FILE edits. | med | Carry FC's exact PostPassGuard check: a changed `sim.ts` SEED (and edits to canary/determinism/sim-purity or the gate script) → revert + block. §7. (See 13.29 for WHERE the guard diffs.) |

### Persona-round findings (2026-06-30 — griffon / completionist / quest-designer; auto-checkpoint VERIFIED in `.claude/settings.json`)
| # | Hole | Severity | Resolution |
|---|---|---|---|
| 13.27 | **agy SUCCESS-detection also breaks on the hook** (not just revert). Worker judges "did agy edit?" by dirty-tree/`git diff`; a prior tick's Stop-hook commit leaves the tree CLEAN at the next tick's start → worker sees no diff → falsely reports no-edit → reverts good work + `attempts++`. | **high** | Success = `git diff $BASE_SHA --stat` (vs the persisted base), never working-tree-dirty. Same anchor as revert (§13.21). |
| 13.28 | **`BASE_SHA` + in-flight tick pid must be persisted by the SUPERVISOR to `control/` BEFORE spawn** — not captured in-tick. A killed/crashed tick's in-memory base+pid die with it → post-kill reconcile has no base; a Launcher restart forgets the pid → single-flight passes → double-tick (reachable via §13.22). | **high** | Supervisor writes `control/.tick.json {pid,startEpoch,baseSha}` before spawning; cc-start re-arm reads it: live pid → adopt/wait/kill; dead → `reset --hard baseSha` + clear. Closes the 13.5/13.16/13.17/13.22 interaction. |
| 13.29 | **FC's PostPassGuard is POST-commit (`BASE_SHA..HEAD`)**; porting it to a pre-commit working tree inverts its logic (`git show BaseSha:` + `git diff BaseSha HEAD`). | high | Since the hook commits on exit, run the guard on **`git diff $BASE_SHA..HEAD`** (post-commit), exactly as FC does; spec the diff base in the `GATE_RESULT` contract. Supersedes the "pre-commit working tree" framing. |
| 13.30 | **Revert/commit routed to haiku is the wrong economy** — it's the highest-stakes git surgery (right SHA, squash, atomic bookkeeping, not fooled by a hook commit). | med-high | Make reset/commit/bookkeep a **deterministic script** (`bookkeep.ps1`/`.mjs`) the tick invokes — mirror FC's `postpass-guard.ps1`; no model picks the reset target. Revises §13.18 routing for THIS stage only. |
| 13.31 | **Planner trigger precedence + idempotency dropped** (FC had diff>blocked>topup>audit; only `diff` clears `.authority-consumed`). The tick's single `if(...)` lets the planner free-pick → a latched `authorityChanged` may never be consumed (infinite replan), and `continue`-after-plan re-senses forever (livelock); opus burns each iteration. | **high** | Restore explicit precedence (diff first, clears the latch); pass the chosen mode to the planner; **one trigger consumed per fire**; per-tick max-replan cap (≤2 then break); a "fired-and-changed-nothing" latch (sha/epoch-stamped) suppresses a trigger until its input changes; back off opus on empty `backlogOps+suggestionOps`. |
| 13.32 | **No terminal state for a dead bead.** A bead cycles worker→blocked→replan→worker… forever; `blocked` beads re-fire the planner every tick (opus spend, zero progress). | **high** | Add `status:"abandoned"` at `attempts>=N` (or planner-drop) → excluded from the blocked trigger + worker drain → surfaced to the operator suggestions queue. (FC demoted; CC must also STOP re-planning it.) |
| 13.33 | **WIP-section block is prompt-only and only in `topup`.** `audit` reads intentionally-unbuilt WIP sections as "drift" → emits reconciliation beads → violates the WIP gate; and a prompt rule "already failed once" (FC §8). | med | Make WIP a **deterministic shared filter**: a Not-Ready Spec asset is EXCLUDED from the compiled authority (the primary WIP wall, §15b); diff/topup/audit ALL honor it; the apply-stage **rejects** any bead mapping to a Not-Ready/WIP spec. *(Original FC framing was GDD `(WIP, DO NOT IMPLEMENT)` headings — GDD REMOVED; the Ready/Not-Ready spec state is the WIP gate now.)* |
| 13.34 | *(Historical — GDD REMOVED 2026-07-02.)* Phase 0 seeded `HUMAN_GDD.md` but not the consumed-marker → the first tick saw sha≠absent → fired a spurious `diff` plan. | med | *(Historical resolution: Phase 0 also wrote the consumed-marker = sha of the seed. No longer applicable — no GDD/seed; authority = Ready Spec set. The consumed-marker is now `.authority-consumed`, keyed on the Ready-Spec authority hash.)* |
| 13.35 | **Infrastructure kill counts as a bead attempt.** cc-stop / tick-timeout (§13.17) mid-valid-work → `reset --hard $BASE_SHA` + `attempts++` → heavy/feel beads accrue toward `abandoned` (§13.32) through no fault of the work. | med | Distinguish "gate-failed" (`attempts++`) from "infra-killed mid-valid-work" (no increment); only the former counts. |

### Barret-round findings (2026-06-30 — "storming the reactor": resource drain, blast radius, accountability for destructive automation)
| # | Hole | Severity | Resolution |
|---|---|---|---|
| 13.36 | **Authority-shrink → self-destruct (the meltdown).** `diff` mode emits "remove built" beads when the authority drops/contradicts a feature, and impl beads AUTO-APPLY (only DESIGN changes are human-gated, §0). *(Original FC/early-CC threat was an external GDD doc being gutted/swapped — GDD REMOVED 2026-07-02, so the doc-ingest vector is gone; the residual risk is an operator gutting Ready specs.)* | **CRITICAL** | Semantic-shrink guard (§15b): never auto-update a `ready`/`implemented` spec (incoming body → a Question); per-spec (<40%) + aggregate (>35%/>2 headings) both `409 needsConfirm`; the removal cascade is suggestion-only; `audit` diffs against `.authority-known-good` (15.5), not the live set. |
| 13.37 | **Destructive beads run autonomously.** Removal/delete impl work drains with no human gate (only design is gated, §0). | **high** | Mark `kind:"remove"`; a destructive bead requires operator confirm (or the suggestions-queue bar) before it drains — additive work stays autonomous. |
| 13.38 | **No agy quota-exhaustion detection.** Free Gemini quota is finite; when dry, agy passes fail BLIND headless (auth/quota error invisible — agy knowledge). Worker sees no `git diff $BASE_SHA` → reverts good-faith work → `attempts++` → beads march to `abandoned` while the loop keeps burning Claude ticks for nothing. | **high** | Detect repeated agy zero-diff/no-op passes → treat as a quota/auth signal → pause the agy lane (alert) or fail over to a Claude worker; don't count quota-blind passes as attempts (ties §13.35). |
| 13.39 | **Supervisor has no consecutive-failure circuit breaker.** Daily cap + tick-timeout don't stop a FAST-failing loop (broken gate, dry quota): the supervisor respawns full `claude -p` ticks back-to-back, draining tokens at max rate to the cap. | **high** | N consecutive failed/no-progress ticks → auto-pause + loud alert (FC's 5-fail breaker, carried to the SUPERVISOR loop; distinct from the daily cap and the per-tick timeout). |
| 13.40 | **No known-good recovery point.** Many individually gate-green ticks can still drift the game somewhere bad; the `cosmo-canyon` branch is the only artifact, no "restore to last milestone." | med | Tag a known-good SHA on a cadence (after an audit-clean pass / epic complete); dashboard "roll back to last good tag". **(revised §15i — 15.40b):** the built `cc-known-good` moves to EVERY green commit (= "last green", NOT a milestone) → add a distinct `cc-milestone` tag that moves ONLY on a deterministic cadence signal (audit-clean/epic-complete flag written to `control/`, anchored to the persisted green SHA); point the rollback endpoint at `cc-milestone`, falling back to `cc-known-good`. |
| 13.41 | **`assets/source/` originals unprotected.** Full-res source art is sacred (derive regenerates `art/`+`atlas/` from it); a worker bead could overwrite/delete an original. | med | Deterministic guard: worker writes to `assets/source/**` are forbidden → revert + block (mirrors FC's "source kept full-res, never mutated" invariant). |

### Varesa-round findings (2026-06-30 — "did it actually LAND?": energy economy + committed plunges that must finish atomically)
| # | Hole | Severity | Resolution |
|---|---|---|---|
| 13.42 | **Bead-level acceptance is self-attested, never independently verified (false "it landed!").** The node gate (canary/determinism/sim/budget/sim-purity) proves the BUILD is healthy — NOT that THIS bead's feature works. The worker cites its own `acceptance` in `completions.json`; nothing checks the citation → a worker passes the generic gate while shipping a no-op/broken feature for the bead. | **high** | Independent acceptance-verify per bead: where acceptance is sim-checkable, compile it into a per-bead assertion run in the gate (not just the global sweep); else a separate cheap agent asserts the cited result against `git diff $BASE_SHA`. Never trust the worker's self-citation alone. **(revised §15i — 15.13):** bookkeep AUTO-DISCOVERS `game/accept/<bead.id>.ts` and runs it if present (independent of `acceptanceCmd`); a grader is REQUIRED (FAIL-closed if missing) only for beads the planner flags sim-checkable (a `renderOnly`/`acceptanceKind` DATA field) — so legitimately render-only beads skip without deadlocking to `abandoned`; every `assetKey` bead always requires one. Persist the projection inputs in `recordCompletion` (15.50). |
| 13.43 | **Stale single-writer lock wedges the whole control plane.** A tick/agy killed mid-write (cc-stop / timeout / crash — which §13.16/13.17 guarantee happen) never releases the `control/locks` dir-lock → GUI add-task, planner-apply, and the next tick block FOREVER. | **high** | Lock entries are pid+epoch-stamped; acquisition breaks any lock whose pid is dead OR older than a TTL. A "best-effort" lock that can't self-heal is a deadlock waiting for a kill. **(revised §15i — 15.48):** the load-bearing fix is a BUSY-grace keyed on the lock DIR's own ctime/mtime (owner.epoch is UNREADABLE during the mkdir→write window, so only the dir timestamp exists) — a just-created lock <~2s old reads BUSY, not stale; temp+rename `owner.json` into the dir is secondary defense. STEP-0. |
| 13.44 | **Planner apply isn't crash-atomic across files (the half-landed plunge).** Apply writes `backlog.json` + `suggestions.json` + `.authority-consumed`; per-file temp+rename is atomic, but a kill BETWEEN files leaves inconsistency — marker before beads (trigger cleared, work lost) or beads before marker (trigger re-fires → duplicate beads). | med-high | Every planner op gets a stable id; apply is idempotent on replay (dedupe by op-id); write the trigger-clearing marker (`.authority-consumed`/`.lastaudit`) LAST, after beads are durably written. |
| 13.45 | **Multiple git committers race one working tree.** *(Historically the GDD-ingest endpoint committed directly — GDD ingest REMOVED, §4.)* A tick commits via the Stop hook; committers touching one tree on `cosmo-canyon` → `index.lock` collisions / interleaved commits. The single-writer lock guards control JSON, NOT git. | med | Serialize git: one committer per tree at a time. **(revised §15i — 15.47/15.38, raised med→high):** route ALL `C:/Vibes` committers (bookkeep, plan-apply, merge, Stop hook) through ONE `git-tree` named lock; pull Stop-hook neutralization FORWARD — it interleaves with tick TODAY under the workflow host, not just under parallel. STEP-0. |
| 13.46 | **No budget-headroom precondition before a heavy plunge.** A `structural`/`heavy` bead launched under a near-exhausted daily cap can't finish before the cap trips mid-pass → killed, wasted, churns toward `abandoned` (ties §13.35). | med | Before draining a heavy-tier bead, check enough daily-cap headroom for its expected cost; if low, defer it (drain light beads) until the budget resets. |

**Net:** the persona round (verified against `.claude/settings.json`) exposed that the auto-commit is a
**`Stop` hook, not a timer** — reshaping the commit model (§13.21/13.27/13.29) toward **`RALPH_PASS` labeled
commits + a supervisor-persisted `BASE_SHA` anchor + a deterministic revert/commit script**. The planner
**state machine** cluster (13.31 precedence/idempotency, 13.32 terminal `abandoned`, 13.33 WIP shared
filter) is the next must-fix. The **Barret round** adds the **blast-radius** cluster — worst is **13.36
(authority-shrink self-destruct)**: the diff→remove-built→auto-apply chain can DELETE the game if the
authority shrinks *(originally framed as a GDD source-doc shrink — GDD REMOVED; residual = gutting Ready
specs)*. Gate destructive work (13.36/13.37), detect agy quota-blindness (13.38), and give the supervisor a
consecutive-failure breaker (13.39) before any hands-off run. Wire commit-model + `BASE_SHA` in Phase 1; the
state machine + blast-radius guards in Phases 3/5. The **Varesa round** adds the **"did it actually land?"**
cluster: bead-acceptance is self-attested with no independent check (**13.42 — false-green at the bead
level**), and a killed writer can wedge the control plane (**13.43 stale lock**) or half-apply a plan
(13.44). Fix 13.42 + 13.43 before any hands-off run. Everything else is additive.

---

## 14. Build progress (append-only, newest on top)

- **Host primacy REFRAMED — the desktop app is the PRIMARY orchestrator (2026-07-01).** Per operator: use the
  Claude Code desktop app as much as possible over the CLI — better visibility (the app's tasks pane shows the
  running Workflow + subagents + shell live), less bespoke-glue bugginess, and no invisible token-eating
  processes. Decision: **PRIMARY host = the in-app dynamic Workflow** (`cc-loop.workflow.js`, already built +
  ran); the detached `supervisor.mjs` + per-tick `claude -p` is DEMOTED to a **LOCAL lights-out FALLBACK**
  (KEPT — built + proven — not deleted). Unattended/overnight → prefer the app's native **Routines / cloud
  sessions** (researched: run with the app closed, still visible in the app) over the fallback. **Hard new UI
  requirement (operator):** whenever the fallback supervisor is alive, the dashboard MUST show a LOUD, pinned,
  non-collapsible "fallback supervisor running" banner (pid + tick + one-click Stop) + tab-title flag — it is
  the ONLY path that can run a claude instance invisible to the app's tasks pane. Edited §0/§1/§3/§6/§7/§11.5/
  §15d. The deterministic rails (bookkeep/BASE_SHA/breakers) are host-independent — unchanged. The §15g step-9
  doc-sync must carry the two-host primacy + loud-banner into `AGENTS.md`/`README.md`.
- **§15 redesign RESOLVED into ONE buildable design — DESIGN-EDIT PASS (2026-06-30).** A senior-design-editor
  pass folded §15i (12 new rows 15.41–15.52 + 17 corrections) into the §15a–§15h body + build phases, applied
  §15h to §0/§4/§7/§9/§11, and killed every open contradiction so the doc is ONE authoritative source (not
  design + 3 trailing errata layers). Landed: **(1) STANDALONE app decision** — Cosmo Canyon moves OUT of the
  Launcher into `cosmo-canyon\server.mjs` (:7788) + `ui\index.html`; the Launcher shrinks to a launch-or-open
  button; ONE repo now (§13.20 RESOLVED, §13.22 RESOLVED — the supervisor is a SEPARATE detached process the
  standalone server spawns/adopts). **(2) FC / Workshop / ralph fleet RETIRED** — §13.23 cross-system mutex
  REMOVED; 15.43 fleet-worktree protection simplified to plain hygiene; 15.49 narrowed to human-WIP/other-branch
  scope; the tree-wide guards (15.41), BASE_SHA anchor + one-committer rule STAY (the repo-wide Stop hook still
  exists). **(3) Contradictions eliminated doc-wide:** the GDD is authority NOWHERE (authority = Ready Spec set;
  the GDD was then REMOVED ENTIRELY 2026-07-02 — even the §4 SPLITTER is gone); `state` enum = `not_ready|ready` only (Implemented is a DERIVED projection, never a stored
  flag); `needs_answer` deleted as a state → `hasOpenQuestions` badge; dirty clears only on green-land/unsure-park;
  spec-bead `files[]` = SRC only (`accept/` PROTECTED); server.js is LOCKLESS → imports `lock.mjs`; the
  snapshot/trigger fork is 4-way → shared `spec-core`; completions.json persists projection inputs; REPLACE bumps
  rev + land hash-assert; atlas = filesystem race → serialize derive. **(4) §15g rewritten** — STEP-0 front-loads
  the must-fix-before-ANY-build set (15.41/15.47/15.48/15.23/15.46/15.44/15.49/15.45), a new "Extract to
  standalone" phase precedes the Browser-UI phase, parallel gates on the must-fix-before-PARALLEL set. The
  §13/§15f/§15i tables are kept as the AUDIT TRAIL (Resolution cells updated + tagged "(revised §15i)", no rows
  deleted). **Still design — not built; §15g is the build order.** Follow-ups this pass ALSO closed: **(5)
  tick-driver model (§11.2) RESOLVED** — per-role split (work=sonnet, planner=opus, deterministic stages=Node/no
  model or cheap haiku wrappers), not one global model. **(6) the 5 fleet worktrees `C:/Vibes-wt-*` were actually
  REMOVED** (`git worktree remove` by explicit path — their `ralph-*` branches/commits persist) → the retirement
  premise is now literally true; 15.41's clean-safety was made retirement-agnostic. **(7) `AGENTS.md`+`README.md`
  doc-sync is now a PLANNED build step (§15g step 9), updated incrementally as each phase lands so the docs never
  describe vaporware — NOT rewritten ahead of the code.**
- **Asset Browser + Spec-authority + parallel-toggle redesign — PLANNED (2026-06-30, full design = §15).** Per
  operator ask: (1) tell CC desktop "Do work in Cosmo Canyon" → orchestrator loops subagents until the game is
  to spec; (2) a companion Cosmo tab with a playable preview (Reload/Fullscreen/**Popout**), a live active-tasks
  list, and an **Asset Browser** — each Asset = File (image/audio/spec) + human **Instructions** + agent
  **Questions** + **State** (not_ready/ready/implemented); sortable/searchable + quick-filters + drag-drop; ANY
  change flags it dirty → the orchestrator reworks it. Confirmed decisions: **Specs = source of truth** (the
  Google-Doc GDD retired to an import SPLITTER — later REMOVED entirely 2026-07-02), **Asset Browser = primary input** (backlog/suggestions/
  completions demoted to read-only monitoring), **concurrency = runtime toggle** (serial default / parallel
  worktree-isolated) with the parallel-safe contracts (per-asset file-ownership claim, deadlock-free lock order,
  single-committer merge) **first-class from phase 1** (NOT retrofitted). Designed + adversarially audited by a
  12-agent Workflow (6 design slices + 5 persona rounds → 69 findings → **40 resolved rows, §15f**). Full design:
  **§15** (15a store · 15b spec-authority · 15c loop + 15c-2 parallel model · 15d UI/endpoints · 15e
  Implemented→game + acceptance · 15f 15.x audit · 15g build phases · 15h edits to §0/§4/§7/§9/§11). **Not built
  yet** — §15g is the build order (step 0 = shared-core `spec-core.mjs` + `lock.mjs` hardening). ⚠️ §0/§4/§7/§9/§11
  are now RECONCILED to the spec-authority + standalone-app model (§15h APPLIED — see the top §14 entry); the doc is one consistent source.
  **Hardened (§15i, 2026-06-30):** a 2nd multi-agent CODE-GROUNDED pass (5 lenses code-truth/security/parallel/
  cross-cutover/contradiction → 42 findings → **12 new rows 15.41–15.52 + 17 corrections**, every claim cited to
  file:line, 14 hallucinated/already-guarded findings killed by an adversarial verify stage). Headline **15.41
  (CRITICAL, security):** bookkeep's scope/tamper guard is PREFIX-scoped to `cosmo-canyon/` while the repo Stop
  hook auto-commits the WHOLE tree and workers run `--dangerously-skip-permissions` → an adversarial/injected
  worker can write out-of-tree (e.g. rewrite `.claude/settings.json`) and survive BOTH revert paths → make the
  guard tree-wide + scope the Stop hook before any hands-off run. **Serial-MVP verdict:** safe to build once the
  STEP-0 blockers (15.41/15.47/15.48/15.23/15.46) land; parallel stays gated behind 15.7/15.42/15.43/15.26.
- **In-app Workflow orchestration — desktop client AS the loop host ✅ DONE + RAN (2026-06-30).** Per operator
  ask ("the Claude Code desktop app should be the orchestrator; no CLI process to orchestrate"), added a
  second, interchangeable loop host: a **Workflow** (`orchestrator/cc-loop.workflow.js`) launched from a
  desktop chat session. The Workflow IS the loop (breaker/replan/sinceCommit = script vars; trigger precedence
  = pure JS); every fs/git step is a **spawned agent** (sense=haiku, work=sonnet, planner=opus) running new
  deterministic glue (`state.mjs` shared + `sense/preflight/tick-prep/post-tick/plan-prep.mjs`). **The safety
  rails are unchanged and still deterministic** — `bookkeep.mjs`/`plan-apply.mjs` own gate/commit/revert
  (§13.30/13.42 preserved); `supervisor.mjs` is untouched and remains the detached/unattended path (§13.1's
  spike only ruled out a *headless `claude -p`* hosting a background Workflow, NOT an interactive desktop
  session driving the Workflow tool — that distinction is what makes this path valid). **RAN/verified (live):**
  a full run did 7 ticks → 2 commits (cc-0007 glow, cc-0008 tiled floor), 2 reverts, 3 planner fires
  (blocked×2 + topup), broke cleanly on `.paused`. All rails fired under the new host: opus planner (blocked
  rescope-to-suggestion + topup +3 beads), sonnet worker, **gate-fail revert** (cc-0005), **tamper-guard
  revert+block** (cc-0009 — the agy worker edited `test/sim.ts`, the recurring SEED cheat, caught exactly as
  on the supervisor path), `cc-known-good` correctly tracking the last GREEN commit (cc-0008, NOT the reverted
  cc-0009), clean tree + `.tick.json` cleared on exit. **Gotcha found+fixed:** the Workflow tool passes `args`
  as a **JSON string**, not an object — the script now parses it (else it silently ran 50-tick/planner-on
  defaults); confirmed a bounded `{ticks:1,noPlanner:true}` run now applies `MAX_TICKS=1, NO_PLANNER=true`.
  **Liveness tradeoff (accepted):** the in-app loop runs only while the desktop session is alive; overnight
  unattended stays `supervisor.mjs`'s job. Two hosts, one control plane, one set of rails — see `AGENTS.md`
  "Two ways to host the loop". (Launcher `cc-start`-spawns-the-Workflow wiring deferred — the desktop path
  runs from chat and needs no Launcher.)
- **Phase 7 — Safety rails & drift ✅ DONE + RAN (2026-06-30).** Most rails were already built + proven in
  Phases 1-6: PostPassGuard-as-JS (oversize >800 / tamper, fixes 4/3), attempts++→`abandoned` (5/13.32),
  sim-purity determinism ban (7), daily `.usage` cost cap (8), consecutive-failure breaker (13.39),
  agy-quota failover (13.38), WIP filter (13.33), GDD shrink-guard (13.36), source-art lockout (13.41). Phase
  7 ADDED: **known-good tagging (13.40)** — the supervisor moves a `cc-known-good` git tag to each gate-green
  commit (and correctly does NOT tag a reverted tick); a Launcher **`POST /api/cosmocanyon/rollback`** resets
  the game to that tag (guarded: refuses while the loop is alive). **stalled heartbeat (fix 6)** — tracks
  `sinceCommit`, writes `control/.stalled` after N no-progress ticks (UI flag; the breaker still does the hard
  pause). **auto-snapshot cadence (fix 10)** — Launcher timer runs `snapshot.mjs` → `latest.png` every 180s.
  - **RAN/verified:** cost-cap (`--cap 0` → immediate stop); a real tick correctly **reverted** cc-0005
    (worker edited `test/sim.ts` to fake a pass — the exact FC cheat — tamper guard caught it, no tag created);
    `cc-known-good` tagged at a green commit; **rollback** restored the game from a throwaway change to the tag;
    status surfaces `knownGood`/`stalled`/snapshot. (The tamper guard tripping on cc-0004 AND cc-0005 shows the
    rail works repeatedly against the recurring "edit the gate test" worker behavior.)
  - **HARDENING BACKLOG still open** (per the build order, deferred): heavy-bead budget headroom (13.46),
    git-committer serialization ingest-vs-tick (13.45, partial — ingest has a brief index.lock retry),
    planner-apply op-id replay (13.44, markers-last done), infra-kill≠attempt nuance (13.35, basic), and the
    Launcher FC-watchdog-vs-cosmo coexistence (§Phase-4 note).
- **Phase 6 — Asset pipeline ✅ DONE + RAN (2026-06-30).** `derive.mjs` (pngjs source→art box-downsample +
  shelf-pack atlas), `registry.ts`, `budget.ts` came with the borrowed scaffold (gate-green). Added Launcher
  **`GET /api/cosmocanyon/assets`** (manifest + wishlist) + **`POST /assets/upload {key,file}`** (base64,
  magic-byte validate, atomic source write, run derive, flip status `real`) and **source-art lockout
  (13.41)** in bookkeep (worker writes to `assets/source/**` → tamper revert). **RAN:** full upload
  round-trip — generated 32×32 PNG → POST → derive ran (`player.hero -> art/...; atlas.png 2048×48; manifest
  updated`) → status `real`, counts 1/3 → reverted to placeholder; gate green. Source-lockout proven
  (deterministic: a `source/` edit → `reverted (tamper: ...assets/source/...)`). (Asset UI panel deferred —
  endpoints work.)
- **Phase 5 — GDD ingest ✅ DONE + RAN (2026-06-30).** Ported FC's `/api/cosmocanyon/gdd` (extract doc-id,
  fetch `export?format=md`, validate: non-doc→400, HTTP-err→502, empty/login-HTML→422, never clobber a good
  GDD) + the **semantic-shrink self-destruct guard (13.36)**: a diff removing >35% content / >2 headings
  returns `409 {needsConfirm}` (operator must re-submit `confirm:true`) so an accidental edit / swapped
  share-link can't cascade "remove built feature" beads. Destructive `remove` work is routed to design
  suggestions, not autonomous (13.37, planner-side). **RAN (live, real Google Doc):** reject paths (400/502)
  + no-clobber (sha unchanged) ✓; **the configured doc had actually CHANGED** since the snapshot (Environment
  section split) → happy-path **fetched/validated/committed** it; the new sha ≠ `.gdd-consumed` → the next
  tick's sense fired a **diff planner** (opus) that recognized the new "Environment - In-Match" section,
  added 2 beads, and **`.gdd-consumed` was updated → trigger cleared** (full ingest→diff→idempotent chain).
  Shrink guard proven: a 53.2% shrink → `409 needsConfirm`.
- **Phase 4 — Launcher integration ✅ DONE + RAN (2026-06-30).** `D:\Ag\launcher` (separate repo): added
  `/api/cosmocanyon/*` (status/backlog/suggestions/completions/agent/focus/snapshot/aux) + **`cc-start`/
  `cc-stop` as dedicated endpoints** that spawn/kill the **standalone detached `node supervisor.mjs`** (NOT a
  server setInterval — survives a Launcher restart, §13.22) + `ccEnsureVite` (:8780). `index.html`: a PRIMARY
  **`#view-cosmocanyon`** tab (status banner + Start/Stop + engine picker + steer + backlog + suggestions +
  completions + planner/host logs + preview iframe) and **shelved Fleet/Workshop/FC** to a muted "legacy:"
  nav cluster.
  - **RAN (live):** started the Launcher → `/api/cosmocanyon/status` + `/agent` return correct JSON; UI tab +
    nav served. `POST /start` → supervisor spawned (pid 41064), `status` showed `alive=true, inFlight=cc-0005`
    (the GUI sees the running loop); `POST /stop` → `killed=41064`, `paused=true`, 0 orphans. Start→watch→stop
    from the GUI works.
  - **Two real bugs found + fixed:** (1) the cross-system mutex false-positived on the FC **vite dev server**
    (its `npm run dev` logs to a `...fortcondor/logs` path → the broad regex matched the substring) → tightened
    to match only real loop processes (`ralph.ps1`/`refinery.ps1`/`planner-prompt`/`PROMPT.md`). (2) several
    runtime markers (`.supervisor.pid`, `.plan-latch.json`, `.agy-strikes`, …) weren't gitignored → they made
    the supervisor see a "dirty tree" every start (spurious reset) and `clean -fd` **deleted `.supervisor.pid`**
    (so cc-stop couldn't find the loop) → gitignored them all.
  - **Known coexistence issue (noted, not yet fixed):** the Launcher's FC **watchdog** (`fcWatchdogTick`) fires
    FC opus planners independent of FC's stopped worker; on the shared `cosmo-canyon` branch those would commit
    FC backlog churn. The cosmo mutex blocks cc-start while an FC *planner* is alive, but the Launcher should
    pause its FC watchdog when cosmo is running (or run only one system). For now: stop the Launcher's FC side
    before a hands-off cosmo run. (Ties §13.45 git-committer serialization / §13.20 two-repo.)
- **Phase 3 — opus planner (4 modes) + deterministic apply + trigger precedence ✅ DONE + RAN (2026-06-30).**
  `planner.md` (opus, topup/diff/blocked/audit → `control/.plan-result.json`), `plan-apply.mjs` (the
  deterministic authority: validate + dedup + **WIP-reject (13.33)** + route design→suggestions + markers-LAST
  (13.44) + latch-on-empty (13.31) + commit), supervisor `computeSnapshot`/`computeTrigger` with **precedence
  diff>blocked>topup>audit, one trigger/fire, per-mode latch, max-replan ≤2 (13.31)**, abandoned excluded
  from triggers (13.32). The supervisor now spawns a PLANNER tick (opus) when a trigger is live, else a WORK
  tick.
  - **RAN (real opus):** empty backlog → readyCount 0<3 → **topup fired** → opus invented 6 beads toward the
    Realm Survivors GDD; plan-apply **committed 4, WIP-rejected 2** — including one the planner *explicitly*
    proposed citing "GDD line 24" (reduced-reward hex replay) that the deterministic WIP filter caught
    because it maps to the `(WIP, DO NOT IMPLEMENT)` Quests/Rewards section. Exactly 13.33's purpose (a prompt
    rule isn't enough; the shared filter is the backstop). Then the supervisor drained `cc-0004` (planner→work
    handoff). **Bonus:** cc-0004's first attempt edited `test/sim.ts` → the tamper guard reverted it
    (`ralph cc-0004: revert (tamper...)`), the worker self-corrected, and it landed clean. Gate green throughout.
- **Phase 2 — hybrid worker dispatch (agy logic / Claude feel) ✅ DONE + RAN (2026-06-30).** Built
  `orchestrator/agy-pass.ps1` (the proven §12 own-console recipe — `& agy -p` with NO stdout redirect,
  `--model gemini-3.5-flash --print-timeout 30m --dangerously-skip-permissions --log-file`; writes its pid
  to `control/.agy.pid`), `orchestrator/snapshot.mjs` (puppeteer-core headless shot of :8780), engine
  routing in `tick.md` (bead.engine > feel/visual→claude > `agent.json` default), `bookkeep.mjs --result
  agy-noop` (zero-diff = quota/auth signal, no attempt bump, strike counter), supervisor agy lifecycle
  (orphan-kill at reconcile + on timeout since agy is in its OWN console not the tick tree §13.16;
  quota-strike → flip `agent.json` to claude + alert §13.38), `control/agent.json` default engine.
  - **RAN both engines (real):**
    - **agy logic bead (cc-0002, lowestHp):** the claude tick wrote an absolute-path worker prompt, launched
      agy own-console, polled `git diff $BASE_SHA`; **agy (free Gemini) edited the 3 scoped files (6 lines)**
      and self-reported via `progress.json`; bookkeep gate PASS + independent grader PASS (`lowestHp
      monotonic, final=300`) → committed `ralph cc-0002:` in 134s. **The hybrid free-Gemini worker lands a
      verified edit.**
    - **feel bead (cc-0003, menu title outline):** the claude tick edited `mainmenu.ts` (4px dark stroke),
      committed (gate green) in 51s; the headless `snapshot.mjs` → `latest.png` → **Claude `Read` the PNG and
      confirmed the gold title now has a readable dark outline** (FEEL-VERIFIED logged). This is the in-loop
      visual QA FC could never do (agy is preview-blind, spike B).
  - **agy smoke (isolation) before the run:** proved agy works end-to-end via the recipe (real
    `streamGenerateContent` calls, auto-approved tool exec, wrote the requested file). The
    `not logged into Antigravity` log lines are a SECONDARY service; the model backend (`daily-cloudcode-pa`,
    G1 credits) authed fine. So agy is usable headless here right now.
  - **Gotchas fixed:** (1) agy resolves a *relative new-file* path against a different cwd (wrote to
    cosmo-canyon root, not game/) → the agy worker prompt now uses ABSOLUTE paths. (2) `snapshot.mjs` (in
    orchestrator/) couldn't import `puppeteer-core` (it's in game/node_modules; ESM resolves from the script
    dir) → use `createRequire` rooted at game/. **STOPPED here for review before Phase 3 (planner).**
- **Phase 1 — core tick + supervisor + deterministic bookkeep ✅ DONE + RAN (2026-06-30).** Built
  `orchestrator/{tick.md, supervisor.mjs, bookkeep.mjs, lock.mjs}` (Claude-only worker, no planner yet).
  - **Commit model exactly per §13.21/13.27/13.29/13.30:** supervisor persists `control/.tick.json
    {pid,startEpoch,baseSha,beadId}` BEFORE spawning each `claude -p < tick.md` (env `RALPH_PASS=<bead>`,
    `CLAUDE_PROJECT_DIR=C:/Vibes`). `bookkeep.mjs` is the SOLE deterministic authority — runs the node gate +
    the per-bead independent acceptance check + guards, then commits (`ralph <id>: <title>`) on pass or
    `git reset --hard $BASE_SHA` + scoped `clean -fd cosmo-canyon` on fail. The model only does sense+work
    and calls `bookkeep --result work|blocked`; it never runs git or decides pass/fail. The repo Stop hook is
    a harmless backstop (bookkeep leaves a clean tree → hook no-ops).
  - **Supervisor guards (BLOCKER set):** branch assert ==`cosmo-canyon` (13.24); cross-system mutex — refuse
    if any FC/Workshop/fleet loop alive (13.23); single-flight via `.tick.json` live-pid check (13.5/13.28);
    per-tick timeout → `taskkill /T` (13.17); consecutive-failure breaker → auto-`.paused` (13.39); daily
    `.usage-YYYYMMDD.json` cap; killed-tick reconcile on start (dead pid → `reset --hard baseSha` + clean +
    clear). Infra-kill (timeout) does NOT count as a bead attempt (13.35).
  - **bookkeep guards:** tamper (edits to `test/{canary,determinism,sim-purity,budget,sim}.ts`, `accept/**`,
    `package.json` — SEED protected via whole-`sim.ts` block, 13.26/13.29); out-of-scope edit (outside
    `game/**`); no-op (zero diff vs BASE → 13.42 crude false-green catch); oversize >800 lines (13.4);
    independent per-bead acceptance via `game/accept/<id>.ts` run vs the worker's diff (13.42); `attempts>=3`
    → `status:"abandoned"` terminal (13.32). Control writes wrapped in stale-breakable pid+epoch locks (13.43).
  - **RAN it (real `claude -p` ticks, not simulated):**
    - **Happy path (live):** seeded bead `cc-0001` (add `peakEnemyCount` to Snapshot) → supervisor spawned one
      sonnet tick → worker edited exactly the scoped files (`state.ts`+`loop.ts`+`harness.ts`, 7 src lines) →
      bookkeep gate PASS + independent grader PASS (`peakEnemyCount monotonic, final=26 >= enemyCount 26`) →
      committed `ralph cc-0001:` in **51s**; backlog drained → next tick idle → supervisor stopped. Gate +
      grader green on the new HEAD.
    - **Forced gate-fail (deterministic):** injected a sim-purity violation → bookkeep → `reset --hard
      BASE_SHA` + clean, bad edit gone, tree clean, `attempts=1`, bead `blocked`, anchored revert commit,
      `status.json stage=reverted`. Orchestrator files survived (scoped clean).
    - **Tamper (deterministic):** edited `test/canary.ts` → caught by path (pre-gate) → reverted + blocked.
    - **Killed-tick reconcile (deterministic):** stale `.tick.json` (dead pid) + dirty tree → supervisor
      `--reconcile-only` reset to baseSha + cleaned + cleared `.tick.json`.
  - **Gotcha learned:** a PowerShell backtick (``` `t ```) inside a JS template literal silently terminates
    the string (SyntaxError, script never runs) — rewrote `assertMutex` without backticks. And: **any
    uncommitted edit to a TRACKED orchestrator file is wiped by `reconcile`/`bookkeep`'s `reset --hard`** — fixes
    to the loop scripts must be COMMITTED before the loop relies on them. **STOPPED here for review before Phase 2.**
- **Phase 0 — scaffold ✅ DONE (2026-06-30).** `cosmo-canyon/game/` borrowed from `realm-survivors/v2` (fresh,
  no FC collision); harness `window.__rs`→`window.__cc`; port 8770→**8780** (`.claude/launch.json cc-game`);
  +`puppeteer-core@23.11.1`; fresh `npm install` (lockfile committed). Node gate GREEN (canary/determinism/
  sim-purity/sim/budget). Seeded `game/docs/HUMAN_GDD.md` from `control/gdd-snapshot.md` + `control/.gdd-consumed`
  (git blob sha, 13.34). Empty committed control plane + `cosmo-canyon/.gitignore` (13.9). Dedicated
  **`cosmo-canyon` branch** (13.8); preflight stopped the live FC loop (mutex 13.23) + checkpointed its
  untracked file on `ralph-proto-iter`. Vite serves on 8780 (title + `main.ts` 200).

## 15. Asset Browser + Spec-authority model (operator redesign 2026-06-30)

Three confirmed operator decisions reshape the input model. **(1) Specs are THE source of truth** — the Google-Doc GDD becomes an optional bulk IMPORT that SPLITS into Spec assets; ONE authority, no monolith-vs-specs contradiction. **(2) Concurrency is a RUNTIME toggle** (serial single-flight default, or parallel worktree-isolated) via `control/config.json` both hosts read — but the parallel-safe contracts (per-asset file-ownership, disjoint-files partition, deadlock-free lock order, single-committer merge) are FIRST-CLASS from phase 1, baked into the core data model + lock discipline; only the toggle selects runtime behavior. **(3) The Asset Browser is the PRIMARY human input surface** — existing backlog/suggestions/completions views become read-only monitoring. Every rail from §13 is REUSED, not forked: assets FEED the existing bead ledger; `bookkeep.mjs` stays the sole deterministic gate/commit authority anchored to `BASE_SHA`; the manifest stays the positioning authority.

### 15a. Asset data contract + store layout

Folder-per-asset (independent atomic-rename targets → a browser edit to asset X never races an agent implementing asset Y). `assets.json` is a **pure derived index** (rebuildable by scanning meta files — never the authority, never a merge bottleneck).

```
cosmo-canyon/control/
  assets.json                 ← DERIVED index (list/sort/search; rev-checked, rebuildable)
  assets/<assetId>/
    meta.json                 ← per-asset AUTHORITY (schema below)
    file.<ext>                ← artifact (png|jpg|wav|mp3|txt|md); one per asset
    history/<contentHash>.<ext>  ← prior versions on replace (MANDATORY for implemented images + all audio, §15.9)
  claims/<assetId>.claim.json ← parallel claim token {beadId,pid,startToken,epoch,filesLease[],baseSha,worktree}
  locks/                      ← EXISTING lock.mjs dir + named locks (§15c-2 order)
  .trash/<assetId>/           ← tombstone on DELETE (recoverable, never hard-unlink)
```

`assetId` = `a-<base36 epoch>-<4rand>` (opaque, stable, never reused). ONE id scheme across all slices (resolves the a-…/cc-a…/slug three-way contradiction — audit 15.30). **Every client-supplied `id` MUST be validated (15.45, revised §15i):** regex-match the mint format EXACTLY `^a-[0-9a-z]+-[0-9a-z]{4}$` AND assert path-containment `resolve(base,id).startsWith(resolve(base)+sep)` for ALL roots (`assets/`, `claims/`, `.trash/`); extend the SAME guard to `lock.mjs` (sanitize/reject the `asset-<id>` name before `mkdirSync`) — else `id=../../game/src/...` traverses out of the store.

```jsonc
// meta.json — the per-asset authority
{ "id":"a-lx9k2p-7fa3", "kind":"image|audio|spec",
  "filename":"hero_walk.png", "file":"file.png", "mime":"image/png",
  "contentHash":"sha256:…",            // artifact BYTES only (detects REPLACE)
  "instructions":"", "questions":[{ "id","text","by","at" }],
  "state":"not_ready|ready",           // HUMAN-owned ONLY (§15c). Implemented is DERIVED, NEVER a stored value (15.32, revised §15i)
  "dirty":true, "hasOpenQuestions":false,  // badge over state=ready, NOT a state (15.34); true when a worker appended a Question
  "manifestKey":"player.hero|null", "files":["src/render/stage.ts"],  // OWNERSHIP unit = SRC files ONLY (§15c-2; accept/ is PROTECTED — never a partition token, 15.7)
  "placeholderOnly":false,             // migration: manifest slot w/ no source art (§15.31)
  "implementedBy":{ "beadId","sha","contentHash","rev" }|null,  // PROVENANCE only, written by bookkeep — NOT a state flag (15.32)
  "implementedContentHash":null,       // hash accepted at land (dirty-flap guard + stale-bytes land assert, 15.29/15.39)
  "abandonCount":0, "questionRounds":0,  // per-ASSET breakers (§15.3/15.4)
  "created","updated","rev":7 }
```

**Dirty rules (exact, idempotent, last-writer-safe — read-modify-write under `asset-<id>` lock, absolute value not toggle).** SET `dirty=true` on: upload; file REPLACE (`newHash != implementedContentHash` — NOT `!= stored`, kills the implemented↔dirty flap, audit 15.29); instructions edit; clear-questions; `not_ready→ready`; `implemented→not_ready`; agent appends a Question. CLEAR `dirty=false` ONLY on bookkeep landing the owning bead green (`ready→implemented`), and on `unsure`-park (§15c). NOT cleared by minting/claiming/blocked. **contentHash is computed ONCE in the create/replace endpoint's critical section that renames the artifact — never re-derived by a scanner** (that mismatch causes the H_old/H_new re-work loop, audit 15.28). Artifact + meta write = temp files + rename BOTH in under `asset-<id>` lock, **meta renamed LAST** (markers-last) → a kill leaves the old consistent pair. **`rev` is a monotonic authority-generation** — bumped by upload / **REPLACE** / instructions / answer / state (add REPLACE to the rev++ set, 15.39, revised §15i) so the `(assetKey,rev)` fire-latch re-fires on new bytes and `reconcile` supersedes ANY prior non-terminal bead for the key; bookkeep asserts `hash(graded artifact)==meta.contentHash` at land as the deterministic stale-bytes-false-green backstop (15.39).

### 15b. Spec-authority (the Ready Spec set is THE authority)

Authority is the **set of Ready Spec assets** (there is NO GDD — removed 2026-07-02; no `HUMAN_GDD.md`). A deterministic `spec-compile.mjs` compiles Ready specs → `control/spec-doc.md` (planner north-star) + `spec-index.json` (machine list), **under the `assets-index` lock held for the whole scan+write** so the set is snapshot-consistent; it RETURNS the authority-hash it computed so `state.mjs` latches on the exact set compiled (no independent recompute → no doc/hash disagreement, audit 15.24). (A torn compile read is NOT observable while both hosts are strictly serial — this lock is a forward-looking guard for the concurrency toggle; keep it as-written. 15.24, revised §15i.)

- **Authority latch:** `authoritySha()` = `sha1(join(sorted(readySpecs.map(s=>s.id+":"+s.blobSha))))`. SNAPSHOT fields are `authorityChanged`/`authoritySha` (the old `gddChanged`/`gddSha` names were RENAMED with the GDD removal); the consumed marker is `.authority-consumed` (stores the authority-hash; renamed from the old `.gdd-consumed`). `wipKeywords()` reads `spec-doc.md`; a Not-Ready spec is EXCLUDED from the compiled doc (primary WIP wall, stronger than the keyword grep, which stays as a secondary backstop). **Shared-core extraction is step 0** (§15g): `spec-core.mjs` owns `computeSnapshot` + **`computeTrigger(snap)→{mode,latchKey}`** + `authoritySha` + `wipKeywords`; `supervisor.mjs`'s duplicated `computeSnapshot`/`computeTrigger`/`wipKeywords` are deleted + imported. **The trigger+latch logic was duplicated across FOUR surfaces** (supervisor.computeTrigger, `state.mjs` computeSnapshot, `cc-loop.workflow.js` decideTrigger inline, `plan-prep.mjs` latchKey — 15.22 correction, revised §15i): supervisor imports spec-core directly; `sense.mjs` COMPUTES+EMITS the trigger decision INTO the SNAPSHOT so `decideTrigger` becomes a passthrough (the Workflow host CANNOT `require` a `.mjs` at runtime — that's why it stays inline JS). Add `readySpecCount`/`authorityEmpty` to the shared SNAPSHOT and make `authorityEmpty` the FIRST `computeTrigger` branch (`→{mode:null}`, write idle-blocked-on-human, never reach the topup line — 15.33 correction). Reconcile is NOT unified (git/fs-mutating; the Workflow host has no fs/git — already correctly factored as `preflight.mjs`). Parity gate-test asserts identical SNAPSHOT AND identical `computeTrigger(snap)` (mode AND latchKey) across hosts on a fixture — else the two hosts disagree on the authority = dual authority ACROSS hosts (audit 15.22, CRITICAL).
- **Debounced authority changes (audit 15.16):** an `authoritySha` change starts/extends a `control/.authority-settle` window (≈90s); `authorityChanged` fires `diff` only after the window settles + coalesces to once-per-settled-generation. Browser authority-writes touch the settle marker. Stops a curation burst (10 quick toggles) = 10 opus plans.
- **GDD-ingest / SPLITTER — REMOVED (2026-07-02, §4).** There is no doc import. Specs are authored/edited directly in the Asset Browser (§15d). *(Historically a `POST /api/cosmocanyon/gdd` splitter parsed a Google-Doc into Not-Ready Spec assets; it was deleted with the GDD concept — do NOT reintroduce.)*
- **Spec-delete / shrink / self-destruct guard (§13.36/13.37 parity, hardened):** Ready == authority == operator-owned. **Never auto-update a spec that is `ready` OR `implemented`** — a body-change to a live-authority spec is appended as a Question, never clobbers (audit 15.6). **Per-spec shrink check** (incoming body < 40% of current) AND the aggregate `ccShrinkGuard` (35%/>2) both trip `409 needsConfirm` (aggregate alone misses gutting 3-of-40 specs, audit 15.11). Toggling a spec Ready→Not-Ready that has completed beads citing it requires confirm ("N built features cite this — retiring flags them as drift"); DELETE of a Ready/Implemented spec is confirm-gated + tombstoned; the cascade (removing built code) is always suggestion-only, never an autonomous bead. Persist `.authority-known-good` (Ready-spec-id-set + hash) at each green commit; `audit` diffs drift against LAST-GREEN authority, not the live set (audit 15.5).

### 15c. Loop integration

Assets are a deterministic PROJECTION `dirty+Ready asset → bead`, and REFLECTION `bead outcome → asset state`. No parallel state machine; the asset never decides pass/fail.

State machine (human/agent transitions strictly partitioned; illegal = 409/no-op):

The `state` enum is HUMAN-owned and contains ONLY `not_ready|ready` — **Implemented is a DERIVED projection, never a stored state** (15.32); **open Questions are a `hasOpenQuestions` badge over `state=ready`, never a `needs_answer` state** (15.34, revised §15i):

| From | To | Actor | Trigger |
|---|---|---|---|
| not_ready | ready | HUMAN | operator marks implementable |
| ready | not_ready | HUMAN | resume editing (Questions persist through ready↔not_ready — 15.34) |
| ready | *(Implemented — DERIVED projection)* | **DERIVED (bookkeep records `implementedBy` provenance, NOT a flag — 15.32)** | owning bead lands green + acceptance pass + (img/audio) manifest real, bound to contentHash+rev |
| *(Implemented)* | not_ready | HUMAN | re-open for rework → full cross-authority invalidation (§15e) |
| ready | ready + `hasOpenQuestions` badge | AGENT (bookkeep, `unsure`) | worker appended a Question — **state STAYS `ready`**, `dirty=false`, `bead.status=parked` (15.34) |
| `hasOpenQuestions` cleared | ready + dirty | HUMAN | `/assets/answer` clears Questions → bumps rev → re-arms ONE fresh bead |

- **Asset-scan lives INSIDE `sense`** (both hosts, factored to shared `assets-core.mjs` — never a 3rd top-level branch): `reconcileAssets(backlog)` pre-step, pure fs, LOCKED, idempotent — for each `ready+dirty` asset with no non-terminal bead referencing it, mint `asset-<key>-r<rev>` (rev in id → a re-armed asset is a new bead). **Own fire-latch** keyed `(assetKey,rev)` (`.asset-scan-latch.json`) so a permanent ready+dirty asset doesn't re-scan the whole store every tick — bounds sense to O(changed) (audit cost-round). `dirty` clears ONLY on green-land OR unsure-park (aligns to the normative §15a dirty-rules — 15.34, revised §15i).
- **projectAssetToBead:** image→light/claude, audio→light/claude, spec→heavy. **Acceptance MANDATORY for any `assetKey` bead** — bookkeep **AUTO-DISCOVERS `game/accept/<bead.id>.ts`** and runs it if present (independent of `acceptanceCmd`, since the planner/server never emit `acceptanceCmd`); missing grader = **FAIL-closed** for `assetKey` beads AND for any bead the planner flags sim-checkable via a `renderOnly:false`/`acceptanceKind` DATA field, while legitimately render-only beads skip WITHOUT deadlocking to `abandoned` (15.13 correction, revised §15i — kills the "no grader = default green" class, audit 15.13; broaden beyond `assetKey`). Image/audio auto-mint a deterministic grader at mint time (render-reachability + snapshot-region for image; file-exists+decodable+reached-by-playback for audio, §15e). Spec grader = REQUIRED, planner-authored but **lands DISABLED until operator confirms** + must pass a **mutation check** (grader run against BASE/unimplemented tree must FAIL, else tautological → rejected) + a positive `ACCEPT-PASS <beadId>` token on the last line (exit-0-without-asserting is caught, audit 15.14/15.17).
- **Questions gate (anti-livelock):** worker returns `unsure` → `bookkeep --result unsure` (deterministic): revert partials, append+dedup Questions, set `hasOpenQuestions=true` (**state STAYS `ready`**, NOT a `needs_answer` state — 15.34, revised §15i), `dirty=false` (never re-fires the scan), `bead.status=parked` (terminal-for-loop, NOT attempts++). Human answers = ONE atomic `POST /assets/answer {id,rev,instructions,clearQids[]}` — verifies rev, sets instructions, clears ONLY the questions present at that rev (a concurrently-appended new question survives), sets `ready+dirty`, bumps rev (audit concurrency-round). **Per-asset `questionRounds` breaker:** after 3 re-arm cycles → hard-park `escalated` + `.needs-human` alert; a same-inputs re-arm (instructions hash unchanged) warns instead of re-looping.
- **Completion predicate ("run until to spec"):** `every ready asset implemented && no ready+dirty && readyCount==0 && gate green && no hasOpenQuestions (open-questions assets excluded from readyCount — 15.34) && no feel-pending/escalated`. Invariant asserted each sense: `dispatchable ∪ implemented ∪ waiting-on-human = all ready assets` — an asset in none is logged+parked, never silently spun. Two honest stops: `to-spec` (fully done) and `idle-blocked-on-human` (work left needs a human). **Breakers OUTRANK completion** and STAY: per-tick order `paused? → capReached? → [work/plan; breaker may trip] → toSpec/idle?`. The consecutive-fail breaker is **redefined for the asset era**: trip on `M cycles with NO NET REDUCTION in openWork (unimplemented ready assets + non-terminal beads)`, NOT on "any green somewhere" — else one trivial asset landing per cycle masks an infinite thrash (audit cost-round CRITICAL). Benign outcomes (`parked`/`unsure`/infra-kill) do NOT increment the failure breaker.
- **Toggle surface:** `control/config.json` `{concurrency:{mode,maxConcurrency,isolation:"auto",worktreeRoot:"C:/Vibes-cc-wt",perAgentTimeoutMin,heavyCostReserve}}` (COMMITTED). Read in `computeSnapshot`; default `serial` if absent/invalid.
- **`control/active.json`** (gitignored) — live in-flight tasks; browser polls 3s. Written at dispatch (with the tick record, under `active` lock), removed by post-tick/bookkeep on ANY terminal outcome. `reconcileActive()` every sense GCs entries whose pid is dead (parallel) or whose `.tick.json`/run-token is gone (workflow path, pid is null). Grace window (≈30s) so a just-spawned entry isn't GC'd mid-start.
- **Single cc-host lock:** `control/.cc-host.lock` (pid+kind, stale-breakable) refuses a second cc host on the same control plane — the asset/parallel model assumes ONE loop host (audit cost-round: dual hosts double-bump the cap + reconcile-destroy each other).

### 15c-2. Parallel concurrency model

Core invariant: **at any instant the union of all in-flight agents' `files[]` is owned by AT MOST ONE agent, and control-plane lock acquisition follows ONE global order** → no two agents write the same game file, no lock-wait cycle. Serial = N=1 of this model (disjoint test vacuous, inline no-worktree = today's path byte-for-byte). Designed UP FRONT; the toggle only selects runtime.

- **Unit = an ASSET.** `resolveFiles`: (1) declared `files[]`; (2) inferred by kind (image→`assets/source/<key>.*`+manifest; audio→`assets/audio/<key>.*`+audio-manifest); (3) UNKNOWN (spec with empty files) → **global-exclusive but NOT blanket**: give spec beads an explicit `files[]` = planner-declared **SRC files ONLY** (NOT the `accept/` grader path — `game/accept/**` is a PROTECTED_PREFIX the worker may not edit → it can NEVER be a partition/ownership token; the grader is authored in a SEPARATE planner step under its own committer BEFORE dispatch — 15.7 correction, revised §15i) so they partition normally; only truly-unknown-surface assets go exclusive, capped to a small dispatch budget, and the scheduler `continue`s past an exclusive asset when lighter partitionable work exists + priority-ages the deferred (audit cost-round: don't starve behind a churning exclusive spec).
- **schedule.mjs** (shared, deterministic, top of cycle): `slots = clamp(min(maxConcurrency-active, capRemaining))` computed **inside schedule right before dispatch** (not cycle-top — else a cycle starting at cap-1 overshoots by N-1, audit cost-round). Cap is **tier-weighted** (`light:1,heavy:3,structural:5`) so N heavies can't sneak under a count ceiling; a heavy needs `capRemaining>=heavyCostReserve` (hard precondition). Overlapping-files assets are SERIALIZED by construction (not picked this cycle, no lock held across the wait → can't deadlock).
- **claim.mjs** (atomic, exactly-one, heartbeat-stale): claim under the `active` lock with a disjointness re-check (TOCTOU guard = linearization point). **Work-claims heartbeat**, not the 5-min control-plane TTL: the agent re-stamps `epoch` every N s; staleness = missed heartbeats (≈3×), aligned to exceed the per-agent timeout so the watchdog frees the claim, never a race between two schedulers (audit concurrency-round). Guard pid-reuse with a `startToken` (process start-time/random) — `alive(pid)` counts only if the token still matches.
- **Deadlock-free GLOBAL LOCK ORDER (Invariant L):** `1 active → 2 assets-index → 3 asset-<id>(subsort by id) → 4 claims → 5 backlog → 6 completions → 7 manifest/audio-manifest → 8 git-tree`. `acquireOrdered([...])` holds the needed set until the op completes, releases in reverse; a cross-file invariant (claim vs files[]) REQUIRES holding both — resolve the §7 "one-at-a-time" vs `acquireOrdered` contradiction in favor of hold-ordered (audit concurrency-round). **Fix `lock.mjs` first (15.48, revised §15i — STEP-0):** the load-bearing fix is a BUSY-grace keyed on the lock DIR's own `ctime/mtime` (`statSync(dir)`), NOT `owner.epoch` (UNREADABLE during the mkdir→write window) — on read/parse failure, if `Date.now()-statSync(dir).ctimeMs < ~2000` → stale=false (sleep+retry), else stale=true; temp+rename `owner.json` INTO the dir is SECONDARY defense (the real race is ENOENT, not a torn read). NOT sufficient alone for one-writer-on-backlog — must land WITH 15.23 (server.mjs must take the SAME `backlog` lock; today it takes none).
- **Worktree lifecycle + single-committer merge:** parallel agent gets `C:/Vibes-cc-wt/<assetId>` off `cosmo-canyon` `--detach` at BASE_SHA. **Per-agent tick anchor** — `.tick.json` is NOT singular under N>1; store `baseSha`/`beadId`/`worktree` in the claim, pass `bookkeep --tick <path>` (audit concurrency-round CRITICAL). **Retrofit bookkeep so EVERY git mutation takes an explicit tree arg + asserts `rev-parse --show-toplevel == the claim's worktree` before any `reset --hard`/`clean -fd`** — a mismatch aborts non-zero (audit blast-radius CRITICAL: current `reset --hard`+`clean -fd cosmo-canyon` runs in cwd, wrong-tree = nuked sibling work). Gate runs in the worktree; on green the agent does NOT self-commit — it hands to a merge step holding the `git-tree` lock (rank 8) that cherry-picks onto current HEAD, **re-runs the bead's ACCEPTANCE at post-merge HEAD** (not just `npm run gate` — audit false-green: green build ≠ working feature at the seam), commits via `bookkeep.commit()`, moves `cc-known-good`, cleans up. `manifest.json`/`backlog.json` are merged as **structured JSON key-unions under their own ranked locks BEFORE the cherry-pick** (two different keys never textually conflict) so the expensive re-gate is reserved for genuine source conflicts + auto-drops `maxConcurrency` if it fires too often. **Atlas is a global reduction, NOT per-key disjoint (15.37 correction, revised §15i):** `derive.mjs` regenerates the WHOLE atlas from all real keys, and `atlas.png/json`+`art/*` are GITIGNORED → the hazard is a FILESYSTEM race in a shared checkout, not a git-merge conflict. Serialize ALL `derive()` invocations (incl. the GUI-upload derive) on ONE lock, and run `derive` ONLY in the single-committer merge tree (after all keys are in the manifest) — never concurrently; exclude image beads from the disjoint-files assumption on the atlas (only committed `manifest.json` can line-conflict → route it through the single committer too).
- **Stop-hook neutralized on parallel workers (audit concurrency-round CRITICAL):** the real Stop hook `cd $CLAUDE_PROJECT_DIR (=C:/Vibes main tree) && git add -A && commit` — it does NOT self-isolate per worktree. Spawn parallel workers with an env flag the hook early-exits on (bookkeep = sole committer, §13.30) OR set `CLAUDE_PROJECT_DIR` to the worktree. **Scope every committer narrowly:** `git add -A cosmo-canyon/game` for worker commits; control-plane files committed by their own path under lock; `revertToBase` must NOT `reset --hard` across `control/assets` (a failed tick would destroy a concurrent upload, audit concurrency-round).
- **N-agent reconcile** (`preflight.mjs`, both hosts) — **MODE-CONDITIONAL on `config.concurrency.mode` BEFORE any destructive op (15.26 correction, revised §15i):** serial → keep today's single `.tick.json` reset byte-for-byte; parallel → iterate `active.json`, per dead/stale claim `worktree remove --force <its explicit path>` + release claim + kill its per-agent agy pid, each `show-toplevel==claim.worktree`-guarded (isolated tree discard, shared branch untouched); infra-kill mid-valid-work ≠ attempt; the singleton `.tick.json` reset is NOT run under N>1. **FORBID bare `git worktree prune` anywhere in cosmo code (15.43, simplified §15i — FC/fleet retired):** remove ONLY by explicit `C:/Vibes-cc-wt/<assetId>` path (refuse any path not matching `/^C:\/Vibes-cc-wt\//`); if `remove` fails (dir vanished), delete ONLY that assetId's `.git/worktrees/<name>` admin dir — never a bare prune. There are no live `C:/Vibes-wt-*` fleet worktrees to protect anymore, but the explicit-path rule STAYS as plain hygiene in case stray worktrees exist. **agy is main-tree-bound (15.42, §3c):** the per-agent `.agy-<agentId>.pid` doesn't exist until `agy-pass` runs worktree-aware — serialize agy under N>1 until then.

### 15d. Browser UI + endpoints

**Hosted by the standalone app (revised §15i — standalone decision).** MOVE the existing Launcher cosmo markup/JS/routes into `cosmo-canyon\server.mjs` + `ui\index.html` (§1/§6), THEN extend with the Asset Browser / active-tasks / Popout. (Supersedes the old "insert into `D:\Ag\launcher\{server.js,index.html}` at lines X" targeting — those Launcher line-numbers are retired; the code now lives in `ui\index.html`.) New Asset Browser card = PRIMARY human surface in the left column; Active-tasks card after it; Popout button in the preview header (open a REAL browser window). Reuse the ported `ccApi`/`ccEsc`/`showToast`/FileReader→data-URL helpers + a `cosmocanyon`-tab-equivalent init that calls `loadCosmoAssets(); ccWireDropzone();`. **No websocket — 3s poll.** The UI also renders the **LOUD, pinned, non-collapsible "fallback supervisor running" banner** off `status.supervisor.alive` (spec in §7) + a browser-tab-title flag — the anti-invisible-process rail; wire it in the same status poll.

`express.json` limit → **24mb** (default 100KB rejects base64 drops with 413; must exceed `CC_ASSET_MAX*1.4`, audit UX-round). Multi-type magic-byte `ccSniffKind` (PNG/JPG/GIF/WAV/MP3/OGG + UTF-8-or-declared-spec for text) + **degenerate-image reject** (dims>0, not fully-transparent, decodes — reject 0-byte/all-alpha at upload, audit blast-radius) with distinct empty-vs-unrecognized messages. **PNG decompression-bomb guard (15.51, revised §15i):** parse IHDR from bytes 16..24 and reject BEFORE `PNG.sync.read` if `w*h > ~16M px` or `w>8192||h>8192`; enforce a `CC_ASSET_MAX` byte cap on the decoded payload; give `ccRunDerive` a hard child timeout (kill on overrun) + single-in-flight guard. Same header/size precheck for the §15e audio path.

**REQUIRED input guards on EVERY endpoint (15.45/15.44, revised §15i):** validate every client `id` against `^a-[0-9a-z]+-[0-9a-z]{4}$` + path-containment for ALL roots (`assets/`/`claims/`/`.trash/`) else `id=../../…` traverses out (§15a); where an endpoint stores an `acceptanceCmd`/grader path, constrain it to an allowlisted `node accept/<id>.ts` shape (reject shell metachars — bookkeep runs gate/commit `shell:false`, §3a(6)).

| Method / Path | Disk R/W (via `lock.mjs` — the standalone `cosmo-canyon\server.mjs` MUST import the SAME `orchestrator/lock.mjs` + `CC_CONTROL/locks` in the §15c-2 order, NOT a private `fcWithLock`; the cosmo routes are LOCKLESS today, so the retrofit covers ALL control writes — backlog POST/DELETE, suggestions, agent, focus, /assets/* — 15.23 CRITICAL, revised §15i) |
|---|---|
| GET `/assets/list` | R `assets.json`; returns `{assets,counts}`; **rev-check** rows vs meta, rebuild-on-drift |
| GET `/assets/file?id=` | R `assets/<id>/file.<ext>`; `placeholderOnly`/`file:null` → labeled placeholder, never 404 (audit 15.31) |
| POST `/assets/create` | sniff+degenerate-check → W `assets/<id>/file.<ext>`+`meta.json` (temp+rename, meta last) + index; state `not_ready`; collision warn on filename/contentHash |
| POST `/assets/replace` `{id,rev,file}` | **REQUIRED (Slice A makes replace first-class; had no UI)**; replace on an `implemented` slot forces `not_ready`+confirm, copies prior → `history/` (audit blast-radius) |
| POST `/assets/instructions` `{id,rev,instructions}` | rev-guarded PATCH of the field only under lock; 409 on rev mismatch |
| POST `/assets/answer` `{id,rev,instructions,clearQids[]}` | atomic: set instructions + clear only listed Qs + `ready+dirty` + rev++ |
| POST `/assets/state` `{id,rev,state}` | human `not_ready↔ready`, `implemented→not_ready`; **reject human `implemented` 400** |
| DELETE `/assets` `{id}` | state-aware + confirm for ready/implemented; **tombstone to `.trash/`** not unlink; image/audio also clears manifest slot→placeholder (no orphan real-slot) |
| GET `/active` | R `active.json`; pure read |

**Poll must not clobber input (audit UX-round, ship-blocker):** do NOT `innerHTML`-replace a row whose textarea/search has focus or is dirty — diff-patch by `rev`, skip the actively-edited asset, preserve caret+scroll. **Composable filters** (kind multi-select × state) so "Ready + Questions" (core triage) is one click; guard `lastCosmoCounts`; default sort surfaces actionable first (ready+`hasOpenQuestions` > ready > not_ready > implemented-projection); summary header + visible-active-filter clear. Queue-at-scale: priority/drag-reorder writing the operator-order `schedule` reads; batch mark-Ready with a **pre-flight cap warning** ("30 beads, est cost X, headroom Y"); `idle-blocked-on-human` deep-links the exact Ready+Questions assets.

### 15e. Implemented→game link + acceptance

**Implemented is a DERIVED projection, NEVER a written `state` flag** (resolves the Slice C stored-flag vs Slice E projection contradiction, audit 15.32 in favor of derived — the `state` enum has no `implemented` value, §15a): `implemented(asset) ⟺ bead in completions.json AND acceptance recorded PASS (not skipped) AND (image/audio) manifest[key].status=="real"`, all at the committed SHA and bound to the verified `contentHash`+`rev`; the SAME pure predicate is called by the §15c completion check and the browser. `bookkeep` writes **`implementedBy`+`implementedContentHash` PROVENANCE only** as the LAST step of a verified land, under lock, in the SAME critical section as the bead-terminal write (asset-<id> + backlog held together) so a crash mid-flip is self-healing (commit landed → projection true; else asset stays `ready`, correctly re-worked). **`recordCompletion` MUST persist the projection inputs (15.50, revised §15i): `{sha, assetKey|null, contentHash|null, rev|null, acceptanceSkipped:bool}`** — the commit sha is created AFTER recordCompletion, so compute it first or patch the record post-commit under the completions lock; legacy no-`acceptanceCmd` completions migrate with `acceptanceSkipped:true` and the predicate treats skipped-acceptance as NOT-implemented (fail-closed, per 15.13). A contentHash change deterministically invalidates acceptance (compare `implementedContentHash==current`); a replaced file re-deriving `real` is NOT sufficient — the grader re-runs against the new bytes, and **bookkeep asserts `hash(graded artifact)==meta.contentHash` at land** (stale-bytes false-green backstop, 15.39, audit false-green).

- **Image** REUSES `derive.mjs`+manifest. Deterministic `parse-instructions.mjs` (NOT the model) maps Instructions ("24x24, 6 frames, horizontal, 8fps") → manifest config; `derive.mjs` gains a frames>1 slice+pack path (`<key>#0..N-1`). **Acceptance proves RENDER-REACHABILITY, not file-presence** (audit false-green CRITICAL): static-assert `getTexture('<key>')` is referenced in `src/**` AND a headless `snapshot.mjs` pixel-diffs the sprite region vs the labeled placeholder (still-placeholder = FAIL); a flipbook grader advances the render clock and asserts two phases DIFFER (static = FAIL). QA-mode `getTexture` THROWS on a real-status key that fails to load (silent placeholder fallback can't mask a broken wire).
- **Audio** — new `game/assets/audio-manifest.json` + `derive-audio.mjs` (validate magic bytes, header duration, copy to `assets/audio/`, flip `real`); `render/audio.ts` gains a file-backed path (fallback to procedural). Acceptance: file exists+nonzero+decodable AND **static-assert the key is referenced by a `playSfx/playMusic` call site AND the sim drives the event that fires it + asserts the file-backed branch (not the synth fallback) was taken** (audit false-green CRITICAL: "valid file on disk" ≠ "game plays it").
- **Spec** — the hard class. Two-role split: opus planner authors `game/accept/<beadId>.ts` (PROTECTED_PREFIX, worker can't edit), worker implements the feature. Grader lands DISABLED until operator confirm + passes the mutation check + emits the PASS token (§15c). `sim-checkable`→hard gate; `visual/feel`→snapshot + a critic verdict that is **advisory only, routed to a human-GATED opt-IN FEEL-REVIEW queue** ("critic-passed, unconfirmed" sub-state; only operator confirm flips Implemented — a model verdict never lands a green, audit false-green); `unverifiable`→decompose or route to suggestions, **fail-closed, never auto-pass**. `audit` runs the cited spec's grader on current HEAD — a `specId` citation is never proof.
- **Re-open (`implemented→not_ready`)** = full cross-authority invalidation (`asset-reconcile.mjs`): NEVER auto-delete shipped code (§13.36 blast radius, operator-gated); mark the completion `supersededByReopen` (audit stops seeing it satisfied); bump rev so the regenerated grader binds to the new rev (grader filename ALWAYS `impl-<assetId>@rev<N>.ts`, never reused — audit false-green); image/audio flag `placeholderStale`. Invariant: a NOT-implemented asset has NO downstream authority (manifest/completions/grader) claiming it is.

### 15f. Failure-mode audit (15.x)

| # | Hole | Severity | Resolution |
|---|---|---|---|
| 15.1 | `plan-apply` "remove routes to suggestions" rests on planner self-labeled `kind`, not a deterministic effect classifier — a `kind:impl` bead can delete a working file, sailing under `MAX_DIFF_LINES`. | **CRITICAL** | Deterministic destructive classifier in `plan-apply`: any bead whose `op.motivation` references a retired/shrunk spec → hard-route to suggestions regardless of `kind`. Net-deletion guard in `bookkeep` separate from oversize: `git diff --numstat $BASE` net-deletes >40 tracked src lines OR any whole `.ts` file on a non-`kind:remove` bead → tamper-revert + require an operator-gated `kind:remove` bead. Deletion is a distinct gated class, never an impl side-effect. |
| 15.2 | Drag-drop REPLACE over an Implemented image overwrites shipped source art + re-derives with zero gate. | **high** | Replace on `implemented`/real slot forces `not_ready`+confirm BEFORE writing source; prior byte always kept in `history/`; degenerate-image reject at upload (§15d). |
| 15.3 | Per-asset `abandoned` never stops re-minting — each re-arm is a fresh `asset-<key>-r<rev>` bead with attempts=0. | **CRITICAL** | Move the ceiling to the ASSET: `asset.abandonCount`; `reconcileAssets` refuses to mint past `ASSET_ABANDON_N` UNLESS the artifact contentHash actually changed (a metadata wiggle does not reset it) → park `blocked_needs_operator` + confirm to re-enable. |
| 15.4 | Questions re-arm loop (answer→re-ask) burns a heavy Spec bead each round unbounded. | **high** | `questionRounds` counter; after 3 → hard-park `escalated` + alert + operator override token; skip a fresh grader author if instructions changed trivially. |
| 15.5 | Fat-finger Ready→Not-Ready silently drops content from the authority-hash + cascades removal pressure. | **high** | Confirm on retiring a spec with citing beads; audit drifts against `.authority-known-good` (last-green authority + debounce), not the live set. |
| 15.6 | GDD re-import auto-UPDATE overwrites the body of an operator-Ready (authoritative) spec (toggle Ready doesn't set `source:operator`). | **high** | Never auto-update a `ready`/`implemented` spec regardless of `source`; incoming body → Question; silent update only `not_ready`+`gdd-import`; per-spec shrink check. |
| 15.7 | Worktree BASE_SHA anchor + `reset --hard`/`clean -fd` unproven per-worktree; wrong-tree = nuked sibling/main work. | **CRITICAL** | Retrofit `bookkeep` — every git mutation takes an explicit tree arg + asserts `show-toplevel==claim.worktree` + detached HEAD before any destructive op; mismatch aborts non-zero. Ship serial-only until this audit passes. **(revised §15i):** spec beads partition/claim on SRC files ONLY — `game/accept/**` is PROTECTED and can never be a worker ownership token; the grader is authored in a SEPARATE planner step before dispatch (§15c-2). |
| 15.8 | Migration marks `real` manifest slots `implemented` with `files:[]` → orphan Implemented assets whose re-open re-mints a global-exclusive bead with nothing to grade from. | **med** | Set `implemented` ONLY with `implementedBy` from manifest AND infer `files=['assets/source/<key>.png','assets/manifest.json']`; re-open reuses the existing image acceptance (no planner grader needed); never let a `files:[]` asset become global-exclusive silently. |
| 15.9 | Gitignored audio + no `history/` = a fat-finger replace/delete of hand-made audio is unrecoverable. | **med** | If audio is gitignored, `history/` retention is MANDATORY + DELETE = move-to-`history`/`.trash`; or commit audio <1MB. No path destroys the sole copy. |
| 15.10 | DELETE unlinks with no state check even for a Ready/authoritative Spec or an Implemented image (orphan real-slot). | **med** | State-aware + confirm; tombstone not unlink; image/audio DELETE clears the manifest slot→placeholder. |
| 15.11 | Aggregate-only import shrink guard misses gutting 3-of-40 specs. | **med** | Per-spec shrink check (incoming <40% of current → 409) alongside the aggregate wholesale-swap wall; report per-spec deltas. |
| 15.12 | Image flips `real`+Implemented on file-presence — sprite never proven to render (derive success ≠ wired-in; `getTexture` silent placeholder fallback masks a broken wire). | **CRITICAL** | Acceptance = render-reachability: static-assert `getTexture('<key>')` referenced + snapshot pixel-diff vs placeholder baseline; QA-mode `getTexture` throws on load-fail (§15e). |
| 15.13 | Bead with no `acceptanceCmd` passes by default (skipped=pass); image/audio auto-mint WITHOUT one. | **CRITICAL** | Acceptance MANDATORY for `assetKey` beads — missing grader = FAIL; image/audio carry a deterministic auto-grader at mint under `game/accept/`. **(revised §15i):** bookkeep AUTO-DISCOVERS `game/accept/<bead.id>.ts` (independent of `acceptanceCmd`, which the planner/server never emit); FAIL-closed only for `assetKey` + planner-flagged sim-checkable beads (a `renderOnly` DATA field) so render-only beads skip without deadlocking to `abandoned`; persist projection inputs in `recordCompletion` (15.50). |
| 15.14 | Audio "Implemented" with no sound — file valid, never reached by the (procedural-only) playback path. | **CRITICAL** | Acceptance static-asserts the key is at a `playSfx/playMusic` call site + drives the firing event + asserts the file-backed branch (not synth fallback) ran. |
| 15.15 | Spec grader author = the same opus that invents the bead → weak/tautological grader; feel/unverifiable skip the teeth. | **CRITICAL** | Planner grader lands DISABLED until operator-confirmed; mutation check (must FAIL on unimplemented BASE) blocks tautologies; audit runs the cited grader on HEAD (citation ≠ proof). |
| 15.16 | Authority-hash churn: every Ready-toggle/edit fires an opus `diff` — the primary editing surface is a replan-storm generator. | **high** | `.authority-settle` debounce (≈90s) + coalesce to once-per-settled-generation before `authorityChanged` fires; browser writes touch the settle marker. |
| 15.17 | Grader that exits 0 without asserting = silent pass. | **med** | Require a positive `ACCEPT-PASS <beadId>` token on the last line AND status 0; combine with the mutation check. |
| 15.18 | Feel/visual critic verdict IS model self-attestation (forbidden §13.30/13.42), judging a downscaled PNG. | **high** | Feel specs route to a human-GATED opt-IN FEEL-REVIEW queue; critic verdict advisory ("critic-passed, unconfirmed"); only operator confirm flips Implemented; zero-observable spec = fail-closed. |
| 15.19 | Stop hook `cd main-tree && git add -A && commit` defeats worktree isolation — N workers race one index/branch. | **CRITICAL** | Neutralize the hook on parallel workers (env early-exit; bookkeep sole committer) OR set `CLAUDE_PROJECT_DIR` to the worktree; scope all committers narrowly (`add -A cosmo-canyon/game`). |
| 15.20 | Consecutive-fail breaker resets on ANY green — one trivial asset landing per cycle masks an infinite heavy/spec thrash. | **CRITICAL** | Redefine: trip on M cycles with no NET reduction in openWork; benign `parked`/`unsure`/infra-kill don't increment; per-asset dispatch budget on top. |
| 15.21 | Daily cap checked at cycle-top before N picked → overshoots by N-1; count-based cap ignores heavy cost. | **high** | Cap check INSIDE `schedule` right before dispatch, hard; tier-weighted counter; heavy needs `capRemaining>=heavyCostReserve` (precondition). |
| 15.22 | Two hosts have independent `computeSnapshot` copies — re-point one, the supervisor stays on the dead GDD = dual authority across hosts. | **CRITICAL** | Extract shared `spec-core.mjs` as STEP 0; delete supervisor's duplicates; gate-test asserts both hosts' SNAPSHOT identical on a fixture. **(revised §15i):** the fork is 4-way — `spec-core.mjs` also owns `computeTrigger(snap)→{mode,latchKey}`; supervisor imports it, `sense.mjs` EMITS the trigger into the SNAPSHOT so `decideTrigger` is a passthrough (Workflow host can't `require` a `.mjs`); parity gate-test covers SNAPSHOT AND trigger decision; reconcile stays `preflight.mjs` (git/fs, not unified). |
| 15.23 | server.js (D:\Ag) writes control files with its own `fcWithLock` — NOT the orchestrator's `lock.mjs` → browser vs agent lost-update on `assets.json`. | **CRITICAL** | server.js imports `cosmo-canyon/orchestrator/lock.mjs` with the SAME `CC_CONTROL/locks` dir + named locks in the §15c-2 order; stress-test browser POST vs a bookkeep loop for lost revs. **(revised §15i):** the cosmo routes are LOCKLESS today (bare `fcWriteArr`/`fcAtomicWrite`, NOT a wrong `fcWithLock`) → the retrofit must cover ALL existing control writes (backlog POST/DELETE, suggestions, agent, focus, gdd), plus `supervisor.mjs`'s `agent.json` write; now hosted by the standalone `server.mjs` (§6/§15d). |
| 15.24 | `spec-compile` reads the store while it mutates → torn doc / hash≠doc → replan storm or planning against half-written authority. | **med** | Compile under the `assets-index` lock for the whole scan+write; `spec-compile` RETURNS the hash it emitted; latch consumes that (no independent recompute). **(revised §15i):** not observable while both hosts are strictly serial — forward-looking guard for the toggle; keep as-written (the §13.44 markers-last invariant preserved). |
| 15.25 | Index/meta drift "rebuildable" has no rebuild trigger + `/assets/list` never rev-checks. | **high** | Startup `reconcileAssetIndex()` (both hosts); `/assets/list` rev-checks + rebuilds-on-drift; DELETE removes dir+row atomically. |
| 15.26 | `.tick.json` singleton + single-flight REFUSE silently break under N>1 (last-write-wins base; reconcile resets to one base). | **CRITICAL** | Per-agent anchor in the claim (`baseSha`/`beadId`/`worktree`); `bookkeep --tick <path>`; reconcile iterates `active.json`; single-flight REFUSE gated to N=1 only. **(revised §15i):** §13.28's INTENT (BASE persisted before edit, survivable by reconcile) is met by supervisor-before-spawn OR `tick-prep` as the agent's first action — amend "by the SUPERVISOR" to "by the active host"; make preflight/supervisor reconcile MODE-CONDITIONAL (serial keeps the `.tick.json` reset byte-for-byte; the singleton reset is NOT run under N>1). |
| 15.27 | Stale-claim self-heal on `kill(pid,0)` + 5-min TTL frees files a live long agent is still editing (pid reuse; heavy spec >5min). | **high** | Heartbeat claims (missed-beat staleness, not fixed TTL), TTL > per-agent timeout so the watchdog frees it; `startToken` guards pid reuse. |
| 15.28 | contentHash re-derived by a scanner from disk bytes → H_old/H_new mismatch re-work loop; two-file write not atomic. | **med** | contentHash computed ONCE in the create/replace endpoint's rename critical section; artifact+meta temp+rename together, meta last; scanners/derive treat `meta.contentHash` as authoritative. |
| 15.29 | contentHash "replace" flags dirty even on a byte-identical re-derive / implemented↔dirty flap. | **med** | Record `implementedContentHash` at land; dirty-on-replace fires only when `new != implementedContentHash`; derive's own source write never feeds dirty detection. |
| 15.30 | Three slices ship three asset stores + three id schemes (`meta.json` vs `assets-state.json` vs flat `index.json`; `a-…`/`cc-a…`/slug). | **CRITICAL** | ONE store: `meta.json` per-asset authority + derived `assets.json` index; DELETE `assets-state.json` + the flat inline index; ONE id scheme `a-<base36>-<rand>`; Implemented is a bookkeep-enforced projection, not a separate file. |
| 15.31 | Migration of the 3 current placeholder keys (empty `source/`) → zombie file-less Image assets → broken `<img>`. | **high** | `placeholderOnly:true`/`file:null`; `/assets/file` returns a labeled placeholder; distinct "positioning slot — drop art" tile; test migration against the REAL manifest. |
| 15.32 | Slice C writes `state=implemented` as a flag vs Slice E's derived projection — a spoofed/half-flip flag ends the run on false-green. | **CRITICAL** | Implemented is a PURE FUNCTION of committed truth (bead done ∧ acceptance ∧ manifest real, bound to contentHash+rev); nothing writes it as a bare flag; completion predicate reads the same derived source + gate-green-at-HEAD. **(revised §15i):** PURGE the stored representation — `state` enum = `not_ready|ready` only (DELETE the `implemented` value, §15a); bookkeep writes `implementedBy`+`implementedContentHash` PROVENANCE only; the §15c table Actor is DERIVED, not "AGENT writes"; `recordCompletion` persists the projection inputs (15.50). |
| 15.33 | Empty-authority regression at cutover (bootstrap specs Not-Ready → zero Ready → empty north-star → junk/idle). | **high** | Ship ONE Ready `spec-legacy` seeded from existing authority (`HUMAN_GDD.md`, else `control/gdd-snapshot.md`); migration gate asserts readySpecCount≥1 + SNAPSHOT byte-identical pre/post; empty-authority → pause, never topup. **(revised §15i):** the guard belongs at RUNTIME too, not just migration — an operator toggling all Ready→Not-Ready drains readySpecCount to 0 later; add `readySpecCount`/`authorityEmpty` to the shared SNAPSHOT and make `authorityEmpty` the FIRST `computeTrigger`/`decideTrigger` branch (`→{mode:null}`), so runtime-empty fires zero topup/opus. |
| 15.34 | Questions have no defined behavior across state transitions; enum disagreement (`needs_answer`); not_ready+questions is an invisible dead-end. | **high** | Questions persist through ready↔not_ready; the browser answers on not_ready too; reopen stamps stale prior Questions; ONE agreed state model (`needs_answer` internal, rendered as a badge over ready); completion covers not_ready+open-questions. **(revised §15i):** DELETE `needs_answer` as a STATE — it is a `hasOpenQuestions` badge over `state=ready` (the `state` enum has no `needs_answer` value); unsure-park sets the badge, LEAVES `state=ready`, keeps `dirty=false`+`bead.status=parked` (no attempts++); add `hasOpenQuestions==false` to the completion predicate; fix §15c's "dirty STAYS true until land" → "dirty clears ONLY on green-land OR unsure-park". |
| 15.35 | 3s poll clobbers a half-typed Instructions textarea (the answer surface). | **high** | Diff-render by rev; never re-render a focused/dirty row; preserve caret+scroll; ship-blocker. |
| 15.36 | Legacy backlog beads have no `assetKey`/`specId` → post-cutover the loop keeps building unauthorized GDD-era features. | **med** | One-time migration audit: tag surviving beads with the owning specId or abandon-via-suggestion those with no Ready spec; GC terminal legacy beads out of the partition set. |
| 15.37 | Structured merge gap: `derive.mjs` regenerates the WHOLE atlas — two parallel image landings both touch `atlas.json` → conflict on nearly every parallel image merge. | **med** | Atlas is a global reduction, not per-key disjoint: run `derive` ONCE in the single-committer merge (after all keys are in the manifest), or serialize image/audio landings through a dedicated asset-pipeline lane; exclude image beads from the disjoint-files assumption on the atlas. **(revised §15i):** atlas is a FILESYSTEM race in a shared checkout, NOT a git-merge conflict (`atlas.png/json`+`art/*` are gitignored) → serialize ALL `derive()` invocations (incl. the GUI-upload derive) on ONE lock and derive ONLY in the single-committer tree; only committed `manifest.json` can line-conflict. |
| 15.38 | Merge/ingest/asset-commit don't all take `git-tree`; inline serial `commit()` takes no lock → two committers on a toggle flip; conflict re-gate runs in the live main tree. | **med** | ALL main-tree commits (inline, merge, ingest, asset-meta) route through one function acquiring `git-tree` first; conflict re-gate runs in a throwaway checkout; drain to zero in-flight before switching isolation mode. **(revised §15i, raised med→high — same substance as 15.47):** the Stop hook is a FOURTH committer that must take `git-tree` too in the SERIAL workflow host (not just parallel); delete the ingest `for(i<3)` index.lock retry band-aid; pull Stop-hook neutralization forward. STEP-0. |
| 15.39 | parked-bead + re-armed-asset GC is prose-only; incomplete answers accumulate ghost beads + near-dup Questions; same-inputs re-arm livelocks. | **med** | On rev bump, `reconcile` cancels ALL prior parked beads for the key in the same pass (≤1 live bead/asset); bound parked history; dedup Questions semantically; same-inputs guard warns instead of re-looping. **(revised §15i):** ALSO make `rev` monotonic on REPLACE (today only /answer + human transitions bump it → the `(assetKey,rev)` latch never re-fires → a live bead implements STALE bytes); extend the cancel to ANY prior NON-terminal bead for the key (not just parked) + revert partials + free claim/worktree; deterministic backstop — bookkeep asserts `hash(graded artifact)==meta.contentHash` at land (fail-closed). |
| 15.40 | Commit-vs-gitignore of artifacts unresolved — gitignoring Spec files makes the AUTHORITY local-only (empty on a fresh clone / the other host). | **med** | COMMIT all Spec `file.md`+`meta.json`+`assets.json`+`config.json` (authority MUST be in git) + small images; gitignore `control/claims/`,`active.json`,`assets/*/history/`,`.agy-*.pid`; audio gitignored → `meta` records `available:false` + gate-on-presence (never false-green). Verify a fresh clone has non-empty Spec authority. **(revised §15i, raise med→high — STEP-0 blocker):** the gitignore split is UNBUILT — none of the §15 runtime paths are ignored (verified `git check-ignore` exit 1) → `clean -fd cosmo-canyon` would DELETE live claims/active/history mid-run and every restart sees a dirty tree → spurious reset (the Phase-4 `.supervisor.pid` bug class); `config.json` MUST be committed (force-add/negation). Separate §13.40 correction: add a `cc-milestone` cadence tag (rollback target), distinct from per-green `cc-known-good`. |

### 15g. Build phases (ordered, each ends verifiable)

Parallel is NOT deferred: the parallel-safe contracts (per-asset `files[]` ownership + claim, the deadlock-free lock order, the single-committer merge) are in the CORE data model + locks from phase 1. A later phase flips the runtime toggle for validation; nothing about parallel is retrofitted onto a serial-only model. **STEP-0 front-loads the ENTIRE §15i "must-fix-before-ANY-build (serial MVP)" set; the parallel phase gates on the "must-fix-before-PARALLEL" set — the serial MVP is NOT safe to build until the STEP-0 blockers land (§15i Verdict).**

0. **Shared-core + lock + safety hardening (STEP 0, prereq — the must-fix-before-ANY-build set).** Land ALL of these BEFORE any endpoint/tick writes a new path:
   - **15.41 (CRITICAL):** bookkeep's scope/tamper guard goes TREE-WIDE + allowlist-based (drop the `cosmo-canyon/`-prefix pre-filter; check repo-wide `git status --porcelain`, allow only `cosmo-canyon/game/**` + the control files bookkeep writes), whole-worktree `git clean -fd` on revert (assert `show-toplevel==C:/Vibes`, never `-x`), scope the serial Stop hook to `git add -A cosmo-canyon` (or env early-exit), pre+post-tick byte-identity assert `.claude/settings.json` vs BASE.
   - **15.47/15.38 (high):** ONE `git-tree` named lock (rank 8) across bookkeep/plan-apply/ingest (delete the `for(i<3)` retry) + the merge commit + the Stop hook — pull Stop-hook neutralization FORWARD to serial.
   - **15.48 (high):** `lock.mjs` BUSY-grace keyed on the lock DIR ctime/mtime + temp+rename `owner.json`.
   - **15.23 (high):** the standalone `server.mjs` imports the SAME `orchestrator/lock.mjs`+`CC_CONTROL/locks` for ALL control writes (backlog/suggestions/agent/focus/gdd/assets); bring `supervisor.mjs`'s agent.json write under the lock.
   - **15.46/15.40 (high):** gitignore the §15 runtime paths (`claims/`,`active.json`,`assets/*/history/`,`.trash/`,`.cc-host.lock`,`.authority-settle`,`.asset-scan-latch.json`,`.agy-*.pid`) + COMMIT `config.json`/`meta.json`/`assets.json` (force-add) BEFORE any new path is written.
   - **15.44 (med):** bookkeep gate/commit via argv-array `shell:false` (temp-file commit msg; strip metachars); allowlist `acceptanceCmd` to a `node accept/<id>.ts` shape.
   - **15.49 (med):** pathspec-scoped serial revert (`git checkout BASE -- cosmo-canyon`) or dirty-tracked-scope assert — narrow the reset blast radius on the shared repo (human WIP / other branches).
   - **15.45 (high, also ships with §15d):** asset-id regex + path-containment (+ `lock.mjs` name sanitize).
   - Plus shared-core extraction: `spec-core.mjs` owns `computeSnapshot`+`computeTrigger`+`authoritySha`+`wipKeywords` (delete supervisor's duplicates); `acquireOrdered` + the §15c-2 named-lock order.
   *Verify:* an adversarial fixture worker writing `../evil.ps1` / editing `.claude/settings.json` is tamper-reverted (both PASS and FAIL paths); both hosts' SNAPSHOT AND `computeTrigger(snap)` identical on a fixture; a browser-POST-vs-bookkeep-loop stress test shows zero lost rev; a killed-tick reconcile leaves `control/claims/`+`active.json` intact; a fresh clone has non-empty authority + zero claims.
1. **Asset store + migration.** `assets.mjs` (folder-per-asset, meta schema `state:not_ready|ready`, derived index, dirty/replace/contentHash rules, monotonic-rev-on-REPLACE, rebuild), `assets-migrate.mjs` (3 placeholder keys → `placeholderOnly` assets with inferred `files[]`). *Verify:* migrate the real manifest → non-broken Browser; crash-inject between meta-write and index-write → index self-heals within one poll.
2. **Parallel-safe claim/schedule/worktree core (contracts, toggle OFF).** `schedule.mjs`, `claim.mjs` (heartbeat+startToken), `worktree.mjs` (per-agent tick anchor in the claim; `bookkeep` git-op tree-arg retrofit + toplevel assertion); `config.json` defaulting `serial`. *Verify:* serial run is byte-for-byte today's path; a unit test drives `schedule(N=3)` on a fixture asset set and asserts disjoint partitions + lock-order compliance (no dispatch yet).
3. **Extract to standalone (the standalone-app move — §1/§6/§15d).** Move the Launcher cosmo server code + UI into `cosmo-canyon\server.mjs` (:7788, single-instance) + `ui\index.html`; `server.mjs` spawns/adopts the detached `supervisor.mjs` and owns `ccEnsureVite`(:8780)+auto-snapshot; carry EVERY §15i endpoint check WITH the code (15.23 lock, 15.44 acceptanceCmd allowlist, 15.45 id validation, 15.51 PNG cap, 15.52 GDD fetch cap). Add the launch-or-open button to the Launcher + REMOVE the old cosmo tab/routes/ingest/upload/snapshot/rollback/ccEnsureVite from `D:\Ag`. *Verify:* Launcher button → probe :7788 → spawn-if-down / open-if-up works; the loop still runs under the standalone host; a second `server.mjs` refuses to start (single-instance).
4. **Browser UI + endpoints (in the new home, `ui\index.html`).** Asset card + Active-tasks + Popout; all endpoints (create/replace/answer/state/list/file/DELETE/active) under shared locks + input guards (15.45/15.44/15.51); composable filters; focus-safe poll (15.35); drag-drop multi-type + degenerate reject. *Verify:* drop image/audio/spec, edit instructions across >3s with zero loss, toggle Ready, DELETE tombstones.
5. **Asset-scan loop integration (serial).** `reconcileAssets` in `sense` (fire-latched), `projectAssetToBead`, `ask.mjs`+`unsure` park, `active.json` writer, completion predicate + redefined breaker, per-asset abandon/question breakers, **15.13/15.50** (auto-discover `accept/<id>.ts` + `renderOnly` flag + fail-closed skipped-acceptance; `recordCompletion` persists projection inputs), **15.34** (`hasOpenQuestions` badge, no `needs_answer` state). *Verify:* a Ready+dirty image mints a bead, lands, flips derived-Implemented; a spec that asks a Question parks (badge over ready, no attempts++, no spin); answer re-arms one fresh bead.
6. **Implemented→game + acceptance.** `parse-instructions.mjs` + `derive.mjs` frames path + render-reachability grader; `derive-audio.mjs` + reached-by-playback grader; spec grader (disabled-until-confirm + mutation check + PASS token); feel-review queue; `asset-reconcile.mjs` reopen invalidation. *Verify:* a no-op image edit FAILS acceptance (still-placeholder); a tautological grader is rejected by the mutation check; reopen un-Implements + supersedes the completion.
7. **Spec-authority cutover.** `spec-compile.mjs` (locked, returns hash) + `spec-index.json`; state.mjs re-point (authority-hash swap → `authoritySha`, add `readySpecCount`/`authorityEmpty`); planner input swap + open-Questions gate; **15.33 runtime `authorityEmpty` FIRST trigger branch**; **15.32 derived-only Implemented** (no stored `state:implemented`); `.authority-known-good`; debounce. *(Note: the ORIGINAL cutover also shipped a GDD→Not-Ready splitter + a `spec-legacy` HUMAN_GDD bootstrap; BOTH are REMOVED 2026-07-02 with the GDD — the splitter, the `spec-migrate.mjs` bootstrap, and HUMAN_GDD.md are gone. The consumed marker is `.authority-consumed`.)* *Verify:* toggling one Ready fires exactly one debounced `diff`; toggling ALL Ready→Not-Ready at runtime fires zero topup/opus.
8. **Parallel validation (flip the toggle) — GATED on the must-fix-before-PARALLEL set.** Land FIRST: **15.7** (bookkeep git-op tree-arg + `show-toplevel==claim.worktree` + detached-HEAD assert), **15.42** (agy worktree-blind → serialize or worktree-arm agy), **15.43** (FORBID bare `git worktree prune`; explicit `C:/Vibes-cc-wt/<id>` removal), **15.26** (per-agent claim anchor + `bookkeep --tick`; MODE-CONDITIONAL reconcile), **15.22** (spec-core `computeTrigger`+latchKey parity across all 4 surfaces), **15.32/15.34/15.50** (derived Implemented / badge / projection fields), **15.39** (monotonic rev on REPLACE + supersede + land hash-assert), **15.37** (serialize all `derive` on one lock, derive only in the committer tree). Then `config.json` mode=parallel N=2-3; Stop-hook neutralization; N-agent reconcile; structured manifest/backlog merge + single-committer git-tree gate on every committer; HEAD-time acceptance re-run at merge. *Verify:* two disjoint image assets land concurrently in isolated worktrees, merge serialized green; a killed agent's worktree GC'd by EXPLICIT path (no bare prune) without touching the shared branch; agy beads never run concurrently; cap tier-weighted + heavy-headroom enforced.
9. **Docs + operator sync (runs INCREMENTALLY, not last).** As each phase lands, update `AGENTS.md` + `README.md` so they never describe vaporware — retarget the GDD-authority + Launcher-tab + FC-coexistence prose to the **spec-authority + standalone-app** model: authority = Ready Spec set (not the GDD); host = **desktop-app Workflow PRIMARY** (`cc-loop.workflow.js`, launched from a chat session) with the detached `supervisor.mjs` + `claude -p` as a **LOCAL lights-out FALLBACK** the dashboard flags LOUDLY when alive (§3/§7), unattended → the app's native **Routines/cloud sessions**; UI = standalone `cosmo-canyon\server.mjs` :7788 + `ui\` (Launcher = launch-or-open only); "two repos, one branch" → ONE repo; DELETE the FC/Workshop/fleet cross-system-mutex + coexistence notes (retired); the tick-driver-model split (§11.2) + the new asset/spec workflow ("how to add or steer work" now = the Asset Browser). Tie each doc edit to the phase that makes it TRUE (e.g. the standalone paragraph lands with phase 3, the spec-authority paragraph with phase 7). *Verify:* a fresh reader following `AGENTS.md`/`README.md` can start the standalone app, add a Spec asset, and mark it Ready without hitting a stale GDD/Launcher instruction. **These files stay accurate to the currently-BUILT system until the phase that supersedes them ships — do NOT rewrite them ahead of the code.**

### 15g-T. Test/regression COST-SCALING (planned, folded into the phases above)

The operator's prior autonomous-build systems slowed + got token-expensive as the game grew because **test
cost per iteration became O(codebase) instead of O(change)**. The deterministic-bookkeep design already avoids
the worst of it — a GREEN gate costs ~0 tokens (bookkeep reads only the EXIT CODE; the model never sees a
passing gate), so the residual scalers are WALL-CLOCK (typecheck + the sim suite) and VISION (feel review). Keep
both budgets O(change). Work items, each tied to the phase that makes it land (do NOT pull forward):

- **Split the monolithic `game/test/sim.ts` into per-system files** (`sim.combat.ts`/`sim.boss.ts`/…) — **phase 6**
  (game refactor via the loop or an operator pass). Three wins: (a) enables per-change test SELECTION;
  (b) a seeded win-path shift touches ONE file, not the mega-file (the recurring "adjust SEED" churn); (c) it is
  a **phase-8 parallelism PREREQUISITE** — a shared test file listed in many beads' `files[]` would serialize
  disjoint work. NOTE: the SCHEDULER side of this already landed in phase 2 — `resolveFiles` strips
  `test/`+`accept/` from ownership tokens (SRC-only, §15c-2/15.7), so a mis-emitted `files[]` no longer falsely
  serializes; the game-side file split is the remaining half.
- **Test-selection keyed on the bead's `files[]`** — **phase 5/6.** The WORKER's inner fix-loop (tick.md) runs
  only the tests mapped from the changed SRC files (fast + terse failures = cheap iteration). bookkeep's LAND
  gate STAYS FULL + authoritative (a selected land-gate risks a false green in an untouched system — never
  weaken the sole gate).
- **Incremental typecheck** (`tsc -b` + project refs + `.tsbuildinfo`) — **phase 6 game-infra.** `tsc --noEmit`
  is O(codebase) every tick; make it incremental. Silent wall-clock scaler.
- **Property/invariant tests over golden-value tests + kill the fragile seeded win-path** — **phase 6 (planner/
  grader authoring guideline).** Exact-value goldens (`xpValue===20`, cc-0005) break on every legitimate change →
  a token-costly fix cycle each time; prefer range/invariant asserts (`|player.x|<=FIELD_HALF_W ∀ ticks`,
  cc-0009). Replace the single seeded win-path smoke with seed-independent properties + a playtest-harness
  survival check.
- **Vision rationing (the real per-iteration token sink)** — **phase 6 (§15e graders).** Default asset
  acceptance to the DETERMINISTIC region grader (render-reachability + snapshot-REGION pixel check, exit-code);
  reserve the full Claude visual-critique for `renderOnly` beads + milestones, and BATCH them (one review over
  several landed render beads, not one PNG per tick). One 1024px PNG ≈ 1–1.5k vision tokens.
- **Time-box the gate + graders** (child `timeout` on bookkeep's `runGate`/`runAcceptance`) — **phase 6 bookkeep
  hardening.** A hung/infinite-loop test currently burns the whole per-agent timeout on a dead tick. (Deferred
  from phase 2 to avoid perturbing the just-proven byte-for-byte serial gate authority.)
- **Bound each sim test's tick count + share one `node_modules` across worktrees** (junction/symlink, not
  per-worktree install) — **phase 8 worktree infra.** Keep tests bounded (no 10-min-equivalent sims); install is
  the hidden per-worktree cost.
- **Terse single-line failure reporter** — **phase 6.** Keep the `PASS:`/`FAIL:` one-liner pattern; on failure
  feed the worker first-failing-assertion + expected/actual ONE LINE, never a full log dump into model context.

### 15h. Edits to existing sections — ✅ APPLIED (2026-06-30, folded into the body)

These edits are now FOLDED INTO the body (§0/§4/§7/§9/§11) — see the §14 top changelog entry. What landed (+ the standalone-app decision that modifies them):
- **§0 (Settled design):** added Authority = **Ready Spec set**, Concurrency = **runtime toggle (parallel-safe from phase 1)**, Primary input = **Asset Browser**; the GUI row is now **STANDALONE app** (`server.mjs`:7788 + `ui\`), NOT "extend the Launcher"; the human-gate row covers spec-retire/asset-delete/GDD-shrink confirms.
- **§4 (GDD ingest):** *(2026-06-30)* was rewritten to a **SPLITTER** (import → Not-Ready Spec assets); the GDD was no longer the source of truth. **SUPERSEDED 2026-07-02: §4 = REMOVED entirely — no GDD, no splitter, no doc import.** Authority = the Ready Spec set only.
- **§7 (Dashboard UI):** primary card is the **Asset Browser** (+ Active-tasks card + preview Popout); Backlog/Suggestions/Completed are read-only monitoring; focus-safe 3s poll; hosted by the standalone `ui\index.html`.
- **§9 (Build phases):** points to §15g as the next-gen build order; parallel-safe contracts core from phase 1 (supersedes §11.4 "parallel lanes later — out of scope").
- **§11 (Open decisions):** #3 GUI = STANDALONE; #4 parallel first-class toggle; #7 .gitignore resolved (COMMIT authority+config; gitignore runtime paths); #8 one-store / one-id / **derived-Implemented** settled.


### 15i. Hardening round (2026-06-30 — multi-agent, code-grounded)

4 lenses (code-truth verify / adversarial-input security / parallel-correctness / cross-slice-cutover) → 42 raw → 12 NEW verified holes + 17 corrections to existing 15.x rows, every code claim re-checked at file:line on disk (bookkeep/lock/supervisor/state/plan-apply/tick-prep/preflight/post-tick, game/registry+stage+derive, D:\Ag\launcher\server.js, .claude/settings.json, `git worktree list`, `git check-ignore`). Continuing the sequence from 15.40.

> **These findings are now FOLDED into the body (§15a–§15h) + the affected 15.x Resolution cells (tagged "(revised §15i)") + the §15g build phases + the standalone-app decision — this section remains as the code-grounded AUDIT TRAIL (append-only, rows not deleted).** Two rows are simplified by the FC/Workshop/ralph-fleet **retirement**: 15.43's fleet-worktree protection (no live `C:/Vibes-wt-*` to protect — the FORBID-bare-prune + explicit-path rule stays as plain hygiene) and 15.49's framing (the concern narrows to human WIP + other branches on the shared repo). The tree-wide guards (15.41), BASE_SHA anchoring, and one-committer rule ALL still stand (the repo-wide Stop hook still exists).

| # | Hole | Severity | Resolution | Evidence (file:line) |
|---|---|---|---|---|
| 15.41 | **Scope/tamper guard is prefix-scoped to `cosmo-canyon/` — an adversarial worker writing OUTSIDE it (esp. `.claude/settings.json`, or a NEW `C:/Vibes/evil.ps1`) evades BOTH the out-of-scope filter AND tamper hits, then gets committed by the repo-wide Stop hook.** Worst case: worker rewrites the Stop hook to add an exfil/RCE command that runs every future session = persistence + priv-esc from one injected spec. `reset --hard BASE` reverts only TRACKED; `clean -fd cosmo-canyon` cleans only under the game subtree → a new untracked out-of-tree file survives BOTH the FAIL revert and the (never-reverts) PASS path. | **CRITICAL** | Make bookkeep's guard TREE-WIDE: `outOfScope = dirty.filter(p => !p.startsWith(SCOPE_PREFIX))` (drop the `startsWith("cosmo-canyon/")` pre-filter) against repo-wide `git status --porcelain`, allowlisting only `cosmo-canyon/game/` + the specific control/ files bookkeep itself writes → any other dirty path (tracked OR untracked) trips tamper-revert. On revert, keep `reset --hard BASE` AND run a whole-worktree `git clean -fd` (drop the cosmo-canyon scoping so a root `evil.ps1` is removed) — assert `show-toplevel==C:/Vibes` first, never `-x` (keep gitignored runtime), and any sibling `C:/Vibes-wt-*` trees (the retired fleet's, if still registered in git) are OUTSIDE `C:/Vibes` so a clean inside `C:/Vibes` cannot reach them whether or not they exist (hard rule honored automatically, retirement-agnostic). Scope the serial Stop hook to `git add -A cosmo-canyon` (or env early-exit; bookkeep = sole committer). Pre+post-tick byte-identity assert `.claude/settings.json` vs BASE. bookkeep stays sole gate/commit authority, BASE_SHA anchor + single-committer intact. | bookkeep.mjs:203 `outOfScope=dirty.filter(p=>p.startsWith("cosmo-canyon/")&&!p.startsWith(SCOPE_PREFIX))`; :204 tamperHits only PROTECTED/PROTECTED_PREFIX/SOURCE_PREFIX (all `cosmo-canyon/game/*`); :73-76 revertToBase `reset --hard ${BASE}`+`clean -fd cosmo-canyon`; :218 revert on fail only; :79 commit `add -A cosmo-canyon`; .claude/settings.json Stop hook `cd "${CLAUDE_PROJECT_DIR:-C:/Vibes}" && git add -A && git commit` (repo-wide); supervisor.mjs:140 `CLAUDE_PROJECT_DIR: REPO`; `git rev-parse --show-toplevel`=C:/Vibes (one repo). |
| 15.42 | **agy worker is hard-bound to the SHARED main tree — §15c-2's per-asset worktree isolation is vacuous for the DEFAULT engine.** tick.md launches agy with `-GameDir C:/Vibes/cosmo-canyon/game` (main checkout), detects success via a MAIN-tree diff, and writes ONE global `.agy.pid`. Two parallel agy beads would (a) both edit shared `game/`, (b) each read the OTHER's diff as its own success, (c) collide on the single pid file. agy is the default (`agent.json engine:agy`; cc-0005/0009/0012 are `engine:agy`). §15c-2 line 698 kills `.agy-<agentId>.pid` — a per-agent file that DOES NOT EXIST in code. | **critical** (parallel-only; serial correct) | Add a phase-8 precondition SEPARATE from 15.7 (15.7 = bookkeep git-op tree-safety; THIS = worker-launch tree-binding). Minimal: scheduler routes ALL agy beads through one serial lane, only parallelizes claude beads (§15c-2:681 already sends image/audio→claude, spec→agy — so "agy beads never run concurrently"). Full: pass `-GameDir=<worktree>/game` + per-agent `-PidFile .agy-<agentId>.pid` (agy-pass.ps1 already PARAM-izes both), and change tick.md's diff detection from `git -C C:/Vibes diff <baseSha> -- cosmo-canyon/game` to worktree-scoped `git -C <worktree> diff <baseSha>` at BOTH the POLL and DECIDE branches. The toggle's parallel mode MUST hard-refuse or auto-serialize agy until the full fix lands. | tick.md:44 GameDir `C:/Vibes/cosmo-canyon/game` + single PidFile `control/.agy.pid`; tick.md:46/50 `git -C C:/Vibes diff <baseSha> --stat -- cosmo-canyon/game`; supervisor.mjs:26 `AGY_PID=${CONTROL}/.agy.pid` (global); agy-pass.ps1:20-22 `-GameDir`/`-PidFile` params; backlog.json cc-0005/0009/0012 `engine:"agy"`; §15c-2 PLAN.md:698 kills non-existent `.agy-<agentId>.pid`. |
| 15.43 | **N-agent reconcile's `worktree prune` is repo-WIDE and cannot honor a namespace — it can deregister the 5 LIVE ralph-fleet worktrees** (violates hard rule "NEVER touch C:/Vibes-wt-*"). Cosmo + fleet worktrees are in the SAME repo (`C:/Vibes`); `git worktree prune` removes ANY worktree whose dir is momentarily missing/unmounted, ignoring §15c-2's claimed `C:/Vibes-cc-wt/`-only restriction. | **critical** (parallel-only) | FORBID bare `git worktree prune` anywhere in cosmo code. Reconcile iterates ONLY active.json/claim paths and per dead claim calls `git worktree remove --force C:/Vibes-cc-wt/<assetId>` by EXPLICIT path, refusing any path not matching `/^C:\/Vibes-cc-wt\//`. If `remove` fails (dir already vanished), delete ONLY that assetId's `.git/worktrees/<name>` admin dir — never a bare prune. Gate-test: a reconcile with fleet trees registered + one CC worktree dir deleted leaves `git worktree list \| grep Vibes-wt-` byte-identical. **(§15i-simplified — FC/fleet RETIRED):** no live `C:/Vibes-wt-*` remain to protect; the FORBID-bare-prune + explicit-`C:/Vibes-cc-wt/<id>`-path rule STAYS as plain hygiene against stray worktrees. | `git worktree list`: `C:/Vibes-wt-{art,audio,content,enemies,systems}` all live, SAME repo C:/Vibes; §15c-2 PLAN.md:698 `worktree remove --force <its path> + prune ... namespace-restricted to C:/Vibes-cc-wt/`; refinery.ps1 "lanes run in their own C:\Vibes-wt-* worktrees". |
| 15.44 | **`bookkeep` runs untrusted planner/manual text through the shell — `runAcceptance` executes `bead.acceptanceCmd` directly (`shell:true`) and `commit()` interpolates `bead.title` into `git commit -m "…"` (only `"`-escaped).** acceptanceCmd is the WORSE vector: the string IS the command (planner/server-emitted, unvalidated). On cmd.exe `$()`/backtick are literal but `%VAR%` still expands inside quotes (info-leak/corruption); the same code is live RCE if a host ever runs git via bash/PowerShell (tick.md drives PS via the Bash tool). The deterministic authority is the ONE component that must never be steerable by untrusted input. | **med** (latent RCE) | commit via `spawnSync('git',['-C',REPO,'commit','-q','-F',tmpMsgFile],{shell:false})` (temp+rename the msg file); strip `%&\|<>\`$\n` from interpolated untrusted fields (title/detail/note/reason). runGate → argv-array `['npm','run','gate']` `shell:false`. **acceptanceCmd MUST be constrained to an allowlisted `node accept/<id>.ts`-shaped form (reject any shell metachar)** rather than run as free-form shell. No hard rule touched (still deterministic, BASE_SHA-anchored). | bookkeep.mjs:52 `git=execSync(\`git -C "${REPO}" ${cmd}\`)` (shell=cmd.exe); :83 `commit(\`...${msg.replace(/"/g,"'")}...\`)`, msg=`ralph ${bead.id}: ${bead.title}` (:236); :107 runGate `spawnSync("npm run gate",{shell:true})`; :120 runAcceptance `spawnSync(bead.acceptanceCmd,{shell:true})`; server.js:1232 stores acceptance raw; plan-apply.mjs:83 passes acceptanceCmd through. |
| 15.45 | **§15d asset endpoints key filesystem paths on a CLIENT-supplied `id` with NO validation/containment** — `id=../../game/src/sim/state` → replace/instructions/state/file/DELETE read/write/tombstone arbitrary paths under `control/assets/<id>`, `.trash/<id>`, `claims/<id>.claim.json`. NET REGRESSION vs the current upload endpoint (which gates on manifest membership + `/^[A-Za-z0-9_.-]+$/`); §15 drops the membership gate for opaque ids but never re-adds validation. | **high** | Every client `id`: (1) regex-match the mint format EXACTLY `^a-[0-9a-z]+-[0-9a-z]{4}$` (base36); (2) ALSO assert containment `path.resolve(base,id).startsWith(path.resolve(base)+sep)` for ALL three roots (assets/claims/.trash). (3) Extend the SAME guard to `lock.mjs` — `acquire()` interpolates `name` (asset-<id>) raw into `mkdirSync`, so a traversing id escapes the locks dir; sanitize/reject the name before mkdir. Make this an explicit REQUIRED-guard note in §15d, not implied by the id scheme. | PLAN.md:627/631/633 paths built from `<assetId>`; :636 format defined but not enforced; §15d L711-716 all take `{id}`/`?id=` with no stated validation; contrast server.js:1342 `if(!key\|\|!/^[A-Za-z0-9_.-]+$/.test(key))` + :1345 manifest-membership gate; lock.mjs:23/27 interpolate `name` raw. |
| 15.46 | **New §15 runtime files (`claims/`, `active.json`, `.cc-host.lock`, `.authority-settle`, `assets/*/history/`, `.trash/`, `.agy-*.pid`) are NOT gitignored (verified `git check-ignore` exit 1 for all).** Two failures: (a) `clean -fd cosmo-canyon` in reconcile/revert DELETES untracked-non-ignored files → a killed-tick reconcile wipes live claims/active/history mid-run; (b) a dirty-tree check sees them → spurious reset (the exact `.supervisor.pid` bug from Phase 4, §14). The Stop hook's `git add -A` also commits runtime claims into game commits. | **high** (STEP-0 blocker) | Land BEFORE any endpoint/tick writes the new paths: add to `cosmo-canyon/.gitignore` — `control/claims/`, `control/active.json`, `control/assets/*/history/`, `control/.trash/`, `control/.cc-host.lock`, `control/.authority-settle`, `control/.asset-scan-latch.json`, `control/.agy-*.pid`. COMMIT (never cleaned): `control/assets/<id>/meta.json`, `control/assets.json`, `control/config.json` (the serial/parallel + authority). `config.json` MUST be committed (force-add / negation if a broad rule catches it). Verify: a killed-tick reconcile leaves `control/claims/`+`active.json` intact; a fresh clone has non-empty authority + zero claims. | `git check-ignore control/active.json control/claims/x.json control/assets/a-1/meta.json control/config.json control/assets.json control/.cc-host.lock` → exit 1 (none ignored); .gitignore lists only current markers; supervisor.mjs:107/116/268 + bookkeep.mjs:75 `clean -fd cosmo-canyon`; §14 Phase-4 `.supervisor.pid` bug; settings.json Stop hook `git add -A`. |
| 15.47 | **Three git committers on the shared main tree with no shared git mutex — ingest, bookkeep, plan-apply (and rollback) all commit independently; the single-flight `.tick.json` guards TICK overlap only, NOT ingest-vs-tick.** The ingest 3× index.lock retry loop is a band-aid, not serialization. Under §15 parallel a per-worktree merge is a 5th committer. (Confirms 15.38; raise from med→high.) | **high** | Introduce a `git-tree` named lock (lock.mjs, §15c-2 rank 8); route ALL C:/Vibes committers through ONE helper that acquires it first: bookkeep.commit(), plan-apply commit (DELETE the ingest `for(i<3)` retry, replace with the lock), the future merge commit, AND the Stop hook (pull §15g-phase-8 "Stop-hook neutralization" FORWARD — it is a TODAY problem under the workflow host, not parallel-only). Rollback is already ccAlive-fenced. | bookkeep.mjs:78-85 commit(); plan-apply.mjs:143-146 add+commit (no git lock); server.js:1315-1319 ingest commit w/ `for(i<3)` index.lock retry; server.js:1368-1370 rollback reset --hard; lock.mjs callers use only 'backlog'/'completions' — no 'git-tree'. |
| 15.48 | **`lock.mjs` mkdir-then-write TOCTOU: a half-created lock (dir exists, `owner.json` not yet written) is read as stale and STOLEN mid-write → two acquirers both enter.** The catch treats an absent/unparseable `owner.json` as immediately stale (`rmSync`+re-mkdir), with no BUSY-grace for a just-created lock. Prereq for BOTH parallel work and correct control-plane serialization (bookkeep/plan-apply single-writer). (Confirms 15c-2 lock-fix + refines it.) | **high** | The load-bearing fix is the GRACE rule, keyed on the LOCK DIR's own ctime/mtime (`statSync(dir)`) NOT owner.epoch — during the window owner.epoch is UNREADABLE so there is no epoch to compare, only the dir timestamp exists: at lock.mjs:34 on read/parse failure, if `Date.now()-statSync(dir).ctimeMs < ~2000` → stale=false (fall to sleep+retry), else stale=true. temp+rename owner.json into the dir is SECONDARY defense-in-depth (one small JSON write appears whole once it exists; the real race is ENOENT, not a torn read). Land as STEP 0. Note: NOT sufficient alone for one-writer-on-backlog — must land WITH 15.23 (server.js must take the SAME 'backlog' lock; today it takes none). | lock.mjs:26-27 `mkdirSync(dir)` then separate `writeFileSync(\`${dir}/owner.json\`)`; :31-34 `JSON.parse(readFileSync(owner.json))` catch→`stale=true`; :35 rmSync+continue; no dir-mtime grace. |
| 15.49 | **Serial-mode `reset --hard BASE` is repo-GLOBAL on the shared C:/Vibes, not scoped to cosmo-canyon** — it discards ALL uncommitted TRACKED changes anywhere in the repo (ralph/FC/human WIP), not just the game. Only the paired `clean -fd cosmo-canyon` is path-scoped; `reset --hard` cannot be pathspec-scoped. §15.7 frames this ONLY for the parallel wrong-worktree case; it is a (narrow, Stop-hook-rare) exposure TODAY in serial. Fleet `C:/Vibes-wt-*` siblings are UNAFFECTED (outside the tree). | **med** | Before any `reset --hard` in bookkeep/supervisor/preflight, assert `git status --porcelain` shows dirty TRACKED paths only under `cosmo-canyon/` — else abort + write `.guard-alert` (don't destroy). OR replace tree-wide `reset --hard ${BASE}` with pathspec-scoped `git checkout ${BASE} -- cosmo-canyon` (+ existing `clean -fd cosmo-canyon`), which restores only the game subtree and cannot touch ralph/FC files. Honors all hard rules (sole authority, BASE_SHA anchor, one committer, breakers, C:/Vibes-wt-* untouched). | bookkeep.mjs:74 `git(\`reset --hard ${BASE}\`)` (REPO=C:/Vibes, no pathspec); supervisor.mjs:116/268 + preflight.mjs:43/50 `reset --hard HEAD` on dirty tree; repo shared (branch cosmo-canyon on C:/Vibes, FC/fleet on other branches of the SAME repo). |
| 15.50 | **completions.json has NO sha/contentHash/manifestKey — the "implemented = pure function of committed truth" projection (15.30/15.32) is derivable IN PRINCIPLE but the persisted records lack the fields it needs.** Entries are `{id,title,acceptance,result,ts}`; bookkeep computes `sha` but drops it from the completion record; manifest keys carry status/atlasHash but nothing binds a key to a completion. | **med** | §15e must add data-shape build requirements: (1) `recordCompletion` persists `{sha, assetKey\|null, contentHash\|null, rev\|null, acceptanceSkipped:bool}` — and because the commit sha is created AFTER recordCompletion (bookkeep.mjs:234 then :236), either compute the commit sha first then record, or patch the record post-commit under the completions lock (do NOT claim to reuse a sha bookkeep holds at record time — it doesn't). (2) Legacy no-acceptanceCmd completions (cc-0003/4/7/8) migrate with `acceptanceSkipped:true` and the derived predicate treats skipped-acceptance as NOT-implemented (fail-closed, per 15.13). | completions.json entries `{id,title,acceptance,result,ts}` (no sha/hash/key); bookkeep.mjs:138-144 recordCompletion pushes exactly those; :234 recordCompletion then :236 commit (sha created AFTER); manifest.json keys carry status/atlasHash only. |
| 15.51 | **§15d raises `express.json` to 24mb but adds NO PNG decode-dimension cap — decompression bomb via pngjs in `derive.mjs`.** A tiny crafted PNG with a huge IHDR W×H passes the 8-byte magic check, then `PNG.sync.read` allocates W*H*4 bytes (empirically 972KB PNG → 4.9GB RSS at 16000²). The current default 100KB express limit does NOT fully mask it (a 4000² zlib-of-zeros is only ~81KB base64, decodes to ~61MB); the 24mb bump is an amplifier. derive is out-of-process (`exec node derive.mjs`) so the launcher loop is spared, but the child is unbounded, and repeated drops spawn many children. | **med** | Parse IHDR from bytes 16..24 (`readUInt32BE(16/20)`) and reject BEFORE `PNG.sync.read` if `w*h > ~16M px` or `w>8192\|\|h>8192`; enforce a `CC_ASSET_MAX` byte cap on the decoded payload; give `ccRunDerive` a hard child timeout (kill on overrun) + single-in-flight guard mirroring the `ccSnapping` latch. Same header/size precheck for the proposed §15e audio path (`derive-audio.mjs`, same decode-before-validate shape). No git / bookkeep touched. | server.js:1338 `ccRunDerive → exec('node derive.mjs')` (unbounded child); :1347 PNG_MAGIC check only; :1350 no dimension cap; derive.mjs:98/102 `PNG.sync.read` with no dimension guard (pngjs 7 has no default maxDimension); PLAN.md:704 raises to 24mb + degenerate reject, no pixel-budget cap. |
| 15.52 | **GDD/spec fetch has no response-size cap** — `await r.text()` buffers the whole export before the min-length/login/shrink checks; an oversized doc (accidental, or a swapped share-link to a huge doc) balloons memory and is fed whole to the opus planner (token blowup). SSRF proper is NOT exploitable (host pinned to docs.google.com, id charset `[A-Za-z0-9_-]` can't inject a host) — bounded to oversized-doc DoS. | **low** | Cap the body BEFORE writing/splitting: STREAM-LIMIT (count bytes off `r.body`, abort at ~2MB → 413) since Content-Length is optional on a Google export (treat a present Content-Length as an early-reject fast path only). Then bound per-heading/per-spec block size in the §15b splitter. Host already pinned → no SSRF work, size ceiling only. | server.js:1295-1296 `fetch(.../export?format=md)`; `text=await r.text()` (no cap); :1299 only min-length 30; fcDocId:1001 `/\/document\/d\/([A-Za-z0-9_-]+)/` (host hardcoded, no SSRF); §15b PLAN.md:662 "keep the proven front half" — no size cap. |

#### Corrections to existing rows

**Folded into the affected 15.x rows (Resolution cells tagged "(revised §15i)") + the §15a–§15h body — this bullet list is SUPERSEDED; see the §14 changelog.** The named contradictions those corrections eliminate (now consistent doc-wide): Implemented is DERIVED-only, never a stored `state` flag (15.32); `needs_answer` is a `hasOpenQuestions` badge, not a state (15.34); dirty clears ONLY on green-land OR unsure-park (15.34); spec-bead `files[]` partition on SRC only, `accept/` PROTECTED (15.7); server.js is LOCKLESS → imports `orchestrator/lock.mjs` for ALL cosmo control writes (15.23); the snapshot/trigger fork is 4-way → shared `spec-core` owns `computeSnapshot`+`computeTrigger`+latchKey (15.22); torn spec-compile read is forward-looking (15.24); reconcile is MODE-CONDITIONAL + BASE persisted by the active host (15.26); completions.json persists the projection inputs {sha,assetKey,contentHash,rev,acceptanceSkipped} (15.50); REPLACE bumps `rev` + supersede-on-rev-change + bookkeep land hash==meta.contentHash (15.39); atlas is a FILESYSTEM race → serialize `derive` on one lock, derive only in the committer tree (15.37); runtime `authorityEmpty` first trigger branch (15.33); gitignore split + `cc-milestone` cadence tag (15.40/§13.40).

#### Must-fix before ANY build (serial MVP)

1. **15.41 (CRITICAL) — tree-wide bookkeep scope/tamper guard + serial Stop-hook scoping + settings.json byte-identity assert.** The one adversarial-input hole reachable in the SHIPPED serial loop (persistence/RCE via out-of-tree write + repo-wide Stop hook).
2. **15.47 / 15.38 (high) — git-tree lock across bookkeep/plan-apply/ingest + pull Stop-hook neutralization forward.** Ingest-vs-tick and Stop-hook-vs-tick interleave TODAY under the workflow host.
3. **15.48 (high) — lock.mjs BUSY-grace (dir-mtime keyed) + temp+rename owner.json.** Correct control-plane single-writer serialization; STEP 0.
4. **15.23 / correction (high) — server.js imports orchestrator/lock.mjs for ALL cosmo control writes; bring supervisor.mjs:280 agent.json write under the lock.** Live lost-update between GUI and agents.
5. **15.46 / 15.40 (high) — gitignore the §15 runtime paths + commit config.json/meta/assets.json BEFORE any new path is written.** Else `clean -fd` wipes live runtime and restarts spuriously reset.
6. **15.44 (med) — argv-array (shell:false) for gate/commit + allowlist acceptanceCmd shape.** Untrusted text through the deterministic authority's shell.
7. **15.49 (med) — pathspec-scoped revert (`checkout BASE -- cosmo-canyon`) or dirty-tracked-scope assert.** Narrow serial reset blast radius on the shared repo.
8. **15.13 correction / 15.50 (med) — auto-discover accept/<id>.ts + renderOnly flag + fail-closed skipped-acceptance; persist projection inputs in recordCompletion.** Closes the live false-green class.
9. **15.45 (high, ships with §15d) / 15.51 / 15.52 — asset-id validation+containment, PNG dimension cap + derive child timeout, GDD fetch size cap.** Land with the Asset Browser endpoints.

#### Must-fix before PARALLEL (toggle N>1)

1. **15.7 (CRITICAL) — bookkeep every git op takes an explicit tree arg + `show-toplevel==claim.worktree` + detached-HEAD assert before any destructive op.** Ship serial-only until this passes.
2. **15.42 (critical) — agy engine is worktree-blind (main-tree GameDir + diff + single .agy.pid); serialize agy or make agy-pass worktree-aware; DEFAULT engine is agy, so N>1 with defaults is unsafe.**
3. **15.43 (critical, simplified — FC/fleet RETIRED) — FORBID bare `git worktree prune`; remove ONLY by explicit `C:/Vibes-cc-wt/<id>` path.** No live `C:/Vibes-wt-*` remain to protect; the rule stays as plain hygiene against stray worktrees.
4. **15.26 correction (CRITICAL) — per-agent claim anchor + `bookkeep --tick <path>`; make preflight/supervisor reconcile MODE-CONDITIONAL (keep serial `.tick.json` reset, gate the singleton reset off under N>1).**
5. **15.22 correction (CRITICAL) — spec-core owns computeTrigger+latchKey across all 4 surfaces; parity gate-test on SNAPSHOT AND trigger decision.**
6. **15.32/15.34/15.50 corrections (high) — derived-only Implemented (purge stored `state:implemented`), needs_answer→badge, projection-input fields — the single-source-of-truth prerequisites.**
7. **15.39 correction (med) — monotonic rev on REPLACE + supersede-on-rev-change + bookkeep hash==meta.contentHash land assertion (stale-bytes false green).**
8. **15.37 correction (med) — serialize ALL derive invocations on one lock (GUI upload + committer); derive only in the single-committer tree.**
9. **15.7 correction / spec-bead files[] (med) — partition worker beads on SRC only; grader authored separately (accept/ is PROTECTED, cannot be a worker ownership token).**

#### Verdict

§15g's serial MVP is **NOT safe to start building exactly as written** — but the blockers are few and surgical, all landing in STEP-0/phase-1 which §15g already front-loads. The design's spine is sound (deterministic bookkeep as sole gate/commit authority, BASE_SHA anchoring, breakers, one-committer intent) and the 40-row audit holds up under code-truth verification; most §15.x resolutions are correct — the corrections tighten emphasis (server.js is LOCKLESS not wrong-locked; the snapshot fork is 4-way not 2-way; Implemented is stored-AND-derived contradictorily) rather than overturn. The **single biggest residual risk is 15.41: the deterministic tamper/scope backstop does NOT hold when the worker is adversarial**, because it only inspects `cosmo-canyon/` paths while the worker runs `--dangerously-skip-permissions` with `CLAUDE_PROJECT_DIR=C:/Vibes` and the Stop hook auto-commits the whole repo — one injected spec can rewrite `.claude/settings.json`'s Stop hook for persistence/RCE, and neither the FAIL revert nor the PASS path removes an out-of-tree untracked file. The hard-rule promise ("the deterministic guard holds even when the worker is fully adversarial") is currently FALSE; make the guard tree-wide (allowlist, not `cosmo-canyon/`-prefix) and scope the Stop hook before ANY hands-off serial run. Do that plus the git-tree lock, lock.mjs grace, and server.js lock retrofit, and the serial MVP is safe to build phase-by-phase; parallel stays correctly gated behind the 15.7/15.42/15.43/15.26 retrofit set.
