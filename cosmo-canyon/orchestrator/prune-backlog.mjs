// Backlog GC — archive terminal FOSSIL beads out of control/backlog.json → control/backlog-archive.json.
//
// WHY: backlog.json is a bead ledger with lifecycle states (done/superseded/abandoned/blocked/parked). Landed
// beads are already recorded in completions.json; superseded/rework fossils are dead. They were never pruned, so
// the file fills with unremovable done/superseded clutter. This archives (never deletes — history is preserved in
// backlog-archive.json, mirroring DONE.md → DONE-archive.md) the fossils and rewrites a lean backlog.json.
//
// FIRE-LATCH SAFETY (the load-bearing rule): assets-core.reconcileAssets uses the PRESENCE of a terminated bead
// as a fire-latch — if a (assetId,rev,contentHash) already fired and terminated, it will NOT re-mint a bead for
// it. So a terminal `abandoned`/`blocked`/`parked` bead that is STILL the current-rev bead of a Ready+dirty,
// not-implemented asset is a LIVE latch (it stops the loop re-minting + re-failing the same bead). Archiving it
// would re-trigger the churn. So we KEEP those; everything else terminal is a fossil and gets archived:
//   • done / superseded            → always archive (landed-clean in completions, or replaced by a newer rev).
//   • abandoned / blocked / parked → archive UNLESS it's a live latch (asset ready+dirty, not implemented,
//                                     bead.rev >= asset.rev). If the asset moved on / landed / vanished, archive.
//   • non-terminal (ready/open/…)  → always keep.
//
// All writes take the §15c-2 'backlog' lock (rank 5) — the SAME lock bookkeep/assets-core/server use for backlog
// RMW — so it never races a concurrent tick. Idempotent (a second run archives nothing).
import { readFileSync, existsSync } from "node:fs";
import { CONTROL, writeJson } from "./state.mjs";
import { isTerminal } from "./assets-core.mjs";
import { acquire, release } from "./lock.mjs";

const LOCKS = `${CONTROL}/locks`;
const BACKLOG = `${CONTROL}/backlog.json`;
const ARCHIVE = `${CONTROL}/backlog-archive.json`;
const INDEX = `${CONTROL}/assets.json`;

function readJson(p, d) { try { return JSON.parse(readFileSync(p, "utf8")); } catch { return d; } }

// Map assetId → { rev, state, dirty, implemented } from the derived index (pure meta projection).
function assetStates() {
  const idx = readJson(INDEX, []);
  const list = Array.isArray(idx) ? idx : (idx.assets || idx.items || []);
  const m = new Map();
  for (const a of list) m.set(a.id, {
    rev: a.rev, state: a.state, dirty: !!a.dirty,
    implemented: !!(a.implementedBy || a.feelConfirmed),
  });
  return m;
}

// Is this terminal bead still a LIVE fire-latch (must be kept)?
function isLiveLatch(bead, assets) {
  if (bead.status === "done" || bead.status === "superseded") return false; // never a latch worth keeping
  const a = bead.assetId ? assets.get(bead.assetId) : null;
  if (!a) return false; // asset gone → fossil
  if (a.implemented || !a.dirty || a.state !== "ready") return false; // moved on / landed → fossil
  // still Ready+dirty+unimplemented: keep only if this bead is the CURRENT (or newer) rev — an older-rev
  // terminal bead has been superseded by a rework and is safe to archive.
  return bead.rev == null || a.rev == null || bead.rev >= a.rev;
}

export function pruneBacklog({ apply = false } = {}) {
  const lock = apply ? acquire(LOCKS, "backlog") : null;
  try {
    const backlog = readJson(BACKLOG, []);
    const assets = assetStates();
    const keep = [], archived = [];
    for (const b of backlog) {
      if (!b || !isTerminal(b.status)) { keep.push(b); continue; }
      if (isLiveLatch(b, assets)) keep.push(b);
      else archived.push(b);
    }
    if (apply && archived.length) {
      const prior = readJson(ARCHIVE, []);
      const stamped = archived.map((b) => ({ ...b, archivedFrom: "backlog" }));
      writeJson(ARCHIVE, prior.concat(stamped)); // append-only history
      writeJson(BACKLOG, keep);
    }
    return {
      kept: keep.length,
      archived: archived.length,
      byStatus: archived.reduce((o, b) => ((o[b.status] = (o[b.status] || 0) + 1), o), {}),
      archivedIds: archived.map((b) => b.id),
    };
  } finally { if (lock) release(lock); }
}

// CLI: `node orchestrator/prune-backlog.mjs`  (dry-run) · `--apply` (write). Prints a JSON summary.
if (process.argv[1] && process.argv[1].replace(/\\/g, "/").endsWith("prune-backlog.mjs")) {
  const apply = process.argv.includes("--apply");
  const r = pruneBacklog({ apply });
  console.log(JSON.stringify({ mode: apply ? "applied" : "dry-run", ...r }));
}
