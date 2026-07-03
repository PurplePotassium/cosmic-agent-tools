# Cosmo Canyon — orchestrator KNOWLEDGE (system-level, newest on top)

> ⛔ GDD REMOVED (2026-07-02): Cosmo Canyon has NO GDD, no import-splitter, no HUMAN_GDD seed. Authority = the Ready Spec set ONLY. Older entries below may mention a GDD — it no longer exists and must NEVER be reintroduced.

Project-local notes for the deterministic orchestrator + control plane. Game-local notes live in
`game/docs/KNOWLEDGE.md`. Cross-harness lessons → `D:\Ag\knowledge\`.

---

## 2026-07-02 (later 7) — MAX_CONCURRENCY 2→5 ceiling test + shared-screen-file merge thrash

Task: raise the parallel ceiling 2→5 and drive a real char-select feature (3 hero placeholders +
campfire flipbook + bg + screen spec) through the loop. Ceiling works; the feature's image beads thrash
at merge on the ONE shared screen file. Full write-up:
`D:\Ag\knowledge\cosmo_canyon_system\artifacts\parallel-scheduler-ownership-2026-07-02.md`.

- **`dispatch 5 worker(s)` CONFIRMED.** Raised `MAX_CONCURRENCY` 2→5 in `orchestrator/config.mjs` (the const
  is the ONLY high-side cap — `config.mjs:58` `Math.min(MAX_CONCURRENCY, clampInt(c.maxConcurrency,1,2))`,
  clampInt doesn't cap high) + `control/config.json` `maxConcurrency:5` AND `mode:"parallel"` (mode MUST be
  parallel or `readConfig` forces N=1), both COMMITTED before start. The 5 image beads are file-disjoint by
  the scheduler's ownership model (`assets/source/<key>.*` each) → all 5 co-dispatched; spec `deferred 1`
  (GLOBAL-EXCLUSIVE, runs alone).
- **Then merge thrash on the shared screen file.** Every image worker ALSO wrote
  `game/src/render/charselect.ts` (getTexture wiring for the render-reachability grader) — NOT in its
  declared/inferred ownership → scheduler thought them disjoint → co-dispatch → single-committer `git apply`
  collided (`patch does not apply`). landed 3/conflicts 2, then landed 1/reverted 1 on retry → cumulative
  reverts=3 → **auto-drop N→4** (`MERGE_DROP_THRESHOLD`). Root cause = under-declared `files[]` (true
  write-set spans shared wiring); fixes = planner declares real files[] / learned-ownership-from-merge-diff /
  decompose to one EXCLUSIVE screen bead + disjoint art beads.
- Asset-authoring recipe (create+Ready via the :7788 API; **COMMIT `control/assets` before start** or the
  preflight `clean -fd` wipes untracked new assets) is in the same artifact.

## 2026-07-02 (later 6) — PARALLEL detached loop (mode C, N=2) hardening: 3 rail bugs fixed; clean 11-cycle run

Task: author assets for a settings-menu volume slider (SEPARATE music vs sfx) + a settings Spec asset, then run
the **parallel detached supervisor** (Start button / `cc-start`, `config.mode=parallel maxConcurrency=2`) and
babysit stop→fix→restart until done. Shipped the feature; found+fixed 3 bugs in the deterministic rails; a clean
run = 11 parallel cycles, every asset landed, **0 reverts / 0 conflicts / N held at 2**, then a correct
`idle-blocked-on-human → stop`. Full write-up: `D:\Ag\knowledge\cosmo_canyon_system\artifacts\parallel-loop-hardening-2026-07-02.md`.

- **bookkeep no-op REVERTED grader-passing 0-diff asset work** → an already-real+wired asset that goes dirty
  (an instructions edit ALWAYS sets `dirty:true`) re-projects a bead that can only 0-diff + idempotent-bind →
  no-op revert → abandon; it churns + mislabels done work as failed. FIX (commit 4c8adbc): `bookkeep.mjs` ~L743
  `noop = 0diff && (isAssetImgAud ? (!bindChanged && !accept.pass) : true)` — a 0-diff asset whose grader PASSES
  LANDS as a re-confirm (clears dirty → converges), honest because grader-gated. (Specs have no auto path → use
  `close-satisfied.mjs`.)
- **Auto-drop of maxConcurrency counted ALL seam reverts, not just conflicts** → a few grader-unverifiable beads
  (acceptance-fail reverts) collapsed N 2→1 permanently. FIX (commit 84e1ae4): `merge.mjs mergeGreen` only bumps
  `regateFails` on a seam **GATE-fail** (`/gate fail/i.test(raw)` = the real concurrency signal: green in worktree,
  breaks at merged HEAD) or an apply conflict; acceptance/stale-rev/no-op reverts don't shrink N.
- **`not_ready` does NOT retract an already-queued bead** — the parallel cycle scheduled 3 not_ready main-menu
  button beads (stale-rev revert → fed the auto-drop). FIXED (commit 85a4d36): `dispatch.mjs planCycle` now gates
  every candidate on the DERIVED asset index — a bead with an assetId is dropped (deferred reason
  `asset-not-ready` | `asset-stale-rev` | `asset-missing`) unless its asset exists, is `state:ready`, and its rev
  matches; non-asset beads are never gated; bookkeep's fail-closed stale/terminal checks stay the land-time
  backstop. (The initial run used a manual `superseded` workaround, now superseded by this proper fix.) Verified
  with a fixture-driven planCycle test (cap:0 so nothing dispatches). Serial mode was never affected (its per-tick
  sense runs reconcileAssets).
- Out of scope, LEFT: `mainmenu.ts` fetches button art via `getTexture(key)` (variable), so `_image-grader`'s
  literal-key render-reachability can't verify `button.play/settings/shop` → curated them `not_ready`.
- Reminders that held: operator must upload REAL art (degenerate 153-byte `slider.knob.png` → grader
  `still placeholder`; replaced with a real 20×28 knob, verified via a `CC_DERIVE_ASSETS` throwaway seam before
  the run); a spec's authority body = its `instructions` (de-scramble before running); COMMIT control/ curation
  before Start (reset --hard wipes it); feel spec completes via `POST /assets/feel-confirm` after a real
  screenshot (puppeteer swiftshader, click the menu button coord).

## 2026-07-02 (later 5) — DRIVE run (mode A) found a grader FALSE-GREEN: create-once sprites stuck on placeholder

Drove Cosmo Canyon from a Claude Code chat (mode A / subagents) to "implement all assets". Sense reported all 4
Ready assets already Implemented + `to-spec` — but a visual snapshot (the whole point of mode A) exposed a bug the
deterministic rails cannot see. Two layers, both fixed; one latent trap logged.

**(1) 32×32 squish (asset-metadata).** The 3 menu-button image assets (`button.play/settings/shop`) had Instructions
`"Found in the main menu"` — no size → `parse-instructions.mjs` DEFAULTS `size:[32,32]` → `derive.mjs` box-downsampled
the real 160×48 button art to a 32×32 blur. Fix: reopened the 3 assets (`asset-reconcile.reopenAsset` → not_ready+dirty,
rev++, supersede completion) with Instructions `size: 160x48`, sense re-minted 3 beads, bookkeep re-derived crisp (bead 1
via a `web-game-dev` subagent that also rewrote the render; beads 2/3 = driver-run `tick-prep`+`bookkeep`, pure derive,
no code). Commits `4e7fe56`/`cfa9512`/`21b7d47`.

**(2) THE false-green — create-once sprite never gets the real texture (engine).** `registry.getTexture(key)` returns a
placeholder Texture SYNCHRONOUSLY, then `loadReal().then(real => texCache.set(key, real))` swaps the **cache** — but that
never reassigns `.texture` on a Sprite already created with the placeholder. `stage.ts` dodges this by re-pulling
`getTexture(key)` EVERY FRAME (`sp.texture = getTexture(key)`); `mainmenu.ts` built its button sprites ONCE in `init()` →
stuck on the placeholder forever. The render-reachability grader (`_image-grader`) **PASSED** it anyway: getTexture is
referenced ✓ + manifest `real` ✓ + atlas frame exists ✓, and QA-mode `__ccTexErrors` stayed EMPTY because `loadReal`
*succeeds* (it's the swap-not-observed that fails, not the load). So a "real, Implemented" asset rendered as a grey
placeholder box with the key-name text — invisible to every gate. Fix `8960f75`: menu stores `{sprite,key}` refs and
re-pulls `getTexture(key)` per frame in `sync()` (re-asserting width/height since setting `.texture` recomputes scale
from the new frame). Verified: PLAY/SETTINGS/SHOP now show the blue/purple/orange art + icons.

**LATENT / REUSABLE LESSON.** Render-reachability ≠ render-correctness. The grader proves a texture is *reachable*, not
that it *displays*. Any FUTURE create-once sprite hits the same trap and green-passes. Two durable follow-ups worth doing:
(a) registry root-cause — on swap, mutate the cached Texture's `source`/`frame` IN PLACE (+`update()`) so ALL holders
update, not just future getTexture callers; (b) a grader that samples rendered pixels and fails if a `real` key still
equals its placeholder. Minor footgun spotted: `parse-instructions` size regex `(\d{1,4})[x×*,](\d{1,4})` matches ANY
`N×N` in prose (the `,` separator is especially eager — `"1, 2"` → `[1,2]`); always give sprite size explicitly.

**Rails that worked (positive):** preflight branch/mutex/clean assert; reopen→dirty→bead-mint→derive→DERIVED-Implemented
projection; `backlog.json`/`assets.json` correctly in bookkeep `ALLOW_CONTROL` (uncommitted bead-mint survived the guard);
beads with `files:[]` are scoped by the tree-wide allowlist (`game/` + control allowlist), so derive outputs under
`game/assets/` are NOT reverted. Mode A caught what B/C (autonomous, preview-blind) would have shipped green.

---

## 2026-07-02 (later 4) — FIXED the 3 first-run gotchas + added the "Drive = spawn subagents" DEFAULT mode

Follow-up to "later 3". Fixed all three gotchas (commit `6379477`) + made "Drive Cosmo Canyon" mean the
Claude-Code-agent-driven-with-subagents mode (docs commit). Gate GREEN, all fixes are additive (existing serial
paths byte-identical when the new flag/field/endpoint is absent — parity preserved).

### The fixes
- **GC2 — spec engine honors acceptanceKind.** `assets-core.projectAssetToBead`: a spec's `engine` is now
  `acceptanceKind === "feel" ? "claude" : "agy"` (was `"agy"` unconditionally → a visual spec went to the
  preview-blind engine). Widened the feel-keyword regex (+`ui|hud|menu|screen|button|title|layout|sprite|anim|
  art|colou?r|theme|font|icon`) AND now matches the FILENAME too (`ui_mainmenu.txt` reads visual even with terse
  instructions). Verified: visual spec→feel/claude, logic spec→sim/agy (unchanged).
- **GC3 — killed the planner↔worker block/unblock churn.** New `bookkeep --result blocked --needs-operator`:
  marks a bead a TERMINAL human gate (`bead.needsOperator=true`, NO attempts++, hard-parks the asset via
  `setOperatorBlock`). `spec-core.computeSnapshot` now drops `needsOperator` beads from `blockedIds`, so the
  planner never sees them → never reopens them; `plan-apply` also refuses any `update/rescope/setStatus` on a
  `needsOperator` bead (belt-and-suspenders). `tick.md`/`planner.md` updated to use + respect it. **Root cause
  recap:** the planner `blocked` mode kept reopening a bead the worker kept blocking, and a `planned` outcome is
  breaker-benign, so the openWork-reduction breaker took ~15 cycles to trip = a slow opus cost leak.
- **GC1 — an already-satisfied spec is now closeable.** New `orchestrator/close-satisfied.mjs` (`closeSatisfied()`
  + a CLI `--id <assetId>`) + server `POST /assets/confirm-satisfied` + a dashboard "✓ satisfied" button (shown
  on a Ready spec that's `blockedNeedsOperator` & not impl) + `assets.markSatisfied()`. The operator (or the
  present driving agent after VERIFYING) attests a spec is satisfied by existing code; it writes the full
  `implemented()` provenance (a `feelAdvisory` completion pinned to HEAD + `implementedBy` + `feelConfirmed`) and
  commits → flips derived-Implemented HONESTLY. **Why this shape:** a sim grader is impossible (tautological —
  passes on BASE where the feature already exists → fails the must-FAIL-on-BASE mutation-check), and there was no
  feel-advisory land for feel-confirm to bind to. `markSatisfied` mirrors `feelConfirm` but ALSO writes
  `implementedBy` (no prior land). It is a HUMAN/present-agent attestation — never a model self-attest green.

### The new DEFAULT drive mode (the headline change; for redistribution)
**"Drive Cosmo Canyon" now means: the present Claude Code agent orchestrates by spawning `Agent`-tool SUBAGENTS**
— NOT the in-app Workflow, NOT the detached `claude -p` supervisor. Documented in `AGENTS.md` ("Three ways to
drive it", CC-subagent = mode A, recommended first; Workflow = B; supervisor = C, last) + a top-of-doc ⭐ callout
+ a full playbook `docs/DRIVE.md`. The deterministic rails (`preflight`/`sense`/`tick-prep`/`bookkeep`) stay the
authority (subagents can't self-attest — the driving agent runs `bookkeep`); what moves to the present agent is
orchestration + feel/visual acceptance judgment (verify via `snapshot.mjs`+Read, close via `close-satisfied`).
This mode SIDESTEPS all three gotchas above by design (a present agent adapts instead of grinding). Design forks
the operator chose: acceptance = "agent verifies + closes" (surfaces for veto); docs recommend CC-subagent first,
supervisor last.

### Live verification finding (button sprites overlap the menu text)
Snapshotted the menu (`snapshot.mjs`): it boots correctly (title "REALM SURVIVORS" + subtitle + PLAY/SETTINGS/SHOP)
so the `ui_mainmenu` spec IS satisfied — BUT the 3 uploaded button sprites render as small gray placeholder-ish
squares CENTERED OVER the text labels (`mainmenu.ts` draws the `getTexture` sprite on top of the `_makeBtn` text
button). The render-reachability graders passed (textures ARE drawn) — they verify reachability, NOT aesthetics
(the documented static-grader limit). So: the menu-boot spec is closeable; the button-art COMPOSITION is a
separate quality issue (small placeholder art + sprite-over-text) to fix in `mainmenu.ts` / re-upload real button
art. This is exactly the kind of thing the "agent verifies + closes" mode catches that a deterministic grader can't.

---

## 2026-07-02 (later 3) — FIRST REAL LOOP RUN (4 dirty assets) — pipeline works; SPEC-BEAD dead-end + planner-churn cost leak

First time the loop drove REAL operator-uploaded assets (all prior verifies used throwaway fixtures). Ran the
in-app Workflow host (`cc-loop.workflow.js`, serial mode) over 4 Ready+dirty assets: 3 image buttons
(`button.play/settings/shop`) + 1 spec (`ui_mainmenu` = "Game starts with the main menu"). **Result: the 3
images reached REAL derived-Implemented end-to-end; the spec bead dead-ended + triggered a slow-burn opus churn
I had to stop manually.** No false green, gate stayed GREEN throughout, tree stayed clean. Nothing landed to fix
these yet — they are OPEN system gotchas to fix before the next real run.

### ✅ What worked (the image pipeline, on real uploads, first time)
`derive-bind` (upload bytes → game source + manifest slot, flip `real`) → auto-minted `_image-grader`
(render-reachability) → real Implemented (dirty cleared, `implementedBy` provenance bound to beadId+sha+
contentHash+rev). The worker WIRED `getTexture('button.play|settings|shop')` into `game/src/render/mainmenu.ts`.
**Self-heal confirmed:** button.play's FIRST attempt was REVERTED (`FAIL _image-grader: render-unreachable`) —
worker derive-bound the bytes but didn't wire an EXECUTING `getTexture` call; planner `blocked`-mode rescoped
("its fail was render[-unreachable]"), retry wired it → PASS. The grader correctly refuses an unwired sprite
(necessary-not-sufficient static check doing its job).

### ⛔ GOTCHA 1 — a spec for an ALREADY-EXISTING feature is an un-closeable dead-end (the core one)
`ui_mainmenu` = "Game starts with the main menu". But the greenfield skeleton ALREADY had
`game/src/render/mainmenu.ts` (title + Play/Shop/Settings stack). So the worker correctly finds nothing to
implement → `bookkeep --result blocked` ("spec already implemented"). It can NEVER reach Implemented:
- classified `acceptanceKind:sim` + `graderNeedsConfirm:true`, but **no grader was authored** (asset-scan minted
  the bead, not the planner);
- **even a correct sim grader is impossible**: "boots to main menu" PASSES at BASE (the feature pre-exists) →
  the mutation-check (must-FAIL-on-BASE, §15.17 anti-tautology) REJECTS it. You cannot retro-fit a sim grader
  for a feature that already exists.
- and because it's `sim` (not `feel`), it never enters the FEEL-REVIEW queue → `feel-confirm` can't close it
  either. **Permanent block; only an operator can resolve it** (retire the spec, reclassify+rework, or a system
  fix). This is a genuine design gap: **the acceptance machinery has no path to stamp "this spec is satisfied by
  pre-existing code."**

### ⛔ GOTCHA 2 — spec mis-classification: a visual spec → `sim` + preview-blind `agy`
`assets-core.projectAssetToBead` (`assets-core.mjs:63-64`): `acceptanceKind` for a spec is `feel` ONLY if the
Instructions match `/\b(feel|visual|juice|looks?|aesthetic|polish|vibe|readable|readability)\b/i`, else `sim`.
"Game starts with the main menu" matches NONE → `sim`. And `assets-core.mjs:84` routes **every** spec to
`engine:"agy"` unconditionally — even a `feel` spec — despite AGENTS.md's "feel/visual ALWAYS routes to Claude
(agy is preview-blind)". So a menu (inherently visual) got the headless-logic engine + the hard sim path. **Fix
candidates:** widen the feel-keyword set / default UI-ish specs to `feel`; route `acceptanceKind:feel` specs to
`engine:"claude"` (the engine map must honor acceptanceKind, not just kind); let the operator set acceptanceKind
in the Asset Browser at Ready-time.

### ⛔ GOTCHA 3 — planner↔worker block/unblock CHURN is a slow opus cost leak the breaker barely catches
Observed commit oscillation (~every 2 min, one opus planner call each): worker `blocked (spec already
implemented)` → `planner-blocked: unblocked/rescoped asset-…-r8` → worker `blocked` → planner rescope … The
planner's `blocked`-mode replan keeps deciding the dead-end bead is actionable and un-blocks/rescopes it. **Why
the §15.20 breaker is slow here:** it trips on M cycles with no net `openWork` REDUCTION, and a productive
planner outcome (`planned` ∈ `BREAKER_BENIGN`) does NOT bump it; only the worker-`blocked` cycles bump. With the
planner resetting `replan=0` each time a work bead dispatches, the pattern is plan→plan→work-block repeating, so
it takes ~5 worker-block cycles ≈ ~15 total cycles / ~30 min / ~10 opus calls to trip. **I stopped it manually**
(TaskStop + a transient `.paused`) rather than pay that out. **Fix candidates:** the planner `blocked` mode must
NOT un-block a bead whose block reason is operator-gated / "already implemented" (respect a terminal-needs-human
flag); and/or make an unblock-of-a-just-blocked-bead a non-benign breaker signal so oscillation trips fast.

### Minor
- **`sense.mjs` is NOT side-effect-free** — running it to "preview" the SNAPSHOT MINTS the asset beads
  (asset-scan `reconcileAssets` runs INSIDE `computeSnapshot`, writes `control/backlog.json`). A dry look writes.
  (By design — the scan is part of sense — but surprising when you just want to inspect.)
- **`.paused` (gitignored) is the clean stop signal for the Workflow host** but the host only breaks at the next
  `sense`; mid-opus-planner it won't see it promptly, so `TaskStop <taskId>` is the immediate halt. After a
  TaskStop the tree was clean (no `.tick.json` — the kill landed between ticks). Cleared `.paused` after so the
  next Start isn't silently blocked.

### State left for the operator
3 images Implemented (committed). `ui_mainmenu` spec: Ready, dirty, bead `asset-a-mr32beju-d0l0-r8` = blocked
(terminal → loop won't re-dispatch). The menu is FUNCTIONALLY done (renders + 3 buttons wired). Closing the spec
needs an operator decision (reclassify to feel + a rework increment to produce a feel-advisory land, OR retire
the spec, OR a system fix per gotchas 1–3). Did NOT fake-close it (no hand-edited Implemented = no false green).

---

## 2026-07-02 (later 2) — Dashboard UX overhaul: single-page → 4-TAB layout

`ui/index.html` restructured from the old 2-column (controls | preview) grid into a **tabbed** app to use the
full width and separate concerns:
- **Persistent header**: title + status/stage pills + global **Start/Stop loop** (always visible, all tabs).
- **Tab bar** (`ccShowTab(name)`, choice persisted in `localStorage['cc-tab']`): panels are `.tab-panel`
  (`#tab-dashboard|assets|preview|settings`), only `.active` shows.
