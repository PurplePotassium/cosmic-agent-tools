// Cosmo Canyon — asset-reconcile: the RE-OPEN (implemented→not_ready) cross-authority invalidation (§15e).
//
// Re-opening an Implemented asset for rework must leave NO downstream authority still claiming it is Implemented.
// The invariant: after reopen, `implemented(asset)` is false AND nothing (completions / manifest / grader / a
// live bead) asserts otherwise. It NEVER auto-deletes shipped code (§13.36 blast radius): the rework bead
// overwrites/re-derives the asset, and any genuinely-orphaned code is cleaned up as part of that rework.
//
// Steps (each write on its §15c-2 lock; the store primitive reopenForRework locks asset-<id> internally):
//   1) reopenForRework(id): clear provenance, state=not_ready + dirty, BUMP rev (regenerated grader binds the new
//      rev, filename never reused), flag placeholderStale (img/audio). [asset-<id>, internal]
//   2) mark the completion that cited the old land `supersededByReopen:true` (the predicate stops seeing it).
//      [completions]
//   3) supersede ANY non-terminal bead for the asset (a stale in-flight bead must not land against old bytes).
//      [backlog]
// NOTE (2026-07-02): reopen used to also append a "review shipped code" operator SUGGESTION. Removed by user
// request — the operator does not review code, so it was pure noise on every reopen. Rework supersedes the old
// work directly; no human-gated review-code suggestion is minted.
//
// ROOT-parameterized via CC_CONTROL. PURE fs + lock.mjs + the store primitive; no git, no model judgment.
import { acquire, release } from "./lock.mjs";
import { reopenForRework, readAsset } from "./assets.mjs";
import { isTerminal } from "./assets-core.mjs";
import { readFileSync, writeFileSync, renameSync } from "node:fs";

const CC = "C:/Vibes/cosmo-canyon";
function controlRoot() { return process.env.CC_CONTROL || `${CC}/control`; }
function locksDir() { return `${controlRoot()}/locks`; }
function completionsPath() { return `${controlRoot()}/completions.json`; }
function backlogPath() { return `${controlRoot()}/backlog.json`; }

const nowIso = () => new Date().toISOString();
function readJsonSafe(p, d) { try { return JSON.parse(readFileSync(p, "utf8")); } catch { return d; } }
function atomicWrite(p, data) { const t = `${p}.tmp`; writeFileSync(t, data); renameSync(t, p); }
function writeJson(p, o) { atomicWrite(p, JSON.stringify(o, null, 2) + "\n"); }

// Mark every completion citing `beadId` as supersededByReopen (the predicate stops treating it as satisfied).
function supersedeCompletions(beadId) {
  if (!beadId) return 0;
  const dir = acquire(locksDir(), "completions");
  try {
    const comp = readJsonSafe(completionsPath(), []);
    if (!Array.isArray(comp)) return 0;
    let n = 0;
    for (const c of comp) { if (c && c.id === beadId && !c.supersededByReopen) { c.supersededByReopen = true; c.supersededAt = nowIso(); n++; } }
    if (n) writeJson(completionsPath(), comp);
    return n;
  } finally { release(dir); }
}

// Supersede any non-terminal bead for the asset (a stale in-flight bead must not land against old bytes), under
// the backlog lock. (No "review shipped code" suggestion is minted — see the header NOTE, 2026-07-02.)
function supersedeBeads(assetId) {
  const dir = acquire(locksDir(), "backlog");
  try {
    const backlog = readJsonSafe(backlogPath(), []);
    let superseded = 0;
    if (Array.isArray(backlog)) {
      for (const b of backlog) { if (b && b.assetId === assetId && !isTerminal(b.status)) { b.status = "superseded"; b.updated = nowIso(); superseded++; } }
      if (superseded) writeJson(backlogPath(), backlog);
    }
    return superseded;
  } finally { release(dir); }
}

// The public entrypoint: reopen an Implemented asset for rework. Captures the old provenance beadId BEFORE the
// store clears it, then invalidates completions + beads + suggests. Returns a summary.
export function reopenAsset(assetId, { reason = "operator reopen" } = {}) {
  let prevBeadId = null, key = assetId;
  try { const m = readAsset(assetId); prevBeadId = m.implementedBy && m.implementedBy.beadId; key = m.manifestKey || m.filename || assetId; } catch { /* absent → nothing downstream to invalidate */ }
  void reason; // kept in the signature for callers/logging; no longer used now the suggestion mint is gone
  const meta = reopenForRework(assetId);                                  // 1) store: clear provenance, not_ready+dirty, rev++
  const completions = supersedeCompletions(prevBeadId);                   // 2) completions: supersededByReopen
  const superseded = supersedeBeads(assetId);                             // 3) supersede stale in-flight beads
  return { assetId, rev: meta.rev, prevBeadId, completionsSuperseded: completions, beadsSuperseded: superseded, key };
}
