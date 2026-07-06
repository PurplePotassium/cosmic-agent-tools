// Cosmo Canyon — thin single-flight supervisor (the loop host; SPIKE decision).
//
// Spawns ONE `claude -p < tick.md` per tick, waits for it to exit, repeats. Claude IS
// the orchestrator (every decision is the model's inside the tick); this is a dumb,
// safe repeater. Per the spike, a detached `claude -p` can't host a long loop, so the
// loop lives HERE. In Phase 4 this same logic becomes a setInterval in the Launcher
// server.js; for Phase 1 it runs standalone so we can drive + observe ticks directly.
//
// Guards (BLOCKER set): branch assert == cosmo-canyon (13.24); cross-system mutex —
// refuse if any FC/Workshop/fleet loop is alive (13.23); single-flight via
// control/.tick.json with BASE_SHA + pid persisted BEFORE spawn (13.28); per-tick
// timeout kill (13.17); consecutive-failure circuit breaker (13.39); daily tick cap.
//
// Usage: node supervisor.mjs [--ticks N] [--timeout-min 45] [--model claude-sonnet-4-6]
//                            [--cap 200] [--breaker 5] [--reconcile-only]
import { execSync, spawn } from "node:child_process";
import { readFileSync, writeFileSync, existsSync, renameSync, mkdirSync, openSync, closeSync, rmSync } from "node:fs";
import { randomBytes } from "node:crypto";
import { acquire, release } from "./lock.mjs"; // §15i 15.23 — agent.json write under the shared control lock
import { computeSnapshot, computeTrigger } from "./spec-core.mjs"; // 15.22 — shared snapshot+trigger (single source across both hosts)
import { readConfig, isSerial } from "./config.mjs"; // §15i 15.26 — mode-conditional reconcile (serial default)
import { reconcileParallel } from "./reconcile.mjs"; // §15i 15.26 — the N-agent parallel reconcile branch (dormant while serial)
import { pickForBead } from "./agent-core.mjs"; // orchestrator worker pick (operator's allowed set → task-fit model)
import { writeActive, removeActive, reconcileActive, breakerStep, isTerminal } from "./assets-core.mjs"; // §15c — active.json writer/GC + redefined breaker (15.20) + terminal-status set
import { writeKnownGood } from "./spec-compile.mjs"; // §15.5 — snapshot the last-GREEN Ready-spec authority
import { planCycle } from "./dispatch.mjs"; // §15g phase 8 — top-of-cycle plan+claim+worktree (parallel)
import { mergeGreen } from "./merge.mjs";   // §15g phase 8 — single-committer serialized merge
import { remove as wtRemove } from "./worktree.mjs"; // §15g phase 8 — GC a crashed worker's worktree by explicit path
import { releaseClaim } from "./claim.mjs";          // §15g phase 8 — release a crashed worker's claim (no attempt — infra-kill)

// §15g phase 8 — REPO/CC/BRANCH honor the CC_* env seams (same family as bookkeep/merge/worktree) so the verify
// harness can drive the FULL host loop against a THROWAWAY repo + fake workers; unset in production → the real
// C:/Vibes on branch cosmo-canyon (byte-for-byte unchanged).
const REPO = process.env.CC_REPO || "C:/Vibes";
const CC = process.env.CC_CC || `${REPO}/cosmo-canyon`;
const CONTROL = `${CC}/control`;
const LOCKS = `${CONTROL}/locks`; // §15i 15.23 — shared control-plane lock dir (same as bookkeep/plan-apply)
const LOGS = `${CC}/logs`;
const TICK_MD = `${CC}/orchestrator/tick.md`;
const TICK_PARALLEL_MD = `${CC}/orchestrator/tick.parallel.md`; // §15g phase 8 — the parallel worker recipe (gate-in-worktree, no self-commit)
const TICKJSON = `${CONTROL}/.tick.json`;
const PAUSED = `${CONTROL}/.paused`;
const AGY_PID = `${CONTROL}/.agy.pid`;
const AGENT_JSON = `${CONTROL}/agent.json`;
const AGY_COOLDOWN = `${CONTROL}/.agy-cooldown`; // §13.38 quota failover marker — drops agy from the pick WITHOUT rewriting the operator's allowed set
const AGY_STRIKES = `${CONTROL}/.agy-strikes`;
const PLAN_INPUT = `${CONTROL}/.plan-input.json`; // authority/.authority-consumed/.lastaudit/.plan-latch reads now live in spec-core (15.22)
const PLANNER_MD = `${CC}/orchestrator/planner.md`;
const BRANCH = process.env.CC_BRANCH || "cosmo-canyon";
// Match only actual rival game-dev LOOP processes (ralph.ps1 workers/lanes, refinery, a
// loop's PROMPT.md, an opus planner-prompt) — NOT a vite/npm dev server that merely logs to a
// path containing "fortcondor"/"workshop" (that false-positive wrongly refused cc-start).
const MUTEX_RE = /ralph\.ps1|refinery\.ps1|planner-prompt|[\\/]PROMPT\.md/i;
const SELF_RE = /cosmo-canyon|agy-pass\.ps1/i; // our own runner/agy must not trip the mutex

