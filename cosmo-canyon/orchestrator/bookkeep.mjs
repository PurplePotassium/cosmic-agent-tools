// Cosmo Canyon — deterministic bookkeep / gate / revert script (§13.30, §13.42).
//
// This is the ONLY authority for git surgery + pass/fail. The tick (a `claude -p`)
// does sense + work, then invokes this with --result work|blocked|idle. EVERYTHING
// that decides whether the increment lands (gate, guard, per-bead acceptance, the
// commit SHA, the revert target, attempts bookkeeping) is computed HERE, not by the
// model — so a worker can't self-attest a false green, and the highest-stakes git
// step isn't a model judgment.
//
// Commit model (§13.21/§13.27/§13.29): anchor everything to the supervisor-persisted
// BASE_SHA in control/.tick.json (NEVER dirty-tree, NEVER HEAD). PASS → commit the
// good tree `ralph <id>: <title>`. FAIL/blocked → `git reset --hard BASE_SHA` +
// scoped clean (clean tree), then commit only the attempts/blocked bookkeeping. The
// repo Stop hook is a harmless backstop: this script leaves a clean tree, so the hook
// finds nothing to commit.
import { execSync, spawnSync } from "node:child_process";
import { readFileSync, writeFileSync, existsSync, renameSync, rmSync } from "node:fs";
import { acquire, release, acquireOrdered, releaseAll } from "./lock.mjs";
// §15g phase 5 — the asset store primitives (green-land flip / unsure-park / abandon breaker) + the active.json
// GC. bookkeep CALLS these (never hand-writes asset meta lockless). Each store primitive locks asset-<id>
// INTERNALLY; writeImplementedProvenanceHeld is the no-lock companion for the §15e land critical section.
import { writeImplementedProvenanceHeld, bindImplementedSha, bumpAbandon, parkUnsure, rebuildIndex, readAsset, setOperatorBlock } from "./assets.mjs";
import { removeActive, isTerminal } from "./assets-core.mjs";
// §15e phase 6 — the DETERMINISTIC manifest config for derive-bind (parse-instructions is NOT the model) + the
// per-agent worktree lifecycle for the spec-grader MUTATION CHECK (run the grader at BASE → must FAIL).
import { manifestEntryFor } from "./parse-instructions.mjs";
import { create as wtCreate, remove as wtRemove } from "./worktree.mjs";

// §15g phase 5 — roots honor env overrides (default = the real repo → production BYTE-FOR-BYTE unchanged) so
// the asset-LAND / unsure-park paths run against a THROWAWAY git repo + control plane in the verify harness
// (gotcha a: a CC_CONTROL test must reach bookkeep too, not just assets.mjs).
const REPO = process.env.CC_REPO || "C:/Vibes";        // control plane + .claude/settings.json guard (NEVER moves)
const CC = process.env.CC_CC || `${REPO}/cosmo-canyon`;
// §SPLIT (2026-07-03) — the game is its OWN nested git repo (cosmo-canyon/game, own .git, gitignored from C:/Vibes).
// GAME is that repo's working tree: serial → ${CC}/game; parallel → the claim's worktree (a worktree OF the game
// repo). Reassigned below once the tick's worktree is known. Explicit CC_GAME pins it (tests).
let GAME = process.env.CC_GAME || `${CC}/game`;
const CONTROL = process.env.CC_CONTROL || `${CC}/control`;
const LOCKS = `${CONTROL}/locks`;
// §SPLIT — CONTROL/guard git (`git`/`gitArgs`) ALWAYS target REPO (C:/Vibes); the control plane never lives in a
// game worktree. GAME git (`ggit`/`ggitArgs`, below) targets the game repo. GIT_CWD is therefore CONSTANT.
const GIT_CWD = REPO;
let IN_WT = false;
const ABANDON_N = 3; // §13.32: attempts >= N → terminal "abandoned"
const MAX_DIFF_LINES = 800; // §13/4: oversized-pass breaker
// §15g-T — child TIME-BOX so a hung/infinite-loop test/grader can't burn the whole per-agent timeout on a dead
// tick (kill the child, treat as fail). SIGKILL because a wedged node child ignores SIGTERM on Windows.
const GATE_TIMEOUT_MS = Number(process.env.CC_GATE_TIMEOUT_MS) || 5 * 60 * 1000;
const ACCEPT_TIMEOUT_MS = Number(process.env.CC_ACCEPT_TIMEOUT_MS) || 3 * 60 * 1000;
const MUTCHECK_TIMEOUT_MS = Number(process.env.CC_MUTCHECK_TIMEOUT_MS) || 3 * 60 * 1000;
const DERIVE_TIMEOUT_MS = Number(process.env.CC_DERIVE_TIMEOUT_MS) || 90 * 1000;
const GRADER_CONFIRM = () => `${CONTROL}/grader-confirm.json`; // §15.15 operator confirm registry for planner graders (durable, committed)
const FEEL_REVIEW_JSON = () => `${CONTROL}/feel-review.json`;  // §15.18 human-gated feel-review queue (durable, committed)

// Worker-protected paths (relative to REPO). Editing these = tamper → revert+block
// (§13.26 SEED cheat, §13.29 gate-test tamper, acceptance-script tamper).
// §SPLIT — paths are now GAME-repo-relative (the game is its own repo; no `cosmo-canyon/game/` prefix). Matched
// against gameDirtyPaths() (ggit status). Editing these = tamper → revert+block (§13.26/§13.29 + acceptance tamper).
const PROTECTED = [
  "test/canary.ts",
  "test/determinism.ts",
  "test/sim-purity.ts",
  "test/budget.ts",
  "test/all.ts",    // §AUDIT-2026-07-04 — the single-process gate aggregator; editing its imports = skipping tests
  "test/_util.ts",  // §AUDIT-2026-07-04 — the assertion helpers EVERY gate test imports; a no-op'd ok() = vacuous green (was an unguarded hole)
  "package.json",
];
// §15g-T — the gate's sim suite was split into per-system files (sim.ts aggregator + sim.<system>.ts). ALL of
// them are gate-authoritative → a worker editing ANY `test/sim*.ts` (the recurring SEED-cheat class) is
// tamper. Prefix-guard covers sim.ts + every split without enumerating each (mirrors PROTECTED_PREFIX).
const PROTECTED_TEST_SIM = "test/sim"; // matches test/sim.ts + test/sim.*.ts
const PROTECTED_PREFIX = "accept/"; // independent acceptance scripts
const SOURCE_PREFIX = "assets/source/"; // §13.41: full-res source art is sacred (derive regenerates art/atlas from it) — never worker-mutated
// (SCOPE_PREFIX removed — the worker's scope IS the entire game repo now; "out of scope" only means the C:/Vibes side.)