- **📊 Dashboard** = realtime worker/loop state: an expanded stat grid (in-flight/ready/done/blocked/suggestions/
  ticks-today/**supervisor**/**mode**/**known-good**, populated in `pollCosmo` via new `#cc-*-stat` ids) + a
  space-filling `.dash-grid` of Active tasks / Feel review / Backlog / Suggestions / Completed / Logs.
- **🎨 Assets** = the Asset Browser alone; `#cc-asset-list` is now a `.cc-asset-grid` (`repeat(auto-fill,
  minmax(400px,1fr))`) so rows tile multi-column instead of one narrow stack.
- **🖥 Preview** = the `:8780` iframe alone, `.preview-card` `height:calc(100vh-11rem)` (iframe was 380px, now
  ~620px); auto-snapshot moved into a `<details>`.
- **⚙ Settings** = Worker engine + Concurrency/runtime (+ an About card).
- `.wrap` max-width 1360→**1880px**. GOTCHA: every existing element **id was preserved** and just moved between
  panels — the poll/asset/config/agent JS is unchanged and finds them by id regardless of tab, so DON'T rename
  ids when editing panels. A hidden panel is `display:none` but its DOM/iframe still lives (poll keeps it fresh).
- **Preview-tool note:** `preview_screenshot` HANGS on this page (the live PixiJS `:8780` canvas iframe) — verify
  layout with `preview_snapshot` (a11y tree) + `preview_eval` `getBoundingClientRect`/`getComputedStyle`, per the
  "route geometry to deterministic measurement, don't trust downscaled images" rule.

---

## 2026-07-02 (later) — Dashboard bug sweep: 7 real bugs found (live walk + 25-agent audit), fixed

Full "go through the UI" pass: drove the live dashboard (:7788) with the Preview MCP AND ran a 5-dimension /
verify Workflow over `ui/index.html` + `server.mjs`. 11 raw findings → 7 unique real bugs fixed, 9 refuted.

### The big one — saved instructions were INVISIBLE (HIGH)
`orchestrator/assets.mjs projectRow()` (the `/assets/list` row projection) did **not** include `instructions`,
but the Asset Browser row textarea reads `a.instructions` (`ccBuildAssetRow`, `ta.value = a.instructions||''`).
So every asset's operator-typed instructions rendered **blank**, and any force-rebuild (`loadCosmoAssets(true)`
from a filter/toggle/answer/replace/delete) wiped the textarea to `''`. Only the Answer sub-box showed real
text (it reads full meta via `/assets/get`). Confirmed live: meta.json had "Found in the main menu" etc. while
the field showed the placeholder. **Fix: add `instructions: m.instructions ?? ""` to `projectRow`.** Rule: any
field the row DOM reads must be in the list projection, not just the full-meta read — a boot `reconcileAssetIndex`
rebuild is needed for it to appear (drift-check compares rev only, so an old assets.json won't self-heal on field add).

### ccToggleState had no `needsConfirm` path — couldn't un-Ready a guarded spec (MED)
Retiring a Ready spec that is the last Ready spec / spec-legacy monolith / cited / implemented returns
`409 {needsConfirm:true}` (server `hAssetsState`). `ccToggleState` only quiet-resynced on `409 && !needsConfirm`,
so it fell to the error-toast branch and redrew the row Ready — a silent dead-end. **Fix: mirror
`ccDeleteAsset`/`ccDoReplace` — `confirm()` then resubmit `{confirm:true}`.** (This class keeps recurring: the
confirm-gated mutators must ALL implement the two-step resubmit; ccToggleState was the one that missed it.)

### Others fixed
- **Empty-state clobbered a kept locked row** (`ccRenderAssets`): `container.innerHTML =` for "no assets match"
  ran on `!filtered.length` even when a focused/dirty row was intentionally kept → wiped it. Fix: APPEND a node,
  gate on `!container.querySelector('.cc-asset')`.
- **Stale snapshot never hidden**: `if (s.snapshotMtime)` had no else → a removed snapshot kept showing the old
  frame. Fix: else hide + `removeAttribute('src')`.
- **Dropzone `.drag` highlight stuck**: cleared only when `e.target===card`; leaving over a child left it lit.
  Fix: `!card.contains(e.relatedTarget)`.
- **Deleted-out-from-under edit → 400 not 409**: `hAssetsInstructions`/`hAssetsAnswer` catch only mapped
  `/rev mismatch/`; a `no asset:` throw returned 400 (client only resyncs on 409). Fix: map `^no asset` → 409.
- **MP3 sniff false-positive**: bare `0xFF Ex/Fx` frame-sync misclassified arbitrary binaries as audio. Fix:
  validate version/layer/bitrate/samplerate header bits.

### Refuted (do NOT re-file) — verified NOT bugs
poll re-entrancy (JS run-to-completion; `ccRenderAssets` fully sync), blur+debounce double-save (both POST the
same full value; loser 409s harmlessly), feel-confirm null assetId (projectAssetToBead always binds a real id),
onclick JS-string entity-decode (ids charset-validated upstream + loopback/CSRF-gated single-user), replace
missing decoded cap (32MB body cap × base64 ¾ < 24MB), UTF-8→spec sniff (inert until operator toggles Ready),
requestFullscreen no-catch (parent-owns-iframe not policy-gated), start junk pidfile (self-heals via ccAlive NaN).

### Known LOW left unfixed (cosmetic, self-heals)
`ccRenderAssets` reorder is gated on global `!anyLocked`, so a new/changed row sits at the bottom out of rank
order while ANY row is locked (open qbox/edit); a later idle poll reorders it. Not fixed — a per-row reorder
rewrite risked regressing the §15.35 locked-row protection for a purely cosmetic, self-healing drift.

---

## 2026-07-02 — Dashboard (Asset Browser) UI gotchas + agent-process mistakes (learned the hard way)

Working the standalone dashboard (`ui/index.html`, served by `server.mjs` :7788). Two real user-facing bugs and
two process mistakes. Commits: `25de87d` (rev-mismatch), `687554f` (render-freeze).

### The §15.35 focus-safe render has TWO traps — both bit us on the Ready/Not-Ready toggle
1. **Stale-rev "rev mismatch: have N, got N-1" on a repeated action.** Every asset mutation is rev-guarded
   (`assets.mjs checkRev`; `setState`/`setInstructions`/`replaceArtifact`/`answer` ALL do `rev: m.rev+1`). A client
   handler that POSTs `rev: Number(el.dataset.rev)` and then relies ONLY on the async `loadCosmoAssets(true)` reload
   to refresh the row leaves `el.dataset.rev` stale → a fast 2nd click re-sends the old rev → 409. **Rule: on
   success, immediately set `el.dataset.rev = j.asset.rev` AND `ccAssetRows[id].rev = j.asset.rev`** (mirror the
   already-correct `ccSaveInstructions`), add an in-flight guard (`el.dataset.stateBusy` / `btn.disabled`), and treat
   a `409 && !needsConfirm` as a quiet resync (the change already applied), not a scary error. `ccToggleState`,
   `ccDoReplace`, `ccAnswer` all had this; `ccSaveInstructions` was the correct reference; `ccDeleteAsset` sends no rev.
2. **"The button only updates when I click a DIFFERENT button."** `ccRenderAssets` skips a *locked* row **even on a
   force render** (`if (ex && ccRowLocked(ex.el)) return;`), and `ccRowLocked` used `el.contains(document.activeElement)`
   — so clicking the Ready button *focused that button*, which locked its own row, so its label never rebuilt until
   focus left. **Fix: lock ONLY on an editable-field focus** (`TEXTAREA` / text `INPUT`), never a button/checkbox/radio.
   The §15.35 lock exists to protect unsaved TYPING (+ the `dirty` flag + open answer-qbox), NOT a click.
- **Meta-lesson:** a "repeated action doesn't work" symptom can be SEVERAL independent bugs stacked (stale rev AND a
  render skip). Fixing the error toast ≠ fixing the visible behavior — verify the actual DOM outcome (`preview_eval`
  reading the button's textContent / row class after a real focus+click), not just "no error".
- **Adversarial audit paid off:** a 7-agent Workflow auditing every mutation handler for the stale-rev class found the
  two SIBLING bugs (`ccDoReplace` durable-lock path, `ccAnswer` double-submit) I'd have missed by fixing only the
  reported one.

### Process mistakes (don't repeat)
- **`control/assets/<id>/` folders may be the OPERATOR's LIVE uploads, not test cruft.** During cleanup I almost
  `rm -rf`'d the user's just-uploaded test assets and I DID hand-zero `assets.json` (desyncing the index from the
  folders). ALWAYS read `<id>/meta.json` before deleting anything under `control/assets/`; to rebuild the index, boot
  `server.mjs` once (its `reconcileAssetIndex` regenerates `assets.json` from the folders) — never hand-write it empty
  while folders exist.
- **The Bash tool is Git Bash (POSIX sh), NOT PowerShell.** Use `git commit -F msg.txt` or a `<<'EOF'` heredoc for
  multi-line messages. A PowerShell here-string (`@'...'@`) passed to the Bash tool leaks a literal `@` into the arg
  and mangled a commit subject (needed `--amend`). PowerShell here-strings belong in the PowerShell tool only.

---

## 2026-07-01 — STEP-8 (§15g phase 8) PARALLEL VALIDATION — the FINAL phase; the §15/§15g build is COMPLETE (10 commits + 4-fix audit)

FLIPPED the concurrency toggle to **parallel, maxConcurrency=2** after proving serial-parity across every module +
both hosts. bookkeep stays SOLE gate/commit/revert authority. Commits `cosmo-canyon`: e71dfef (worktree-junction) ·
0453d9e (gate-only) · 49bf6aa (dispatch) · ad605ce (merge) · af94913 (intg-fixes) · 99edabd (stop-hook) · 96f3bab
(supervisor) · a62d67e (workflow) · b72b488 (flip) · bb729da (audit-fix). `C:/Vibes`: 99edabd also touched `.claude/settings.json`.

### The parallel pipeline (maximal reuse of the audited serial bookkeep)
- **`dispatch.mjs` (NEW) — top-of-cycle plan+claim+worktree.** `planCycle()`: candidates = non-terminal backlog
  beads (decorated with the ASSET kind so `resolveFiles`/`engineOf` infer image/audio ownership) → `schedule`
  (disjoint set, tier-weighted cap, LIVE claims block slots) → per pick `claim()` (per-agent anchor
  {baseSha,beadId,worktree}) + `worktree.create --detach @BASE` + `linkNodeModules` (junction) + `writeActive` +
  `bumpUsage`; rolls back claim+worktree on any failure. **agy → serial lane, NEVER a worktree (15.42):** dispatch
  a parallel BATCH of claude workers OR (only when nothing parallel is pickable) surface the ONE serial agy bead for
  the host's main-tree serial path — never both → agy never concurrent with the merge. SERIAL mode → `planCycle` no-op.
- **`bookkeep --gate-only` (NEW mode) — the worker gates IN its worktree, lands NOTHING.** Runs the SAME
  tamper/scope/gate checks, writes `claims/<id>.gate.json` green/red, leaves the worktree DIRTY (the merge reads the
  diff). NO commit/revert/derive/provenance/shared-status. GAME/TSX_CLI made MUTABLE → retarget to the worktree game
  when `IN_WT && !CC_GAME` (the worker needn't set CC_GAME). Acceptance DEFERRED to the merge (an image render-
  reachability grader needs the merge-only derive to flip manifest real → cannot pass in a worker).
- **`merge.mjs` (NEW) — the SINGLE COMMITTER.** Per green gate marker (deterministic id order), holding `git-tree`
  PER CLAIM (short holds → a GUI-ingest can interleave BETWEEN landings): `git apply` the worktree SRC diff
  (**manifests EXCLUDED** — derive-owned, 15.37) onto current HEAD → run the FULL audited serial `bookkeep --result
  work` at post-merge HEAD (re-gate + acceptance + derive-bind + commit + provenance). **Reusing bookkeep = the git
  surgery is BYTE-FOR-BYTE serial; the merge is orchestration only, no new destructive code — this IS the 15.38
  seam re-gate + 15.37 derive-once-in-committer.** On commit → cc-known-good. RED → `bookkeep --result blocked`.
  Apply-conflict → scrub + discard worktree + retry next cycle. Orphan-sweep crashed workers (no attempt, infra-kill).
  Auto-drop maxConcurrency on repeated re-gate/conflict reverts (→ serial at the floor). **`CC_GITTREE_HELD=1`**: the
  merge holds git-tree so `bookkeep.commit` skips the non-reentrant re-acquire (else self-deadlock). The merge
  subprocess path is located via `import.meta.url` dirname, NOT CC_CC — the orchestrator SCRIPT is always real;
  CC_REPO/CC_GAME/CC_CONTROL redirect only the DATA (verify seam).
- **Stop-hook neutralized (15.19):** `.claude/settings.json` early-exits on `CC_WORKER_NO_COMMIT`. The supervisor
  spawns every worker with it + `CLAUDE_PROJECT_DIR=worktree`. (The Workflow host's `agent()` subagents fire NO
  per-agent Stop hook, so 15.19 is supervisor-only — the audit confirmed this.)
- **Both hosts wired.** supervisor.mjs `!isSerial` → `runParallelCycle` (spawn N `claude -p` workers concurrently in
  worktrees via `tick.parallel.md`, Promise.all, mergeGreen, crash-sweep) + CC_REPO/CC_CC/CC_BRANCH seams +
  `--parallel-once`. cc-loop.workflow.js `concurrencyMode==="parallel"` → dispatch agent → N worker agents
  (Promise.all) → merge agent. **`concurrencyMode` is emitted by the SHARED `computeSnapshot`** so both hosts branch
  identically (15.22 parity preserved — both read the same config.json).

### CRITICAL gotchas proven EMPIRICALLY (carry forward)
- **`git worktree remove --force` FOLLOWS a `node_modules` junction and DELETES the shared node_modules** (a probe
  went 7733→7487 files). The game's node_modules is gitignored → a fresh worktree can't run `npm run gate` → junction
  the MAIN tree's in; but the junction MUST be dropped NON-recursively (`rmSync(recursive:false)` / .NET
  `Directory.Delete(path,false)` — both verified never-follow) BEFORE the git remove. `worktree.remove` does it at the
  single choke point so reconcileParallel + the merge are both safe. **Never `rmSync(recursive:true)` a dir that may
  contain a junction to a shared target.**
- **`git apply --3way` is NOT atomic** — on a seam conflict it WRITES `<<<<<<<` markers into the file + leaves a UU
  index entry, THEN exits non-zero. A failed apply MUST be scrubbed (game-scoped reset+checkout+clean) or the next
  committer's `git add -A` folds the poison in. (The audit's HIGH.)
- **A serial-agy tick runs via `spawnTick` in the MAIN tree even under parallel mode** (writes `.tick.json`, dirties
  game/) → the parallel reconcile must still run the defensive game-clean + stale-`.tick.json` handling (don't
  early-return past it). preflight.mjs never had this gap; the supervisor did (audit MED, fixed).
- **The worker log fd** must be `closeSync`'d in the child exit/error handlers (all 3 spawners leaked → EMFILE over a
  lights-out run; fixed).

### Adversarial audit (8-lens find→verify→synthesize, 19 agents) — 11 findings, 6 CONFIRMED → 4 fixed, 5 refuted
Fixed: (HIGH ×3 same bug) merge conflict-branch left the tree dirty (no scrub); (MED) parallel-reconcile skipped the
defensive clean + serial-agy `.tick.json`; (MED) workflow dispatch ignored a custom cap; (MED) spawner fd leak.
Refuted (correctly): workflow-agent Stop-hook escape (no per-agent hook, documented); two-host over-dispatch (bounded
by the shared claim+slot+usage layer); cleanupClaim-remove-after-taskkill (handle freed first); claim-GC-leaves-worktree
(a live host writes a long-lived pid → GC never fires on a live claim); dropNodeModulesJunction-return-unchecked
(removal robust, trigger refuted). **Lesson: three lenses independently converging on the same line (merge:161) was
the strongest signal — the `--3way`-not-atomic behavior is non-obvious, only surfaced by tracing the conflict path.**

### VERIFY (all pass — throwaway repos, no live commits; the real committer code driven end-to-end)
worktree junction 11/11 (create+link+gate GREEN via junction; remove drops junction FIRST → main node_modules intact
7733; non-root refused). Mechanics 19/19 (2 disjoint beads → 2 isolated worktrees → gate-only green → merge lands 2
SERIALIZED → 2 commits, both edits at HEAD, worktrees GC'd, claims released, branch intact). Mechanics-2: agy-never-
concurrent (deferred while a claude batch runs; alone → serial lane, no worktree/claim), cap clamps slots, killed-agent
reconcile (dead worktree GC by EXPLICIT path, LIVE claim + main branch UNTOUCHED, no bare prune). Supervisor HOST
harness 6/6 (`--parallel-once` + a CC_WORKER_CMD fake-worker seam drives spawn+Promise.all+merge+crash-sweep end-to-end).
Conflict-scrub 11/11 (2 workers rewrite the same shared.ts line → 2nd conflicts → main tree CLEAN, no markers
committed, 1 lands, conflicted bead ready/retryable). SERIAL PARITY byte-identical (sense trigger
`{mode:blocked,latchKey:cc-0009}` under both modes; serial reconcile-only boot unchanged); game `npm run gate` GREEN;
config CLI = parallel N=2; reconcileParallel LIVE at boot. A full live parallel WORK run (real claude workers
committing to the shared branch) is the operator's call — the machinery is proven end-to-end.

STOP point: **§15g phase 8 done + verified → the §15/§15g build is COMPLETE (phases 0–8 all landed + verified).**
Fall back to serial any time via `control/config.json` mode="serial". Docs (AGENTS.md/README.md) retargeted to parallel.

---

## 2026-07-01 — STEP-7 (§15g phase 7) SPEC-AUTHORITY CUTOVER (serial) LANDED (8 commits + 5-fix audit)

> ⚠️ PARTIALLY SUPERSEDED (2026-07-02): the GDD was later REMOVED ENTIRELY. Everything below about a GDD→Not-Ready
> SPLITTER (`ccApplySplit`), the `spec-migrate.mjs` / `spec-legacy` HUMAN_GDD bootstrap, HUMAN_GDD.md, and the
> FROZEN `gddChanged`/`gddSha` field names / `.gdd-consumed` marker is HISTORICAL — those are gone. Current: fields
> are `authorityChanged`/`authoritySha`, the marker is `.authority-consumed`, and there is no splitter/seed. This
> entry records what landed at these commits; do NOT read it as current code.

The loop's north-star moved from the single `game/docs/HUMAN_GDD.md` to the **set of Ready Spec assets** (§15b).
bookkeep stays SOLE gate/commit/revert authority (barely touched). Config stays SERIAL (N=1, toggle NOT flipped).
Commits `cosmo-canyon`: 16770e8 (gitignore) · e4045f3 (spec-compile) · 5512c85 (splitter) · 8798159 (cutover) ·
9b00b5e + 9e548e7 (spec-migrate + live spec-legacy data) · 33cd30f (guards) · 9aed2ca (audit-fix). Did NOT flip
config to parallel + dispatch/claim/worktree/merge/single-committer/HEAD-re-gate (phase 8).

### The cutover
- **`spec-compile.mjs` (NEW) owns the SINGLE authority-hash definition.** `authorityHashOf(specs)=sha1(sorted(
  readySpecs.map(s=>`id:rev:contentHash`)))`; `rev` is the monotonic authority-generation (§15a) so add/remove/edit
  of a Ready spec all move the hash — **no per-sense git/file read** (reads the derived `assets.json` index only).
  `compileSpecs()` holds the `assets-index` lock for the WHOLE scan+write → `control/spec-doc.md` (planner north-
  star, Not-Ready specs EXCLUDED = the PRIMARY WIP wall) + `control/spec-index.json`, and **RETURNS the hash it
  emitted** (15.24: the doc + the hash can't disagree — both call the SAME `authorityHashOf`). `writeKnownGood()`
  persists `.authority-known-good` (15.5). spec-doc.md/spec-index.json/.authority-known-good are GITIGNORED (derived
  views over the committed per-asset `meta.json` authority).
- **`spec-core` decision-path swap.** `specAuthoritySha()` → `authorityHashOf()` (was the `gddSha` alias); the
  SNAPSHOT field names STAY `gddChanged`/`gddSha` (frozen — Workflow trigger / latchKey / `.gdd-consumed` keep
  working). `wipKeywords()` reads `spec-doc.md`. Added `readySpecCount`/`authorityEmpty` + `gddChangePending`.
  **`authorityEmpty` is the FIRST `computeTrigger` branch → null** (no Ready specs ⇒ zero topup/diff/audit/opus,
  15.33); the completion is overridden to `idle-blocked-on-human` in spec-core so BOTH hosts report it identically.
- **Debounce (15.16).** `gddChanged` is DEBOUNCED via `control/.authority-settle` (~90s, `CC_AUTHORITY_SETTLE_MS`
  override): a curation burst = ONE coalesced `diff`, not N opus plans. `.gdd-consumed` (written by plan-apply on
  `diff`) coalesces once-per-settled-generation. The browser touches the marker after a spec write (precision).
- **Authority-only spec exclusion.** `computeAssetSnapshot` EXCLUDES a spec that is `ready && !dirty &&
  !implementedBy && no questions/breakers` (the spec-legacy monolith) from the anomaly/dispatchable/to-spec
  accounting — it is the compiled north-star the planner decomposes, NOT an "asset to implement." A spec the
  operator toggles Ready is ready+DIRTY → NOT excluded → mints a build bead normally.
- **GDD-ingest → SPLITTER (`ccApplySplit`, exported/network-free).** Front half kept (docId/export/15.52 cap/login-
  empty reject). Back half: parse `^#{1,2}` headings → ONE **Not-Ready** Spec asset per heading (idempotent by
  `importDocId+importHeading`; preamble→`Overview`; sub-headings stay in the parent; per-block cap). Import NEVER
  instant-authority / trips `gddChanged`. HUMAN_GDD.md is NO LONGER written (only the spec-legacy seed).
- **Guards.** No-clobber a Ready/Implemented spec (`appendImportQuestion` — NO dirty/rev bump → no re-mint / hash
  perturbation, 15.6); per-spec(<40%)+aggregate(35%/>2h) shrink both 409 (15.11); `/assets/state` Ready→Not-Ready
  confirm on citing beads / Implemented / **drain-to-empty / spec-legacy monolith** (15.5); `.authority-known-good`
  at each green commit (post-tick + supervisor); the `audit` planner drifts vs LAST-GREEN, not the live set.
- **`spec-migrate.mjs` (NEW) — spec-legacy bootstrap (15.33).** Seeds ONE Ready authority-only spec from
  HUMAN_GDD.md (47542B, instructions-body → **dirty=false** → not a build bead), syncs `.gdd-consumed`=hash + seeds
  known-good, then a MIGRATION GATE (readySpecCount≥1 + !gddChanged + trigger==pre-cutover baseline) with all-or-
  nothing rollback. **Ran LIVE** → authority restored, trigger preserved `{mode:blocked,latchKey:cc-0009}`. Idempotent
  (source:`spec-legacy`), and the short-circuit VALIDATES+REPAIRS (re-sync consumed) rather than blind-no-op.

### Adversarial audit (7-lens find→verify→synthesize, 15 agents) — 8/8 CONFIRMED (0 refuted), dedup to 5 fixed
- **HIGH — `ccRefreshSpecAuthority` reset `.authority-settle.firstSeen` on EVERY spec write** (unconditional) →
  editing UNRELATED Not-Ready specs (which don't change the Ready-only hash) restarted the debounce window forever →
  a real pending `diff` was suppressed indefinitely during curation. Fix: read the marker, only START a window on a
  NEW pending sha (mirror `debouncedGddChanged`). **Lesson: a "precision optimization" that WRITES a shared debounce
  marker must mirror the reader's restart guard exactly, or it silently extends the window it meant to only start.**
- **MED — duplicate `^#{1,2}` heading text collapsed the splitter `byHeading` Map** (last-write-wins) → non-idempotent
  churn (updated=N forever → perpetual commits) + mis-keyed per-spec shrink + an orphaned spec. Fix: `ccSplitSpecs`
  disambiguates repeats with a deterministic ` #N` suffix (re-import reproduces the same keys → idempotent).
- **MED — loop idle-EXIT inside the ~90s settle window dropped the diff** until a manual restart (pre-phase-7 the
  raw `sha!=consumed` fired immediately, before idle). Fix: spec-core emits `gddChangePending`; the detached
  supervisor POLL-WAITS in-place (bounded `MAX_SETTLE_WAITS`) instead of exiting; the human-present Workflow host
  logs a "settling" note (the marker persists → next run fires it). **Lesson: converting an immediate trigger into a
  debounced one requires the host to stay alive across the window, or it drops the change on an idle boundary.**
- **MED — retire-confirm (15.5) never fired for spec-legacy** — the authority monolith is decomposed into assetId-
  LESS planner `cc-####` beads and has `implementedBy=null`, so `citing=0 && isImpl=false` → the sole north-star was
  silently retirable (drains authority → idle). Fix: also confirm on drain-to-empty (last Ready spec) or
  `source=spec-legacy`. **Lesson: a "citing beads" count keyed on `assetId` misses the planner-DECOMPOSITION linkage;
  guard the drain-to-empty effect directly, not just the citation.**
- **LOW — spec-migrate idempotency short-circuit skipped consumed-sync + gate** after a mid-bootstrap crash (asset
  committed before `.gdd-consumed`) → a re-run no-op'd with a stale consumed → a spurious post-cutover diff. Fix:
  the short-circuit recompiles + re-syncs `.gdd-consumed` + re-seeds known-good + re-runs the gate (validate+repair).

### Gotchas learned this pass (carry forward)
- **The state↔spec-core cycle TDZ bites a MODULE-INIT const.** spec-core imports `CONTROL` from state.mjs across the
  known-safe function-level cycle; a `const SETTLE_PATH = \`${CONTROL}/…\`` at module top threw `Cannot access
  'CONTROL' before initialization` when state.mjs was the cycle entry (sense.mjs). Fix: `settlePath()` is a FUNCTION.
  **The rails' rule "reference state's exports ONLY inside function bodies" is load-bearing — never a top-level const.**
- **Post-cutover parity is DECISION-level, not literal-byte** (the authority-hash VALUE legitimately changes git-hash
  → sha1-over-specs). The migration gate asserts the TRIGGER decision is preserved (`{mode:blocked,latchKey:cc-0009}`)
  + gddChanged=false; dual-HOST parity (same plane, both hosts) IS exact byte-identical (same spec-core code).
- **spec-legacy must be dirty=FALSE (authority-only) or it mints a 47KB heavy bead.** `createAsset({bytes:null,
  instructions:GDD})` → dirty=false (no bytes). A ready+dirty spec is BOTH authority AND a build bead by design; the
  monolith is authority-only. Excluded from computeAssetSnapshot's implement buckets so it isn't an anomaly.
- **`.authority-settle` written inside `computeSnapshot` is a SIDE EFFECT** — safe because ONE host holds the cc-host
  lock, and it never writes when `sha===consumed` (settled/empty) so parity fixtures stay byte-identical.
- **`ccApplySplit` is exported + network-free (CC_CONTROL-respecting)** so the splitter is unit-testable; only the
  fetch + the git commit stay in `hGddPost` (the git add is hardcoded to the real tree → live-only).

### VERIFY (all pass — throwaway CC_CONTROL/CC_GAME/CC_PORT + live)
spec-compile 23/23 · splitter 27/28 (create/idempotent/no-clobber/per-spec+aggregate shrink; 1 was a test-assumption
fix) · cutover: empty-authority→null trigger + legacy-shim `{mode:blocked,latchKey:cc-0009}` + authority-only-spec
non-anomaly 9/9 · dual-host SNAPSHOT+trigger BYTE-IDENTICAL 4/4 · debounce (one coalesced diff) 6/6 · toggle-ALL-off
→zero topup/opus 2/2 · bootstrap 12/12 + gate-fail rollback 2/2 · guards HTTP 9/9 (settle-touch + retire-confirm) ·
audit-fix F1 4/4 (window preserved on unrelated edits) F2 5/5 (dup-heading distinct+idempotent) F3 4/4 (gddChangePending
+ parity still byte-identical) F4 4/4 HTTP (drain/monolith 409, no over-gating) F5 6/6 (idempotent repair) · phase-4/5/6
regression 10/10 (image mint/routing, predicate fail-closed, spec heavy/agy) · **live migration ran** (spec-legacy
47542B, authorityEmpty=false, trigger preserved) · game `npm run gate` GREEN · supervisor --reconcile-only boots ·
fresh-clone authority non-empty (spec-legacy meta.json committed).

STOP point: STEP-7 done + verified. Do NOT start §15g phase 8 (flip config to parallel + dispatch/claim/worktree/
merge/single-committer/HEAD-time acceptance re-run). Await human review.

---

## 2026-07-01 — STEP-6 (§15g phase 6) IMPLEMENTED→GAME + ACCEPTANCE (serial) LANDED (12 commits + audit-fix)

Made the phase-5 asset-scan machinery REAL: a Ready image/audio/spec asset that lands now flips DERIVED-Implemented
for the RIGHT reason (render-reachability / reached-by-playback / a non-tautological confirmed spec grader), NOT
just because a bead committed. bookkeep stays SOLE gate/commit/revert authority; config stays SERIAL (N=1, toggle
NOT flipped). Commits `cosmo-canyon`: bed2ab6 (parse-instructions) · f09c68a (derive frames/bind/lock) · fd2536e
(image grader) · 8420e21 (audio) · 94b024a (predicate/store parity) · 15dd865 (bookkeep enforce) · 7417797
(mutation-check junction fix) · 8a27cbd (feel-review) · 67514b8 (reopen) · 2d1f1fc (§15g-T cost) · df99b1a
(AGENTS/README) · b23edde (audit-fix). Did NOT build spec-authority cutover (phase 7) or flip concurrency (phase 8).

### The phase-5 INTERIM CUTS are now REAL
- **`parse-instructions.mjs` (NEW, DETERMINISTIC — not the model)** turns an asset's Instructions ("24x24, 6
  frames, horizontal, 8fps") into a manifest config + a `manifestKey`. **The upload-keying gap is CLOSED:** a
  fresh GUI image/audio upload gets a deterministic `manifestKey` at `/assets/create` (`deriveManifestKey`:
  `key:` directive → sanitized filename → `<kind>.<idtail>`), and `reconcileAssets` has a bind-once
  `setManifestKey` backstop for migration/edge assets — so an upload CAN reach Implemented (a null key can never
  be `real`, so this was the linchpin).
- **image/audio mint REAL graders (drop `renderOnly`).** `projectAssetToBead` sets `assetKind` + `acceptanceKind`
  (image|audio|sim|feel); bookkeep routes an image/audio asset bead to the SHARED parameterized grader
  `game/accept/_image-grader.ts`/`_audio-grader.ts <manifestKey>` (auto-minted = a committed shared script keyed
  on the manifestKey, NOT a per-bead file the worker could tamper). **`derive-bind` runs IN bookkeep (the
  committer tree) BEFORE acceptance** (15.37): it copies the upload's control bytes → game source + a manifest
  slot and `derive`s (flips `real`), serialized on the manifest/audio-manifest lock inside `derive.mjs`/
  `derive-audio.mjs` themselves (so EVERY caller — GUI-upload derive + loop derive-bind — is serialized). The
  worker only WIRES `getTexture('<key>')` / `playSfx/playMusic/playSound('<key>')` (a src edit).
- **Image grader = render-reachability (15.12):** manifest `real` ∧ atlas frame present ∧ an EXECUTING
  `getTexture('<key>')` in `src/**` (comments STRIPPED; `getEntry` NOT accepted — it only reads metadata) ∧
  flipbook frame#0 bytes ≠ frame#1 (static = FAIL). A no-op/still-placeholder/never-wired asset FAILS — no false
  green. Optional puppeteer snapshot layer (`_snapshot-region.mjs`) gated behind `CC_GRADER_SNAPSHOT=1` (vision
  rationing — the deterministic core is the default LAND gate, browser-free). QA-mode `getTexture` records a
  real-status load failure to `window.__ccTexErrors` (the silent placeholder fallback becomes observable).
- **Audio grader = reached-by-playback (15.14):** audio-manifest `real` ∧ file decodable ∧ an EXECUTING
  `playSfx/playMusic/playSound('<key>')` call site. `render/audio.ts` gained a file-backed path (fallback synth,
  `__ccAudioFileBacked` QA counter) + `playSound(key)` so an UPLOADED sound beyond the 4 synth `SfxKind` literals
  is honestly implementable.
- **Spec grader path (15.15/15.17), enforced DETERMINISTICALLY in bookkeep.runAcceptance:** a planner-authored
  `game/accept/<beadId>.ts` lands DISABLED until operator confirm (`grader-confirm.json`, set only by the server's
  `/assets/grader-confirm`) + passes a MUTATION CHECK (run in a detached BASE worktree — must FAIL there, else
  tautological → reject) + emits `ACCEPT-PASS <beadId>` on the last line. Feel/visual specs → an ADVISORY critic
  verdict routed to the human-gated **FEEL-REVIEW queue** (`feel-review.json` + `FEEL-REVIEW.md` + a dashboard
  card + `/assets/feel-confirm`): the completion is `feelAdvisory`, the predicate withholds Implemented until an
  operator confirm stamps `feelConfirmed{beadId,rev}`. **A model verdict NEVER lands a green.**
- **`asset-reconcile.mjs` reopen (implemented→not_ready):** `reopenForRework` clears provenance + `feelConfirmed`,
  sets dirty, BUMPS rev (regenerated grader binds the new rev), flags `placeholderStale`; the completion is marked
  `supersededByReopen`; non-terminal beads superseded; an operator-gated code-removal SUGGESTION is written —
  shipped code is NEVER auto-deleted. Wired to `/assets/state` (a not_ready transition on a derived-Implemented
  asset runs the full invalidation).
- **§15g-T cost items:** child TIME-BOX on `runGate`/`runAcceptance`/mutation-check/derive (a hung test/grader is
  SIGKILLed, not a full-timeout burn); `game/test/sim.ts` split into per-system suites (`sim.<system>.ts` +
  aggregator) — the gate stays FULL, `test/_select.mjs` maps `files[]`→suites for the worker's fast inner loop
  (tick.md); incremental tsc (`incremental`+`.tsbuildinfo`); terse 1-line failure reporter (bookkeep first-FAIL
  line + `_util.ok`); vision rationing (deterministic region grader default, snapshot opt-in).

### Adversarial audit (27-agent, 7-lens find→verify→synthesize) — 14/14 CONFIRMED, all fixed
- **HIGH — grader greps matched COMMENTS/dead code** (`// getTexture('k')` or `// playSfx('k')` satisfied
  render-reachability/reached-by-playback → false green). Fix: STRIP comments before the grep + a word-boundary
  lookbehind (`(?<![A-Za-z0-9_$])` rejects a `mygetTexture` helper) + require an EXECUTING call; dropped `getEntry`
  from the image whitelist (metadata read, never draws). **Lesson: a raw-text grep is NECESSARY-NOT-SUFFICIENT —
  strip comments + whitelist only calls that actually fetch/play, but a deliberately-crafted dead STRING literal
  (`const _ = "getTexture('k')"`) still matches; that residual's backstop is the OPT-IN snapshot layer
  (`CC_GRADER_SNAPSHOT=1`) + the human (playing / feel-review). Do NOT strip string literals — the codebase uses
  double-quoted keys, so stripping would false-REJECT real calls (a false-reject breaks the loop; worse than a
  contrived deliberate cheat the snapshot/human catches). A re-audit confirmed the mutation-check + grader-confirm
  fixes fully sound; these grader residuals are the inherent static-grep limit.**
- **HIGH — mutation-check conflated a BASE crash with an honest FAIL.** A grader importing a HEAD-only symbol
  throws at BASE (module-load error, non-zero) → was read as "correctly failed on base = non-tautological" →
  near-zero tautology protection. Fix: a non-zero BASE exit is honest ONLY if the output has a `FAIL`/`ACCEPT-FAIL`
  marker; a crash with no marker → fail-CLOSED (indeterminate). Also pass `beadId` argv to the BASE run (mirror the
  real invocation) + fail-closed on timeout/spawn-error. **Grader contract now: print `FAIL` when the feature is
  absent (mirrors the `ACCEPT-PASS` token).**
- **HIGH — stale-REV bypass:** `assetStaleBytes` keys on contentHash only, so a rev-only reopen (rev++ without a
  byte change) let a stale-rev bead land against the new generation, defeating reopen. Fix: fail-closed
  `String(bead.rev) !== String(assetMeta.rev)` + a terminal-bead-status land guard.
- **HIGH — JPEG/GIF churn:** a non-PNG image passed create but derive is PNG-only → the bead abandoned silently.
  Fix: `ccValidateImage` rejects non-PNG 422 at create.
- **HIGH — worker could self-write `grader-confirm.json`** (it was in ALLOW_CONTROL) → lift the human gate. Fix:
  REMOVED from ALLOW_CONTROL (a worker's uncommitted write is now out-of-scope tamper → reverted, and the
  out-of-scope check fires BEFORE the accept-pass check in the fail chain, so even a self-confirmed grade can't
  land); the server COMMITS a legit operator confirm so it sits clean at BASE. **Lesson: a control file that gates
  a HUMAN decision must not be an ALLOW_CONTROL worker-write — commit it from the server + let the tamper guard
  reject in-tick writes.**
- **MED — asset no-op re-land:** the phase-6 `noop = diffLines===0 && !bead.assetId` disabled the no-op guard for
  ALL asset beads → an already-real+already-wired image re-dispatched could re-land a zero-work green. Fix:
  `deriveBindAsset` reports whether it changed bytes; `noop = diffLines===0 && (isAssetImgAud ? !bindChanged : true)`.
- **MED — non-atomic derive writes:** `writeIfChanged` used a bare `writeFileSync`; the grader/game read
  manifest/atlas WITHOUT the derive lock → a concurrent GUI-upload derive exposed a torn read → spurious grader
  FAIL. Fix: temp+rename (atomic) in both derive scripts.
- **MED — mutation-check argv/timeout** (folded into the HIGH mutation-check fix). **LOW — feel pre-confirm /
  queue-sweep:** `hFeelConfirm` trusted an operator-supplied beadId (pre-confirm a future land) + blanket-marked
  by assetId (swept a concurrent re-land off the queue). Fix: require a real pending queue entry; scope the
  confirm to that beadId. **LOW — `pickup` SFX has no call site** (un-implementable, fails-closed not false-green;
  `playSound` + noting it is the path). Not all findings changed behavior — some were fail-closed-but-confusing.

### Gotchas learned this pass (carry forward)
- **The game's `@esbuild/win32-x64@0.28.1` platform binary was MISSING** (a broken install; only vite's nested
  0.21.5 existed) → `tsx` could transform CACHED files (the gate's test files) but CRASHED on any FRESH grader
  ("The package @esbuild/win32-x64 could not be found"). This would break EVERY new grader in prod. Repaired with
  `npm install @esbuild/win32-x64@0.28.1 --no-save` (from cache, no network). A fresh clone needs `npm install`.
- **A `.ts` grader with a TOP-LEVEL `await` forces tsx down esbuild's binary-service path** (which was broken
  above); a non-TLA grader transforms fine. The graders run their opt-in snapshot layer via `spawnSync` (no TLA).
- **The mutation-check must NOT junction node_modules into the BASE worktree** — removing the worktree follows the
  junction and DELETES the shared node_modules (would bite phase-8 worktrees too). A sim grader is dep-free; tsx
  from the MAIN tree resolves esbuild + `../src/sim` from the worktree. Removed the junction.
- **The repo's human-session auto-checkpoint Stop hook swept a subagent's + my uncommitted edits into a generic
  `auto-checkpoint` commit mid-turn** (STEP-2 gotcha d) — `git commit --amend` relabeled it into the intended
  `ralph step6-cost-scaling`. Commit each module promptly.
- **derive/derive-audio own the serialize lock** (`../orchestrator/lock.mjs`, `manifest`/`audio-manifest` rank 7)
  so no caller can forget it; a `CC_DERIVE_ASSETS` env seam points them at a throwaway assets tree for tests.
- **Grader test seam:** `_image-grader.ts`/`_audio-grader.ts` honor `CC_GRADER_GAME` (throwaway game root) so their
  PASS/FAIL branches are verifiable without the live game; bookkeep honors `CC_MUTCHECK_ROOT` for the mutcheck WT.

### VERIFY (all pass — throwaway CC_CONTROL + CC_REPO + CC_GAME + CC_PORT)
parse-instructions 19/19 · derive (frames/bind/lock/idempotent/atomic) 11/11 · image-grader 7/7 (live-placeholder
FAIL · wired PASS · unwired/comment-only/getEntry-only/static-flipbook FAIL) · audio 11/11 · assets-core
predicate+store 19/19 · **bookkeep phase-6 integration 18/18** (image wired→derive-bind real→predicate flips
Implemented+not-feelPending · unwired & static-flipbook FAIL = no false green · spec unconfirmed=DISABLED ·
tautological=mutation-check REJECT · no-ACCEPT-PASS-token=FAIL · confirmed+mutation-passed+token=COMMIT ·
feel advisory→FEEL-REVIEW→operator-confirm=Implemented · hung grader child-timeout killed) · feel/grader-confirm
endpoints 9/9 · create-reject 2/2 (JPEG 422, PNG+manifestKey) · UI smoke 4/4 (Feel-review card, no JS errors) ·
reopen 11/11 (supersededByReopen · rev++ · placeholderStale · no auto-delete · invariant not-implemented) ·
regression 6/6 (live sense trigger `{mode:blocked,latchKey:cc-0009}` byte-identical to phase-5, config serial N=1)
· game `npm run gate` GREEN with the split suites + incremental tsc.

STOP point: STEP-6 done + verified. Do NOT start §15g phase 7 (spec-authority cutover: spec-compile.mjs /
spec-index / gddSha→specAuthoritySha swap / GDD→Not-Ready splitter / spec-legacy bootstrap / .authority-known-good
/ debounce / authorityEmpty first trigger). Still OUT OF SCOPE: flipping config to parallel + dispatch/claim/
worktree/merge/single-committer/HEAD-time re-gate (phase 8).

---

## 2026-07-01 — STEP-5 (§15g phase 5) ASSET-SCAN LOOP INTEGRATION (serial) LANDED (8 commits + audit-fix)

WIRE the phase-1 store + phase-4 endpoints into the EXISTING serial tick loop — a Ready+dirty asset now
deterministically PROJECTS into a bead, LANDS, and flips a DERIVED "Implemented"; an unsure worker PARKS the
asset (Questions badge over ready, no spin). bookkeep stays SOLE gate/commit/revert authority; config stays
SERIAL (N=1, toggle NOT flipped). Commits `cosmo-canyon`: d5c3581 (store) · 33851f2 (assets-core) · b1f4a84
(sense) · 807dab2 (bookkeep+ask) · b472681 (active/loop) · 9ea3b84 (server) · f88f2cd (breaker parity) +
step5-audit-fix. Did NOT build grader BODIES / parse-instructions / derive frames>1 / feel-review queue /
asset-reconcile reopen (phase 6), spec-authority cutover (phase 7), or flip concurrency (phase 8).

- **`assets-core.mjs` (NEW) = the shared sense PRE-STEP, run INSIDE `computeSnapshot` (spec-core), so BOTH hosts
  (supervisor imports it; the Workflow host runs `sense.mjs` which imports it) reconcile+snapshot IDENTICALLY —
  never a 3rd trigger branch (15.22 parity holds byte-identical). On a plane with NO Ready+dirty assets it is a
  pure no-op → the SNAPSHOT stays byte-for-byte pre-phase-5.** Owns `reconcileAssets` (fire-latch keyed
  `(assetId,rev,contentHash)` in `.asset-scan-latch.json` → O(changed); supersede stale-rev beads 15.39; abandon
  breaker 15.3), `reconcileActive`/`writeActive`/`removeActive` (active.json, 30s grace, runToken-tied),
  `projectAssetToBead` (image/audio→light/claude, spec→heavy/agy; SRC-only files; NO acceptanceCmd), the PURE
  `implemented()` predicate (§15e/15.32) + `feelPending`, `computeAssetSnapshot` (buckets + openWork + completion),
  and `breakerStep` (15.20). Bead id = `asset-<assetId>-r<rev>` (path-safe `^[A-Za-z0-9_-]+$` → accept-discovery
  works in phase 6; rev in the id → a re-armed asset is a NEW bead).
- **The store's phase-5 primitives are the ONLY dirty-clears (§15a).** `markImplemented`/`writeImplementedProvenanceHeld`
  (green-land flip: implementedBy+implementedContentHash provenance, dirty=false, reset per-asset breakers),
  `bindImplementedSha` (post-commit sha, mirrors patchCompletionSha), `parkUnsure` (unsure badge + dirty=false +
  questionRounds++/escalate), `bumpAbandon`+`setOperatorBlock` (15.3). NONE bump rev (provenance/questions/breakers
  are not authority-gen changes — a rev bump would re-fire the scan latch). `writeImplementedProvenanceHeld` is the
  **no-lock companion** for bookkeep's `acquireOrdered(['asset-<id>','backlog'])` land section — `markImplemented`
  would double-lock the non-reentrant asset-<id> (the STEP-4 no-double-lock rule).
- **bookkeep asset-LAND** binds `recordCompletion({assetKey,contentHash,rev,sha})` (15.50) + does the provenance
  flip bundled with the bead-terminal backlog write under acquireOrdered (serial crash-atomicity = the single
  commit; a crash before commit → the game-scoped revert undoes both). Asserts `hash(graded)==meta.contentHash`
  at land (15.39 stale-bytes fail-closed). New **`--result unsure`** path: revert partials, `parkUnsure` the asset,
  bead `status=parked` (terminal-for-loop, **NO attempts++**); `ask.mjs` records the Questions to `control/.unsure.json`.
- **DERIVED-Implemented is DERIVED-ONLY (15.32).** `state` enum stays `not_ready|ready`; nothing writes
  `state:"implemented"`. `/assets/list` computes the SAME `implemented()` predicate the loop's completion check
  uses (single source) → the UI's read-only "provisional" slot is now real. Completion = to-spec vs
  idle-blocked-on-human (both hosts read `snap.completion`). Redefined **breaker (15.20)**: trips on M cycles with
  NO net `openWork` reduction (NOT "any green"); benign parked/unsure/infra-kill + a productive planner don't bump.

### The phase-5/phase-6 boundary cut (INTERIM, documented — read before phase 6)
- **image/audio beads mint `renderOnly:true`** — their deterministic graders (render-reachability/audio) + `derive`
  are PHASE 6. So a renderOnly image LANDS (gate green, provenance written, dirty cleared) but the predicate stays
  false (acceptance skipped) → **`feelPending`** (a computed "landed-but-not-predicate-implemented" bucket, NOT the
  phase-6 review QUEUE) → the loop reports **idle-blocked-on-human**, never a false to-spec, never re-mints (no spin).
  To PROVE the mint→land→FLIP wiring NOW, the tests supply an interim grader + a manifest-`real` slot (what phase-6
  derive will set). A LIVE run that dispatches a renderOnly image bead to a real worker will flail (no derive tools)
  and abandon (bounded) — acceptable interim; real asset work is phase 6. The 3 seeded assets are `not_ready` so a
  live Start mints nothing until a human marks one Ready.

### Adversarial audit (12-agent find→verify→synthesize) — 5/5 CONFIRMED, all fixed (2 lenses found nothing)
- **HIGH — parked/superseded beads re-dispatched forever:** I added `parked`/`superseded` to `assets-core.TERMINAL`
  but the THREE head-ready-bead filters (spec-core `ready`, state/supervisor `headReadyBead`) still excluded only
  `blocked/abandoned/done` → a parked bead stayed "ready" → re-dispatched (spin, bypassing the human gate) AND the
  honest to-spec/idle stop was unreachable. **Lesson: a new terminal-for-loop status must be added to EVERY
  head-ready filter, not just the snapshot's openWork set — I now import a shared `isTerminal` in all four sites.**
- **HIGH — whole-tree `reset --hard <sha>` destroyed a concurrent upload:** I narrowed the paired `clean` to
  `/game` but `reset --hard` **cannot take a pathspec**, so it reverted the TRACKED `control/assets/*/meta.json`
  (+`assets.json`) a concurrent GUI REPLACE/edit wrote. Fix: a game-scoped restore `reset -q <sha> -- game;
  checkout <sha> -- game; clean -fd game` (supervisor.resetGameTo + preflight) — mirrors bookkeep.fullRevert AND
  fixes the pre-existing 15.49 whole-tree-reset human-WIP exposure. **Lesson: `reset --hard` is whole-tree, full
  stop; scope a revert with checkout+clean, never a pathspec on reset --hard.**
- **HIGH — unguarded `landAssetProvenance` throw left a phantom completion:** acquireOrdered lock-busy (GUI holding
  asset-<id>) or `readMeta` "no asset" (concurrent DELETE) threw AFTER `recordCompletion` → phantom completion +
  re-dispatched bead. Fix: land + recordCompletion in one try/catch, completion recorded ONLY after a successful
  land; a throw → revert + bumpAttempt (bounded), no phantom.
- **MED — `implemented()` false-green:** the img/audio manifest-`real` gate was guarded on `&& asset.manifestKey`,
  which is **null for every uploaded asset** (createAsset default) → the gate short-circuited, so a committed grader
  alone (acceptanceSkipped=false) flipped Implemented without the sprite ever wired into the manifest. Fix:
  fail-closed — a null/absent manifestKey img/audio can NEVER be real. **Lesson: a guard `X && cond` silently skips
  `cond` when X is the common default — gate on the KIND, not on an optional field.**
- **LOW — `computeAssetSnapshot` didn't Array-guard `idx.assets`** (it runs OUTSIDE senseAssets' try/catch): a
  corrupt derived `assets.json` (`{"assets":{}}`) crashed sensing instead of degrading. Array-guarded.

### Gotchas learned this pass
- **`state.CONTROL` now honors `CC_CONTROL`** (was hardcoded) + **bookkeep honors `CC_REPO`/`CC_GAME`/`CC_CONTROL`**
  (default real → serial byte-for-byte) so the sense pre-step + the full asset-LAND/unsure path run against a
  THROWAWAY control plane + a THROWAWAY git repo. Gotcha (a) reaches DEEP: a `CC_CONTROL` test must set the env
  BEFORE importing (const-evaluated at module load) and reach bookkeep too, not just assets.mjs.
- **`node <file>.ts` RUNS as CJS** on Node 18 (no "unknown extension" error) if the file is valid JS — so a
  throwaway interim grader needs no tsx (bookkeep's plain-node fallback runs it). The earlier "Invalid token" was a
  `printf`-mangled newline in the source, NOT the `.ts` extension.
- **`allowed()` in bookkeep now covers the live asset store** (`control/assets|claims|locks|.trash/` + assets.json/
  active.json/latch) so a concurrent GUI upload during a tick is NOT flagged out-of-scope (would fail the tick) NOR
  destroyed by fullRevert (§15c-2). A worker planting `control/<junk>` NOT under an allowed prefix is still flagged
  + surgically removed by revertOnePath (tamper protection intact; verified).
- **`fullRevert` scoped to `cosmo-canyon/game`** (was whole `cosmo-canyon`) — protects control/assets uploads;
  bookkeep re-writes its own control files (backlog/completions) after, so not reverting them is fine.
- **Residual (accepted, low-sev):** completions.json / meta.json sha-patches (patchCompletionSha/bindImplementedSha)
  leave the file DIRTY post-commit — the RALPH_PASS Stop hook folds them (supervisor path); the Workflow host has
  no per-agent Stop-hook-equivalent (pre-existing STEP-0b limitation, not phase-5). A worker CAN write
  control/completions|assets (allowed) to spoof the loop's Implemented perception → only a self-defeating DoS (the
  game gate/acceptance is independent; no bad code lands); the tradeoff favors not-destroying-uploads.

### VERIFY (all pass — throwaway CC_CONTROL + throwaway git repo + throwaway CC_PORT)
Unit 39/39 (mint exactly-1 + fire-latch + supersede + abandon-breaker 15.3 + question-rounds→escalate 15.4 +
answer re-arms ONE + active writer/GC + predicate fail-closed cases + feelPending + breakerStep 15.20 +
completion to-spec/idle-blocked). Bookkeep integration 19/19 on a throwaway git repo (asset-LAND → provenance +
completion bind {assetKey,contentHash,rev,sha,acceptanceSkipped:false} + dirty cleared + predicate flips
implemented; `--result unsure` → park, badge, dirty=false, bead parked NO attempts++, no spin; stale-bytes
15.39 fail-closed; abandon→bumpAbandon). Server 5/5 (`/assets/list` implemented flag + counts.implemented +
`/active` over HTTP). Audit-fix regression 11/11 (parked/superseded excluded; null-manifestKey img fail-closed;
game-scoped restore preserves a concurrent control/assets edit; missing-asset land → reverted, no phantom
completion). Regression: game `npm run gate` GREEN; 15.22 SNAPSHOT+trigger byte-identical supervisor vs
workflow (`{mode:blocked,latchKey:cc-0009}`); config serial N=1; `ui/index.html` untouched (phase-4 focus-safe
UI + banner unaffected; `/assets/list` additive). Live tree clean throughout (all tests in the session scratchpad).

STOP point: STEP-5 done + verified. Do NOT start §15g phase 6 (parse-instructions / derive frames / render-
reachability·audio·spec grader BODIES / feel-review queue / asset-reconcile reopen). Still OUT OF SCOPE: spec-
authority cutover (phase 7); FLIPPING config to parallel + dispatch/claim/worktree/merge (phase 8).

---

## 2026-07-01 — STEP-4 (§15g phase 4) ASSET BROWSER + endpoints LANDED (3 commits: server / ui / docs)

WIRE the phase-1 store to §15d endpoints + a NEW UI surface — NOT new pipeline logic. bookkeep stays SOLE
gate/commit/revert authority (untouched); config still SERIAL (read for display only). Commits `cosmo-canyon`:
70926e9 (server.mjs endpoints), 8c4f1d5 (ui Asset Browser), + this docs commit. Did NOT build the asset-scan
loop / reconcileAssets / projectAssetToBead / ask.mjs / active.json WRITER / completion predicate (phase 5),
derive/graders/parse-instructions (phase 6), spec-authority (phase 7), or flip concurrency (phase 8).

- **The store locks INTERNALLY → endpoints call it directly, NO double-lock.** `assets.mjs`'s
  create/replace/setInstructions/setState/answer each acquire `asset-<id>` (meta write) then `rebuildIndex()`
  acquires `assets-index` as a SEPARATE step (never both held → crash-between self-heals). `lock.mjs` is
  NON-REENTRANT, so an endpoint wrapping a store mutator in `ccWithLock('asset-<id>')` would SELF-DEADLOCK.
  The ONLY endpoint doing raw locked fs is DELETE (no store primitive): tombstone-rename under `asset-<id>`,
  then `rebuildIndex()` separately — same never-both-held shape.
- **`process.env.CC_CONTROL` is PINNED to the host's resolved path at server.mjs top** (`process.env.CC_CONTROL
  = CC_CONTROL`). `assets.mjs` reads `process.env.CC_CONTROL` LAZILY; server.mjs resolves its own const. Pinning
  the env removes the / vs \\ + relative-vs-absolute divergence under a throwaway-`CC_CONTROL` test (gotcha a) —
  store + host ALWAYS agree on the control root + locks dir.
- **Endpoints (§15d):** `GET assets/list` (readIndex + rev-check rows-vs-meta → rebuild-on-drift, 15.25);
  `GET assets/file?id=` (image bytes else labeled placeholder SVG, NEVER 404, 15.31); `GET assets/get?id=`
  (full meta incl `questions[]` the derived index row omits — feeds the Answer UI; a phase-4 read, not phase 5);
  `POST assets/create` (`ccSniffKind` magic bytes PNG/JPG/GIF/WAV/MP3/OGG + UTF-8 spec → degenerate reject
  0-byte/all-alpha/decode-fail + 15.51 IHDR dim/px/byte cap BEFORE decode → `createAsset` not_ready; collision
  WARN non-blocking); `POST assets/replace {id,rev,file,confirm}` (rev-guarded; implemented/real slot forces
  not_ready + confirm; prior bytes → history via store, 15.2); `POST assets/instructions|answer|state`
  (rev-guarded 409; state REJECTS human `implemented` 400, 15.32; answer clears listed qids + ready+dirty+rev++,
  15.34); `DELETE assets {id,confirm}` (state-aware confirm → TOMBSTONE to `.trash/`, not unlink; dir+row atomic,
  15.25/15.10); `GET active` (pure read → [] absent; writer is phase 5). EVERY id-route guards 15.45 at BOTH the
  endpoint layer (`ASSET_ID_RE`) AND the store (`validateId` path-containment).
- **all-alpha degenerate reject needs a decoder** → lazy file-URL import of `game/node_modules/pngjs/lib/png.js`
  (the cosmo-canyon root has no package.json). Dim/byte caps are checked BEFORE `PNG.sync.read` so a bomb never
  reaches the decoder; decode only runs for ≤8192/side (≤~64MB alloc). Best-effort: a missing decoder degrades
  to dims-only, never crashes the endpoint.
- **Asset Browser UI = PRIMARY input surface.** Drag-drop multi-type create; per-row thumbnail (real bytes or
  placeholder), instructions autosave (debounced, rev-guarded, 409→reload that row), Ready toggle, ⇄ replace
  (confirm on implemented/real), 🗑 delete (confirm→tombstone), 💬 answer (fetch `/assets/get` → checkbox qids →
  `/assets/answer`). Composable filters (kind × state × questions) + one-click 🔥 actionable preset. Implemented
  shown read-only + *provisional* (`implementedBy` null until phase 5).
- **FOCUS-SAFE 3s poll (15.35 SHIP-BLOCKER) — the load-bearing UI invariant.** `ccRenderAssets` diff-renders by
  rev: `ccRowLocked(el) = el.contains(document.activeElement) || el.dataset.dirty==='1'` → a locked row is NEVER
  `replaceChild`'d; the reorder pass runs ONLY when NO row is locked (moving a focused DOM node would disturb
  caret/scroll). `dataset.dirty` is set on `input`, cleared on save; `data-rev` tracks the server-confirmed rev
  so a skipped-render row still saves against the right rev. Proven in-browser: focus a textarea, type, wait >3s
  (poll + autosave fire) → the node is the SAME element, half-typed text intact, still focused, autosave landed
  rev-guarded (rev 1→2, dirty cleared).

### Gotchas learned this pass
- **The derived index row (`projectRow`) OMITS the `questions[]` array** → the Answer UI can't get qids/texts
  from `/assets/list`. Added a pure-read `GET /assets/get?id=` (full meta) rather than widen the index row
  (which would change the phase-1 store contract). A read endpoint is in-scope; the index stays lean.
- **A 1×1 "test PNG" b64 off the internet is often malformed** → pngjs threw "unrecognised content at end of
  stream" → the endpoint correctly 422'd (decode-fail = degenerate). Generate real fixtures with `pngjs`
  (`PNG.sync.write`) or a browser canvas `toDataURL`, never a hand-copied 1×1.
- **A dead dummy `:8780` listener hangs `preview_screenshot`** (the dashboard iframe `src=:8780` never loads).
  DOM `preview_snapshot`/`preview_eval` still work; for a paint screenshot, `eval`-remove the iframe first.
- **The Bash tool's cwd PERSISTS between calls** — a prior `cd cosmo-canyon/game` made a later relative
  `import('./cosmo-canyon/...')` resolve under `game/` (ENOENT). `cd /c/Vibes` explicitly in each check.
- **Cross-process lost-update (15.23):** proven zero-lost over 60 interleaved writes on ONE asset (30 GUI HTTP
  rev-guarded refetch-retry POSTs + 30 external unconditional `setInstructions` via the SAME lock.mjs) → final
  `meta.rev == 1 + 60`, index rev == meta rev, meta not torn. The single server event loop serializes its OWN
  writes; the real race is server-process vs the loop/agent-process, which lock.mjs on `asset-<id>` covers.
- **Adversarial 5-lens Workflow audit (10 agents: find→verify→synthesize) caught 2 REAL high bugs the happy-path
  harness missed (3 refuted):** (1) `hAssetsReplace` decoded `b.file` via `ccDecodeB64` BEFORE a presence guard —
  `Buffer.from(String(undefined),'base64')` = 6 non-empty garbage bytes, so `!buf` was DEAD; a SPEC replace
  (sniffs by `declared` kind, not bytes) then overwrote the asset with garbage. Fix: presence/type guard on
  `b.file` BEFORE decode (mirror create) + `ccDecodeB64` rejects non-string. Lesson: `Buffer.from` NEVER
  returns null/throws — guard field PRESENCE before decoding, don't rely on a post-decode `!buf`. (2) An OPEN
  answer qbox (`.cc-qwrap[data-open]`) wasn't in `ccRowLocked`, so a rev-change/force poll could `replaceChild`
  it away, dropping a half-composed answer — a 15.35 clobber the *focused-textarea* check missed because the
  qbox holds unsaved state even when focus is OUTSIDE it. Fix: `ccRowLocked` also true for an open qbox. Lesson:
  a focus-safe poll must protect EVERY unsaved-edit surface, not just the one that happens to hold focus.

### VERIFY (all pass — pasted in the session)
Endpoint harness 30/30 (throwaway CC_CONTROL+CC_PORT; incl. the 2 audit-fix regressions): drop PNG/WAV/spec → 3 minted not_ready, correct kind,
index reflects; 0-byte/all-alpha→422, 9000×9000→413 (before write), rejects wrote NOTHING; instructions
rev-guarded rev++ + stale→409; state ready toggle + human implemented→400 + answer clears Q+dirty+rev++;
DELETE ready needs confirm → tombstoned to .trash/ (not unlink) + dir+row removed; /assets/file placeholderOnly
→ labeled placeholder (never 404); /assets/get full meta + traversal→400; 15.45 id=../../x on file/instructions/
delete/get→400; corrupt assets.json → /assets/list rebuilds on drift; /active absent→[]. Lost-update 3/3 (0 lost
over 60 cross-proc writes). UI in-browser: Asset Browser + Active-tasks + filters + actionable preset render;
focus-safe poll (node not replaced, text intact, autosave rev-guarded); answer flow clears Q + re-arms ready+
dirty; Ready toggle; drag-drop canvas-PNG create + thumbnail serves real bytes; no console errors. Phase-3
regress 10/10 (single-instance exit 1, boot serves /status+ui, 15.44/15.45/15.51/15.52, status.mode serial).
Regression: game `npm run gate` GREEN; `sense` trigger unchanged `{mode:blocked,latchKey:cc-0009}`; config serial
N=1.

STOP point: STEP-4 done + verified. Do NOT start §15g phase 5 (reconcileAssets/projectAssetToBead/ask/
active.json WRITER/completion predicate/asset-scan). Still OUT OF SCOPE: derive/graders/parse-instructions
(phase 6); spec-authority cutover (phase 7); FLIPPING config to parallel + dispatch/merge/single-committer
(phase 8).

---

## 2026-07-01 — STEP-3 (§15g phase 3) STANDALONE-APP EXTRACTION LANDED (2 cosmo commits + 1 launcher commit + docs)

MOVE + wire, NOT new features. The Launcher's cosmo control surface moved OUT of `D:\Ag\launcher` INTO the
cosmo-canyon repo as a standalone app; the Launcher keeps only a launch-or-open button. bookkeep stays SOLE
gate/commit/revert authority (untouched); config read for display only (toggle OFF, no dispatch). Commits
`cosmo-canyon`: 873ea3b (server.mjs), 8c070f6 (ui/index.html); `D:\Ag`: 63d5e24 (launcher). Did NOT build the
Asset Browser (phase 4), flip concurrency (phase 8), or touch dispatch/scanner/supervisor.

- **`server.mjs` — Node built-in `http`, `:7788`, single-instance.** The cosmo-canyon root has NO package.json,
  so the host uses only Node builtins (global `fetch` needs Node ≥18) + a relative `./orchestrator/lock.mjs`
  import (same-drive, in-package — the Launcher needed a `file:///C:/…` URL only because it was cross-drive
  D:→C:). Serves `ui/index.html` + the snapshot PNG, hosts ALL `/api/cosmocanyon/*` routes (start/stop/status/
  backlog/suggestions/completions/agent/focus/snapshot/aux/gdd/assets/rollback), spawns/kills the DETACHED
  `supervisor.mjs`, owns `ccEnsureVite(:8780)` + the auto-snapshot cadence. A tiny hand-rolled router + a
  capped JSON body reader replace express.