const args = process.argv.slice(2);
const arg = (n, d) => { const i = args.indexOf(`--${n}`); return i >= 0 && i + 1 < args.length ? args[i + 1] : d; };
const has = (n) => args.includes(`--${n}`);
const MAX_TICKS = Number(arg("ticks", "1e9"));
const TIMEOUT_MS = Number(arg("timeout-min", "45")) * 60 * 1000;
const MODEL = arg("model", readConfig().models.work || "claude-sonnet-4-6"); // §AUDIT-2026-07-04 — config.json {models:{work}} overrides the aging hardcoded default; explicit --model still wins
const DAILY_CAP = Number(arg("cap", "200"));
// The daily tick budget is operator-editable in the dashboard (control/config.json `tickBudget`) and read LIVE
// each cycle, so an edit takes effect at the NEXT cycle with no restart. An explicit --cap flag (dev/CLI) pins
// it and overrides the config. effectiveCap() is the single source the loop-break + snapshot cap read.
const CAP_FLAG = has("cap");
const effectiveCap = () => (CAP_FLAG ? DAILY_CAP : (readConfig().tickBudget || DAILY_CAP));
const BREAKER_N = Number(arg("breaker", "5"));
const FAILOVER_N = Number(arg("agy-failover", "2")); // agy zero-diff strikes before lane → claude (13.38)
const OPUS = arg("planner-model", readConfig().models.planner || "claude-opus-4-8"); // §AUDIT-2026-07-04 — config-overridable (see MODEL above)
const AUDIT_MS = Number(arg("audit-hours", "6")) * 3600 * 1000;
const MAX_REPLAN = Number(arg("max-replan", "2")); // consecutive planner ticks before forcing work/idle (13.31)
const STALLED_N = Number(arg("stalled", "3")); // ticks with no commit → flag .stalled for the UI (fix 6)
const NO_PLANNER = has("no-planner"); // Phase-1/2 mode: skip the planner entirely
// AUDIT FIX (step7 / §15.16): when a real authority change is DEBOUNCE-PENDING but the loop would otherwise idle-
// EXIT, poll-wait instead of exiting so the coalesced `diff` fires when the ~90s window elapses (else the change
// is dropped until a manual Start). Bounded: a persistent curation burst restarts the window each time → cap the
// total waits then idle-exit (the settle marker persists; the human's next Start fires it).
const SETTLE_POLL_MS = Number(arg("settle-poll-sec", "15")) * 1000;
const MAX_SETTLE_WAITS = Number(arg("max-settle-waits", "40"));
// HARDENING (2026-07-02, "run forever without deadlocks"): bound the two unbounded-wait vectors the serial
// loop had. GIT_TIMEOUT_MS caps every git/taskkill/WMI execSync so a stuck .git/index.lock, a credential
// prompt, or a wedged WMI query throws instead of hanging the loop with no wall-clock bound. KILL_GRACE_MS
// force-resolves a spawned tick/planner promise this long after the timeout taskkill if the child STILL
// hasn't exited (taskkill ineffective on a detached/protected tree) — else the awaited promise never
// resolves and the whole loop wedges permanently (the single most severe true-hang path).
const GIT_TIMEOUT_MS = Number(arg("git-timeout-sec", "120")) * 1000;
const KILL_GRACE_MS = Number(arg("kill-grace-sec", "30")) * 1000;
// STUCK-TICK DETECTION (2026-07-02): the flat 45-min timeout meant a hung tick cost up to 45 min to notice.
// Give each WORK tick a per-tier FLOOR timeout (fast stuck-detection) that ADAPTS upward — never past the
// --timeout-min ceiling — toward the tier's OWN rolling actuals, so a legitimately-slow tier is never
// false-killed. tickTimeoutMs = clamp(max(tierFloorMin, MULT × rolling-p90-secs), floor, TIMEOUT_MS). The
// planner keeps the full ceiling (opus is legitimately slow). Same benign-kill semantics as the hard timeout
// (no attempt bump; the breaker backstops a bead that repeatedly times out). Floors/mult are flag-tunable.
const TIER_FLOOR_MIN = { light: Number(arg("light-timeout-min", "12")), heavy: Number(arg("heavy-timeout-min", "25")), structural: Number(arg("structural-timeout-min", "45")) };
const TIMEOUT_MULT = Number(arg("timeout-mult", "4"));   // timeout ≈ this × the tier's rolling p90 actual
const STAT_MIN_SAMPLES = 5, STAT_MAX_SAMPLES = 20;        // need ≥5 samples to adapt; keep the last 20 per tier
const TICK_STATS = `${CONTROL}/.tick-stats.json`;         // gitignored rolling per-tier tick-duration store

const git = (cmd) => execSync(`git -C "${REPO}" ${cmd}`, { encoding: "utf8", maxBuffer: 64 * 1024 * 1024, timeout: GIT_TIMEOUT_MS });
const gitQuiet = (cmd) => { try { return git(cmd); } catch (e) { return e.stdout || ""; } };
// §SPLIT — the GAME repo (own .git) runners. baseSha (C:/Vibes) anchors control; gameBaseSha (game repo) anchors the
// game gate/revert/commit. GAME honors CC_GAME (harness) else ${CC}/game.
const GAME = process.env.CC_GAME || `${CC}/game`;
const ggit = (cmd) => execSync(`git -C "${GAME}" ${cmd}`, { encoding: "utf8", maxBuffer: 64 * 1024 * 1024, timeout: GIT_TIMEOUT_MS });
const ggitQuiet = (cmd) => { try { return ggit(cmd); } catch (e) { return e.stdout || ""; } };
const gameHead = () => ggitQuiet("rev-parse HEAD").trim();
const readJson = (p, d) => { try { return JSON.parse(readFileSync(p, "utf8")); } catch { return d; } };
const atomicWrite = (p, s) => { const t = `${p}.tmp`; writeFileSync(t, s); renameSync(t, p); };
const writeJson = (p, o) => atomicWrite(p, JSON.stringify(o, null, 2) + "\n");
const nowIso = () => new Date().toISOString();
const log = (m) => console.log(`[sup ${new Date().toISOString().slice(11, 19)}] ${m}`);

// ── stuck-tick timeout: per-tier floor that adapts to rolling actuals (see the const block above) ──
const readStats = () => readJson(TICK_STATS, {});
function recordStat(tier, secs) { // record a COMPLETED tick's wall-duration (never a timeout) for the estimate
  if (!tier || !(secs > 0)) return;
  const s = readStats(); const arr = Array.isArray(s[tier]) ? s[tier] : [];
  arr.push(Math.round(secs)); while (arr.length > STAT_MAX_SAMPLES) arr.shift();
  s[tier] = arr; try { writeJson(TICK_STATS, s); } catch {}
}
// MEDIAN (p50), not p90 — robust to one slow outlier. Using p90 would let a single slow tick balloon the
// tier's timeout and then MISS a stuck tick under the loosened bound; median keeps the floor binding for a
// normal-speed tier (fast stuck-detection) and only loosens when the tier is BROADLY slow (real adaptivity).
function pctl(arr, q) { if (!arr || !arr.length) return 0; const a = [...arr].sort((x, y) => x - y); return a[Math.min(a.length - 1, Math.floor(a.length * q))]; }
function tickTimeoutMs(bead) { // clamp(max(tierFloor, MULT × rolling-median), floor, TIMEOUT_MS); cold start → floor
  const tier = (bead && bead.tier) || "structural";
  const floorMs = (TIER_FLOOR_MIN[tier] || TIMEOUT_MS / 60000) * 60 * 1000;
  const hist = readStats()[tier] || [];
  const adaptMs = hist.length >= STAT_MIN_SAMPLES ? TIMEOUT_MULT * pctl(hist, 0.5) * 1000 : 0;
  return Math.min(TIMEOUT_MS, Math.max(floorMs, adaptMs));
}
function alive(pid) { if (!pid || pid <= 0) return false; try { process.kill(pid, 0); return true; } catch (e) { return e.code === "EPERM"; } }
// HARDENING (2026-07-02, review-driven): distinguish a GENUINELY-live tick/planner child (our own process,
// possibly an orphan that outlived a crashed supervisor) from an unrelated OS-RECYCLED pid that merely happens
// to be alive(). reconcile() uses this so it NEVER resets the game tree under a live writer (corruption) AND
// never wedges startup forever on a recycled pid. Checks the process command line for our tick/planner recipe
// markers via WMI. Conservative: any query failure or unreadable cmdline → return true (treat as ours → refuse,
// the safe side — a spurious refuse is operator-recoverable; a reset under a live tick corrupts the tree).
function pidIsOurTick(pid) {
  if (!pid || pid <= 0) return false;
  try {
    const out = execSync(`powershell.exe -NoProfile -Command "(Get-CimInstance Win32_Process -Filter 'ProcessId=${Number(pid)}').CommandLine"`, { encoding: "utf8", timeout: GIT_TIMEOUT_MS });
    const cl = (out || "").trim();
    if (!cl) return true; // alive but no readable cmdline → cannot rule it out → conservatively ours (refuse)
    return /tick\.md|planner\.md|tick\.parallel\.md|claude\s+-p/i.test(cl);
  } catch { return true; } // WMI query failed → conservatively ours (refuse; never reset under a maybe-live tick)
}

