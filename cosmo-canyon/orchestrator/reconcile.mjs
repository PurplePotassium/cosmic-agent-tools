// Cosmo Canyon — MODE-CONDITIONAL reconcile (§15c-2 / 15.26). The N-agent parallel branch.
//
// Serial (N=1, default) reconcile is UNCHANGED and stays inline in supervisor.mjs/preflight.mjs — the single
// `.tick.json` reset, byte-for-byte. This module owns ONLY the PARALLEL branch (dormant while the toggle is
// OFF): under N>1 there is NO singleton `.tick.json` to reset — the anchor lives per-agent in each CLAIM — so
// reconcile instead iterates the LIVE claim set and, for every DEAD/STALE claim, discards that agent's
// ISOLATED worktree by EXPLICIT path (15.43, show-toplevel-guarded inside worktree.remove → the shared
// branch is never touched), releases the claim, kills its per-agent agy pid, and GCs its active.json row.
// An infra-kill mid-valid-work is NOT an attempt (that bookkeeping is the caller's).
//
// Toggle OFF: no live caller takes this path (config is serial). It EXISTS + is unit-tested on a fixture so
// phase 8 flips the toggle onto proven code. Everything is injectable (repo/root/now/killers) for that test.
import { execSync } from "node:child_process";
import { readFileSync, writeFileSync, renameSync, existsSync } from "node:fs";
import { acquire, release } from "./lock.mjs";
import { readConfig } from "./config.mjs";
import { stealStale } from "./claim.mjs";
import { remove as worktreeRemove } from "./worktree.mjs";

const CC = "C:/Vibes/cosmo-canyon";
const REPO = "C:/Vibes";
// §SPLIT (2026-07-03) — worktrees are now checkouts of the GAME repo (cosmo-canyon/game, own .git). worktree.remove's
// `repo` is the repo the worktree belongs to (it runs `git -C <repo> worktree remove` + touches
// `<repo>/.git/worktrees/<name>`), so it MUST be the GAME repo, NOT C:/Vibes. CC_GAME pins it (mirrors worktree.mjs).
const GAME = process.env.CC_GAME || `${process.env.CC_REPO || REPO}/cosmo-canyon/game`;
function controlRoot() { return process.env.CC_CONTROL || `${CC}/control`; }

// kill a per-agent agy process tree by pid (own console, not a child of any tick — §13.16). Best-effort.
function killPidTree(pid) {
  if (!pid || pid <= 0) return false;
  try { execSync(`taskkill /PID ${Number(pid)} /T /F`, { stdio: "ignore" }); return true; } catch { return false; }
}
// per-agent agy pid for a claim: an explicit claim.agyPid, else the §15c-2 `.agy-<assetId>.pid` sidecar.
// Belt-and-suspenders re-validation of the INTERNAL assetId before building any path (audit MEDIUM-1): even
// though readClaims() already drops malformed-assetId records, refuse here too so `.agy-<id>.pid` can never
// become `.agy-x/../secret.pid` → out-of-namespace file → taskkill. (readClaims is the root guard; this is depth.)
const SAFE_ID = /^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$/;
function agyPidFor(claim, control) {
  if (claim && claim.agyPid) return Number(claim.agyPid) || null;
  const id = claim && claim.assetId;
  if (typeof id !== "string" || !SAFE_ID.test(id) || id.includes("..")) return null;
  const p = `${control}/.agy-${id}.pid`;
  if (existsSync(p)) { try { return Number(readFileSync(p, "utf8").trim()) || null; } catch {} }
  return null;
}

function writeAtomic(p, data) { const t = `${p}.tmp`; writeFileSync(t, data); renameSync(t, p); }

// GC the active.json rows for a set of reconciled assetIds (under the `active` lock, rank 1). Best-effort —
// the active.json WRITER is phase 5; here we only prune dead rows so a killed agent doesn't linger in the UI.
function pruneActive(control, assetIds) {
  const path = `${control}/active.json`;
  if (!existsSync(path) || !assetIds.length) return 0;
  const dir = acquire(`${control}/locks`, "active");
  try {
    let arr; try { arr = JSON.parse(readFileSync(path, "utf8")); } catch { return 0; }
    if (!Array.isArray(arr)) return 0;
    const drop = new Set(assetIds);
    const kept = arr.filter((r) => !drop.has(r && (r.assetId || r.id)));
    if (kept.length !== arr.length) writeAtomic(path, JSON.stringify(kept, null, 2) + "\n");
    return arr.length - kept.length;
  } finally { release(dir); }
}

// The PARALLEL reconcile branch (15.26). Iterate the claim set; per dead/stale claim discard its worktree by
// explicit path + release + kill agy pid + prune active.json. NEVER resets a singleton .tick.json (there is
// none under N>1). Returns a summary. All roots/killers injectable for the fixture test.
export function reconcileParallel(opts = {}) {
  const config = opts.config || readConfig();
  const control = opts.control || controlRoot();
  const repo = opts.repo || GAME; // §SPLIT — the GAME repo (worktrees belong to it); still injectable for the fixture test
  const root = opts.root || config.concurrency.worktreeRoot;
  const now = opts.now == null ? Date.now() : opts.now;
  const startTokenOf = opts.startTokenOf; // injectable (test); undefined → claim.mjs default osStartToken
  const removeWt = opts.removeWorktree || worktreeRemove;
  const killAgy = opts.killAgy || killPidTree;

  // steal (GC) every dead/stale claim — returns the stolen records (with worktree/agyPid to clean up)
  const stolen = stealStale({ now, cfg: config, ...(startTokenOf !== undefined ? { startTokenOf } : {}) });
  const results = [];
  for (const c of stolen) {
    const r = { assetId: c.assetId, worktree: null, agyKilled: false };
    if (c.worktree) {
      try { r.worktree = removeWt(c.worktree, { repo, root }); } // explicit-path + show-toplevel guarded (15.43)
      catch (e) { r.worktree = { ok: false, error: String(e && e.message || e) }; }
    }
    const apid = agyPidFor(c, control);
    if (apid) r.agyKilled = killAgy(apid);
    results.push(r);
  }
  const pruned = pruneActive(control, results.map((r) => r.assetId));
  return { mode: "parallel", reconciled: results.map((r) => r.assetId), pruned, results };
}