- **Every §15i guard carried WITH the moved code:** 15.23 (control RMW via the shared `lock.mjs` — backlog/
  suggestions→`backlog`, agent→`agent`, GDD commit→`git-tree` **replacing the old `for(i<3)` index.lock retry**;
  focus is a blind atomic-rename, no lock per §6); 15.44 (optional `acceptanceCmd` allowlisted to
  `node accept/<id>.ts`, mirrors bookkeep's `ACCEPT_CMD_RE`); 15.45 (client id charset `^[A-Za-z0-9_-]+$` rejects
  `../../x`; upload key charset + `path.relative` containment under `assets/source/`); 15.51 (upload parses the
  PNG **IHDR width/height at bytes 16..24** — reject >8192/side or >16M px BEFORE any decode — + decoded byte cap
  + derive child `timeout`+`ccDeriving` single-in-flight); 15.52 (GDD ingest streams `r.body` + aborts past
  ~2MB, Content-Length fast-path — a NEW cap the launcher lacked).
- **Single-instance = TWO guards.** (1) `server.listen(PORT)` → `EADDRINUSE` on the `error` event → exit 1;
  (2) a stale-breakable `control/.cc-host.lock` pidfile (pid+kind+started, gitignored) pre-checked before
  listen. Either refuses a 2nd `node server.mjs` (proven both ways independently).