// §SPLIT — restore the GAME repo to <ref> (whole-repo reset --hard + clean, NEVER -x). The game is its own repo now,
// so a concurrent GUI upload under control/assets (in C:/Vibes) + human WIP elsewhere are untouched by definition.
function resetGameTo(ref) {
  ggitQuiet(`reset --hard ${ref}`);
  ggitQuiet("clean -fd"); // drop untracked worker files (NEVER -x → keeps gitignored node_modules/dist/derived)
}

function fail(msg) { log(`REFUSE: ${msg}`); process.exit(1); }

// kill an orphaned agy (its own console, NOT in the tick's child tree — §13.16) + clear its pid file
function killAgy(reason) {
  if (!existsSync(AGY_PID)) return;
  const apid = Number(readFileSync(AGY_PID, "utf8").trim());
  if (alive(apid)) { log(`${reason}: taskkill agy pid ${apid} /T (13.16)`); try { execSync(`taskkill /PID ${apid} /T /F`, { timeout: GIT_TIMEOUT_MS }); } catch {} }
  try { rmSync(AGY_PID, { force: true }); } catch {}
}

// ── preflight guards ──────────────────────────────────────────────────────────
function assertBranch() {
  const b = gitQuiet("rev-parse --abbrev-ref HEAD").trim();
  if (b !== BRANCH) fail(`branch is '${b}', expected '${BRANCH}' (13.24) — checkout ${BRANCH} first`);
  log(`branch ok: ${b}`);
}

function assertMutex() {
  // refuse if any rival game-dev loop (FC/Workshop/fleet) is alive (13.23). Enumerate
  // all process command lines and JS-filter by MUTEX_RE. Our own node supervisor and
  // the claude tick (`...tick.md | claude -p`) do NOT match MUTEX_RE.
  let out = "";
  try {
    out = execSync(
      'powershell.exe -NoProfile -Command "Get-CimInstance Win32_Process | Select-Object -ExpandProperty CommandLine"',
      { encoding: "utf8", maxBuffer: 16 * 1024 * 1024, timeout: GIT_TIMEOUT_MS } // hardening: a wedged WMI query must not hang preflight forever
    );
  } catch {}
  const hits = out.split("\n").map((l) => l.trim()).filter((l) => l && MUTEX_RE.test(l) && !SELF_RE.test(l));
  if (hits.length) fail(`rival loop alive (13.23):\n  ${hits.slice(0, 3).map((h) => h.slice(0, 120)).join("\n  ")}\n  stop FC/Workshop/fleet before running Cosmo Canyon`);
  log("mutex ok: no rival FC/Workshop/fleet loop");
}

