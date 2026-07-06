# Cosmo Canyon — module map

Autonomous **spec-authority-driven** game-dev loop (§15b, phase 7 — north-star = the set of Ready Spec assets,
compiled to `control/spec-doc.md`; a Google-Doc is an optional import splitter), Claude Code as orchestrator.
**New here? Read [`AGENTS.md`](AGENTS.md) first** (how it works, how to run it, gotchas). Build log: [`PLAN.md`](PLAN.md).

## Where things live (the ONE file to edit for X)

```
cosmo-canyon/
  AGENTS.md            ← READ FIRST: model, how-to-run, commit model, gotchas, hard rules
  README.md            this map
  PLAN.md              full design (§2 bead schema, §3 loop, §13 failure-mode fixes) + §14 per-phase build log
  SPIKE-RESULTS.md     why the host is a supervisor (not a long-lived Workflow) + why feel=snapshot (not preview MCP)

  server.mjs           ← STANDALONE app host (§15g phase 3; Node http, :7788, single-instance). Serves ui/ +
                         ALL /api/cosmocanyon/* routes; spawns/kills the detached supervisor; owns
                         ccEnsureVite(:8780) + auto-snapshot + rollback. Every control write via the shared
                         orchestrator/lock.mjs; carries the §15i guards (15.23/15.44/15.45/15.51/15.52).
                         §15g phase 4: hosts the §15d FOLDER-PER-ASSET endpoints (assets/list·file·get·create·
                         replace·instructions·answer·state·DELETE + /active), wired to orchestrator/assets.mjs
                         (which locks internally); create sniffs magic bytes + rejects degenerate/oversized
                         images (15.51), DELETE tombstones to .trash/, every id-route guards 15.45.
  ui/index.html          the standalone dashboard (single file; 3s poll; LOUD fallback-supervisor banner, §7).
                         §15g phase 4: the ASSET BROWSER (primary input surface) + Active-tasks card — drop
                         image/audio/spec → create; per-row instructions autosave (rev-guarded), Ready toggle,
                         answer Questions, replace/tombstone; composable filters; FOCUS-SAFE poll (15.35).

  orchestrator/        ← the brain prompts + the deterministic scripts (the loop machinery)
    supervisor.mjs       THE LOOP HOST (detached fallback). Serial: single-flight repeater. Parallel (phase 8,
                         !isSerial): runParallelCycle = plan+claim+worktree → spawn N workers concurrently
                         (gate-only in worktrees, Stop hook neutralized) → single-committer merge → crash-sweep.
                         Preflight guards, breaker/cap/known-good tag. CC_REPO/CC_CC/CC_BRANCH seams + --parallel-once.
    tick.parallel.md     PARALLEL worker recipe (phase 8): work IN your worktree, `bookkeep --gate-only`, commit NOTHING.
    tick.md              WORK tick prompt: sense → implement ONE bead (route agy/claude) → call bookkeep.
    planner.md           PLANNER tick prompt (opus): topup/diff/blocked/audit → writes control/.plan-result.json.
    bookkeep.mjs         THE GIT/GATE AUTHORITY (deterministic). Runs the gate + per-bead acceptance + guards
                         (tamper, scope, oversize, no-op, source-lockout), then commits or reverts to BASE_SHA.
                         Phase 8: --gate-only (worker gates IN its worktree, writes a marker, commits NOTHING;
                         GAME retargets to the worktree when IN_WT) + CC_GITTREE_HELD (merge holds git-tree → skip re-acquire).
    plan-apply.mjs       Deterministic planner-apply: validate + dedup + WIP-reject + markers-last + commit.
    agy-pass.ps1         The proven own-console agy dispatch recipe (no stdout redirect; writes .agy.pid).
    snapshot.mjs         Puppeteer headless screenshot of the :8780 dev server → PNG (feel-verify input).
    lock.mjs             Stale-breakable pid+epoch control-plane lock (no deadlock on a killed holder);
                         acquireOrdered/releaseAll/orderedLockNames = §15c-2 global lock-rank order (wired by
                         schedule/claim, phase 2).
    spec-core.mjs        Shared computeSnapshot+computeTrigger+latchKeyFor+specAuthoritySha (single source, 15.22);
                         phase 7: specAuthoritySha=Ready-spec hash, authorityEmpty-first trigger, authorityChanged debounce.
    spec-compile.mjs     §15b/phase-7: authorityHashOf(Ready specs) + compileSpecs→spec-doc.md/spec-index.json (locked,
                         returns the hash) + writeKnownGood (.authority-known-good). THE authority-hash single source.
    config.mjs           §15c-2 concurrency toggle reader: control/config.json {concurrency:{mode,maxConcurrency,
                         autoConcurrency,worktreeRoot,heavyCostReserve,…}} → normalized. MAX_CONCURRENCY=1000 (§H7).
                         autoConcurrency=fan-out-to-ceiling. Pure reader. Phase 8: FLIPPED to
                         mode=parallel N=2 (serial N=1 is the fallback; a malformed field can never widen concurrency).
    schedule.mjs         §15c-2 deterministic top-of-cycle PLANNER (phase 2, no dispatch): resolveFiles ownership,
                         slots-before-dispatch, tier-weighted cap, overlapping-files serialized, agy→one serial lane.
    claim.mjs            §15c-2 atomic per-asset CLAIM store (phase 2): exactly-one under active+claims lock w/
                         disjointness re-check (TOCTOU), heartbeat staleness + startToken pid-reuse guard; the
                         per-agent tick anchor (baseSha/beadId/worktree) lives in the claim, not .tick.json (15.26).
    worktree.mjs         §15c-2 per-agent worktree lifecycle: create C:/Vibes-cc-wt/<id> --detach; remove by
                         EXPLICIT path only (15.43, never a bare `worktree prune`); show-toplevel guard. Phase 8:
                         linkNodeModules junction (so a worker can gate in isolation) — remove() DROPS the junction
                         (non-following) BEFORE git remove (else git-remove follows it + nukes shared node_modules).
    reconcile.mjs        §15i 15.26 parallel N-agent reconcile — now LIVE under mode=parallel: GC dead/stale claims
                         + their worktrees by explicit path (show-toplevel-guarded); serial keeps the singleton reset.
    dispatch.mjs         §15g phase 8 top-of-cycle: planCycle = schedule (disjoint set) → claim (per-agent anchor)
                         → worktree.create + junction → writeActive. Agy → serial lane (never a worktree). Serial=no-op.
    merge.mjs            §15g phase 8 SINGLE COMMITTER: per green gate marker (git-tree held), apply the worktree SRC
                         diff onto HEAD → run bookkeep --result work at post-merge HEAD (re-gate+acceptance+derive+commit,
                         15.38/15.37) → cc-known-good → GC worktree; orphan-sweep crashed workers; auto-drop maxConcurrency.
    assets.mjs           §15 ASSET STORE (phase 1): folder-per-asset meta authority + DERIVED assets.json index
                         (rebuild/self-heal); create/replace/instructions/state/question/answer mutators + the
                         loop primitives (markImplemented/parkUnsure/bumpAbandon/setOperatorBlock — the ONLY
                         dirty-clears, §15a) + phase-6 (setManifestKey bind-once, feelConfirm, reopenForRework);
                         markers-last atomic writes, contentHash-once, 15.45 id-guard.
    assets-core.mjs      §15c/§15e SHARED SENSE PRE-STEP (run inside computeSnapshot, both hosts): reconcileAssets
                         (fire-latch, supersede, abandon breaker, phase-6 manifestKey backstop) · projectAssetToBead
                         (phase-6: image/audio mint REAL graders + acceptanceKind, spec sim|feel) · reconcileActive
                         · implemented() DERIVED predicate (image/audio manifest split · feel-confirm · reopen) +
                         feelPending · computeAssetSnapshot (openWork/completion) · breakerStep (15.20).
    parse-instructions.mjs  §15e DETERMINISTIC (not the model): Instructions "24x24, 6 frames, 8fps" → manifest
                         config + deriveManifestKey (closes the upload-keying gap so an upload can flip Implemented).
    asset-reconcile.mjs  §15e RE-OPEN (implemented→not_ready) cross-authority invalidation: reopenForRework +
                         supersede completion/beads + operator-gated code-removal suggestion (NEVER auto-delete).
    ask.mjs              phase-5 worker helper: records unsure Questions to control/.unsure.json (bookkeep
                         --result unsure then parks the asset + bead deterministically).
    assets-migrate.mjs   One-shot: placeholder manifest keys → placeholderOnly assets (idempotent, §15.31).

  control/             ← the on-disk control plane (durable; survives crash/restart; GUI + scripts both r/w)
    backlog.json         task queue ("beads"); suggestions.json (design gate); rejected.json; accepted.md
    completions.json     landed-task log (cited acceptance); agent.json ({engine,model} worker pick)
    assets.json          §15 DERIVED asset index (rebuildable from meta; list/sort/search — never the authority)
    config.json          §15c-2 concurrency toggle (committed; default serial); claims/<id>.claim.json (gitignored)
    assets/<id>/meta.json  §15 per-asset AUTHORITY (committed); (gitignored) history/ .trash/ claims/ active.json
    spec-doc.md spec-index.json  §15b DERIVED authority views (compiled from Ready specs; gitignored, regenerable)
    focus.md             operator "prioritize X" steer (gitignored)
    (gitignored runtime) status.json .tick.json .supervisor.pid .agy.pid .agy-strikes .paused .authority-settle
                         .authority-known-good .authority-consumed .lastaudit .plan-* .stalled .guard-alert .usage-*.json locks/
  logs/  snapshots/      host/tick/planner logs (gitignored) + latest.png auto-snapshot (gitignored)

  game/                ← the actual game (borrowed from realm-survivors/v2; Vite+TS+PixiJS v8, port 8780)
    src/sim/             DETERMINISTIC render-free sim (rng/grid/pool + systems) — the headless target
    src/render/          PixiJS renderer (never imported by sim/) — the feel/visual surface
    src/harness.ts       window.__cc = { beginRun, step, aiPick, getState } (the playtest/snapshot handle)
    test/                canary/determinism/sim-purity/budget + sim.ts aggregator over sim.<system>.ts SPLIT
                         suites (§15g-T, per-change test selection via test/_select.mjs) — THE GATE (tamper guard)
    accept/<id>.ts       independent per-bead acceptance graders (operator-written; worker can't touch); phase-6
                         SHARED parameterized graders _image-grader.ts (render-reachability) / _audio-grader.ts
                         (reached-by-playback) + _snapshot-region.mjs (opt-in puppeteer layer)
    assets/              manifest.json (positioning authority) · audio-manifest.json (phase-6) · source/ (sacred
                         originals) · art/+atlas/+audio/ (derived, gitignored)
    derive.mjs           source→art box-downsample + frames>1 slice/pack + shelf-pack atlas; --bind (upload→real);
                         serialized on the manifest lock (15.37, idempotent). derive-audio.mjs = the audio analogue.
    docs/                DONE.md · KNOWLEDGE.md · FEEL-REVIEW.md · adr/
```