- **Launcher shrinks to launch-or-open.** `server.js`: deleted the whole cosmo section + the `lock.mjs` import
  + the listen-block cosmo intervals; added `POST /api/cosmocanyon/launch` (spawn detached `server.mjs`).
  `index.html`: deleted `#view-cosmocanyon` markup + the `ccApi`/`cc*`/poll script + the `switchMainTab` cosmo
  branch; the nav button now calls `openCosmoCanyon()` (probe :7788 → open-if-up / POST /launch spawn-if-down →
  poll → open). Grep-clean: no dangling cosmo route/markup left in `D:\Ag`.

### Gotchas learned this pass
- **Dual-stack bind is load-bearing.** `server.listen(PORT, "127.0.0.1", …)` is UNREACHABLE via `localhost:7788`
  on Windows because `localhost` resolves to `::1` (IPv6) first — the browser + the Launcher probe both use
  `localhost`, so the probe silently failed (server up, `fetch` refused). Fix: `server.listen(PORT)` with NO
  explicit host (dual-stack, same as the Launcher's own `app.listen`). Caught only by an end-to-end launch-or-open
  test that used `localhost` (the guard harness used explicit `127.0.0.1` and passed — a false green).
- **Env test seams beat perturbing live state.** `server.mjs` is ROOT-parameterized via `CC_CONTROL` (+ `CC_PORT`,
  `CC_SUPERVISOR`) exactly like the STEP-1/2 scripts, so the whole verify runs against a throwaway control plane
  on a throwaway port with a stub supervisor — single-instance + the 60-write lost-update stress + cc-start/stop
  never touch the live backlog. The boot is guarded (`process.argv[1] === this file`) so a unit test can `import`
  the module for its exported `ccFetchCapped` (15.52) WITHOUT binding the port.
- **The 15.23 lost-update test needs a SECOND process.** Within one server process the RMW is already serialized
  by the single-threaded event loop, so a single-process hammer is a false green — the real race is server-write
  vs the loop/agent process. The harness spawns an external writer doing 30 locked appends to `backlog.json` via
  the SAME `lock.mjs`+`backlog` lock WHILE 30 concurrent HTTP POSTs land → asserts all 60 survive (0 lost).
- **The full autonomous work tick was DELIBERATELY not auto-run.** cc-start→detached-supervisor→status.alive +
  the banner data + `supervisor --reconcile-only` were proven safe (zero tokens, tree untouched). A real
  `--ticks 1` claude-p tick would COMMIT game code on the shared branch — left for a human-supervised run.

### VERIFY (all pass — pasted in the session)
Server harness 10/10: boot serves /status + ui; single-instance refuses (both `.cc-host.lock` AND EADDRINUSE,
exit 1); 15.45 `id=../../x`→400; 15.44 metachar `acceptanceCmd`→400 + allowlisted accepted; 15.51 9000×9000
PNG→413 (before write); 15.52 >2MB fetch aborted + small passes; 15.23 60 interleaved backlog writes lose 0.
cc-start/stop 4/4: status.supervisor.alive flips true/false, banner data (pid/started/usage N/cap) present.
Launch-or-open 4/4: :7788 down→spawn brings it up→probe-up→open; cleanup down. Real `supervisor --reconcile-only`:
branch ok / mutex ok / reconcile done / exit 0 / tree clean. Regression: game `npm run gate` GREEN; `sense.mjs`
trigger unchanged `{mode:blocked,latchKey:cc-0009}`; config still serial; `D:\Ag` grep-clean of cosmo routes/markup.

STOP point: STEP-3 done + verified. Do NOT start §15g phase 4 (Asset Browser card + asset CRUD endpoints/
filters/active-tasks/Popout). Still OUT OF SCOPE: reconcileAssets/projectAssetToBead/ask/active.json/completion
predicate/asset-scan (phase 5); derive/graders/parse-instructions (phase 6); spec-authority cutover (phase 7);
FLIPPING config to parallel + dispatch/merge/single-committer (phase 8).

---

## 2026-07-01 — STEP-2 (§15g phase 2) parallel-safe claim/schedule/worktree CORE LANDED (7 commits, each verified)

CONTRACTS ONLY, concurrency toggle **OFF** (config default serial, N=1). New deterministic libraries +
a surgical BEHAVIOR-PRESERVING bookkeep retrofit; NO dispatch, NO endpoints, NO scanner, toggle NOT flipped
(phase 8). bookkeep stays SOLE gate/commit/revert authority. Commits `cosmo-canyon`: 1f53be6 (config),
c98f216 (schedule), 3a9f0d4 (claim), 95118f5 (worktree), 895ebec (bookkeep-15.7), 3aeab82 (reconcile),
83e3e9a (audit-fix + README). Built ON step-0/0b/0c + step-1 (lock.acquireOrdered, assets.mjs, spec-core).

- **`config.mjs` — the §15c-2 toggle reader.** `readConfig()` normalizes `control/config.json`
  `{concurrency:{mode,maxConcurrency,isolation,worktreeRoot,perAgentTimeoutMin,heavyCostReserve,heartbeatSec}}`;
  absent/unparseable/invalid → `SERIAL_DEFAULTS` (N=1). Each field falls back individually (a partly-broken
  config can NEVER widen concurrency past what it validly declares); `mode==="serial"` FORCES maxConcurrency=1.
  `config.json` is COMMITTED (§15.46 `!control/config.json` negation — verified via `git check-ignore`). ROOT
  parameterized via `CC_CONTROL` (scratch-testable). Added pure `lock.orderedLockNames(names)`/`lockRank(name)`
  (Invariant-L order WITHOUT acquiring) so schedule emits ordered lock sets + tests assert order-compliance.
- **`schedule.mjs` — deterministic top-of-cycle PLANNER (PURE, no fs/git/dispatch).** `resolveFiles(item)`:
  declared `files[]` (SRC only, 15.7) → owned tokens; else kind-inferred (image→`assets/source/<key>.*`+manifest
  LOCK; audio→`assets/audio/<key>.*`+audio-manifest LOCK); spec/unknown empty → GLOBAL-EXCLUSIVE. **manifest/
  audio-manifest are MERGE LOCKS in the item's lock SET, NOT overlap tokens** — else all image assets would
  falsely serialize (two disjoint images must land concurrently, phase-8 goal). `slots=clamp(min(maxConcurrency
  -active, capRemaining))` computed INSIDE schedule right before dispatch (15.21, no cycle-top overshoot).
  Tier-weighted cap (light1/heavy3/structural5); heavy/structural needs `capRemaining>=heavyCostReserve`.
  Overlapping-files SERIALIZED by construction (deferred `files-conflict`, priority-aged). ALL agy → ONE serial
  lane (15.42); only claude parallel-eligible; an in-flight agy claim keeps the lane taken.
- **`claim.mjs` — atomic per-asset CLAIM = the tick ANCHOR.** `baseSha/beadId/worktree` live IN the claim
  (`control/claims/<id>.claim.json`, gitignored), NOT a singleton `.tick.json` (15.26). `claim()` under
  `acquireOrdered(['active','claims'])`; disjointness RE-CHECK vs the LIVE set INSIDE the lock = the TOCTOU
  linearization point (proven exactly-one under a real 2-process race). Staleness = HEARTBEAT (missed ~3×
  heartbeatSec AND exceeds perAgentTimeout → `staleMs=max(3·hb, timeout+hb)`), NOT the 5-min control TTL
  (§15.27). pid-reuse guarded by `startToken` = OS process CreationDate ticks (PowerShell, null→don't falsely
  kill), fallback `rnd:<hex>` nonce.
- **`worktree.mjs` — EXPLICIT-PATH-ONLY lifecycle (15.43).** create `C:/Vibes-cc-wt/<id>` `--detach` at
  baseSha; `assertExplicitWorktreePath` refuses any path not a DIRECT child of the configured root; remove by
  explicit path only, on failure delete ONLY that id's `.git/worktrees/<name>` admin dir — **NEVER a bare `git
  worktree prune`** (grep-proven zero code invocations). `assertWorktreeToplevel` (show-toplevel==the tree)
  before any destructive op. git via argv-array (shell:false).
- **bookkeep 15.7 retrofit — ADDITIVE + BYTE-FOR-BYTE serial.** `--tick <path>` overrides the anchor source
  (default `control/.tick.json`; parallel passes the CLAIM path — same `{baseSha,beadId,worktree}` shape).
  Mutable `GIT_CWD` (default REPO) is the tree EVERY git op runs in; `EXPECTED_TREE=tick.worktree||REPO`,
  `IN_WT=EXPECTED_TREE!==REPO`. `assertToplevel` compares show-toplevel to EXPECTED_TREE (==REPO in serial →
  identical to the old `top!==REPO`) AND, when IN_WT, requires HEAD detached — mismatch aborts non-zero BEFORE
  any reset/checkout/clean. Serial `.tick.json` has NO worktree → GIT_CWD=REPO, IN_WT=false → git surface
  unchanged. Every STEP-0 breaker preserved.
- **`reconcile.mjs` — MODE-CONDITIONAL (15.26).** Serial reconcile UNCHANGED (the diff is purely an additive
  `if(!isSerial()){reconcileParallel();return}` wrapper; singleton `.tick.json` reset code textually identical).
  `reconcileParallel()` (dormant while serial): `claim.stealStale` → per dead claim `worktree.remove` by
  explicit path (show-toplevel-guarded — shared branch untouched) + release + kill agy pid + prune active.json.
  NO singleton reset under N>1 (anchor is per-agent in each claim).

### Contract decisions / gotchas learned this pass
- **The claim RECORD is the schedule↔claim↔reconcile interface — it MUST carry every field a later cycle reads.**
  Adversarial audit caught: (HIGH-1) claim()'s re-check did a pure `filesLease` intersection and ignored
  `exclusive` → two global-exclusive specs (or exclusive + any asset) both won; the flag was also LOST through
  the round-trip (schedule reads `activeClaims[].exclusive` but claim never wrote it). (HIGH-2) schedule's
  cross-cycle agy-serial guard reads `activeClaims[].engine`, which the claim record didn't persist → dead code,
  two agy could run concurrently. FIX: persist `exclusive`+`engine` on the record; make claim()'s re-check
  MIRROR `schedule.overlaps()` (a live-or-new exclusive conflicts with everything). Lesson: when a pure planner
  and a stateful store BOTH judge the same predicate, they must share the SAME record fields + the SAME overlap
  fn, or the store silently disagrees with the planner under concurrency.
- **A random-nonce fallback token can WRONGLY steal a live claim if compared to a real OS token (HIGH-3).**
  `mkStartToken` returns `rnd:<hex>` when PowerShell is slow at CREATE; a later `claimIsLive` that reads the
  REAL OS ticks compares two incompatible namespaces (never equal) → declares a LIVE agent dead. FIX: skip the
  pid-reuse comparison for `rnd:` tokens (they carry no OS identity); heartbeat staleness is the correctness
  backstop. The startToken guard is an OPTIMIZATION (faster reclaim of a pid-reused claim), not a correctness
  necessity — staleness reclaims a truly-dead owner within the window regardless.
- **An adversarial worker can plant a claim file with a traversing INTERNAL assetId (MEDIUM-1).**
  `validateClaimId` only guarded the FILENAME (via `claimPath`); `readClaims()` returned the raw record, so an
  `assetId:"x/../secret"` reached reconcile's `.agy-<id>.pid` path build → arbitrary-file read → `taskkill`.
  FIX: `readClaims()` validates each record's internal assetId and DROPS malformed ones (inert file left on
  disk, never acted on); `agyPidFor` re-validates as depth. §15.41 threat model = workers run
  `--dangerously-skip-permissions`, so any control-plane file a worker writes is attacker-controlled input.
- **15.7 serial-parity proof method: A/B the retrofitted bookkeep vs the pre-retrofit one (extracted to a
  scratch dir OUTSIDE cosmo-canyon so its own revert can't wipe it) on an idle+throwaway-dirt scenario with
  `baseSha=HEAD`** → identical outcome + clean tree. Then prove the parallel asserts on REAL worktrees: a
  detached WT at HEAD (toplevel matches + detached) proceeds and reverts its own dirt in ISOLATION (main tree
  0 dirty lines); a branch WT (toplevel matches, on a branch) → detached-assert ABORTS; a toplevel-MISMATCH →
  aborts — all BEFORE any destructive op. `worktree:"C:/Vibes"` in a claim equals REPO → IN_WT=false (serial),
  so test the detached path with a real NON-REPO worktree.
- **Test-only gotchas (Windows/MSYS/this-harness):** (a) MSYS `$$` ≠ the Windows pid `process.kill` checks —
  write a "live" fixture claim from INSIDE the node test with its real `process.pid`, not via a bash-expanded
  `$$`. (b) The Bash tool COLLAPSES `\\`→`\` in heredocs/printf, so you cannot author a backslash-containing
  JSON fixture through it (invalid `\e` escapes → JSON.parse throws) — author such fixtures with the Write tool.
  (c) `node -e` needs `C:/…` paths, not MSYS `/c/…` (ENOENT); ESM import of an absolute Windows path needs
  `file:///C:/…`. (d) The repo's human-session auto-checkpoint Stop hook may sweep uncommitted edits into a
  generic commit — commit each module with its labeled `ralph step2-<id>:` message promptly, and if the hook
  beats you, `git commit --amend` the checkpoint (it captured exactly your files) to the intended message.
- **Adversarial multi-lens Workflow audit (5 lenses → verify → synthesize, 13 agents) earned its keep:** it
  surfaced 4 real N>1 contract bugs (2 HIGH exactly-one/serial-lane, 1 HIGH liveness, 1 MEDIUM traversal) that
  unit tests targeting the happy path missed, and correctly REFUTED 3 (a "deadlock" that was a subprocess-under-
  lock not a lock cycle; an "unreachable" shell-injection). All 4 fixes verified serial-parity-neutral.

### VERIFY (all pass — pasted results in the session)
SERIAL PARITY: bookkeep A/B idle+dirt IDENTICAL to pre-retrofit; `sense.mjs` trigger unchanged
`{mode:blocked,latchKey:cc-0009}`; `config` CLI serial; game `npm run gate` GREEN. SCHEDULE 25/25 (N=3 disjoint
partitions, lock sets == orderedLockNames, agy never parallel + <=1 agy + cross-cycle serial-lane, heavy respects
reserve, slots clamp, serial <=1). CLAIM 19/19 in-proc + 2-process REAL race exactly-one (same-asset +
overlapping-lease). WORKTREE 13/13 (create/remove explicit path, non-root REFUSED, toplevel-mismatch aborts,
admin-dir fallback) + grep zero bare prune. 15.7: detached-OK proceeds isolated / branch-abort / toplevel-abort
/ main tree untouched. RECONCILE 14/14 (dead GC'd by explicit path, live+shared-branch untouched, evil-claim
dropped) + serial diff additive-only + `isSerial(live config)=true` (branch dormant in prod). AUDIT-FIX 13/13.
CONFIG 12/12. Tree clean; toggle OFF.

STOP point: STEP-2 done + verified. Do NOT start §15g phase 3. Still OUT OF SCOPE: standalone server.mjs (phase
3); Asset Browser + endpoints + 15.45/15.51/15.52 (phase 4); reconcileAssets/projectAssetToBead/ask.mjs/
active.json writer/completion predicate/asset-scan (phase 5); derive/graders/parse-instructions (phase 6);
spec-authority cutover (phase 7); FLIPPING the toggle to parallel + live dispatch/merge/single-committer + full
agy-pass worktree-arming (phase 8).

---

## 2026-07-01 — STEP-1 (§15g phase 1) asset store + migration LANDED (3 commits, each verified)

FIRST phase-1 subsystem, unblocked by STEP-0/0b/0c. New DETERMINISTIC LIBRARY + one-shot migration; EDITS
NOTHING in the loop's decision path (standalone, not wired — reconcile/project/endpoints/scanner are phases
2/4/5). bookkeep stays SOLE gate/commit/revert authority (untouched). Commits `cosmo-canyon`: 4beddf8
(assets.mjs), b2a42d1 (assets-migrate.mjs), fa005f6 (migrated data: 3 meta.json + assets.json).