// reconcile a killed/crashed prior tick from disk (13.28). §15i 15.26 — MODE-CONDITIONAL: serial (default,
// N=1) keeps the exact singleton `.tick.json` reset below, BYTE-FOR-BYTE; parallel (N>1) has NO singleton
// anchor to reset (it lives per-agent in each claim) → route to reconcileParallel and RETURN before the
// serial path. isSerial() reads control/config.json (default serial) so today's runs are unchanged.
function reconcile() {
  // §AUDIT-2026-07-03 — run reconcileParallel in BOTH modes. A crashed parallel cycle followed by an operator
  // flip back to mode:serial used to skip this GC forever → orphaned claims (their assets deferred every cycle
  // until the TTL steal) + leaked worktrees on disk. No-op when claims/ is empty, so serial boots are unchanged.
  const r = reconcileParallel();
  if (r.reconciled.length || r.pruned) log(`parallel reconcile (15.26): GC'd ${r.reconciled.length} dead claim(s) [${r.reconciled.join(", ")}], pruned ${r.pruned} active row(s)`);
  if (!isSerial(readConfig())) {
    // (audit MED) a SERIAL-AGY tick runs via spawnTick even under parallel mode → it writes a singleton .tick.json
    // + dirties the MAIN game tree. A crash mid-agy-tick leaves both behind; reconcileParallel touches NEITHER. So
    // ALSO reconcile a DEAD .tick.json here (never fail on it — single-flight is per-claim under N>1) and FALL
    // THROUGH to the shared killAgy + defensive game-clean below (mirrors preflight.mjs, which never had this gap).
    if (existsSync(TICKJSON)) {
      const t = readJson(TICKJSON, null);
      if (t && !alive(t.pid) && (t.gameBaseSha || t.baseSha)) { const gb = t.gameBaseSha || "HEAD"; log(`reconciling killed serial-agy tick → restore game to ${String(gb).slice(0, 8)}`); resetGameTo(gb); }
      if (!(t && alive(t.pid))) { rmSync(TICKJSON, { force: true }); try { reconcileActive(); } catch {} }
    }
    // NOTE: no early return — the killAgy + defensive `status --porcelain -- cosmo-canyon/game` clean below run in
    // BOTH modes so a crashed agy tick's abandoned edits can never leak into the next parallel cycle's merge.
  } else if (existsSync(TICKJSON)) {
    const t = readJson(TICKJSON, null);
    if (t && alive(t.pid)) {
      // HARDENING (2026-07-02, review-hardened): the pid is alive — but is it GENUINELY our in-flight/orphaned
      // tick, or an unrelated OS-RECYCLED pid (alive() is also true on EPERM)? The two failure modes pull opposite
      // ways: refusing UNCONDITIONALLY wedges every restart forever on a recycled pid; blindly RESETTING would
      // clobber the game tree UNDER a genuinely-live tick that outlived a crashed supervisor (no job-object binds
      // the child to the parent on Windows → it keeps writing game/). So DISAMBIGUATE by command line: if the pid
      // IS our tick → REFUSE (never reset under a live writer — the safe side); if it is NOT (recycled/unrelated)
      // → fall through and reconcile it as a killed tick. pidIsOurTick is conservative (uncertain → treat as ours).
      if (pidIsOurTick(t.pid)) fail(`a tick (pid ${t.pid}) is genuinely still in flight — single-flight (13.5/13.28); if you are CERTAIN it is dead, delete control/.tick.json`);
      log(`stale .tick.json: pid ${t.pid} is alive but is NOT our tick (recycled/unrelated pid) → reconcile as killed`);
    }
    if (t && (t.gameBaseSha || t.baseSha)) {
      const gb = t.gameBaseSha || "HEAD"; // §SPLIT — restore the GAME repo to the killed tick's game base
      log(`reconciling killed tick (pid ${t.pid}) → restore game to ${String(gb).slice(0, 8)}`);
      resetGameTo(gb);
    }
    rmSync(TICKJSON, { force: true });
    try { reconcileActive(); } catch {} // §15c GC the killed tick's active.json row
  }
  // orphaned agy from a killed tick (§13.16): agy runs in its own console, not the tick's child tree,
  // so a tick-tree kill can leave it alive. The owning tick is gone here → kill it.
  killAgy("orphaned agy at reconcile");
  // ensure a clean tree before looping (defensive). Clean scoped to /game so a not-yet-committed GUI upload
  // under control/assets is not deleted at boot (§15c-2); control/assets is an allowed dirty surface.
  const gdirty = ggitQuiet("status --porcelain").trim(); // §SPLIT — the game's own repo status
  if (gdirty) {
    // cc-safety — leftover manual WIP: STASH (recoverable) before the reset --hard so hand-edits are never
    // silently destroyed at boot (this wipe cost a full manual game rebuild 2026-07-04). Stash-fail → no regression.
    log("game tree dirty at start → STASH (cc-safety, recoverable via `git -C cosmo-canyon/game stash list`) then restore game to HEAD");
    ggitQuiet('stash push -u -m "cc-safety: dirty game tree at loop boot — recover via git -C cosmo-canyon/game stash list"');
    resetGameTo("HEAD");
  }
}

// ── daily cap (.usage-YYYYMMDD.json) ────────────────────────────────────────────
function usagePath() { return `${CONTROL}/.usage-${nowIso().slice(0, 10).replace(/-/g, "")}.json`; }
function bumpUsage() { const p = usagePath(); const u = readJson(p, { date: nowIso().slice(0, 10), ticks: 0 }); u.ticks++; writeJson(p, u); return u.ticks; }
function usageToday() { return readJson(usagePath(), { ticks: 0 }).ticks; }

// ── bead selection (head ready bead) ────────────────────────────────────────────
function headReadyBead() {
  const backlog = readJson(`${CONTROL}/backlog.json`, []);
  return backlog.find((b) => !isTerminal(b.status)) || null; // §15c isTerminal excludes parked/superseded too (no re-dispatch spin)
}

// ── spawn one tick, wait, return outcome ────────────────────────────────────────
function spawnTick(bead, baseSha, gameBaseSha, model) {
  return new Promise((resolve) => {
    const startEpoch = Date.now();
    const runToken = randomBytes(6).toString("hex"); // ties .tick.json ↔ the active.json row
    writeJson(TICKJSON, { pid: null, startEpoch, baseSha, gameBaseSha, beadId: bead.id, runToken });
    // §15c — write the in-flight task row at DISPATCH (supervisor host); removed on terminal by bookkeep + the
    // post-tick cleanup below; a killed tick is GC'd by reconcileActive next sense.
    try { writeActive({ runToken, beadId: bead.id, assetId: bead.assetId || null, kind: "work", engine: bead.engine || null, tier: bead.tier || null, title: bead.title || bead.id, baseSha, startEpoch, pid: null }); } catch {}
    const logFile = `${LOGS}/tick-${bead.id}-${startEpoch}.log`;
    const fd = openSync(logFile, "a");
    const psCmd = `Get-Content -Raw -LiteralPath '${TICK_MD}' | & claude -p --model ${model || MODEL} --dangerously-skip-permissions`;
    const child = spawn("powershell.exe", ["-NoProfile", "-Command", psCmd], {
      cwd: CC,
      env: { ...process.env, RALPH_PASS: bead.id, CLAUDE_PROJECT_DIR: REPO },
      stdio: ["ignore", fd, fd],
      windowsHide: true, // no popup console window per tick (matches hStart's supervisor spawn + FC/Workshop -WindowStyle Hidden)
    });
    writeJson(TICKJSON, { pid: child.pid, startEpoch, baseSha, gameBaseSha, beadId: bead.id, runToken });
    log(`tick spawned pid ${child.pid} bead ${bead.id} base ${baseSha.slice(0, 8)} (log ${logFile.split("/").pop()})`);
    let timedOut = false, hardTimer = null;
    const toMs = tickTimeoutMs(bead); // per-tier stuck-detection timeout (floor + rolling-actuals adapt, ≤ TIMEOUT_MS)
    const timer = setTimeout(() => {
      timedOut = true;
      log(`tick TIMEOUT after ${Math.round(toMs / 60000)}m (tier ${bead.tier || "?"}) → taskkill tree pid ${child.pid} + agy (13.17/13.16)`);
      try { execSync(`taskkill /PID ${child.pid} /T /F`, { timeout: GIT_TIMEOUT_MS }); } catch {}
      killAgy("tick timeout");
      // HARDENING (2026-07-02): if taskkill fails to reap a detached/protected tree, 'exit' never fires and
      // this await hangs FOREVER (the loop wedges). Force-resolve after a grace so the supervisor reconciles
      // the tick to base + advances. resolve() is idempotent → a later real 'exit' is a harmless no-op.
      hardTimer = setTimeout(() => { log(`tick child did not exit ${KILL_GRACE_MS / 1000}s after taskkill → hard-resolve (avoid permanent await)`); resolve({ code: -1, timedOut: true, killFailed: true, pid: child.pid, secs: Math.round((Date.now() - startEpoch) / 1000) }); }, KILL_GRACE_MS);
    }, toMs);
    child.on("exit", (code) => { clearTimeout(timer); if (hardTimer) clearTimeout(hardTimer); try { closeSync(fd); } catch {} resolve({ code, timedOut, secs: Math.round((Date.now() - startEpoch) / 1000) }); });
    child.on("error", (e) => { clearTimeout(timer); if (hardTimer) clearTimeout(hardTimer); try { closeSync(fd); } catch {} resolve({ code: -1, timedOut, error: String(e), secs: 0 }); });
  });
}

