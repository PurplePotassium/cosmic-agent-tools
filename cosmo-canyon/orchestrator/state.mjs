// Cosmo Canyon — shared deterministic state reader (desktop/Workflow orchestration path).
//
// The Workflow script (cc-loop.workflow.js) is the loop HOST but has NO fs/git access, so
// all disk/git sensing lives in small Node scripts that agents run via Bash. This module is
// the shared core they import (sense.mjs / plan-prep.mjs), so the SNAPSHOT the loop branches
// on and the input the planner reads are computed by the SAME deterministic code — no drift.
//
// This module owns the fs/git PRIMITIVES (git, readJson, headSha, plannerLatch, usage, paused,
// paths). The SNAPSHOT + trigger logic itself lives in spec-core.mjs (single source across both loop
// hosts, audit 15.22); computeSnapshot/wipKeywords are RE-EXPORTED from there below so existing
// `import {computeSnapshot} from "./state.mjs"` callers keep working. supervisor.mjs no longer keeps a
// private copy — it imports spec-core directly.
import { execSync } from "node:child_process";
import { readFileSync, writeFileSync, existsSync, renameSync } from "node:fs";

export const REPO = "C:/Vibes";
export const CC = "C:/Vibes/cosmo-canyon";
// §15g phase 5 — CONTROL honors env CC_CONTROL (same convention as assets.mjs/config.mjs/server.mjs) so the
// asset-scan sense pre-step + a full-loop test run against a THROWAWAY control plane; unset in production →
// the real control/ (byte-for-byte unchanged). Evaluated once at import → a test sets CC_CONTROL BEFORE importing.
export const CONTROL = process.env.CC_CONTROL || `${CC}/control`;
export const LOGS = `${CC}/logs`;
export const BRANCH = "cosmo-canyon";

// HARDENING (2026-07-02): bound every git exec so a stuck .git/index.lock or credential prompt THROWS
// instead of hanging the sensing/planner scripts (and the supervisor's computeSnapshot→headSha) forever.
const GIT_TIMEOUT_MS = Number(process.env.CC_GIT_TIMEOUT_SEC || 120) * 1000;
export const git = (cmd) => execSync(`git -C "${REPO}" ${cmd}`, { encoding: "utf8", maxBuffer: 64 * 1024 * 1024, timeout: GIT_TIMEOUT_MS });
export const gitQuiet = (cmd) => { try { return git(cmd); } catch (e) { return e.stdout || ""; } };

// §SPLIT (2026-07-03) — the game is now its OWN nested git repo (cosmo-canyon/game, own .git, gitignored from
// C:/Vibes). GAME git ops target IT; control/orchestrator ops keep targeting REPO. CC_GAME overrides (a worktree
// of the game repo in parallel, or a throwaway repo in a test). ggit = the game-repo runner (mirror of git).
export const GAME = process.env.CC_GAME || `${CC}/game`;
export const ggit = (cmd) => execSync(`git -C "${GAME}" ${cmd}`, { encoding: "utf8", maxBuffer: 64 * 1024 * 1024, timeout: GIT_TIMEOUT_MS });
export const ggitQuiet = (cmd) => { try { return ggit(cmd); } catch (e) { return e.stdout || ""; } };
export function gameHeadSha() { return ggitQuiet("rev-parse HEAD").trim(); }
export const readJson = (p, d) => { try { return JSON.parse(readFileSync(p, "utf8")); } catch { return d; } };
export const atomicWrite = (p, s) => { const t = `${p}.tmp`; writeFileSync(t, s); renameSync(t, p); };
export const writeJson = (p, o) => atomicWrite(p, JSON.stringify(o, null, 2) + "\n");
export const nowIso = () => new Date().toISOString();

export function headSha() { return gitQuiet("rev-parse HEAD").trim(); }

// SNAPSHOT + WIP filter are owned by spec-core.mjs (single source, audit 15.22). Re-exported here so
// existing `import {computeSnapshot|wipKeywords} from "./state.mjs"` callers (sense.mjs, plan-prep.mjs)
// keep working unchanged. computeTrigger/specAuthoritySha/latchKeyFor are imported from spec-core directly.
export { computeSnapshot, wipKeywords } from "./spec-core.mjs";
import { isTerminal } from "./assets-core.mjs"; // §15c — shared terminal-status set (blocked/abandoned/done/parked/superseded)

// head READY bead = first NON-terminal (isTerminal excludes the phase-5 parked/superseded too — else a parked
// bead is re-selected forever). state↔assets-core has no import cycle (assets-core never imports state).
export function headReadyBead() {
  const backlog = readJson(`${CONTROL}/backlog.json`, []);
  return backlog.find((b) => !isTerminal(b.status)) || null;
}

export function plannerLatch() { return readJson(`${CONTROL}/.plan-latch.json`, {}); }

// daily tick cap (.usage-YYYYMMDD.json) — counter is bumped by tick-prep/plan-prep (one bump per tick).
// §AUDIT-2026-07-04 — LOCAL day, not UTC: nowIso() rolled the file at UTC midnight (~late afternoon local),
// splitting an evening run across two "days" and disagreeing with server.mjs's banner readout (already local).
function localDay() { const d = new Date(); return `${d.getFullYear()}${String(d.getMonth() + 1).padStart(2, "0")}${String(d.getDate()).padStart(2, "0")}`; }
export function usagePath() { return `${CONTROL}/.usage-${localDay()}.json`; }
export function usageToday() { return readJson(usagePath(), { ticks: 0 }).ticks; }
export function bumpUsage() {
  const p = usagePath();
  const d = localDay();
  const u = readJson(p, { date: `${d.slice(0, 4)}-${d.slice(4, 6)}-${d.slice(6, 8)}`, ticks: 0 });
  u.ticks++; writeJson(p, u); return u.ticks;
}

export function isPaused() { return existsSync(`${CONTROL}/.paused`); }