- **`orchestrator/assets.mjs` — the folder-per-asset store.** `control/assets/<id>/meta.json` is the ONLY
  authority; `control/assets.json` is a PURE DERIVED index. API: `mintId()`/`validateId()`, `createAsset`,
  `replaceArtifact`, `setInstructions`/`setState`/`appendQuestion`/`clearQuestions`/`answer`, `readAsset`,
  `rebuildIndex`/`readIndex` (+ a `node assets.mjs rebuild` ops CLI). ROOT parameterized via env
  **`CC_CONTROL`** (default real `control/`) so unit tests point at a scratch dir.
- **Store contract decisions (the load-bearing ones):**
  - **Derived index = PURE function of the meta set → byte-equivalent across rebuilds.** No wall-clock in
    `assets.json` (only meta rows sorted by id + counts) → a corrupt/deleted/stale index is fully repaired by
    one `rebuildIndex()`. Every mutator commits meta (authority) then rebuilds the index as a SEPARATE step
    under separate locks (`asset-<id>` rank 3, then `assets-index` rank 2) — NEVER both held → a crash between
    them leaves authority intact + index recoverable. (No nested lock needed; the self-healing index is why.)
  - **Markers-last via a REV-ADDRESSED artifact name (design choice, deviates from PLAN's literal
    `file.png`).** On REPLACE the NEW bytes go to a NEW name `file.r<rev>.<ext>`; the OLD live artifact is
    untouched until the single atomic `meta.json` rename flips `meta.file`. So a kill between the artifact
    rename and the meta rename leaves the OLD `{meta,artifact}` pair fully consistent (old artifact still
    present + hashes to old `meta.contentHash`); the new bytes are an orphan the next rebuild ignores. This is
    STRICTER than overwrite-in-place (which would leave new-bytes/old-meta). meta.file is opaque to the game
    (game reads `game/assets/manifest.json`, not this store) so the rev-name is free. Old bytes ALSO retained
    in `history/<oldHexHash>.<ext>` (§15.9); history filename uses the HEX digest (":" is illegal in Win
    filenames, so not the full `sha256:…`).
  - **contentHash computed ONCE** in the create/replace critical section, stored on meta; the scanner
    (`rebuildIndex`) NEVER re-derives it (audit 15.28). **Dirty = absolute-value RMW** per §15a: upload /
    REPLACE(`newHash != implementedContentHash`, NOT != stored → no implemented↔dirty flap 15.29) /
    instructions / clear-Q / not_ready→ready / question-append all SET true; NO store primitive CLEARS dirty
    (green-land + unsure-park live in bookkeep, phase 5). **rev monotonic**, bumped by upload/REPLACE/
    instructions/answer/state — NOT by question append/clear (15.39). `setState` rejects human `implemented`
    (derived-only 15.32); `answer` clears only the listed qids (concurrent new Q survives) + rev-guards.
  - **15.45 id-guard is enforced at the STORE layer** (`validateId`: `^a-[0-9a-z]+-[0-9a-z]{4}$` + path-
    containment under assets/claims/.trash) before any path or `asset-<id>` lock name is built — mint + every
    id-taking fn call it. Endpoint-layer guard is phase 4 (do BOTH).