## The app (STANDALONE — §15g phase 3)
Cosmo Canyon is a **standalone app**: `cosmo-canyon\server.mjs` (Node `http`, **:7788**, single-instance)
serves `cosmo-canyon\ui\index.html` + ALL `/api/cosmocanyon/*` routes + `cc-start`/`cc-stop` (spawn/kill the
detached `supervisor.mjs`) + `ccEnsureVite`(:8780) + asset upload + auto-snapshot + `/rollback`,
each carrying its §15i guard (15.23 shared lock, 15.44 acceptanceCmd allowlist, 15.45 id validation, 15.51
PNG cap). Run `node cosmo-canyon\server.mjs` (or the Launcher button below) → open
`http://localhost:7788/`. The dashboard flags a running fallback supervisor with a LOUD pinned banner (§7).
A 2nd instance refuses (port-in-use / `control/.cc-host.lock`).

The **Asset Browser** (§15g phase 4) is the PRIMARY input surface: drop an image / audio / spec file to mint
a folder-per-asset (`control/assets/<id>/meta.json` authority + derived `assets.json` index, via
`orchestrator/assets.mjs`), edit its Instructions (autosave, rev-guarded), toggle it **Ready** when it's
implementable, answer a worker's Questions, or replace/tombstone it — all over the `/assets/*` endpoints. A
Ready+dirty asset is auto-projected into a bead by the **phase-5 scan loop** (`assets-core.reconcileAssets`);
`/assets/list` returns the DERIVED `implemented` flag. An **Active-tasks** card reads `control/active.json`
(written at dispatch by the phase-5 writer, GC'd by `reconcileActive`). The 3s poll
is focus-safe (never clobbers a half-typed field, 15.35).

The **Launcher** (`D:\Ag\launcher`, separate repo) keeps ONLY a **launch-or-open** control: its Cosmo Canyon
nav button probes `:7788` → opens it if up, else POSTs `/api/cosmocanyon/launch` (the Launcher spawns the
detached `server.mjs`) then opens it. No cosmo routes/markup live in `D:\Ag` anymore.

## One-line mental model
Ready Spec assets → compiled north-star (`spec-doc.md`) → planner invents tasks → worker (agy logic / Claude feel)
builds one → deterministic gate commits or reverts → repeat, with a wall of guards so it never lands broken or
faked work. (No GDD, no document import — the Ready Spec set is the only authority; author specs in the Asset Browser.)
