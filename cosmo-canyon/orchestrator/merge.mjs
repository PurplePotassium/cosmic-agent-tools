// Cosmo Canyon — SINGLE-COMMITTER MERGE (§15g phase 8 / §15c-2 / 15.37/15.38). The sole committer of parallel work.
//
// N workers gate IN their isolated worktrees (bookkeep --gate-only) and land NOTHING. This module — running in
// the MAIN tree, ONE claim at a time, holding the `git-tree` lock (rank 8) across each landing — takes each green
// worktree's diff, applies it onto the CURRENT HEAD, and re-runs the FULL audited serial bookkeep at post-merge
// HEAD (gate + acceptance + derive + commit + provenance). Reusing bookkeep as the committer means the actual
// git surgery is BYTE-FOR-BYTE the serial path (baseSha=HEAD, no worktree), so the merge adds orchestration only —
// no new destructive git code. Key seams:
//  • the merge holds git-tree across apply→bookkeep (CC_GITTREE_HELD=1 → bookkeep.commit skips the re-acquire →
//    no self-deadlock); currentHEAD can't move under an interleaving GUI-ingest committer (15.38).
//  • RE-RUN acceptance at post-merge HEAD (bookkeep does gate AND acceptance) — a green build in isolation ≠ a
//    working feature at the seam (15.38). derive runs ONCE here, in the committer tree, after the bind (15.37).
//  • manifest.json/audio-manifest.json are EXCLUDED from the applied worker patch — they are derive-owned global
//    reductions (15.37); the merge's bookkeep derive-binds them from the manifest. Only SRC lines are applied.
//  • backlog is written by the single committer under its lock (serialized per-claim) → no structured JSON merge
//    needed; two disjoint SRC diffs never textually conflict.
//  • auto-drop maxConcurrency when re-gates/conflicts fire too often (concurrency-pressure feedback).
import { execSync, spawnSync } from "node:child_process";
import { readFileSync, writeFileSync, existsSync, renameSync, rmSync, readdirSync } from "node:fs";
import { acquire, release } from "./lock.mjs";
import { readConfig } from "./config.mjs";
import { remove as wtRemove } from "./worktree.mjs";
import { releaseClaim, readClaim } from "./claim.mjs";
import { removeActive } from "./assets-core.mjs";
import { writeKnownGood } from "./spec-compile.mjs";

import { fileURLToPath } from "node:url";
import { dirname } from "node:path";
const REPO = process.env.CC_REPO || "C:/Vibes";
const CC = process.env.CC_CC || `${REPO}/cosmo-canyon`;
// §SPLIT (2026-07-03) — the game is its OWN nested git repo (cosmo-canyon/game, own .git, gitignored from C:/Vibes).
// GAME = that repo's working tree. The single committer applies each green worktree's WHOLE-repo game diff onto the
// GAME repo HEAD and commits the GAME repo; control bookkeeping (backlog/completions/.merge-tick/markers) + the
// .claude/settings.json guard stay on REPO (C:/Vibes). ggit/ggitArgs mirror git/gitArgs but target GAME.
const GAME = process.env.CC_GAME || `${CC}/game`;
// bookkeep.mjs is merge.mjs's SIBLING in the orchestrator dir — locate it by THIS file's path, NOT by CC_CC. The
// orchestrator SCRIPT is always the real one; CC_REPO/CC_GAME/CC_CONTROL redirect only the DATA it operates on
// (so the verify harness can point bookkeep at a throwaway repo while still running the real committer code).
const ORC = dirname(fileURLToPath(import.meta.url)).replace(/\\/g, "/");
function controlRoot() { return process.env.CC_CONTROL || `${CC}/control`; }
function locksDir() { return `${controlRoot()}/locks`; }
function claimsDir() { return `${controlRoot()}/claims`; }
function configPath() { return `${controlRoot()}/config.json`; }
function regateFailsPath() { return `${controlRoot()}/.merge-regate-fails.json`; }

const MERGE_DROP_THRESHOLD = Number(process.env.CC_MERGE_DROP_THRESHOLD) || 3; // cumulative re-gate/conflict reverts → drop maxConcurrency by 1