- **`orchestrator/assets-migrate.mjs` — idempotent one-shot.** For each `status:placeholder` manifest key →
  ONE `placeholderOnly:true` image asset (`manifestKey:<key>`, `files:["assets/source/<key>.png"]` SRC ONLY
  per 15.7, `state:not_ready`, `file:null`/`contentHash:null`/`dirty:false` — a positioning slot, no upload).
  Keyed by `manifestKey` → re-run is a pure no-op. Ran for real: player.hero/enemy.grunt/boss.warden → 3
  assets + `assets.json`; git confirms meta.json+assets.json COMMITTABLE (STEP-0 15.46 negations hold),
  history/.trash/claims/ ignored.

### Gotchas / proofs this pass
- **Self-heal proof (the phase-1 gate, PLAN:958):** unit test (a) corrupts `assets.json` with garbage AND
  (b) crash-injects between meta-write and index-write (env `CC_ASSETS_CRASH_BEFORE_INDEX`) → after the crash
  the new asset's meta is on disk (authority) but the index is STALE (missing it); one `rebuildIndex()`
  restores it, byte-equivalent to a clean full rebuild. Markers-last proof: `CC_ASSETS_CRASH_BEFORE_META`
  throws after artifact+history but before the meta flip → meta unadvanced, old artifact intact & hashing to
  old contentHash, no torn meta. (Both env hooks are test-only.)