// §15i 15.41 / §SPLIT — the C:/Vibes-side scope/tamper guard is allowlist-based over `git status` of the WHOLE
// C:/Vibes tree. Since the game is now a SEPARATE repo (gitignored here), game edits never appear in C:/Vibes
// status — so a C:/Vibes dirty path is ALLOWED only if it is one of the specific control/ files bookkeep itself
// writes. ANY other dirty path here (tracked OR untracked — a new C:/Vibes/evil.ps1, an edit to .claude/settings.json,
// or a stray orchestrator write) is tamper → revert+block. Keeps the persistence/RCE hole closed even though the
// Stop hook still stages control (`git add -A cosmo-canyon`).
const ALLOW_CONTROL = new Set([
  "cosmo-canyon/control/backlog.json",     // persistBacklog
  "cosmo-canyon/control/completions.json", // recordCompletion
  "cosmo-canyon/control/suggestions.json", // (plan-apply owns; allow so a stray dirty here doesn't false-trip)
  "cosmo-canyon/control/status.json",      // writeStatus (gitignored — won't appear, but explicit)
  "cosmo-canyon/control/.agy-strikes",     // agy-noop strike counter (gitignored)
  "cosmo-canyon/control/assets.json",      // §15 derived asset index (GUI upload / reconcileAssets / land flip)
  "cosmo-canyon/control/active.json",      // §15c in-flight rows (gitignored)
  "cosmo-canyon/control/.asset-scan-latch.json", // §15c fire-latch (gitignored)
  "cosmo-canyon/control/feel-review.json",       // §15.18 feel-review queue (durable; bookkeep appends at a feel land)
  "cosmo-canyon/control/.merge-tick.json",       // §15g phase 8 merge single-committer anchor (gitignored; defense-in-depth vs a stray dirty instance)
  "cosmo-canyon/control/.merge-regate-fails.json", // §15g phase 8 merge re-gate-fail counter (gitignored; defense-in-depth)
  "cosmo-canyon/control/.agy-cooldown",          // agy quota-failover cooldown (gitignored; written post-tick, defense-in-depth)
  "cosmo-canyon/control/config.json",            // concurrency/runtime settings — GUI may edit live; committed, must survive a tick revert (like a concurrent asset upload)
  "cosmo-canyon/control/agent.json",             // worker allowed-set — GUI may edit live during a tick; must not be reverted
  // §AUDIT-2026-07-03 — planner runtime markers. Were TRACKED (gitignore rules landed after the first commit,
  // which gitignore can't undo) → every planner rewrite dirtied the tree → the tree-wide guard tamper-failed
  // EVERY later work tick (~28 reverts, ~29 beads blocked). Now untracked (git rm --cached) + ignored; listed
  // here as defense-in-depth so a stray dirty instance can never false-trip the guard again (same class as C1).
  "cosmo-canyon/control/.plan-input.json",
  "cosmo-canyon/control/.plan-result.json",
  "cosmo-canyon/control/.plan-latch.json",
  "cosmo-canyon/control/.plan-completions.md", // §AUDIT-2026-07-04 — plan-prep's landed-work digest (gitignored runtime marker; same class)
]);
// §15.15 (audit) — grader-confirm.json is the OPERATOR gate; it is DELIBERATELY *not* allowed as a worker-tick
// write (a worker self-writing {beadId:{confirmed:true}} would lift the human gate). It is NOT in ALLOW_CONTROL,
// so a worker-tick dirtiness of it is out-of-scope tamper → reverted before graderConfirmed() reads it. Legit
// operator confirms are COMMITTED by the server (/assets/grader-confirm), so they sit at BASE, clean, never dirty
// during a tick.
// §15 asset-system control PREFIXES that are LEGIT control-plane writes (GUI uploads, the folder-per-asset
// store, claims, locks) — NOT worker tamper. Marking them allowed keeps a concurrent GUI upload during a tick
// from being flagged out-of-scope (which would FAIL the tick) AND keeps fullRevert from DESTROYING that upload
// (§15c-2: "revert must NOT reset across control/assets — a failed tick would destroy a concurrent upload").
const ALLOW_CONTROL_PREFIX = [
  "cosmo-canyon/control/assets/",   // meta.json + artifacts + history (the input surface)
  "cosmo-canyon/control/claims/",   // parallel claims (gitignored)
  "cosmo-canyon/control/locks/",    // lock dirs (gitignored)
  "cosmo-canyon/control/.trash/",   // tombstones (gitignored)
];
function allowed(p) { return ALLOW_CONTROL.has(p) || ALLOW_CONTROL_PREFIX.some((pre) => p.startsWith(pre)); } // §SPLIT — control-allowlist only (game is a separate repo, gitignored here)
const SETTINGS_REL = ".claude/settings.json"; // §15i 15.41 — byte-identity-guarded vs BASE (repo-wide Stop hook lives here)