const git = (cmd) => { try { return execSync(`git -C "${REPO}" ${cmd}`, { encoding: "utf8", maxBuffer: 64 * 1024 * 1024 }); } catch (e) { return e.stdout || ""; } };
const gitArgs = (...a) => spawnSync("git", ["-C", REPO, ...a], { encoding: "utf8", maxBuffer: 64 * 1024 * 1024 });
// §SPLIT — the GAME-repo runners (mirror of git/gitArgs, rooted at GAME). The apply + game scrub + game commit + the
// game cc-known-good tag all go through these; control ops keep using git/gitArgs (REPO).
const ggit = (cmd) => { try { return execSync(`git -C "${GAME}" ${cmd}`, { encoding: "utf8", maxBuffer: 64 * 1024 * 1024 }); } catch (e) { return e.stdout || ""; } };
const ggitArgs = (...a) => spawnSync("git", ["-C", GAME, ...a], { encoding: "utf8", maxBuffer: 64 * 1024 * 1024 });
function readJson(p, d) { try { return JSON.parse(readFileSync(p, "utf8")); } catch { return d; } }
function atomicWrite(p, s) { const t = `${p}.tmp`; writeFileSync(t, s); renameSync(t, p); }
function writeJson(p, o) { atomicWrite(p, JSON.stringify(o, null, 2) + "\n"); }
const headSha = () => git("rev-parse HEAD").trim();
const gameHeadSha = () => ggit("rev-parse HEAD").trim(); // §SPLIT — the GAME repo HEAD (the apply/commit target)
const nowIso = () => new Date().toISOString();
const log = (m) => console.log(`[merge ${new Date().toISOString().slice(11, 19)}] ${m}`);

// §SPLIT / §15g phase 8 (audit HIGH) — restore the GAME repo to its HEAD as a WHOLE-repo scrub, never -x (mirrors
// bookkeep.fullRevert's game side → untracked-IGNORED node_modules/dist/derived art survive; untracked worker files
// drop). The game is its OWN repo now, so a whole-repo `ggit reset --hard HEAD` + `ggit clean -fd` is safe + complete
// (no control/assets uploads live in the game repo). `git apply --3way` is NOT atomic: on a conflict it WRITES
// <<<<<<< markers into the file + leaves a UU index entry, so a failed apply MUST be scrubbed or the next claim's
// `git add -A` (in commitGame) folds the poison in / commits conflict markers onto the shared game branch.
function scrubGameTree() {
  if (!ggit("status --porcelain").trim()) return false;
  ggitArgs("reset", "--hard", "HEAD");
  ggitArgs("clean", "-fd"); // NEVER -x — keep gitignored node_modules/dist/derived art
  return true;
}

// §SPLIT — worker patch = the worktree's WHOLE-repo diff vs its gameBaseSha, EXCLUDING the derive-owned manifests
// (15.37). The worktree IS the game repo now (a worktree OF cosmo-canyon/game), so there is NO `-- cosmo-canyon/game`
// pathspec — the whole tree is game. Exclude paths are game-repo-relative (no `cosmo-canyon/game/` prefix). The patch
// therefore applies cleanly onto the GAME repo HEAD (same path namespace).
// §M1 — --binary: without it, git emits a textual "Binary files a/… and b/… differ" stub for any binary a
// worker writes under the game repo (a sprite/tileset PNG, audio blob, .ico, .wasm), which `git apply` cannot apply →
// the green worktree was falsely treated as an unmergeable conflict forever. --binary emits a full applyable delta.
function workerPatch(worktree, gameBaseSha) {
  const r = spawnSync("git", ["-C", worktree, "diff", "--binary", gameBaseSha,
    ":(exclude)assets/manifest.json", ":(exclude)assets/audio-manifest.json"],
    { encoding: "utf8", maxBuffer: 64 * 1024 * 1024 });
  return r.stdout || "";
}

// §SPLIT — apply a patch onto the GAME repo at its current HEAD (working tree; bookkeep's commitGame stages). Targets
// GAME via -C GAME, NOT REPO. Try --3way then plain.
// §M1 — --binary on both applies so the full binary delta from workerPatch lands (a plain apply of a --binary
// patch is a no-op without the flag).
function applyPatch(patch) {
  if (!patch.trim()) return { ok: true, empty: true };
  let r = spawnSync("git", ["-C", GAME, "apply", "--3way", "--binary", "--whitespace=nowarn"], { input: patch, encoding: "utf8", maxBuffer: 64 * 1024 * 1024 });
  if (r.status === 0) return { ok: true };
  r = spawnSync("git", ["-C", GAME, "apply", "--binary", "--whitespace=nowarn"], { input: patch, encoding: "utf8", maxBuffer: 64 * 1024 * 1024 });
  if (r.status === 0) return { ok: true };
  return { ok: false, error: ((r.stderr || "") + (r.stdout || "")).split("\n").filter(Boolean).slice(0, 2).join(" | ").slice(0, 200) };
}