// §15g phase 8 — spawn ONE parallel worker into its isolated worktree. The Stop hook is neutralized
// (CC_WORKER_NO_COMMIT=1) and CLAUDE_PROJECT_DIR points at the worktree so it can NEVER commit into the main
// tree (15.19); CC_TICK is the claim anchor + CC_GAME the worktree game (bookkeep --gate-only retargets to it).
// CC_WORKER_CMD is a TEST seam: when set, run `node <that>` as a fake worker instead of `claude -p` (drives the
// host loop end-to-end in the verify harness without a live model). Resolves with the child's exit info.
function spawnWorker(claim) {
  return new Promise((resolve) => {
    const startEpoch = Date.now();
    // §SPLIT — the worktree IS the game repo, so the worker's cwd + CC_GAME are the worktree ROOT (no /cosmo-canyon/game);
    // CLAUDE_PROJECT_DIR points at it too so the neutralized Stop hook (CC_WORKER_NO_COMMIT=1) can never reach C:/Vibes.
    const env = { ...process.env, RALPH_PASS: claim.beadId, CC_WORKER_NO_COMMIT: "1", CLAUDE_PROJECT_DIR: claim.worktree, CC_TICK: claim.claimPath, CC_GAME: claim.worktree, CC_WORKTREE: claim.worktree };
    const logFile = `${LOGS}/worker-${claim.id}-${startEpoch}.log`;
    const fd = openSync(logFile, "a");
    let child;
    if (process.env.CC_WORKER_CMD) {
      child = spawn(process.execPath, [process.env.CC_WORKER_CMD], { cwd: claim.worktree, env, stdio: ["ignore", fd, fd], windowsHide: true });
    } else {
      const psCmd = `Get-Content -Raw -LiteralPath '${TICK_PARALLEL_MD}' | & claude -p --model ${MODEL} --dangerously-skip-permissions`;
      child = spawn("powershell.exe", ["-NoProfile", "-Command", psCmd], { cwd: claim.worktree, env, stdio: ["ignore", fd, fd], windowsHide: true });
    }
    let timedOut = false, hardTimer = null;
    const timer = setTimeout(() => { timedOut = true; log(`worker TIMEOUT ${claim.beadId} → taskkill ${child.pid}`); try { execSync(`taskkill /PID ${child.pid} /T /F`, { timeout: GIT_TIMEOUT_MS }); } catch {}
      hardTimer = setTimeout(() => { log(`worker ${claim.beadId} did not exit ${KILL_GRACE_MS / 1000}s after taskkill → hard-resolve (avoid permanent await)`); resolve({ id: claim.id, beadId: claim.beadId, code: -1, timedOut: true, killFailed: true, pid: child.pid, secs: Math.round((Date.now() - startEpoch) / 1000) }); }, KILL_GRACE_MS); }, TIMEOUT_MS);
    child.on("exit", (code) => { clearTimeout(timer); if (hardTimer) clearTimeout(hardTimer); try { closeSync(fd); } catch {} resolve({ id: claim.id, beadId: claim.beadId, code, timedOut, secs: Math.round((Date.now() - startEpoch) / 1000) }); });
    child.on("error", (e) => { clearTimeout(timer); if (hardTimer) clearTimeout(hardTimer); try { closeSync(fd); } catch {} resolve({ id: claim.id, beadId: claim.beadId, code: -1, error: String(e), secs: 0 }); });
  });
}

// §15g phase 8 — ONE parallel WORK cycle: plan+claim+worktree (dispatch) → spawn N workers CONCURRENTLY (each
// gates in its worktree, commits nothing) → single-committer MERGE lands each green onto HEAD serialized → sweep
// any crashed worker (no gate marker) by explicit-path worktree removal (infra-kill: NO attempt bump, retry next
// cycle). Returns {kind: parallel|serialAgy|empty, ...}. The PLANNER stays serial (main() runs it, unchanged).
async function runParallelCycle(baseSha) {
  const plan = planCycle({ baseSha, cap: effectiveCap(), hostPid: process.pid });
  if (!plan.parallel.length) return plan.serialAgy ? { kind: "serialAgy", bead: plan.serialAgy } : { kind: "empty", deferred: plan.deferred };
  log(`── parallel cycle: dispatch ${plan.parallel.length} worker(s) [${plan.parallel.map((p) => p.beadId).join(", ")}] base ${baseSha.slice(0, 8)} (deferred ${plan.deferred.length})`);
  // §AUDIT-2026-07-03 — stagger spawns (was one Promise.all burst): at wide N, simultaneous `claude -p`
  // sessions arrive at the API as a single burst and trip org rate limits. 400ms apart costs ≤N×0.4s total.
  const SPAWN_STAGGER_MS = 400;
  const results = await Promise.all(plan.parallel.map((c, i) => new Promise((res) => setTimeout(res, i * SPAWN_STAGGER_MS)).then(() => spawnWorker(c))));
  log(`workers done: ${results.map((r) => `${r.beadId}=${r.timedOut ? "TIMEOUT" : r.code}(${r.secs}s)`).join(" ")}`);
  const summary = mergeGreen({ dispatched: plan.parallel });
  log(`merge: landed ${summary.landed.length} [${summary.landed.map((l) => l.beadId).join(", ")}] reverted ${summary.reverted.length} conflicts ${summary.conflicts.length} red ${summary.red.length}${summary.dropped ? ` — AUTO-DROP maxConcurrency→${summary.dropped}` : ""}`);
  // sweep crashed/timed-out workers that never wrote a gate marker → merge left their claim file in place. GC the
  // worktree by EXPLICIT path + release the claim; NO attempt bump (infra-kill, §13.35) → the bead retries next cycle.
  const cfg = readConfig();
  for (const d of plan.parallel) {
    if (existsSync(d.claimPath)) {
      log(`worker ${d.beadId} left no gate marker (crash/timeout) → GC worktree (explicit path) + release claim (infra-kill, no attempt)`);
      try { wtRemove(d.worktree, { root: cfg.concurrency.worktreeRoot }); } catch {}
      try { releaseClaim(d.id); } catch {}
      try { removeActive({ runToken: d.runToken || null, beadId: d.beadId }); } catch {}
    }
  }
  killAgy("post-parallel-cycle");
  return { kind: "parallel", committed: summary.landed.length > 0, anyRevert: (summary.reverted.length + summary.conflicts.length + summary.red.length) > 0, summary };
}