// §15i 15.44 — the deterministic authority must never run untrusted text through a shell.
const NPM = process.platform === "win32" ? "npm.cmd" : "npm";   // gate runs via argv-array (shell:false)
let TSX_CLI = `${GAME}/node_modules/tsx/dist/cli.mjs`;            // run .ts graders via tsx's loader, shell-free (node <cli> <grader>) — recomputed if GAME retargets to a worktree
const ACCEPT_CMD_RE = /^(node|tsx) accept\/[A-Za-z0-9_-]+\.ts$/;  // acceptanceCmd allowlist — exact grader shape, no shell metachars
// strip shell metachars + newlines from any interpolated untrusted field (title/detail/note/reason) — the
// commit path already uses `git commit -F -` (message via stdin, no shell), this is defense-in-depth + tidy logs.
function sanitize(s) { return String(s || "").replace(/[%&|<>`$\r\n]/g, " ").replace(/\s+/g, " ").trim(); }

// ── args ──
const args = process.argv.slice(2);
function arg(name, def = null) {
  const i = args.indexOf(`--${name}`);
  return i >= 0 && i + 1 < args.length ? args[i + 1] : def;
}
const result = arg("result", "work"); // work | blocked | idle
const reasonArg = arg("reason", "");
// §GC3 — `--needs-operator` (with --result blocked): the bead is a TERMINAL human gate, un-actionable by a code
// edit (feature already implemented / spec confirm-only / mis-classified). NOT a rescope-able failure → no
// attempts++, mark bead.needsOperator (excluded from blockedIds → the planner never reopens → no churn) + hard-park
// the asset. The operator resolves it (confirm-satisfied / reclassify / reopen). See docs/KNOWLEDGE.md 2026-07-02.
const needsOperator = args.includes("--needs-operator");

// ── helpers ──
// HARDENING (2026-07-02): bound every git exec so a stuck .git/index.lock / credential prompt THROWS instead
// of hanging bookkeep (the outer 45-min supervisor taskkill was the only backstop before).
const CC_GIT_TIMEOUT_MS = Number(process.env.CC_GIT_TIMEOUT_SEC || 120) * 1000;
const git = (cmd) => execSync(`git -C "${GIT_CWD}" ${cmd}`, { encoding: "utf8", maxBuffer: 64 * 1024 * 1024, timeout: CC_GIT_TIMEOUT_MS });
const gitQuiet = (cmd) => { try { return git(cmd); } catch (e) { return e.stdout || ""; } };
// §SPLIT — the GAME repo runners (mirror of git/gitQuiet, rooted at GAME). GAME is a `let` reassigned to the tick's
// worktree before any ggit call, and these close over it → they always hit the right game tree.
const ggit = (cmd) => execSync(`git -C "${GAME}" ${cmd}`, { encoding: "utf8", maxBuffer: 64 * 1024 * 1024, timeout: CC_GIT_TIMEOUT_MS });
const ggitQuiet = (cmd) => { try { return ggit(cmd); } catch (e) { return e.stdout || ""; } };
function readJson(p, fallback) { try { return JSON.parse(readFileSync(p, "utf8")); } catch { return fallback; } }
function atomicWrite(p, data) { const t = `${p}.tmp`; writeFileSync(t, data); renameSync(t, p); }
function writeJson(p, obj) { atomicWrite(p, JSON.stringify(obj, null, 2) + "\n"); }
function nowIso() { return new Date().toISOString(); }

function writeStatus(s) {
  writeJson(`${CONTROL}/status.json`, { ...s, updated: nowIso() });
}

// ── load tick context ──
// §15i 15.7 — the anchor source is `--tick <path>` (default control/.tick.json). Serial passes NOTHING →
// today's .tick.json. Parallel (phase 8) passes the per-agent CLAIM path (claim.mjs writes the SAME
// {baseSha,beadId,worktree} shape), so every agent gates/reverts against ITS OWN worktree + baseSha (15.26).
const tickPath = arg("tick", `${CONTROL}/.tick.json`);
const tick = readJson(tickPath, null);
if (!tick || !tick.baseSha) {
  console.error(`bookkeep: missing baseSha in ${tickPath} — supervisor/claim must persist it before spawn (§13.28/15.26)`);
  process.exit(2);
}
const BASE = tick.baseSha;
const beadId = tick.beadId || null;
const runToken = tick.runToken || null; // §15c — ties this tick to its active.json row (removed on terminal)
// §SPLIT — the game worktree (parallel) IS a worktree of the GAME repo, so its ROOT is the game working tree (no
// more `${wt}/cosmo-canyon/game`). Serial: no worktree → GAME stays ${CC}/game, IN_WT=false. GIT_CWD is CONSTANT
// (REPO) — the control plane never moves into a worktree. No git op has run yet (only readJson above), so mutating
// GAME/IN_WT here is safe for every helper (they close over the vars). CC_GAME pins GAME (tests).
const WT = tick.worktree ? String(tick.worktree).replace(/\\/g, "/").replace(/\/+$/, "") : null;
IN_WT = !!WT;
if (IN_WT && !process.env.CC_GAME) { GAME = WT; TSX_CLI = `${GAME}/node_modules/tsx/dist/cli.mjs`; }
// §SPLIT — the GAME repo anchor bookkeep gates/reverts/commits against. tick-prep (serial) + claim (parallel) persist
// it; fall back to the game repo's current HEAD (serial == base, no game commit yet this tick) for resilience.
const GBASE = tick.gameBaseSha || ggitQuiet("rev-parse HEAD").trim();
const GATE_ONLY = args.includes("--gate-only"); // §15g phase 8 — parallel worker mode: gate+guard IN the worktree, write a gate marker, NEVER commit/revert/derive (the single-committer merge lands it)

// argv-array git (shell:false) for path-bearing ops — no shell interpolation of untrusted filenames
function gitArgs(...a) { return spawnSync("git", ["-C", GIT_CWD, ...a], { encoding: "utf8", maxBuffer: 64 * 1024 * 1024 }); }
function ggitArgs(...a) { return spawnSync("git", ["-C", GAME, ...a], { encoding: "utf8", maxBuffer: 64 * 1024 * 1024 }); } // §SPLIT — argv-array game-repo git (shell:false)

// §15i 15.41/15.49/15.7 / §SPLIT — refuse a destructive git op unless we are truly at the expected toplevel.
// GAME side: destructive game ops (reset --hard/clean) run via ggit → assert ggit's toplevel IS GAME; parallel
// additionally requires a detached HEAD (worktree isolation). CONTROL side: surgical revertOnePath rm/checkout runs
// via git (REPO) → assert its toplevel is C:/Vibes. A wrong-cwd reset --hard+clean would nuke the wrong tree (15.7).
function assertGameToplevel() {
  const top = ggitQuiet("rev-parse --show-toplevel").trim().replace(/\\/g, "/").replace(/\/+$/, "");
  const want = GAME.replace(/\\/g, "/").replace(/\/+$/, "");
  if (top !== want) throw new Error(`refusing destructive game git op: toplevel '${top}' != '${want}'`);
  if (IN_WT) {
    const ref = ggitQuiet("rev-parse --abbrev-ref HEAD").trim();
    if (ref !== "HEAD") throw new Error(`refusing destructive worktree op: HEAD not detached (on '${ref}') in ${want} (15.7)`);
  }
}
function assertControlToplevel() {
  const top = gitQuiet("rev-parse --show-toplevel").trim().replace(/\\/g, "/").replace(/\/+$/, "");
  const want = REPO.replace(/\/+$/, "");
  if (top !== want) throw new Error(`refusing destructive control git op: toplevel '${top}' != '${want}'`);
}

// §15i 15.41 — did .claude/settings.json change vs BASE? Use git's own diff (autocrlf-aware; a raw byte
// compare vs `git show` would false-positive under core.autocrlf=true). status 1 = differences (tamper),
// 0 = identical, anything else (e.g. 128 error) = can't determine → do NOT false-trip.
function settingsTampered() { return gitArgs("diff", "--quiet", BASE, "--", SETTINGS_REL).status === 1; }

// restore-or-remove one out-of-allowlist repo-relative path vs BASE (surgical — NEVER a blanket clean). Handles
// non-cosmo paths (evil.ps1, .claude/settings.json) AND worker-planted cosmo-canyon/control junk (NOT under an
// allowed prefix). game paths are handled by the scoped checkout+clean below, so skip them here.
function revertOnePath(p) {
  if (!p) return;                                       // §SPLIT — control-side only; the game is a separate repo (ggit reset handles it)
  const inBase = gitArgs("cat-file", "-e", `${BASE}:${p}`).status === 0;
  if (inBase) {
    gitArgs("checkout", BASE, "--", p);                 // tracked-in-BASE (e.g. .claude/settings.json) → restore original bytes
  } else {
    // -r + recursive:true so an untracked out-of-scope DIRECTORY (porcelain collapses it to `evil/`) is fully
    // removed — a non-recursive rmSync throws EISDIR on Windows (swallowed here), leaving the payload on disk
    // for the repo-wide Stop hook to commit (§15.41 persistence hole — was closed for files, not dirs).
    gitArgs("rm", "-r", "-f", "--ignore-unmatch", p);   // tracked-new file OR dir → unstage+delete
    try { const abs = `${GIT_CWD}/${p}`; if (existsSync(abs)) rmSync(abs, { recursive: true, force: true }); } catch {} // untracked-new (evil.ps1 / evil/ dir / control junk) → unlink
  }
}

// §SPLIT / §15i 15.41/15.49 + §15c-2 revert reconciliation — two-repo revert that restores BASE WITHOUT nuking human
// WIP, gitignored runtime, or the live asset store:
//  (a) GAME repo: `ggit reset --hard GBASE` + `clean -fd` (NEVER -x) — the game is its own repo, so a whole-repo
//      reset is safe and complete (untracked-IGNORED node_modules/dist/derived survive; untracked worker files drop).
//  (b) CONTROL (C:/Vibes): surgically restore/remove ONLY the SPECIFIC out-of-allowlist paths (evil.ps1,
//      .claude/settings.json, worker-planted control junk) — never a blanket clean. allowed() already excludes the
//      legit control/assets store, so a concurrent GUI upload + claims/active/latch runtime are preserved.
function fullRevert(outOfScopeControlPaths = []) {
  // §SPLIT — GAME side: hard-reset the game repo to its BASE (discard the worker's tracked+staged edits; untracked-
  // IGNORED files — node_modules/dist/derived art — survive), then drop untracked worker files (NEVER -x). GBASE ==
  // game HEAD in serial (no game commit yet this tick) / the worktree's detach commit in parallel.
  assertGameToplevel();
  ggitArgs("reset", "--hard", GBASE);
  ggitArgs("clean", "-fd");
  // CONTROL side (C:/Vibes): surgically restore/remove ONLY the out-of-allowlist paths (evil.ps1, .claude/settings.json,
  // worker-planted control junk) — never a blanket clean, so a concurrent GUI upload under control/assets survives.
  assertControlToplevel();
  const extra = new Set(outOfScopeControlPaths);
  extra.add(SETTINGS_REL);                          // always re-assert settings.json even if it was staged/committed mid-tick
  for (const p of extra) revertOnePath(p);
}

function commit(msg) {
  // §15i 15.47: one git-tree lock across ALL main-tree committers (bookkeep/plan-apply/ingest/Stop hook)
  // — serialize the stage+commit critical section so a concurrent committer can't race on .git/index.lock.
  // §15g phase 8: the single-committer merge (merge.mjs) already HOLDS git-tree across apply→bookkeep so the
  // patch-onto-HEAD and the commit are ONE atomic critical section (currentHEAD can't move under us). lock.mjs is
  // NON-REENTRANT, so bookkeep-as-a-subprocess-of-merge must NOT re-acquire it (→ self-deadlock). CC_GITTREE_HELD=1
  // says "the caller holds git-tree" → skip the acquire/release here. Serial/normal bookkeep (flag unset) is unchanged.
  const held = process.env.CC_GITTREE_HELD === "1";
  const lk = held ? null : acquire(LOCKS, "git-tree");
  try {
    gitArgs("add", "-A", "cosmo-canyon");
    // commit only if something is staged (avoid empty-commit error)
    const staged = gitQuiet("diff --cached --name-only").trim();
    if (!staged) { return null; }
    // §15i 15.44: message via stdin (`git commit -F -`), argv-array shell:false — the interpolated bead.title
    // never reaches a shell (no injection, no %VAR% expansion). sanitize() strips metachars/newlines for tidy logs.
    const r = spawnSync("git", ["-C", GIT_CWD, "commit", "-q", "-F", "-"], { input: sanitize(msg), encoding: "utf8", maxBuffer: 64 * 1024 * 1024 });
    if (r.status !== 0) throw new Error(`git commit failed: ${((r.stderr || "") + (r.stdout || "")).slice(0, 200)}`);
    return gitQuiet("rev-parse HEAD").trim();
  } finally { if (lk) release(lk); }
}

// §SPLIT — the GAME committer: stage + commit the game repo (the landed increment; the game's provenance sha).
// Sole game committer in serial; the single-committer merge is the sole game committer in parallel. Message via
// stdin (shell:false), sanitized. Returns the new game HEAD sha, or null if nothing was staged.
function commitGame(msg) {
  ggitArgs("add", "-A");
  const staged = ggitQuiet("diff --cached --name-only").trim();
  if (!staged) return null;
  const r = spawnSync("git", ["-C", GAME, "commit", "-q", "-F", "-"], { input: sanitize(msg), encoding: "utf8", maxBuffer: 64 * 1024 * 1024 });
  if (r.status !== 0) throw new Error(`game commit failed: ${((r.stderr || "") + (r.stdout || "")).slice(0, 200)}`);
  return ggitQuiet("rev-parse HEAD").trim();
}

// dirty paths under the repo right now (worker footprint; supervisor guaranteed a clean tree at tick start).
// §L1 — use NUL-delimited porcelain (-z): no path-quoting, and a rename/copy `R old -> new` is emitted as two
// separate NUL fields (new path, then origin path). The old `slice(3).trim()` produced the literal string
// "old -> new" for a rename → an un-revertable bogus path. Here we surface BOTH real paths so revertOnePath
// (which operates vs BASE) can restore the origin AND remove the destination.
// §SPLIT — dirtyPaths() = the C:/Vibes control tree (git, GIT_CWD=REPO); the game is gitignored here, so this is
// purely the out-of-scope surface (control writes + any stray junk). gameDirtyPaths() = the worker's footprint
// INSIDE the game repo (ggit). Same NUL-porcelain rename handling in both.
function parseDirty(porcelainZ) {
  const fields = porcelainZ.split("\0");
  const out = [];
  for (let i = 0; i < fields.length; i++) {
    const rec = fields[i];
    if (!rec) continue;
    const xy = rec.slice(0, 2);
    const p = rec.slice(3);                              // strip "XY " (2 status chars + 1 space)
    if (xy[0] === "R" || xy[0] === "C" || xy[1] === "R" || xy[1] === "C") {
      const orig = fields[++i];                          // rename/copy: the ORIGIN path is the next NUL field
      if (orig) out.push(orig);
    }
    if (p) out.push(p);
  }
  return out;
}
function dirtyPaths() { return parseDirty(gitQuiet("status --porcelain -z")); }       // C:/Vibes control tree
function gameDirtyPaths() { return parseDirty(ggitQuiet("status --porcelain -z")); }  // §SPLIT — inside the game repo

// §SPLIT — numstat (added+deleted) of staged GAME changes vs GBASE, in the game repo — counts modified AND new files
function workerDiffLines() {
  ggit("add -A"); // stage so new files are counted (game .gitignore excludes node_modules/dist/derived art)
  const out = ggitQuiet(`diff --cached ${GBASE} --numstat`);
  let lines = 0;
  for (const row of out.split("\n")) {
    const m = row.match(/^(\d+|-)\t(\d+|-)\t/);
    if (m) lines += (m[1] === "-" ? 0 : +m[1]) + (m[2] === "-" ? 0 : +m[2]);
  }
  return lines;
}

function runGate() {
  // §15i 15.44: argv-array, shell:false (was the shell string "npm run gate"). §15g-T: child TIME-BOX.
  const r = spawnSync(NPM, ["run", "gate"], { cwd: GAME, shell: false, encoding: "utf8", maxBuffer: 64 * 1024 * 1024, timeout: GATE_TIMEOUT_MS, killSignal: "SIGKILL" });
  const out = `${r.stdout || ""}\n${r.stderr || ""}`;
  const timedOut = r.error && (r.error.code === "ETIMEDOUT" || r.signal === "SIGKILL");
  const pass = r.status === 0 && !timedOut;
  let failedTest = "";
  if (!pass) {
    // §15g-T terse reporter: the FIRST failing assertion / TS error, ONE line — never a full log dump.
    const fl = out.split("\n").find((l) => /FAIL[:\s]/i.test(l) || /error TS\d+/.test(l));
    failedTest = timedOut ? `gate TIMED OUT after ${(GATE_TIMEOUT_MS / 1000) | 0}s (killed)` : (fl || `exit ${r.status}${r.error ? " " + r.error.code : ""}`).slice(0, 200);
  }
  return { pass, failedTest, tail: out.split("\n").slice(-6).join("\n") };
}

// Run a .ts grader (relative to game/) via tsx (fallback plain node), argv-array shell:false + child TIME-BOX.
// extraArgs are passed to the grader (image/audio graders take the manifestKey; spec graders take the beadId).
// Returns { pass, out, timedOut, note } — note is the TERSE last-line summary (§15g-T).
function runGraderScript(rel, extraArgs = []) {
  const argv = existsSync(TSX_CLI) ? [TSX_CLI, rel, ...extraArgs] : [rel, ...extraArgs];
  const r = spawnSync(process.execPath, argv, { cwd: GAME, shell: false, encoding: "utf8", maxBuffer: 32 * 1024 * 1024, timeout: ACCEPT_TIMEOUT_MS, killSignal: "SIGKILL" });
  const out = `${r.stdout || ""}\n${r.stderr || ""}`;
  const timedOut = r.error && (r.error.code === "ETIMEDOUT" || r.signal === "SIGKILL");
  const fail1 = out.split("\n").find((l) => /^FAIL[:\s]/i.test(l) || /FAIL /.test(l));
  const note = timedOut ? `grader TIMED OUT (killed)` : (fail1 || out.split("\n").filter(Boolean).slice(-1)[0] || "no output").slice(0, 200);
  return { pass: r.status === 0 && !timedOut, out, timedOut, note };
}

// §15.15 — has the operator CONFIRMED this planner-authored grader? (control/.grader-confirm.json {beadId:{...}})
function graderConfirmed(beadId) {
  const m = readJson(GRADER_CONFIRM(), {});
  const e = m && m[beadId];
  return !!(e && (e === true || e.confirmed === true));
}

// §15.17 — the grader's LAST non-empty stdout line must be exactly `ACCEPT-PASS <beadId>` (a positive assertion
// that the grader actually ran its checks — exit-0-without-asserting is a silent pass and is rejected).
function hasAcceptPassToken(out, beadId) {
  const lines = String(out || "").split("\n").map((l) => l.trim()).filter(Boolean);
  const last = lines[lines.length - 1] || "";
  return last === `ACCEPT-PASS ${beadId}`;
}

// §15.15 MUTATION CHECK — run the grader against the UNIMPLEMENTED BASE tree; it MUST FAIL there (else it does
// not actually depend on the implementation → tautological → reject). Deterministic + isolated: a detached
// worktree at BASE (explicit-path-only, 15.43), the MAIN tree's tsx + a node_modules junction so transitive
// imports resolve, hard TIME-BOX. Returns { ok, passedOnBase, error }.
function runMutationCheck(rel, beadId) {
  const wtId = `mutcheck-${beadId}`.replace(/[^A-Za-z0-9._-]/g, "-").slice(0, 60);
  // §SPLIT — the mutation-check worktree is a checkout of the GAME repo at its BASE (GBASE), so the grader runs
  // against the UNIMPLEMENTED game tree. wtCreate's default repo is already the game repo; pass GAME explicitly.
  const wtOpts = { repo: GAME, root: process.env.CC_MUTCHECK_ROOT || "C:/Vibes-cc-wt" };
  let wt;
  try { wt = wtCreate(wtId, GBASE, wtOpts); } catch (e) { return { ok: false, error: `worktree create threw: ${String((e && e.message) || e).slice(0, 120)}` }; }
  if (!wt || !wt.ok) return { ok: false, error: `worktree create failed: ${(wt && wt.error) || "?"}` };
  try {
    const wtGame = wt.path; // §SPLIT — the worktree IS the game repo root (no cosmo-canyon/game subpath)
    const graderPath = `${wtGame}/${rel}`;
    if (!existsSync(graderPath)) return { ok: false, error: `grader ${rel} absent at BASE (must be committed before dispatch)` };
    // NO node_modules in the worktree: tsx (from the MAIN tree's TSX_CLI) resolves esbuild + its own deps from the
    // main tree, and a sim grader imports only ../src/sim (worktree src, dep-free). Do NOT junction node_modules
    // into the worktree — removing the worktree would follow the junction and DELETE the shared node_modules.
    // (audit) Pass bead.id ARGV so the BASE run is the SAME invocation as the real grade (an argv-dependent grader
    // must reach its actual check at BASE, else an early argv guard fakes "fails-on-base").
    const argv = existsSync(TSX_CLI) ? [TSX_CLI, graderPath, beadId] : [graderPath, beadId];
    const r = spawnSync(process.execPath, argv, { cwd: wtGame, shell: false, encoding: "utf8", maxBuffer: 32 * 1024 * 1024, timeout: MUTCHECK_TIMEOUT_MS, killSignal: "SIGKILL" });
    const out = `${r.stdout || ""}\n${r.stderr || ""}`;
    // (audit) fail-CLOSED on a killed/errored BASE run — a timeout/spawn-fail must NOT read as "correctly failed on
    // base" (that would silently skip the tautology gate). Mirror runGraderScript's timedOut handling.
    if (r.error && (r.error.code === "ETIMEDOUT" || r.signal === "SIGKILL")) return { ok: false, error: `mutation-check BASE run TIMED OUT (killed) — indeterminate` };
    if (r.error) return { ok: false, error: `mutation-check spawn error on BASE: ${r.error.code || r.error.message}` };
    if (r.status === 0) return { ok: true, passedOnBase: true, out: out.slice(-200) }; // passed on BASE → tautological
    // (audit) non-zero at BASE must be an HONEST assertion failure (a FAIL:/ACCEPT-FAIL marker), NOT a module-load
    // crash (e.g. a grader importing a HEAD-only symbol throws at BASE for the WRONG reason). A crash with no fail
    // marker is INDETERMINATE → fail-closed (cannot prove the grader actually tests the implementation).
    const honestFail = /(^|\n)\s*FAIL[:\s]/.test(out) || out.includes(`ACCEPT-FAIL ${beadId}`) || /\bFAIL\b/.test(out);
    if (!honestFail) return { ok: false, error: `grader crashed/errored on BASE with no FAIL assertion — indeterminate (cannot prove non-tautology); a grader must print FAIL when the feature is absent` };
    return { ok: true, passedOnBase: false, out: out.slice(-200) }; // honest fail on BASE → NOT tautological
  } catch (e) {
    return { ok: false, error: String((e && e.message) || e).slice(0, 120) };
  } finally {
    try { wtRemove(wt.path, wtOpts); } catch { /* best-effort cleanup */ }
  }
}

// §15e DERIVE-BIND (runs in the COMMITTER tree, serialized on the manifest/audio-manifest lock inside derive*.mjs
// — 15.37): copy the asset's uploaded control-plane bytes → game source + a manifest slot (parse-instructions
// config), then derive so the manifest flips REAL. This is the deterministic bridge that makes an uploaded image/
// audio actually render-reachable BEFORE its grader runs; the worker only WIRES getTexture (a src edit).
function deriveBindAsset(bead, assetMeta) {
  const key = bead.manifestKey || assetMeta.manifestKey || null;
  if (!key) return { ok: false, error: "no manifestKey (cannot bind)" };
  const bytesPath = assetMeta.file ? `${CONTROL}/assets/${bead.assetId}/${assetMeta.file}` : null;
  if (!bytesPath || !existsSync(bytesPath)) return { ok: false, error: `asset bytes missing (${assetMeta.file || "none"})` };
  const env = { ...process.env, CC_CONTROL: CONTROL };
  if (bead.assetKind === "image") {
    const cfgPath = `${CONTROL}/.derive-cfg-${bead.assetId}.json`;
    try { writeJson(cfgPath, manifestEntryFor(key, assetMeta)); } catch (e) { return { ok: false, error: `cfg write: ${e.message}` }; }
    const r = spawnSync(process.execPath, [`${GAME}/derive.mjs`, "--bind", "--key", key, "--src", bytesPath, "--config", cfgPath], { cwd: GAME, shell: false, encoding: "utf8", maxBuffer: 16 * 1024 * 1024, timeout: DERIVE_TIMEOUT_MS, killSignal: "SIGKILL", env });
    try { rmSync(cfgPath, { force: true }); } catch {}
    if (r.status !== 0) return { ok: false, error: `${(r.stdout || "") + (r.stderr || "")}`.split("\n").filter(Boolean).slice(-1)[0] || `exit ${r.status}` };
    return { ok: true, changed: !/idempotent no-op/.test(`${r.stdout || ""}${r.stderr || ""}`) };
  }
  if (bead.assetKind === "audio") {
    const r = spawnSync(process.execPath, [`${GAME}/derive-audio.mjs`, "--bind", "--key", key, "--src", bytesPath], { cwd: GAME, shell: false, encoding: "utf8", maxBuffer: 16 * 1024 * 1024, timeout: DERIVE_TIMEOUT_MS, killSignal: "SIGKILL", env });
    if (r.status !== 0) return { ok: false, error: `${(r.stdout || "") + (r.stderr || "")}`.split("\n").filter(Boolean).slice(-1)[0] || `exit ${r.status}` };
    return { ok: true, changed: !/idempotent no-op/.test(`${r.stdout || ""}${r.stderr || ""}`) };
  }
  return { ok: false, error: `not an image/audio bead (${bead.assetKind})` };
}

// §15.18 — enqueue a feel/visual spec land into the human-gated FEEL-REVIEW queue (advisory only; the operator
// confirms via /assets/feel-confirm to flip Implemented — a model verdict NEVER lands a green). Appends a queue
// entry + a FEEL-REVIEW.md line. Under the completions lock (a control write bookkeep already owns in this pass).
function enqueueFeelReview(bead, note) {
  const dir = acquire(LOCKS, "completions");
  try {
    const q = readJson(FEEL_REVIEW_JSON(), []);
    const arr = Array.isArray(q) ? q : [];
    if (!arr.some((e) => e && e.beadId === bead.id)) {
      arr.push({ beadId: bead.id, assetId: bead.assetId || null, title: bead.title || "", note: sanitize(note).slice(0, 200), rev: bead.rev ?? null, at: nowIso(), confirmed: false });
      writeJson(FEEL_REVIEW_JSON(), arr);
    }
  } finally { release(dir); }
  const md = `${GAME}/docs/FEEL-REVIEW.md`;
  try {
    const line = `- [ ] **${sanitize(bead.title)}** (${nowIso().slice(0, 10)}) [${bead.id}] — critic ADVISORY: ${sanitize(note).slice(0, 120)}. Operator: review the snapshot, then confirm to flip Implemented.\n`;
    const cur = existsSync(md) ? readFileSync(md, "utf8") : "# FEEL-REVIEW queue (§15.18)\n\nHuman-gated. A model critic verdict is ADVISORY — only an operator confirm flips a feel/visual spec Implemented.\n\n";
    atomicWrite(md, cur + line);
  } catch {}
}

// §15c/§15e — the acceptance authority. Routes by the bead's asset/acceptance kind:
//   image/audio asset bead → the SHARED deterministic grader (`_image-grader`/`_audio-grader` <manifestKey>);
//   spec feel/visual       → ADVISORY (land on gate-green, feelAdvisory:true → FEEL-REVIEW; operator confirms);
//   spec sim-checkable     → the planner-authored per-bead grader, CONFIRM-gated + MUTATION-checked + PASS-token;
//   assetKey bead w/ no grader → FAIL-CLOSED (never auto-pass, §15.13);
//   legacy non-asset bead  → the existing acceptanceCmd / accept/<id>.ts auto-discovery (honest skip if none).
function runAcceptance(bead) {
  if (!bead) return { pass: true, skipped: true, note: "no bead" };
  const assetKind = bead.assetKind || null;
  const acceptanceKind = bead.acceptanceKind || null;
  const key = bead.manifestKey || null;

  // ── image/audio → the auto-minted shared parameterized grader (by manifestKey) ──
  if (assetKind === "image" || assetKind === "audio") {
    if (!key) return { pass: false, skipped: false, note: `${assetKind} bead has no manifestKey — cannot grade (fail-closed, §15e)` };
    const grader = assetKind === "image" ? "accept/_image-grader.ts" : "accept/_audio-grader.ts";
    if (!existsSync(`${GAME}/${grader}`)) return { pass: false, skipped: false, note: `shared grader missing: ${grader} (fail-closed)` };
    const r = runGraderScript(grader, [key, bead.id]);
    return { pass: r.pass, skipped: false, note: r.note };
  }

  // ── resolve a per-bead grader (explicit acceptanceCmd or auto-discovered accept/<bead.id>.ts) ──
  let rel = null;
  if (bead.acceptanceCmd) {
    const cmd = String(bead.acceptanceCmd).trim();
    if (!ACCEPT_CMD_RE.test(cmd)) return { pass: false, skipped: false, note: `rejected acceptanceCmd (must match "node accept/<id>.ts"): ${sanitize(cmd).slice(0, 80)}` };
    rel = cmd.split(" ")[1];
  } else if (/^[A-Za-z0-9_-]+$/.test(String(bead.id || "")) && existsSync(`${GAME}/accept/${bead.id}.ts`)) {
    rel = `accept/${bead.id}.ts`;
  }

  // ── spec feel/visual → ADVISORY (never a model self-attest green; routes to FEEL-REVIEW) ──
  if (assetKind === "spec" && acceptanceKind === "feel") {
    // land on gate-green; the critic verdict (if a grader exists) is recorded as advisory, but NEVER gates the
    // Implemented flip — only an operator confirm does (15.18). We DO run any confirmed+mutation-checked grader
    // to surface its advisory verdict, but pass/fail of it does not block the land.
    let advisory = "no critic grader (snapshot + manual review)";
    if (rel && graderConfirmed(bead.id)) {
      const r = runGraderScript(rel, [bead.id]);
      advisory = `critic ${r.pass ? "passed" : "flagged"}: ${r.note}`;
    }
    return { pass: true, skipped: false, feelAdvisory: true, note: `feel/visual ADVISORY → FEEL-REVIEW (${advisory})` };
  }

  if (!rel) {
    // §15c/15.13 — an assetKey/spec bead with NO grader is FAIL-CLOSED (never auto-pass). A legacy non-asset
    // bead with no grader keeps the honest skip (most current hand beads have none — never hard-block those).
    if (bead.assetId || assetKind) return { pass: false, skipped: false, note: "assetKey/spec bead has no acceptance grader — fail-closed (§15c/15.13)" };
    return { pass: true, skipped: true, note: "unverified: no grader, no acceptanceCmd" };
  }

  // ── a per-bead grader EXISTS. A planner-authored spec grader is CONFIRM-gated + MUTATION-checked + PASS-token. ──
  if (bead.graderNeedsConfirm) {
    if (!graderConfirmed(bead.id)) return { pass: false, skipped: false, note: `grader ${rel} DISABLED until operator confirm (15.15)` };
    const mut = runMutationCheck(rel, bead.id);
    if (!mut.ok) return { pass: false, skipped: false, note: `mutation-check could not run (fail-closed): ${mut.error}` };
    if (mut.passedOnBase) return { pass: false, skipped: false, note: `grader ${rel} is TAUTOLOGICAL — passes on the unimplemented BASE tree (rejected, 15.15)` };
  }
  const r = runGraderScript(rel, bead.graderNeedsConfirm ? [bead.id] : []);
  if (bead.graderNeedsConfirm && r.pass && !hasAcceptPassToken(r.out, bead.id)) {
    return { pass: false, skipped: false, note: `grader exited 0 but no 'ACCEPT-PASS ${bead.id}' token on the last line (silent pass rejected, 15.17)` };
  }
  return { pass: r.pass, skipped: false, note: r.note };
}

// ── bead bookkeeping (locked: §13.43) ──
function bumpAttempt(bead, reason) {
  bead.attempts = (bead.attempts || 0) + 1;
  bead.status = bead.attempts >= ABANDON_N ? "abandoned" : "blocked"; // §13.32 terminal state
  bead.blocked_reason = reason;
  bead.updated = nowIso();
}

function persistBacklog(backlog) {
  const dir = acquire(LOCKS, "backlog");
  try { writeJson(`${CONTROL}/backlog.json`, backlog); } finally { release(dir); }
}

function recordCompletion(bead, citation, acceptanceSkipped, feelAdvisory = false) {
  const dir = acquire(LOCKS, "completions");
  try {
    const comp = readJson(`${CONTROL}/completions.json`, []);
    // §15i 15.50/§15e — persist the derived-Implemented projection inputs. `sha` is bound AFTER the landing
    // commit by patchCompletionSha() (the commit sha doesn't exist at record time — do NOT fake it here).
    // assetKey/contentHash/rev come off the ASSET bead (projectAssetToBead bound them at mint); null for a
    // non-asset bead. acceptanceSkipped (from runAcceptance, 15.13) feeds the fail-closed predicate: skipped
    // acceptance = NOT-implemented (§15e implemented()). feelAdvisory (15.18): a feel/visual spec land is
    // advisory → the predicate withholds Implemented until an OPERATOR feel-confirm (never a model green).
    comp.unshift({
      id: bead.id, title: bead.title, acceptance: bead.acceptance || "", result: citation, ts: nowIso(),
      sha: null, assetKey: bead.assetKey ?? null, contentHash: bead.contentHash ?? null, rev: bead.rev ?? null,
      acceptanceSkipped: acceptanceSkipped === true, feelAdvisory: feelAdvisory === true,
    });
    writeJson(`${CONTROL}/completions.json`, comp);
  } finally { release(dir); }
  // DONE.md prepend (game-local landed log)
  const donePath = `${GAME}/docs/DONE.md`;
  if (existsSync(donePath)) {
    const cur = readFileSync(donePath, "utf8");
    const entry = `- [x] **${bead.title}** (${nowIso().slice(0, 10)}) [${bead.id}]: ${bead.detail || ""} — ${citation}\n\n`;
    const marker = "archived to DONE-archive.md)\n";
    const idx = cur.indexOf(marker);
    const next = idx >= 0 ? cur.slice(0, idx + marker.length) + "\n" + entry + cur.slice(idx + marker.length + 1) : entry + cur;
    atomicWrite(donePath, next);
  }
}

// §15i 15.50 — bind the landing commit sha into the just-recorded completion entry, under the completions lock.
// The sha is created by commit() AFTER recordCompletion, so this patches it in post-commit (idempotent: only the
// front entry for this id whose sha is still null). Leaves completions.json patched-DIRTY; the RALPH_PASS Stop
// hook folds it into a trailing `ralph <id>:` commit (completions.json is an ALLOWED control path — not tamper).
function patchCompletionSha(id, sha) {
  const dir = acquire(LOCKS, "completions");
  try {
    const comp = readJson(`${CONTROL}/completions.json`, []);
    const e = comp.find((c) => c.id === id && !c.sha); // the freshly-unshifted entry (sha still null)
    if (e) { e.sha = sha; writeJson(`${CONTROL}/completions.json`, comp); }
  } finally { release(dir); }
}

// §15e — the Implemented provenance flip, bundled with the bead-terminal (backlog) write. Held under
// acquireOrdered(['asset-<id>','backlog']) (Invariant-L order) so the two writes are ONE crash-self-healing
// critical section vs a CONCURRENT writer; for serial the single landing COMMIT provides atomicity (crash
// before commit → BASE revert undoes both; after → both landed). writeImplementedProvenanceHeld is the no-lock
// companion (markImplemented would double-lock the non-reentrant asset-<id>). sha=null here (created by commit()
// after) → bound post-commit by bindImplementedSha. Index rebuilt SEPARATELY after release (assets-index rank 2
// must never nest under asset-<id> rank 3 / backlog rank 5).
function landAssetProvenance(bead, remaining, meta) {
  const held = acquireOrdered(LOCKS, [`asset-${bead.assetId}`, "backlog"]);
  try {
    writeImplementedProvenanceHeld(bead.assetId, { beadId: bead.id, sha: null, contentHash: meta.contentHash, rev: meta.rev });
    writeJson(`${CONTROL}/backlog.json`, remaining); // bead-terminal write under the SAME held backlog lock
  } finally { releaseAll(held); }
  try { rebuildIndex(); } catch {} // separate assets-index step (self-healing — a fault is repaired next /assets/list)
}

// §15.39 — the in-flight asset bead was minted at bead.contentHash; if the asset was REPLACED since (its
// meta.contentHash differs) the bead graded STALE bytes → fail-closed (it should have been superseded on the rev
// bump — this is the deterministic backstop). A no-bytes asset (both null) passes.
function assetStaleBytes(bead, meta) {
  if (bead.contentHash == null && meta.contentHash == null) return false;
  return String(bead.contentHash) !== String(meta.contentHash);
}

// §15c — the clarifying Questions for an `unsure` park: control/.unsure.json (written by ask.mjs) and/or repeated
// --question args. Falls back to a generic prompt so a bare `unsure` never parks an empty (invisible) question set.
function readUnsureQuestions(bId) {
  const out = [];
  const f = readJson(`${CONTROL}/.unsure.json`, null);
  if (f && f.beadId === bId && Array.isArray(f.questions)) {
    for (const q of f.questions) { const t = String((q && q.text) || q || "").trim(); if (t) out.push({ text: t, by: (q && q.by) || "agent" }); }
  }
  for (let i = 0; i < args.length; i++) if (args[i] === "--question" && args[i + 1]) { const t = String(args[i + 1]).trim(); if (t) out.push({ text: t, by: "agent" }); }
  if (!out.length) out.push({ text: "Worker returned unsure without a specific question — operator: clarify this asset's instructions.", by: "agent" });
  return out;
}

// ══════════════════════════════════════════════════════════════════════════════
const backlog = readJson(`${CONTROL}/backlog.json`, []);
const bead = beadId ? backlog.find((b) => b.id === beadId) : null;

// §15g phase 8 — WORKER GATE-ONLY (parallel, in the worktree). Run the SAME tamper/scope/gate checks the serial
// path runs, but LAND NOTHING: write a green/red gate MARKER next to the claim and leave the worktree DIRTY so
// the single-committer merge can read the diff and land it at post-merge HEAD (15.38). NO commit, NO revert, NO
// derive (15.37 — derive runs ONCE in the committer tree), NO provenance, NO shared status write (N workers would
// race it). Acceptance is DEFERRED to the merge (an image grader needs the merge-only derive to flip manifest
// real, so it cannot pass in a worker). The worker only writes its own marker (claims/, gitignored, allowed).
if (GATE_ONLY) {
  const markerPath = tickPath.replace(/\.claim\.json$/, ".gate.json");
  const assetId = tick.assetId || (bead && bead.assetId) || null;
  const writeMarker = (o) => { try { writeJson(markerPath, { beadId, assetId, worktree: GAME, baseSha: BASE, gameBaseSha: GBASE, at: nowIso(), ...o }); } catch {} };
  if (!bead) { writeMarker({ outcome: "red", reason: "bead-not-found" }); console.log(JSON.stringify({ outcome: "red", reason: "bead-not-found" })); process.exit(0); }
  // §SPLIT — the worker footprint is the GAME repo working tree (the worktree). Everything in it is in-scope (the
  // worker's scope IS the game); only PROTECTED game files are tamper. .claude/settings.json lives in C:/Vibes → the
  // single-committer merge re-asserts it, but flag an obvious mutation here too.
  const dirtyW = gameDirtyPaths();
  const tamperW = dirtyW.filter((p) => PROTECTED.includes(p) || p.startsWith(PROTECTED_PREFIX) || p.startsWith(SOURCE_PREFIX) || p.startsWith(PROTECTED_TEST_SIM));
  const settingsW = settingsTampered();
  const g = runGate();
  const dl = workerDiffLines();
  // an image/audio asset bead may legitimately produce ZERO worker src diff (already-wired key needing only art —
  // the merge's derive-bind does the real work), so ITS no-op is decided at merge (bindChanged). Any other bead
  // with zero diff is a genuine no-op → red (nothing to merge).
  const isAsset = !!bead.assetId && (bead.assetKind === "image" || bead.assetKind === "audio");
  let reason = null;
  if (settingsW) reason = "tamper: .claude/settings.json changed vs BASE";
  else if (tamperW.length) reason = `tamper: edited protected ${tamperW.slice(0, 3).join(", ")}`;
  else if (!isAsset && dl === 0) reason = "no-op: zero diff vs BASE";
  else if (dl > MAX_DIFF_LINES) reason = `oversized: ${dl} > ${MAX_DIFF_LINES} lines`;
  else if (!g.pass) reason = `gate fail: ${g.failedTest}`;
  const outcome = reason ? "red" : "green";
  writeMarker({ outcome, reason, diffLines: dl, gate: g.pass, assetKind: bead.assetKind || null, acceptanceKind: bead.acceptanceKind || null, manifestKey: bead.manifestKey || null });
  console.log(JSON.stringify({ outcome, reason, diffLines: dl, gate: g.pass }));
  process.exit(0);
}

if (result === "idle") {
  // no ready bead drained this tick. Leave the tree as the supervisor found it.
  const dirty = dirtyPaths();
  if (dirty.length) fullRevert(dirty.filter((p) => !allowed(p))); // defensive: a sense-only tick should not have edited anything
  removeActive({ runToken, beadId });
  writeStatus({ stage: "idle", beadId: null, note: "no ready bead", baseSha: BASE });
  console.log(JSON.stringify({ outcome: "idle" }));
  process.exit(0);
}

if (!bead) {
  writeStatus({ stage: "error", beadId, note: "bead id not in backlog", baseSha: BASE });
  console.log(JSON.stringify({ outcome: "error", reason: "bead-not-found" }));
  process.exit(0);
}

if (result === "blocked") {
  fullRevert(dirtyPaths().filter((p) => !allowed(p))); // discard any partial flailing (tree-wide)
  if (needsOperator) {
    // §GC3 — TERMINAL human gate. Do NOT bumpAttempt (not a failure) and do NOT keep it rescope-able: mark the
    // bead needsOperator (spec-core drops it from blockedIds → planner never reopens → no block↔unblock churn) +
    // hard-park the ASSET (reconcileAssets won't re-mint). status stays "blocked" (already terminal-for-dispatch),
    // so the loop honestly IDLES on it instead of spinning. Operator resolves via confirm-satisfied/reclassify/reopen.
    bead.status = "blocked"; bead.needsOperator = true;
    bead.blocked_reason = reasonArg || "needs operator (un-actionable by code edit)"; bead.updated = nowIso();
    persistBacklog(backlog);
    if (bead.assetId) { try { setOperatorBlock(bead.assetId); } catch {} }
    removeActive({ runToken, beadId: bead.id });
    const sha = commit(`ralph ${bead.id}: needs-operator (${(reasonArg || "un-actionable by code").slice(0, 80)})`);
    writeStatus({ stage: "blocked", beadId: bead.id, reason: bead.blocked_reason, status: bead.status, needsOperator: true, sha, baseSha: BASE });
    console.log(JSON.stringify({ outcome: "blocked", status: bead.status, needsOperator: true }));
    process.exit(0);
  }
  bumpAttempt(bead, reasonArg || "worker bounce-back");
  persistBacklog(backlog);
  if (bead.assetId && bead.status === "abandoned") bumpAbandon(bead.assetId, { contentHash: bead.contentHash }); // §15.3 asset ceiling
  removeActive({ runToken, beadId: bead.id });
  const sha = commit(`ralph ${bead.id}: blocked (${(reasonArg || "bounce-back").slice(0, 80)})`);
  writeStatus({ stage: "blocked", beadId: bead.id, reason: bead.blocked_reason, attempts: bead.attempts, status: bead.status, sha, baseSha: BASE });
  console.log(JSON.stringify({ outcome: "blocked", attempts: bead.attempts, status: bead.status }));
  process.exit(0);
}

if (result === "unsure") {
  // §15c / 15.34 — worker can't proceed on an ASSET bead → DETERMINISTIC park (NOT a fail): revert partials,
  // append+dedup the clarifying Questions to the ASSET (parkUnsure: hasOpenQuestions badge, dirty=false so the
  // scan never re-fires, questionRounds++/escalate), then PARK the bead (status=parked, terminal-for-loop, NO
  // attempts++, NO spin). The human answers via POST /assets/answer → ready+dirty+rev++ → reconcileAssets re-arms
  // exactly ONE fresh bead. state STAYS ready (never a needs_answer state).
  fullRevert(dirtyPaths().filter((p) => !allowed(p)));
  const questions = readUnsureQuestions(bead.id);
  let asset = null;
  if (bead.assetId) {
    try { asset = parkUnsure(bead.assetId, { questions }); } catch { asset = null; }
    if (asset && asset.escalated) { // §15.4 — 3 rounds → hard-park escalated + operator alert
      try { atomicWrite(`${CONTROL}/.needs-human`, JSON.stringify({ assetId: bead.assetId, beadId: bead.id, questionRounds: asset.questionRounds, at: nowIso() })); } catch {}
    }
  }
  bead.status = "parked"; bead.blocked_reason = "unsure: awaiting operator answer"; bead.updated = nowIso(); // NO attempts++
  persistBacklog(backlog);
  removeActive({ runToken, beadId: bead.id });
  try { rmSync(`${CONTROL}/.unsure.json`, { force: true }); } catch {}
  const sha = commit(`ralph ${bead.id}: parked (unsure, ${questions.length} question(s))`);
  writeStatus({ stage: "parked", beadId: bead.id, assetId: bead.assetId || null, questionRounds: asset ? asset.questionRounds : null, escalated: !!(asset && asset.escalated), sha, baseSha: BASE });
  console.log(JSON.stringify({ outcome: "parked", questions: questions.length, escalated: !!(asset && asset.escalated) }));
  process.exit(0);
}

if (result === "agy-noop") {
  // agy produced ZERO diff vs BASE (§13.38): headless agy auth/quota errors are invisible, so a
  // no-op pass is the quota/auth signal. Do NOT count it as a bead attempt (§13.35) — leave the bead
  // ready for failover/retry; bump a strike counter the supervisor reads to pause/failover the lane.
  fullRevert(dirtyPaths().filter((p) => !allowed(p))); // defensive: ensure clean (agy should have left nothing), tree-wide
  const strikesPath = `${CONTROL}/.agy-strikes`;
  const prev = existsSync(strikesPath) ? Number(readFileSync(strikesPath, "utf8").trim()) || 0 : 0;
  const strikes = prev + 1;
  atomicWrite(strikesPath, String(strikes));
  removeActive({ runToken, beadId: bead.id });
  writeStatus({ stage: "agy-noop", beadId: bead.id, quotaSuspect: true, agyStrikes: strikes, note: reasonArg || "agy zero-diff (quota/auth suspect)", baseSha: BASE });
  console.log(JSON.stringify({ outcome: "agy-noop", quotaSuspect: true, agyStrikes: strikes }));
  process.exit(0);
}

// ── result === "work": deterministic gate + guard + acceptance ──
// The WORKER footprint (dirty/tamper/scope) is captured BEFORE any bookkeep derive-bind write, so the bind's own
// source/manifest write (which IS under the protected SOURCE_PREFIX) is never mis-flagged as worker tamper.
const dirty = dirtyPaths();                                    // §SPLIT — C:/Vibes control tree (game is a separate repo, gitignored here)
const outOfScope = dirty.filter((p) => !allowed(p));           // any non-allowed C:/Vibes dirty path (tracked OR untracked) = out-of-scope tamper
const gdirty = gameDirtyPaths();                               // §SPLIT — the worker's footprint INSIDE the game repo
const tamperHits = gdirty.filter((p) => PROTECTED.includes(p) || p.startsWith(PROTECTED_PREFIX) || p.startsWith(SOURCE_PREFIX) || p.startsWith(PROTECTED_TEST_SIM));
const settingsTamper = settingsTampered();                     // §15i 15.41 .claude/settings.json vs BASE (pre-gate)

// §15.39 — asset-bead stale-bytes / missing-asset backstop (fail-closed). Read the asset's CURRENT meta; if the
// bytes moved since mint (or the asset was deleted mid-flight) the bead can't honestly land against it.
let assetMeta = null, assetFail = null;
if (bead.assetId) {
  try { assetMeta = readAsset(bead.assetId); } catch { assetMeta = null; }
  if (!assetMeta) assetFail = `asset ${bead.assetId} missing (deleted/tombstoned mid-flight)`;
  else if (assetStaleBytes(bead, assetMeta)) assetFail = `stale bytes: bead@${bead.contentHash || "none"} != asset@${assetMeta.contentHash || "none"} (asset replaced since mint, 15.39)`;
  // (audit) stale REV — a rev-only mutation (re-open, or a null→keyed setManifestKey) bumps rev WITHOUT changing
  // contentHash, so the stale-bytes check above is blind to it. A bead minted at an older rev must NOT land against
  // the asset's newer generation (the reopen invalidation relies on this). Fail-closed on a rev mismatch.
  else if (bead.rev != null && assetMeta.rev != null && String(bead.rev) !== String(assetMeta.rev)) assetFail = `stale rev: bead@r${bead.rev} != asset@r${assetMeta.rev} (asset re-armed/reopened since mint, 15.39)`;
  // (audit) a bead that was SUPERSEDED/parked mid-flight (e.g. by reopen) must not land a work increment.
  else if (isTerminal(bead.status)) assetFail = `bead terminal mid-flight (${bead.status}) — superseded/parked since dispatch`;
}

const gate = runGate();

// §15e DERIVE-BIND — for an image/audio asset bead, copy the upload's control bytes → game source + manifest,
// then derive (flips REAL), in the COMMITTER tree, serialized on the manifest lock (15.37). Runs AFTER the worker
// footprint is captured (its source write isn't worker tamper) and only when the worker footprint + gate are
// clean (don't bind a doomed tick). The subsequent render-reachability grader then sees the real manifest.
let bindFail = null, bindChanged = false;
const isAssetImgAud = !!bead.assetId && (bead.assetKind === "image" || bead.assetKind === "audio");
const preClean = !settingsTamper && !outOfScope.length && !tamperHits.length && gate.pass && !assetFail;
if (isAssetImgAud && assetMeta && preClean) {
  const rb = deriveBindAsset(bead, assetMeta);
  if (!rb.ok) bindFail = `derive-bind failed: ${sanitize(rb.error).slice(0, 100)}`;
  else bindChanged = rb.changed !== false;
}

const accept = bindFail ? { pass: false, skipped: false, note: bindFail } : runAcceptance(bead);

// diffLines is counted AFTER derive-bind so the bound source/manifest is part of the landed diff. (audit) An
// image/audio asset bead may legitimately have ZERO *worker* src diff (an already-wired key that just needs art),
// so its no-op is "no worker diff AND derive-bind changed nothing". HARDENING (no-op-vs-satisfied): an
// already-real+already-wired asset re-dispatched with zero real work is NOT a no-op FAILURE when its deterministic
// acceptance grader still PASSES — it is genuinely satisfied, so RE-CONFIRM (land at the new rev, which clears
// dirty) instead of revert+abandon (which mislabels grader-verified work as failed and churns the loop; e.g. an
// instructions edit bumps rev+dirty on a done asset → its re-projected bead can only produce zero diff). Only
// zero-diff + no bind change + FAILING grader is a real no-op (a fresh/unwired asset the worker never wired). A
// spec/non-asset bead is a no-op on zero diff.
const diffLines = workerDiffLines();
const noop = diffLines === 0 && (isAssetImgAud ? (!bindChanged && !accept.pass) : true);

let fail = null;
// tamper checks FIRST (an adversarial out-of-tree write must be reported as tamper, not masked as no-op)
if (settingsTamper) fail = "tamper: .claude/settings.json changed vs BASE";
else if (outOfScope.length) fail = `tamper: out-of-scope edit ${outOfScope.slice(0, 3).join(", ")}`;
else if (tamperHits.length) fail = `tamper: edited protected ${tamperHits.slice(0, 3).join(", ")}`;
else if (noop) fail = "no-op: worker produced zero diff vs BASE_SHA";
else if (diffLines > MAX_DIFF_LINES) fail = `oversized pass: ${diffLines} > ${MAX_DIFF_LINES} lines`;
else if (!gate.pass) fail = `gate fail: ${gate.failedTest}`;
else if (!accept.pass) fail = `acceptance fail: ${accept.note}`;
else if (assetFail) fail = assetFail; // §15.39 stale/missing asset → fail-closed

if (fail) {
  fullRevert(outOfScope); // scoped game revert + surgically remove/restore out-of-allowlist paths (evil.ps1, settings.json)
  bumpAttempt(bead, fail);
  persistBacklog(backlog);
  if (bead.assetId && bead.status === "abandoned") bumpAbandon(bead.assetId, { contentHash: bead.contentHash }); // §15.3 asset ceiling
  removeActive({ runToken, beadId: bead.id });
  const sha = commit(`ralph ${bead.id}: revert (${fail.slice(0, 80)})`);
  writeStatus({ stage: "reverted", beadId: bead.id, reason: fail, diffLines, attempts: bead.attempts, status: bead.status, gate: gate.pass, sha, baseSha: BASE });
  console.log(JSON.stringify({ outcome: "reverted", reason: fail, attempts: bead.attempts, status: bead.status }));
  process.exit(0);
}

// ── PASS-guard: re-assert .claude/settings.json vs BASE at land (gate/acceptance ran repo code that could
//    have mutated it after the pre-gate check) — mismatch → tamper-revert, never land (§15i 15.41). ──
if (settingsTampered()) {
  const fail2 = "tamper: .claude/settings.json changed during gate/acceptance";
  fullRevert(dirtyPaths().filter((p) => !allowed(p)));
  bumpAttempt(bead, fail2);
  persistBacklog(backlog);
  if (bead.assetId && bead.status === "abandoned") bumpAbandon(bead.assetId, { contentHash: bead.contentHash });
  removeActive({ runToken, beadId: bead.id });
  const sha = commit(`ralph ${bead.id}: revert (${fail2.slice(0, 80)})`);
  writeStatus({ stage: "reverted", beadId: bead.id, reason: fail2, diffLines, attempts: bead.attempts, status: bead.status, gate: gate.pass, sha, baseSha: BASE });
  console.log(JSON.stringify({ outcome: "reverted", reason: fail2, attempts: bead.attempts, status: bead.status }));
  process.exit(0);
}

// ── PASS: land the increment ──
try { const sp = `${CONTROL}/.agy-strikes`; if (existsSync(sp)) atomicWrite(sp, "0"); } catch {} // a landed pass clears quota suspicion
const citation = `gate PASS; ${accept.skipped ? "acceptance: " + accept.note : "acceptance PASS (" + accept.note + ")"}; ${diffLines} diff lines`;
const remaining = backlog.filter((b) => b.id !== bead.id);
bead.status = "done";
bead.updated = nowIso();
// §15e — for an ASSET bead, the bead-terminal (backlog) write is bundled with the Implemented PROVENANCE flip
// under acquireOrdered(['asset-<id>','backlog']), then the completion is recorded; for a plain bead, the ordinary
// completion + backlog write. Both land in the SAME commit below (serial crash-atomicity).
// GUARDED: landAssetProvenance can THROW — a concurrent GUI hold on asset-<id> (lock busy after retries) or a
// DELETE that tombstones the asset in the readAsset→land window (readMeta "no asset"). An uncaught throw here
// would crash bookkeep AFTER a completion write, leaving a PHANTOM completion + a re-dispatched bead. So on a
// throw: revert the game edit, record NO completion, bump the attempt (bounded retry → eventual abandon), leave a
// clean tree. The next tick re-reads the asset (missing → assetFail fail-path; present → clean retry).
const feelAdvisory = accept.feelAdvisory === true;
if (bead.assetId && assetMeta) {
  try {
    landAssetProvenance(bead, remaining, assetMeta);
    recordCompletion(bead, citation, accept.skipped, feelAdvisory);
  } catch (e) {
    const why = `asset-land failed: ${sanitize(String((e && e.message) || e)).slice(0, 100)}`;
    fullRevert(dirtyPaths().filter((p) => !allowed(p)));
    bumpAttempt(bead, why); // resets bead.status (blocked/abandoned) → stays in backlog, retryable/bounded
    persistBacklog(backlog);
    if (bead.status === "abandoned") bumpAbandon(bead.assetId, { contentHash: bead.contentHash });
    removeActive({ runToken, beadId: bead.id });
    const shaR = commit(`ralph ${bead.id}: revert (${why.slice(0, 80)})`);
    writeStatus({ stage: "reverted", beadId: bead.id, reason: why, diffLines, attempts: bead.attempts, status: bead.status, gate: true, sha: shaR, baseSha: BASE });
    console.log(JSON.stringify({ outcome: "reverted", reason: why, attempts: bead.attempts, status: bead.status }));
    process.exit(0);
  }
} else {
  recordCompletion(bead, citation, accept.skipped, feelAdvisory);
  persistBacklog(remaining);
}
// §15.18 — a feel/visual spec land is ADVISORY: route it to the human-gated FEEL-REVIEW queue. The provenance
// was written, but implemented() withholds Implemented until an operator confirm (a model verdict never greens).
if (feelAdvisory) { try { enqueueFeelReview(bead, accept.note); } catch {} }
removeActive({ runToken, beadId: bead.id });
// §SPLIT — land the game increment in the GAME repo FIRST (its provenance sha), bind that into the control
// provenance, then commit the control bookkeeping (backlog/completions/meta) to C:/Vibes in ONE control commit.
const gameSha = commitGame(`ralph ${bead.id}: ${bead.title}`);
if (gameSha) { patchCompletionSha(bead.id, gameSha); if (bead.assetId) { try { bindImplementedSha(bead.assetId, gameSha); } catch {} } }
const sha = commit(`ralph ${bead.id}: ${bead.title}`);
writeStatus({ stage: "committed", beadId: bead.id, title: bead.title, assetId: bead.assetId || null, diffLines, gate: true, acceptance: accept.skipped ? "skipped" : "pass", citation, sha: gameSha || sha, ctlSha: sha, baseSha: BASE, gameBaseSha: GBASE });
console.log(JSON.stringify({ outcome: "committed", sha: gameSha, ctlSha: sha, diffLines, assetId: bead.assetId || null }));
process.exit(0);