// run bookkeep at post-merge HEAD (MAIN control tree + GAME repo HEAD, no worktree → serial commit path via
// commitGame). CC_GITTREE_HELD=1: the merge holds git-tree, so bookkeep.commit must NOT re-acquire it.
// §SPLIT — TWO anchors in the merge-tick: baseSha = the C:/Vibes CONTROL head (bookkeep's .claude/settings.json
// tamper guard, `git -C C:/Vibes`); gameBaseSha = the GAME repo HEAD the patch was just applied onto (bookkeep's
// game gate/revert/commit anchor). If a revert fires at the seam, bookkeep.fullRevert resets the game repo to THIS
// gameBaseSha (the applied edits are dropped, HEAD unchanged). Returns the parsed outcome JSON.
function runBookkeep(result, beadId, baseSha, reason, gameBaseSha) {
  const anchor = `${controlRoot()}/.merge-tick.json`;
  writeJson(anchor, { pid: null, startEpoch: Date.now(), baseSha, gameBaseSha: gameBaseSha || gameHeadSha(), beadId, runToken: null });
  const argv = [`${ORC}/bookkeep.mjs`, "--result", result, "--tick", anchor];
  if (reason) { argv.push("--reason", reason); }
  const r = spawnSync(process.execPath, argv, { cwd: CC, encoding: "utf8", maxBuffer: 64 * 1024 * 1024, env: { ...process.env, CC_GITTREE_HELD: "1" } });
  try { rmSync(anchor, { force: true }); } catch {}
  const out = `${r.stdout || ""}`;
  let parsed = null;
  for (const line of out.split("\n").reverse()) { const t = line.trim(); if (t.startsWith("{")) { try { parsed = JSON.parse(t); break; } catch {} } }
  return { outcome: (parsed && parsed.outcome) || "unknown", parsed, raw: out.slice(-300), stderr: (r.stderr || "").slice(-300), status: r.status };
}

function bumpRegateFails(delta) {
  const p = regateFailsPath();
  const cur = readJson(p, { count: 0, at: null });
  const count = Math.max(0, (cur.count || 0) + delta);
  writeJson(p, { count, at: nowIso() });
  return count;
}
function resetRegateFails() { try { writeJson(regateFailsPath(), { count: 0, at: nowIso() }); } catch {} }

// auto-drop maxConcurrency (never below 1) when re-gate/conflict reverts pile up. Rewrites config.json atomically.
function autoDropConcurrency() {
  let raw = readJson(configPath(), null);
  if (!raw || typeof raw !== "object") return null;
  const c = raw.concurrency || {};
  const cur = Number(c.maxConcurrency) || 2;
  if (cur <= 1) { c.mode = "serial"; c.maxConcurrency = 1; } // already at floor → fall all the way back to serial
  else c.maxConcurrency = cur - 1;
  raw.concurrency = c;
  writeJson(configPath(), raw);
  resetRegateFails();
  return c.maxConcurrency;
}

// clean up a claim's worktree + claim record + active row + markers (explicit-path only, 15.43).
function cleanupClaim(id, worktree, runToken, beadId) {
  const cfg = readConfig();
  let wtResult = null;
  if (worktree) { try { wtResult = wtRemove(worktree, { root: cfg.concurrency.worktreeRoot }); } catch (e) { wtResult = { ok: false, error: String((e && e.message) || e) }; } }
  try { releaseClaim(id); } catch {}
  try { removeActive({ runToken: runToken || null, beadId: beadId || null }); } catch {}
  try { rmSync(`${claimsDir()}/${id}.gate.json`, { force: true }); } catch {}
  return wtResult;
}