// ── planner: SNAPSHOT + trigger precedence now live in spec-core.mjs (single source, 15.22). The
// NO_PLANNER short-circuit + the max-replan drain gate are HOST LOOP STATE and stay in main() below.
function spawnPlanner(trig, snap, baseSha, gameBaseSha) {
  return new Promise((resolve) => {
    writeJson(PLAN_INPUT, { mode: trig.mode, latchKey: trig.latchKey, readyCount: snap.readyCount, blockedIds: snap.blockedIds, wipKeywords: snap.wipKeywords, authoritySha: snap.authoritySha });
    const startEpoch = Date.now();
    const runToken = randomBytes(6).toString("hex");
    const beadId = `planner-${trig.mode}`;
    writeJson(TICKJSON, { pid: null, startEpoch, baseSha, gameBaseSha, beadId, mode: trig.mode, runToken });
    try { writeActive({ runToken, beadId, assetId: null, kind: "planner", engine: "claude", tier: "structural", title: `planner (${trig.mode})`, baseSha, startEpoch, pid: null }); } catch {}
    const logFile = `${LOGS}/planner-${trig.mode}-${startEpoch}.log`;
    const fd = openSync(logFile, "a");
    const psCmd = `Get-Content -Raw -LiteralPath '${PLANNER_MD}' | & claude -p --model ${OPUS} --dangerously-skip-permissions`;
    const child = spawn("powershell.exe", ["-NoProfile", "-Command", psCmd], { cwd: CC, env: { ...process.env, RALPH_PASS: beadId, CLAUDE_PROJECT_DIR: REPO }, stdio: ["ignore", fd, fd], windowsHide: true });
    writeJson(TICKJSON, { pid: child.pid, startEpoch, baseSha, gameBaseSha, beadId, mode: trig.mode, runToken });
    log(`planner spawned pid ${child.pid} mode=${trig.mode} readyCount=${snap.readyCount} base ${baseSha.slice(0, 8)} (log ${logFile.split("/").pop()})`);
    let hardTimer = null;
    const timer = setTimeout(() => { log(`planner TIMEOUT → taskkill ${child.pid}`); try { execSync(`taskkill /PID ${child.pid} /T /F`, { timeout: GIT_TIMEOUT_MS }); } catch {}
      hardTimer = setTimeout(() => { log(`planner child did not exit ${KILL_GRACE_MS / 1000}s after taskkill → hard-resolve (avoid permanent await)`); resolve({ code: -1, killFailed: true, pid: child.pid, secs: Math.round((Date.now() - startEpoch) / 1000) }); }, KILL_GRACE_MS); }, TIMEOUT_MS);
    child.on("exit", (code) => { clearTimeout(timer); if (hardTimer) clearTimeout(hardTimer); try { closeSync(fd); } catch {} resolve({ code, secs: Math.round((Date.now() - startEpoch) / 1000) }); });
    child.on("error", () => { clearTimeout(timer); if (hardTimer) clearTimeout(hardTimer); try { closeSync(fd); } catch {} resolve({ code: -1, secs: 0 }); });
  });
}