- **Test on a THROWAWAY control-root, never live `control/`** — set `CC_CONTROL` to a scratch dir (the store
  is ROOT-parameterized for exactly this). Test files live in the session scratchpad (OUTSIDE `C:/Vibes`) so a
  bookkeep/supervisor revert can't wipe them. Commit the modules BEFORE the real migration (revert gotcha).
- **VERIFY (all pass):** STORE unit 8/8 (mint/validate-reject-traversal · create round-trip · REPLACE
  dirty+history+rev++ · same-as-implemented no-flap · contentHash-stable · markers-last old-pair · index
  self-heal · mutators rev/dirty/guards); migration = 3 placeholderOnly + idempotent no-op re-run + committable;
  regression = `game npm run gate` green + `sense.mjs` trigger still `{mode:blocked,latchKey:cc-0009}` (store
  does not perturb the loop). README file-map updated (AGENTS.md left — store is unwired, no live fact changed).

STOP point: STEP-1 done + verified. Do NOT start §15g phase 2 (claim/schedule/worktree). Still OUT OF SCOPE:
Asset Browser + endpoints + 15.51/15.52 (phase 4); reconcile/project/ask/active.json/completion predicate +
15.13/15.50 asset-scan (phase 5); derive/graders (phase 6); wiring acquireOrdered into a live path (phase 2);
gddSha→specAuthoritySha swap (phase 7); parallel toggle (phase 8).

---

## 2026-07-01 — STEP-0c (§15g phase 0 shared-core) LANDED (5 commits, each verified)

Closes 15.22 (dual-authority-across-hosts) + adds the §15c-2 lock primitive — the LAST phase-0 item.
BEHAVIOR-PRESERVING refactor + one forward-looking lock primitive; bookkeep stays SOLE gate/commit/revert
authority (untouched); all STEP-0/0b breakers/locks/shell:false intact. Commits `cosmo-canyon`:
1fbf041 (spec-core+state), 9316758 (supervisor), 529c4f5 (sense+workflow), 3eaa38c (plan-prep), cf2969c (lock).