// read the gate markers → the green/red worktrees to land, deterministic order (id sort).
export function readGateMarkers() {
  let names = [];
  try { names = readdirSync(claimsDir()).filter((n) => n.endsWith(".gate.json")); } catch { return []; }
  return names.sort().map((n) => { const id = n.replace(/\.gate\.json$/, ""); return { id, marker: readJson(`${claimsDir()}/${n}`, null) }; }).filter((m) => m.marker);
}

// mergeGreen — the single-committer pass. For each gate marker (green → land at HEAD; red → attempt-bump), holding
// git-tree PER CLAIM (short holds so a GUI-ingest can interleave BETWEEN landings). Returns a summary.
export function mergeGreen({ dispatched = [] } = {}) {
  const byId = new Map(dispatched.map((d) => [d.id, d]));
  const markers = readGateMarkers();
  const landed = [], reverted = [], conflicts = [], red = [];
  for (const { id, marker } of markers) {
    const d = byId.get(id) || {};
    const worktree = marker.worktree || d.worktree || null;
    const beadId = marker.beadId || d.beadId || null;
    const runToken = d.runToken || null;
    // §SPLIT — baseSha is now the C:/Vibes CONTROL anchor (settings guard); gameBaseSha is the GAME-repo detach anchor
    // the worktree diff is taken against (the worker wrote both into its gate marker; dispatched record is the fallback).
    const gameBaseSha = marker.gameBaseSha || d.gameBaseSha || null;

    if (marker.outcome === "red") {
      // the worker gated red (gate/tamper/no-op) = a real failed attempt → bookkeep --result blocked bumps it +
      // commits the bookkeeping (control tree clean; game repo untouched — nothing was applied). Under git-tree held.
      // §SPLIT — control base = C:/Vibes head; game base = current game HEAD (no patch applied → nothing to revert).
      const lk = acquire(locksDir(), "git-tree");
      let bk;
      try { bk = runBookkeep("blocked", beadId, headSha(), `parallel gate red: ${(marker.reason || "").slice(0, 80)}`, gameHeadSha()); } finally { release(lk); }
      cleanupClaim(id, worktree, runToken, beadId);
      red.push({ id, beadId, reason: marker.reason, bk: bk.outcome });
      continue;
    }

    // GREEN → apply the worktree's game diff onto the GAME repo HEAD + re-run bookkeep at post-merge HEAD, atomically
    // under git-tree. §SPLIT — the apply/commit target is the GAME repo; control (bookkeep) commits C:/Vibes.
    const lk = acquire(locksDir(), "git-tree");
    let result;
    try {
      scrubGameTree(); // (audit HIGH) PRE-apply defensive clean — a prior iteration/cycle/crash may have left the game repo dirty; never fold residual dirt into this landing
      const currentHead = headSha();        // §SPLIT — C:/Vibes control head → bookkeep's settings-guard BASE
      const gameHead = gameHeadSha();        // §SPLIT — GAME repo HEAD the patch lands onto → bookkeep's game revert anchor
      const patch = workerPatch(worktree, gameBaseSha);
      const ap = applyPatch(patch);
      if (!ap.ok) {
        // seam conflict (a sibling landed an overlapping change since the worktree's game base). `git apply --3way`
        // left conflict markers + a UU index entry in the GAME repo → SCRUB (audit HIGH) so the next claim applies
        // onto a clean game HEAD and no `git add -A` (commitGame) folds the poison in. Not a worker failure → LEAVE
        // the bead non-terminal to retry next cycle against the new HEAD; count toward auto-drop.
        log(`conflict landing ${id} (${beadId}): ${ap.error} → scrub game tree, discard worktree, retry next cycle`);
        scrubGameTree();
        result = { kind: "conflict", error: ap.error };
      } else {
        const bk = runBookkeep("work", beadId, currentHead, null, gameHead);
        // §SPLIT — a landed pass commits the GAME repo (bookkeep.commitGame); tag cc-known-good on BOTH repos (the
        // rollback anchor is the GAME repo's last-green land; C:/Vibes also gets it for the dashboard banner/history
        // marker — mirrors post-tick.mjs).
        if (bk.outcome === "committed") { ggit(`tag -f cc-known-good HEAD`); git(`tag -f cc-known-good HEAD`); try { writeKnownGood(); } catch {} result = { kind: "landed", sha: bk.parsed && bk.parsed.sha }; }
        else result = { kind: "reverted", outcome: bk.outcome, raw: bk.raw, stderr: bk.stderr };
        // if bookkeep applied its own revert (gate/acceptance fail at seam) the game repo is already clean (fullRevert
        // reset it to gameHead); if it is somehow dirty (a crash), scrub the applied game edits so the next claim
        // applies onto a clean game HEAD.
        scrubGameTree();
      }
    } finally { release(lk); }

    // cleanup outside the git-tree lock (worktree removal needs no commit lock)
    cleanupClaim(id, worktree, runToken, beadId);
    if (result.kind === "landed") { landed.push({ id, beadId, sha: result.sha }); }
    else if (result.kind === "reverted") {
      reverted.push({ id, beadId, outcome: result.outcome, raw: (result.raw || "").slice(-240), stderr: (result.stderr || "").slice(-240) });
      // HARDENING (auto-drop scope): only CONCURRENCY-related seam failures should shrink maxConcurrency. The single
      // reliable concurrency signal is a seam GATE-fail: the bead gated GREEN in its worktree at baseSha but the
      // MERGED combination (its patch + another landed patch) breaks the build at HEAD — reducing concurrency (fewer
      // interleavings) genuinely helps. Every other seam revert — acceptance fail, stale-rev/stale-bytes, no-op,
      // terminal/superseded, tamper — is a bead-quality/obsolescence problem that would fail IDENTICALLY serially,
      // so counting it toward auto-drop lets a few unrelated bad beads permanently collapse parallelism (the
      // run-forever regression observed with grader-unverifiable beads). Those are bounded by the abandon ceiling,
      // not by shrinking N. (Apply CONFLICTS — result.kind!=="reverted" — always count, below.)
      if (/gate fail/i.test(result.raw || "")) bumpRegateFails(1);
    }
    else { conflicts.push({ id, beadId, error: result.error }); bumpRegateFails(1); }
  }

  // sweep ORPHAN claims: a dispatched worker that returned WITHOUT writing a gate marker (crashed / failed to run
  // bookkeep --gate-only). Both hosts await ALL workers before calling merge, so a claim file still present here =
  // an orphan → GC its worktree by EXPLICIT path + release + prune active, NO attempt bump (infra-kill, §13.35) →
  // the bead retries next cycle. (The supervisor also sweeps, redundantly + harmlessly; the Workflow host relies
  // on THIS since its merge agent runs `node merge.mjs` with no dispatched list.)
  let orphans = 0;
  try {
    for (const n of readdirSync(claimsDir()).filter((f) => f.endsWith(".claim.json"))) {
      const id = n.replace(/\.claim\.json$/, "");
      const rec = readClaim(id);
      cleanupClaim(id, rec && rec.worktree, null, rec && rec.beadId);
      orphans++;
    }
  } catch {}

  // auto-drop maxConcurrency if re-gate/conflict reverts crossed the threshold; reset the counter on a clean batch.
  let dropped = null;
  const fails = readJson(regateFailsPath(), { count: 0 }).count || 0;
  if (fails >= MERGE_DROP_THRESHOLD) { dropped = autoDropConcurrency(); log(`re-gate/conflict reverts=${fails} ≥ ${MERGE_DROP_THRESHOLD} → auto-drop maxConcurrency → ${dropped}`); }
  else if (!reverted.length && !conflicts.length && landed.length) resetRegateFails();

  return { landed, reverted, conflicts, red, orphans, dropped, mergedAt: gameHeadSha(), controlAt: headSha() }; // §SPLIT — mergedAt = the GAME repo HEAD (the landing target); controlAt = C:/Vibes head
}

// CLI for the Workflow host: `node merge.mjs [--dispatched <json>]` → prints the mergeGreen summary. The dispatched
// list is optional (mergeGreen reads worktree/beadId off the gate markers themselves; the list only supplies
// runTokens for a tidy active.json GC).
import { resolve } from "node:path";
if (process.argv[1] && fileURLToPath(import.meta.url) === resolve(process.argv[1])) {
  const args = process.argv.slice(2);
  const i = args.indexOf("--dispatched");
  let dispatched = [];
  if (i >= 0 && args[i + 1]) { try { dispatched = JSON.parse(args[i + 1]); } catch {} }
  process.stdout.write(JSON.stringify(mergeGreen({ dispatched })));
}