// ── main loop ───────────────────────────────────────────────────────────────────
async function main() {
  mkdirSync(LOGS, { recursive: true });
  assertBranch();
  assertMutex();
  reconcile();
  if (has("reconcile-only")) { log("reconcile-only: done"); return; }
  // §15g phase 8 — run ONE parallel work cycle then exit (ops kick / verify harness). Skips the planner + the
  // infinite loop; still ran the boot guards above. Serial mode → runParallelCycle returns empty (no-op).
  if (has("parallel-once")) {
    const baseSha = gameHead(); // §SPLIT — parallel worktrees detach at the GAME base
    const pr = await runParallelCycle(baseSha);
    log(`parallel-once: kind=${pr.kind} committed=${pr.committed || false} landed=${pr.summary ? pr.summary.landed.length : 0} reverted=${pr.summary ? pr.summary.reverted.length : 0}`);
    return;
  }

  let breaker = 0, replanCount = 0, sinceCommit = 0, prevOpenWork = null, lastOutcome = null, settleWaits = 0;
  let serialBead = null; // §15g phase 8 — the parallel branch forces a serial agy bead through the serial path
  for (let i = 0; i < MAX_TICKS; i++) {
    if (existsSync(PAUSED)) { log("paused flag set → stop"); break; }
    const cap = effectiveCap();
    if (usageToday() >= cap) { log(`daily cap ${cap} reached → stop`); break; }

    // ── sense (runs the asset-scan pre-step inside computeSnapshot) ──
    // spec-core.computeTrigger is PURE; the host-state gates stay HERE: NO_PLANNER short-circuit + the
    // max-replan drain (13.31). auditHours/cap mirror the CLI flags (cap unused by the trigger, harmless).
    const snap = computeSnapshot({ auditHours: AUDIT_MS / 3600000, cap: effectiveCap() });

    // §15.20 REDEFINED consecutive-fail breaker: evaluated at cycle top from the openWork delta since the last
    // cycle, attributed to the PRIOR cycle's outcome. Trips on M cycles with NO net openWork reduction (NOT on
    // "any green" — one trivial land that re-mints keeps openWork flat, 15.20); benign parked/unsure/infra-kill +
    // a productive planner do NOT increment; a strict reduction resets. Single-source formula in assets-core.
    const bstep = breakerStep({ breaker, prevOpenWork, openWork: snap.openWork, outcome: lastOutcome, breakerN: BREAKER_N });
    breaker = bstep.breaker; prevOpenWork = bstep.prevOpenWork;
    if (bstep.tripped) {
      log(`BREAKER: ${breaker} cycles, no net openWork reduction (openWork=${snap.openWork}) → auto-pause (13.39/15.20)`);
      writeFileSync(PAUSED, JSON.stringify({ reason: "consecutive-failure-breaker", count: breaker, openWork: snap.openWork, at: nowIso() }));
      break;
    }

    const rawTrig = NO_PLANNER ? null : computeTrigger(snap);
    const trig = replanCount < MAX_REPLAN ? rawTrig : null;
    if (replanCount >= MAX_REPLAN && rawTrig) log(`max-replan ${MAX_REPLAN} reached → skip planning this cycle, drain instead (13.31)`);

    if (trig) {
      const baseSha = git("rev-parse HEAD").trim();
      const gameBaseSha = gameHead(); // §SPLIT — record the game anchor even for a planner tick (reconcile reads it)
      bumpUsage();
      log(`── tick ${i + 1} ── PLANNER mode=${trig.mode} readyCount=${snap.readyCount} openWork=${snap.openWork} base=${baseSha.slice(0, 8)}`);
      const r = await spawnPlanner(trig, snap, baseSha, gameBaseSha);
      if (r.killFailed) { // HARDENING: un-reaped planner zombie → do NOT advance (a 2nd planner would race it). LOUD-STOP.
        log(`FATAL: planner pid ${r.pid} un-killable after timeout — zombie may still write plan/backlog → LOUD STOP (not advancing). Operator: kill pid ${r.pid}, then restart.`);
        try { writeFileSync(`${CONTROL}/.guard-alert`, JSON.stringify({ kind: "unkillable-planner-zombie", pid: r.pid || null, mode: trig.mode, at: nowIso() })); } catch {}
        try { writeFileSync(PAUSED, JSON.stringify({ reason: "unkillable-planner-zombie", pid: r.pid || null, at: nowIso() })); } catch {}
        break;
      }
      const tk = readJson(TICKJSON, {});
      rmSync(TICKJSON, { force: true });
      try { removeActive({ runToken: tk.runToken || null, beadId: tk.beadId || null }); } catch {}
      const st = readJson(`${CONTROL}/status.json`, {});
      const headNow = git("rev-parse HEAD").trim();
      const progressed = headNow !== baseSha;
      log(`tick ${i + 1} planner done in ${r.secs}s → stage=${st.stage || "?"} netChange=${st.netChange ?? "?"} committed=${progressed} head=${headNow.slice(0, 8)}`);
      replanCount++; settleWaits = 0; // a plan fired (incl. the coalesced diff) → reset the settle-wait budget
      lastOutcome = progressed ? "planned" : "plan-empty"; // benign if productive; a no-op planner bumps the breaker
      continue; // re-sense with the new backlog
    }

    // ── §15g phase 8: PARALLEL work cycle (config mode=parallel). SERIAL mode skips this entirely and takes the
    // unchanged single-flight path below (byte-for-byte). The planner above already ran serially in either mode. ──
    if (!isSerial(readConfig())) {
      const baseShaP = gameHead(); // §SPLIT — parallel worktrees detach at the GAME base
      const pr = await runParallelCycle(baseShaP);
      if (pr.kind === "parallel") {
        replanCount = 0; settleWaits = 0;
        if (pr.committed) { sinceCommit = 0; ggitQuiet("tag -f cc-known-good HEAD"); gitQuiet("tag -f cc-known-good HEAD"); try { writeKnownGood(); } catch {} lastOutcome = "committed"; }
        else { sinceCommit++; lastOutcome = pr.anyRevert ? "reverted" : "idle"; }
        if (sinceCommit >= STALLED_N) writeJson(`${CONTROL}/.stalled`, { sinceCommit, at: nowIso() });
        else { try { rmSync(`${CONTROL}/.stalled`, { force: true }); } catch {} }
        continue; // re-sense
      }
      if (pr.kind === "serialAgy") { serialBead = pr.bead; } // fall through to the serial path below for the ONE agy bead
      else { // kind === "empty" → nothing dispatchable this cycle → the SAME idle/settle decision as the serial !bead branch
        if (snap.authorityChangePending && settleWaits < MAX_SETTLE_WAITS) {
          settleWaits++; lastOutcome = "idle";
          log(`authority change settling (debounce, wait ${settleWaits}/${MAX_SETTLE_WAITS}) → hold ${SETTLE_POLL_MS / 1000}s + re-sense (not idle-exit)`);
          await new Promise((r) => setTimeout(r, SETTLE_POLL_MS));
          continue;
        }
        const comp = snap.completion || {};
        const reason = comp.toSpec ? "to-spec — every ready asset implemented, no open work"
          : comp.idleBlockedOnHuman ? comp.reason : "no dispatchable work and no planner trigger";
        log(`idle: ${reason} → stop`);
        writeJson(`${CONTROL}/status.json`, { stage: "idle", note: reason, completion: comp, updated: nowIso() });
        break;
      }
    }

    // ── no planner trigger → work the head ready bead ──
    const bead = serialBead || headReadyBead();
    serialBead = null;
    if (!bead) {
      // AUDIT FIX (step7 / §15.16): a real authority change is debouncing but nothing else to do → do NOT exit
      // inside the settle window (the coalesced diff would be dropped until a manual restart). Poll-wait then
      // re-sense so the diff fires when the window elapses. Bounded (a persistent burst → give up, settle persists).
      if (snap.authorityChangePending && settleWaits < MAX_SETTLE_WAITS) {
        settleWaits++;
        // RE-AUDIT FIX (step7): a work-free settle wait must be BENIGN for the §15.20 breaker — else, if the tick
        // just before entering the wait was 'committed' (non-benign) and openWork is flat, breakerStep at the loop
        // top increments every poll cycle and auto-PAUSES (~4-5 waits ≈ 60-75s) BEFORE the ~90s window elapses,
        // dropping the coalesced diff (the exact regression this poll-wait was meant to prevent). Set lastOutcome to
        // a BREAKER_BENIGN value so the wait cannot trip the breaker.
        lastOutcome = "idle";
        log(`authority change settling (debounce, wait ${settleWaits}/${MAX_SETTLE_WAITS}) → hold ${SETTLE_POLL_MS / 1000}s + re-sense (not idle-exit)`);
        await new Promise((r) => setTimeout(r, SETTLE_POLL_MS));
        continue;
      }
      // §15c honest stop: to-spec (all implemented) vs idle-blocked-on-human (work left needs a human).
      const comp = snap.completion || {};
      const reason = comp.toSpec ? "to-spec — every ready asset implemented, no open work"
        : comp.idleBlockedOnHuman ? comp.reason
        : "no ready bead and no planner trigger";
      log(`idle: ${reason} → stop`);
      writeJson(`${CONTROL}/status.json`, { stage: "idle", note: reason, completion: comp, updated: nowIso() });
      break;
    }
    replanCount = 0; settleWaits = 0;

    // orchestrator worker pick: the operator's allowed set → the task-fit engine+model for THIS bead. The tick
    // re-derives the SAME choice via `agent-core --pick` (pure fn, same inputs) so host + tick never disagree.
    // The claude-direct case runs the tick ON that model (sonnet/opus); an agy bead keeps the cheap default
    // model to orchestrate the dispatch. A null engine = the operator disabled every model this bead can use
    // (only agy checked + feel work) → honest-stop with the reason rather than spin on an unrunnable bead.
    const pw = pickForBead(bead, CONTROL);
    if (!pw.engine) {
      const why = `worker-selection: ${pw.reason} (bead ${bead.id}) — enable a Claude model in the dashboard`;
      log(`idle: ${why} → stop`);
      try { writeFileSync(`${CONTROL}/.guard-alert`, JSON.stringify({ kind: "no-enabled-model", beadId: bead.id, reason: pw.reason, at: nowIso() })); } catch {}
      writeJson(`${CONTROL}/status.json`, { stage: "idle", note: why, updated: nowIso() });
      break;
    }
    const tickModel = pw.engine === "claude" ? pw.model : MODEL;
    const baseSha = git("rev-parse HEAD").trim();
    const gameBaseSha = gameHead(); // §SPLIT — the game repo anchor for this tick
    bumpUsage();
    log(`── tick ${i + 1} ── bead=${bead.id} "${bead.title}" engine=${pw.engine} model=${tickModel} openWork=${snap.openWork} base=${baseSha.slice(0, 8)} game=${gameBaseSha.slice(0, 8)}`);
    const r = await spawnTick(bead, baseSha, gameBaseSha, tickModel);

    // HARDENING (2026-07-02, review-driven): killFailed = the timeout taskkill did NOT reap the tick tree, so a
    // zombie `claude -p` is STILL writing cosmo-canyon/game. Do NOT advance — a fresh spawnTick would be a SECOND
    // concurrent writer (breaks single-flight, races/corrupts the tree). LOUD-STOP: alert + pause + break, leaving
    // .tick.json in place so the next boot's reconcile re-guards single-flight (pidIsOurTick → refuse until the
    // zombie dies). Strictly better than the old permanent-await hang AND the naive advance-into-corruption.
    if (r.killFailed) {
      log(`FATAL: tick pid ${r.pid} un-killable after timeout — zombie still writing the game tree → LOUD STOP (not advancing; single-flight would break). Operator: kill pid ${r.pid}, then restart.`);
      try { writeFileSync(`${CONTROL}/.guard-alert`, JSON.stringify({ kind: "unkillable-tick-zombie", pid: r.pid || null, beadId: bead.id, at: nowIso() })); } catch {}
      try { writeFileSync(PAUSED, JSON.stringify({ reason: "unkillable-tick-zombie", pid: r.pid || null, beadId: bead.id, at: nowIso() })); } catch {}
      break;
    }
    if (!r.timedOut && r.secs > 0) recordStat(bead.tier, r.secs); // feed the per-tier stuck-detection estimate (never a timeout)

    // outcome = side-effect truth (status.json), never the tick's stdout
    const st = readJson(`${CONTROL}/status.json`, {});
    let outcome = st.stage || "unknown";
    if (r.timedOut) {
      // infra-kill (13.35): reconcile the GAME repo to its base, do NOT count as a bead attempt; benign for the breaker.
      log(`tick timed out (${r.secs}s) → reconcile game to base (no attempts++, 13.35)`);
      resetGameTo(gameBaseSha); // §SPLIT — restore the GAME repo to its base
      outcome = "timeout";
    }
    const tk = readJson(TICKJSON, {});
    rmSync(TICKJSON, { force: true });
    try { removeActive({ runToken: tk.runToken || null, beadId: bead.id }); } catch {} // backstop; bookkeep removed it on terminal
    killAgy("post-tick stale agy cleanup"); // §13.16: never leave an agy alive across ticks

    // agy quota/auth failover (§13.38): repeated zero-diff agy passes → flip the lane to claude + alert
    if (st.stage === "agy-noop") {
      const strikes = existsSync(AGY_STRIKES) ? Number(readFileSync(AGY_STRIKES, "utf8").trim()) || 0 : 0;
      log(`agy zero-diff (quota/auth suspect), strike ${strikes}/${FAILOVER_N}`);
      if (strikes >= FAILOVER_N) {
        log(`agy ${strikes} strikes ≥ ${FAILOVER_N} → FAILOVER: agy cooldown + alert (13.38)`);
        // Drop agy from the orchestrator's pick TEMPORARILY (agent-core honors .agy-cooldown) — do NOT rewrite
        // the operator's allowed set (their checkboxes are sacred; re-saving them, or the TTL, clears this).
        const alk = acquire(LOCKS, "agent"); // §15i 15.23 — serialize vs the GUI's POST /agent (which clears the cooldown)
        try { writeJson(AGY_COOLDOWN, { at: Date.now(), strikes, note: `auto-failover from agy after ${strikes} zero-diff passes` }); } finally { release(alk); }
        writeFileSync(`${CONTROL}/.guard-alert`, JSON.stringify({ kind: "agy-quota-failover", strikes, at: nowIso() }));
        try { writeFileSync(AGY_STRIKES, "0"); } catch {} // reset counter post-failover
      }
    }

    const headNow = git("rev-parse HEAD").trim();
    const committed = headNow !== baseSha;
    log(`tick ${i + 1} done in ${r.secs}s → outcome=${outcome} committed=${committed} head=${headNow.slice(0, 8)}`);

    if (outcome === "committed") { sinceCommit = 0; ggitQuiet("tag -f cc-known-good HEAD"); gitQuiet("tag -f cc-known-good HEAD"); try { writeKnownGood(); } catch {} } // §SPLIT §13.40 known-good on BOTH repos (game=rollback anchor, C:/Vibes=banner) + §15.5
    else { sinceCommit++; }
    lastOutcome = outcome; // the breaker consumes this at the NEXT cycle top (15.20 openWork-based)
    // stalled heartbeat (fix 6): alive but no commit for STALLED_N ticks → flag for the UI (breaker is the hard pause)
    if (sinceCommit >= STALLED_N) writeJson(`${CONTROL}/.stalled`, { sinceCommit, at: nowIso() });
    else { try { rmSync(`${CONTROL}/.stalled`, { force: true }); } catch {} }
  }
  log("supervisor loop ended");
}

main().catch((e) => { log(`FATAL ${e.stack || e}`); process.exit(1); });