- **15.22 shared-core extraction — the FOUR-surface trigger/snapshot duplication is GONE.** New
  `orchestrator/spec-core.mjs` owns the PURE logic: `computeSnapshot({auditHours,cap})` +
  `computeTrigger(snap)→{mode,latchKey}` + `latchKeyFor(mode,snap)` + `specAuthoritySha()` + `wipKeywords()`.
  The 4 old copies (supervisor's private set, state's computeSnapshot, cc-loop.workflow's inline
  `decideTrigger` that DROPPED latchKey, plan-prep's 4th inline latchKey map) are deleted/re-pointed.
  - `state.mjs` keeps only the fs/git PRIMITIVES and **re-exports** `computeSnapshot`+`wipKeywords` from
    spec-core → existing `import {computeSnapshot} from "./state.mjs"` callers unchanged.
  - `supervisor.mjs` imports computeSnapshot+computeTrigger from spec-core directly.
  - `sense.mjs` **emits `snap.trigger = computeTrigger(snap)` INTO the SNAPSHOT** (raw, pre-gate);
    `cc-loop.workflow.js`'s decideTrigger is now a **passthrough** (`return snap.trigger||null`) that applies
    ONLY the host-state gates (NO_PLANNER + max-replan). This is the pattern for sharing pure .mjs logic with
    the Workflow host, which **cannot `require`/`import` a .mjs at runtime** — precompute in the .mjs the agent
    runs via Bash, emit into the JSON the script reads.
  - `plan-prep.mjs` uses `latchKeyFor(mode,snap)` (single source with computeTrigger).
- **computeTrigger is PURE (snapshot-only).** NO_PLANNER + max-replan are HOST LOOP STATE → they stay in the
  callers (supervisor main() + cc-loop.workflow.js), NOT in computeTrigger. Both hosts apply their own gate on
  the shared raw trigger.
- **specAuthoritySha() = gddSha() alias today** (phase-7 replaces ONLY its body with sha1-over-Ready-specs);
  the SNAPSHOT field name STAYS `gddSha` (do NOT rename in the emitted schema until phase 7).
- **§15c-2 lock order — `acquireOrdered(locksDir, names[])` + `releaseAll(dirs)` added to `lock.mjs`** (additive;
  acquire/release untouched; NO live caller yet — phase-2 wires it). Deadlock-free TOTAL order (rank asc, name
  tiebreak): **1 active · 2 assets-index · 3 asset-<id> · 4 claims · 5 backlog/suggestions · 6 completions/agent
  · 7 manifest/audio-manifest · 8 git-tree** (git-tree=8 pinned by PLAN:949); unknown name → rank 99. Under-spec'd
  names (suggestions, agent) sit at an equal rank to their sibling + name-tiebreak so the order stays total.
  ALL-OR-NOTHING: any mid-acquire failure releases everything already held and rethrows (no partial hold / leaked
  dir). **No name-sanitize yet** — 15.45 lands with the asset endpoints before any UNTRUSTED name reaches a lock.

### Gotchas learned this pass
- **The state↔spec-core edge is a CYCLE — but a SAFE one.** spec-core imports fs/git primitives from state.mjs;
  state.mjs re-exports computeSnapshot/wipKeywords from spec-core. Safe because spec-core references state's
  exports ONLY inside function bodies (never at module-init) → no TDZ; by the time any fn is called both modules
  are fully evaluated. Verified: import + real-backlog snapshot byte-identical, no break.
- **Use `latchKeyFor(mode,snap)`, NOT `computeTrigger(snap).latchKey`, in plan-prep.** plan-prep needs the key for
  its GIVEN fired mode, which is NOT necessarily the highest-precedence mode (and computeTrigger can return null →
  `.latchKey` crash). The prompt said `computeTrigger(snap).latchKey`; extracting the shared `latchKeyFor` map
  achieves the same single-source goal while preserving the exact per-mode behavior + being null-safe.
- **VERIFY (all pass):** parity fixtures (topup/blocked/diff/audit/latched/precedence/multi-blocked) → supervisor
  path == workflow passthrough == pre-refactor {mode,latchKey}; host gates (noPlanner + replan≥MAX → null) hold;
  `state.computeSnapshot === spec-core.computeSnapshot` (one fn); real-backlog snapshot+trigger byte-identical to
  pre-refactor ({mode:blocked,latchKey:cc-0009}); `game npm run gate` green; lock test = 2-proc mutual exclusion
  (no lost update, no deadlock) + all-or-nothing rollback.

STOP point: STEP-0c done + verified → **§15g phase 0 (STEP-0 shared-core) COMPLETE**. Do NOT start phase 1.
Still OUT OF SCOPE (later phases): gddSha→specAuthoritySha SEMANTIC swap + real spec-authority (phase 7); assets
store/migration (phase 1); claim/schedule/worktree + wiring acquireOrdered into a live path (phase 2); standalone
server.mjs (phase 3); asset endpoints + 15.45 name-sanitize/15.51/15.52 (phase 4); derived-Implemented predicate
(phase 5-6); parallel toggle (phase 8).

---

## 2026-07-01 — STEP-0b (§15g phase 0 LAST item) false-green closure LANDED (2 fixes, each verified)

§15i shortlist item 8 (rows 15.13 + 15.50) — the last "must-fix before ANY build". bookkeep stays SOLE
gate/commit/revert authority; anchored to BASE_SHA; additive; all STEP-0 breakers/locks/shell:false intact.
Commits `cosmo-canyon`: c0d0412 (15.13), ecca808 (15.50 code), 1d0f049 (15.50 migration); D:\Ag: 21a2087.

- **15.13 acceptance grader AUTO-DISCOVERY + renderOnly** (`bookkeep.mjs` runAcceptance; `plan-apply.mjs` +
  `D:\Ag\launcher\server.js` bead builders): if a bead has NO `acceptanceCmd` but `game/accept/<bead.id>.ts`
  exists, auto-discover + RUN it (was skipped → false-green: a real independent grader never ran). Runs the
  same allowlisted `node accept/<id>.ts` TSX_CLI shell:false path. `bead.id` is charset-validated
  `^[A-Za-z0-9_-]+$` before building the path (no traversal/metachars). Explicit allowlisted `acceptanceCmd`
  still wins; metachar `acceptanceCmd` still REJECTED (STEP-0 15.44). No grader → HONEST `skipped:true` with
  a `renderOnly: no grader (feel-review path)` vs `unverified: no grader, no acceptanceCmd` note. `renderOnly`
  (default false) threaded through both bead builders. **Ordering matters:** the tamper check (`accept/**` under
  PROTECTED_PREFIX) fires BEFORE `!accept.pass` in the fail chain, so even though runAcceptance runs the
  ON-DISK grader, a worker that plants/edits a grader is tamper-reverted first — auto-discovery only ever LANDS
  a grader that existed at BASE. Did NOT hard-block non-graded beads (most current beads have none → would halt
  the loop); fail-CLOSED enforcement is the §15e predicate (OUT OF SCOPE) — we only made the DATA honest.
- **15.50 recordCompletion persists projection inputs** (`bookkeep.mjs`): completions now carry
  `{sha, assetKey:null, contentHash:null, rev:null, acceptanceSkipped}`. **The sha↔commit circularity is
  fundamental** — a completion entry can NOT be committed carrying its own commit's sha. Chose approach (i):
  recordCompletion writes `sha:null`; after `commit()` returns the landing sha, `patchCompletionSha()` binds it
  under the completions lock → `entry.sha == landing commit == HEAD`. This leaves `completions.json` **patched-
  DIRTY** on a green LAND (one field), so bookkeep no longer leaves a strictly-clean tree on land; the RALPH_PASS
  **Stop hook is load-bearing** — it folds the patch into a trailing `ralph <id>:` commit (`completions.json` is
  an ALLOWED control path → not tamper). `assetKey/contentHash/rev` stay null until the §15 asset system.
  `acceptanceSkipped` (from 15.13) feeds the future fail-closed predicate. Idempotent legacy migration marked
  cc-0003/4/7/8 → `acceptanceSkipped:true` (no-acceptanceCmd → NOT-implemented), cc-0001/2 → false (real graders).

### Gotchas learned this pass
- **A git-tracked file can never contain the sha of the commit that contains it** (hash includes its own bytes).
  So "completion.sha == its landing commit" REQUIRES the sha be filled AFTER that commit — either patch-and-
  leave-dirty (chosen; Stop hook persists) or a second follow-up commit (rejected: `entry.sha != HEAD`).
- **Commit each orchestrator script BEFORE any bookkeep test** (revert = `git checkout BASE -- cosmo-canyon`
  wipes uncommitted tracked cosmo edits — same STEP-0 gotcha). Verified on throwaway temp-BASE + `reset --hard`.

STOP point: STEP-0b done + verified (4 auto-discovery scenarios + 2 land scenarios all correct; tree clean).
Do NOT start §15g phase 1. Still OUT OF SCOPE: derived-Implemented PREDICATE itself (§15e/phase5-6),
15.22 spec-core/acquireOrdered, 15.45/15.51/15.52 (ship with Asset Browser endpoints), standalone server.mjs,
Asset Browser / spec-authority / parallel toggle, phase-1 asset store + migration.

---

## 2026-07-01 — STEP-0 (§15g phase 0) security + lock hardening LANDED (6 fixes, each verified)

Must-fix-before-ANY-build set from PLAN.md §15i. All committed on `cosmo-canyon` as `ralph step0-<id>:`;
the launcher change committed to the D:\Ag repo. bookkeep stays the SOLE gate/commit/revert authority;
everything anchors to the supervisor-persisted BASE_SHA in `control/.tick.json`; additive only.

- **15.48 lock.mjs BUSY-grace** (`orchestrator/lock.mjs`): the old `mkdirSync(dir)` then separate
  `writeFileSync(owner.json)` had a TOCTOU window — a half-created lock (dir exists, owner.json absent)
  was read as stale and STOLEN → two acquirers entered. Fix: owner.json written temp+rename; on
  owner.json read/parse failure, staleness keys on the LOCK DIR's own `statSync` ctime/mtime with a 2s
  grace (NOT owner.epoch, which is unreadable in that window) — fresh dir → BUSY, old ownerless dir → stale.
  Verified: 2-proc mutual exclusion, dead-pid break, fresh half-lock treated BUSY.
- **15.46 .gitignore** (`.gitignore`): ignore the §15 runtime paths (`control/claims/`, `active.json`,
  `assets/*/history/`, `.trash/`, `.cc-host.lock`, `.authority-settle`, `.asset-scan-latch.json`,
  `.agy-*.pid`) BEFORE anything writes them, so `clean -fd cosmo-canyon` can't wipe live runtime + a restart
  doesn't see a spurious-dirty tree. Explicit negations keep `config.json`/`assets.json`/`assets/*/meta.json`
  committable. Verified via `git check-ignore`.
- **15.41 (CRITICAL) tree-wide guard + 15.49 revert** (`bookkeep.mjs`, `.claude/settings.json`):
  - Guard is now TREE-WIDE + allowlist (dropped the `cosmo-canyon/`-prefix pre-filter). `outOfScope =
    repo-wide dirty NOT under `cosmo-canyon/game/` and NOT a control file bookkeep writes` → ANY other dirty
    path (tracked OR untracked: a root `evil.ps1`, an edit to `.claude/settings.json`) is tamper-revert.
  - `.claude/settings.json` byte-identity vs BASE both pre-gate AND at land. **Use `git diff --quiet BASE --
    .claude/settings.json` (exit 1 = changed), NOT a raw `readFileSync` vs `git show` byte compare** — the
    repo has `core.autocrlf=true`, so a byte compare false-positives (CRLF worktree vs LF blob) and would
    brick every tick.
  - Revert is pathspec-scoped (15.49, protects human WIP on the shared repo): NO repo-wide `reset --hard`.
    Sequence in `fullRevert`: `git reset -q BASE -- cosmo-canyon` (unstage index so worker-staged NEW files
    become untracked) → `git checkout BASE -- cosmo-canyon` (restore modified/deleted) → `git clean -fd
    cosmo-canyon` (drop untracked; **NEVER -x** → keeps gitignored runtime + gitignored human scratch files).
    Then surgically remove/restore ONLY the detected out-of-allowlist paths (rm untracked / `checkout BASE`
    tracked) — never a blanket whole-tree clean. `assertToplevel()==C:/Vibes` before any destructive op;
    path-bearing git ops via argv-array (shell:false).
  - Repo Stop hook (`.claude/settings.json`) staging scoped: **RALPH_PASS sessions stage only
    `cosmo-canyon`** (a tick can't commit an out-of-tree evil file); human sessions keep repo-wide
    auto-checkpoint (chose RALPH_PASS-conditional over a blanket `add -A cosmo-canyon` so human auto-commit
    of non-cosmo work is NOT nerfed — every adversarial worker path always has RALPH_PASS set).
  - Verified: adversarial fixture (evil.ps1 out-of-tree; settings.json edit; worker-created NEW game file) —
    all tamper-reverted, removed/restored, tree clean, a gitignored human file left UNTOUCHED. Both would-pass
    and gate-fail paths remove the out-of-tree file.
- **15.44 shell:false** (`bookkeep.mjs`): `runGate` → `spawnSync('npm.cmd',['run','gate'],{shell:false})`
  (npm.cmd + shell:false works on Win Node 18). `runAcceptance` → REJECT any `acceptanceCmd` not matching
  `^(node|tsx) accept/<id>.ts$` (fail-closed, never free-form shell); run the .ts grader via tsx's cli
  (`node node_modules/tsx/dist/cli.mjs <grader>`, shell-free). `commit` → `git commit -F -` (message via
  stdin, argv shell:false) so `bead.title` never hits a shell (no injection / %VAR% expansion); `sanitize()`
  strips `%&|<>` backtick `$` + newlines. Verified: metachar acceptanceCmd rejected, normal grader runs,
  title with `"`/`$`/`%` commits clean (no expansion). **Follow-up:** `plan-apply.mjs`'s commit was the last
  untrusted-text-to-shell path in a committer (it interpolated the opus planner `note` into a `-m` string) —
  now also `git commit -F -` (stdin) + `sanitize()`, so NO committer runs untrusted text through a shell.
  Verified: a planner note with `$HOME`/`%PATH%`/`` `id` ``/`&` commits clean, no expansion.
- **15.47 git-tree lock** (`bookkeep.mjs`, `plan-apply.mjs`): both committers `acquire(LOCKS,'git-tree')`
  around stage+commit → no `.git/index.lock` race. Verified: 2 concurrent committers, 0 index.lock hits
  (control run dropped 1/16 without the lock). **NOTE:** ingest (server.js `for(i<3)` retry) + any future
  merge committer MUST take the same lock; delete the retry when ingest moves to the standalone server.mjs.
- **15.23 shared control lock** (`D:\Ag\launcher\server.js`, `supervisor.mjs`): server.js imports the SAME
  `orchestrator/lock.mjs` (via `file:///C:/…` URL — cross-drive absolute ESM specifiers need it) + wraps the
  lockless RMW writes (backlog POST/DELETE, suggestions accept/reject on the **'backlog'** lock — plan-apply
  writes suggestions.json under that lock; POST /agent on 'agent'). supervisor's agy-failover agent.json
  write under 'agent'. Verified: 60-write GUI-vs-loop race lost 0 (was 30 without the lock).

### Gotchas learned this pass
- **Commit each orchestrator script BEFORE any test that runs bookkeep/supervisor** — their revert now does
  `git checkout BASE -- cosmo-canyon`, which reverts uncommitted edits to tracked orchestrator files (same
  class as the old `reset --hard` gotcha). Test adversarial cases on THROWAWAY dirt only, reset to origHead
  between runs.
- **`node -e` on Windows needs `C:/…` paths, not MSYS `/c/…`** (Node treats `/c/…` as drive-root-relative →
  ENOENT). Bash builtins/git accept `/c/…`.
- **ESM `import` of an absolute Windows path fails** (`ERR_UNSUPPORTED_ESM_URL_SCHEME`) — use a
  `file:///C:/…` URL (static import literal works).
- **`git checkout BASE -- <dir>` does NOT drop new tracked/staged files** — must `git reset BASE -- <dir>`
  first (index) so they become untracked for `clean` to remove. (Caught on self-review; the initial revert
  left worker-created new files.)

STOP point: STEP-0 done + verified; do NOT start §15g phase 1 — awaiting human review. Out of scope
(later phases): 15.45 asset-id validation, spec-core.mjs extraction / acquireOrdered, standalone server.mjs,
Asset Browser, parallel toggle.
