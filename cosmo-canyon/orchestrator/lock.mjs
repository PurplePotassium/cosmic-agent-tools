// Stale-breakable single-writer lock for the control plane (Cosmo Canyon §13.43 / §15i 15.48).
// A lock is a dir control/locks/<name>.lock holding owner.json {pid,epoch}. Acquire breaks any lock
// whose holder pid is DEAD or older than TTL — a killed tick/agy (which §13.16/13.17 guarantee happen)
// never deadlocks the GUI/planner/next-tick. A best-effort lock that can't self-heal is just a deadlock
// waiting for a kill.
//
// §15i 15.48 (TOCTOU BUSY-grace): mkdirSync(dir) then a SEPARATE writeFileSync(owner.json) leaves a
// window where the dir exists but owner.json is absent/unparseable. The old code read that half-created
// lock as stale and STOLE it → two acquirers both entered. Fix: (1) write owner.json via temp+rename
// INTO the dir (a reader sees the whole JSON or nothing — never a torn read); (2) on read/parse failure
// of owner.json, key staleness on the LOCK DIR's OWN ctime/mtime (statSync), NOT owner.epoch (which is
// unreadable during the window): a just-created dir (< GRACE_MS old) is treated BUSY (sleep+retry), an
// old ownerless dir (a crashed writer) is stale (break).
import { mkdirSync, writeFileSync, readFileSync, rmSync, statSync, renameSync } from "node:fs";

// §AUDIT-2026-07-02 HIGH-5 — TTL is the LAST-RESORT wedge-breaker for an alive-but-hung holder (a dead pid is
// broken immediately, regardless of age). It MUST exceed the worst-case legitimate hold or a live holder gets its
// lock stolen mid-op → two committers. merge.mjs holds `git-tree` across apply→runBookkeep("work"), whose gate
// (GATE_TIMEOUT ~5min) + acceptance (~3min) can approach ~8-10min; the old 5-min TTL let a concurrent committer
// (server confirm endpoint / Stop hook) steal git-tree during a slow seam gate. Raised to 15min > worst-case hold.
const TTL_MS = 15 * 60 * 1000; // an ALIVE holder older than this is assumed wedged (dead pids break immediately)
const GRACE_MS = 2000; // a dir younger than this with no readable owner.json is BUSY (mid-acquire), not stale

function alive(pid) {
  if (!pid || pid <= 0) return false;
  try { process.kill(pid, 0); return true; }
  catch (e) { return e.code === "EPERM"; } // EPERM = exists but not ours
}

function sleepSync(ms) {
  // sync wait without busy-spin (Node scripts only; not the Workflow sandbox)
  Atomics.wait(new Int32Array(new SharedArrayBuffer(4)), 0, 0, ms);
}

// write owner.json atomically INTO the lock dir (temp+rename) so a concurrent reader never sees a torn write
function writeOwner(dir, pid) {
  const tmp = `${dir}/.owner.json.tmp`;
  writeFileSync(tmp, JSON.stringify({ pid, epoch: Date.now() }));
  renameSync(tmp, `${dir}/owner.json`);
}

export function acquire(locksDir, name, { pid = process.pid, retries = 100, waitMs = 100 } = {}) {
  mkdirSync(locksDir, { recursive: true });
  const dir = `${locksDir}/${name}.lock`;
  for (let i = 0; i < retries; i++) {
    try {
      mkdirSync(dir); // atomic: fails if exists
      writeOwner(dir, pid); // temp+rename → whole-or-nothing owner.json
      return dir;
    } catch {
      let stale;
      try {
        const o = JSON.parse(readFileSync(`${dir}/owner.json`, "utf8"));
        stale = !alive(o.pid) || Date.now() - o.epoch > TTL_MS;
      } catch {
        // owner.json missing/unparseable → either a half-created lock (fresh, another acquirer is mid-write)
        // or a crashed writer that never wrote it. During this window owner.epoch does not exist, so key
        // staleness on the LOCK DIR's own timestamp: < GRACE_MS old → BUSY (sleep+retry), else → stale.
        let dirAge = Infinity;
        try { const st = statSync(dir); dirAge = Date.now() - Math.min(st.ctimeMs, st.mtimeMs); } catch { dirAge = Infinity; }
        stale = dirAge >= GRACE_MS;
      }
      if (stale) { try { rmSync(dir, { recursive: true, force: true }); } catch {} continue; }
      sleepSync(waitMs);
    }
  }
  throw new Error(`lock '${name}' busy after ${retries} tries`);
}

// §AUDIT-2026-07-02 HIGH-5 — OWNERSHIP-checked release. If this lock was stolen (TTL/dead-pid break) and
// re-acquired by ANOTHER live process between our acquire and release, a blind rmSync would delete the NEW owner's
// live lock → a third acquirer enters → 2+ concurrent holders. Only remove the dir when owner.json still names OUR
// pid (or is unreadable — treat as ours to avoid leaking a dir into a TTL-length deadlock; the common case is our
// own lock and an unreadable owner is almost always a torn/rm'd file we created).
export function release(dir) {
  try {
    const o = JSON.parse(readFileSync(`${dir}/owner.json`, "utf8"));
    if (o && o.pid && o.pid !== process.pid) return; // a different live process now owns this lock — do NOT delete it
  } catch { /* owner unreadable → fall through and remove (ours, or already gone) */ }
  try { rmSync(dir, { recursive: true, force: true }); } catch {}
}

// ── §15c-2 Invariant L: the ONE global lock-acquisition order (deadlock-free) ────────────────────
// Every acquirer takes its lock set in ONE total order (low rank first, ties broken by NAME) → no two
// acquirers can form a wait cycle. The 8 canonical ranks are PINNED by §15c-2 (git-tree = rank 8, per
// PLAN §15g line 949); names §15c-2 leaves under-specified (suggestions, agent) are slotted at an equal
// rank to their sibling lock and broken by name so the order stays TOTAL. Dynamic families share a rank
// and sub-sort by name: asset-<id> (rank 3, "asset-a" < "asset-b") and the two manifests (rank 7,
// "audio-manifest" < "manifest"). Any name not listed → rank 99 (acquired last), still name-sorted, so
// an unknown lock can never sit BETWEEN two ordered locks and open a cycle.
//
//   1 active · 2 assets-index · 3 asset-<id> · 4 claims · 5 backlog/suggestions ·
//   6 completions/agent · 7 manifest/audio-manifest · 8 git-tree
//
// NOTE: acquireOrdered has NO live caller yet (phase-2 schedule/claim + the derive/assets locks wire
// it). It does NOT sanitize lock names — the 15.45 name-sanitize lands with the asset endpoints before
// any UNTRUSTED name reaches a lock; today all callers pass trusted literals.
const LOCK_RANK = {
  "active": 1,
  "assets-index": 2,
  // asset-<id> → 3 (prefix, handled in rankOf)
  "claims": 4,
  "backlog": 5,
  "suggestions": 5,      // written under the backlog lock today; sibling → equal rank, name-tiebreak
  "completions": 6,
  "agent": 6,            // engine-config write; unrelated to the merge set → equal rank, name-tiebreak
  "manifest": 7,
  "audio-manifest": 7,
  "git-tree": 8,
};
function rankOf(name) {
  if (name in LOCK_RANK) return LOCK_RANK[name];
  if (/^asset-/.test(name)) return 3; // §15c-2 asset-<id>, sub-sorted by the name tiebreak below
  return 99;                          // unknown → last, deterministic
}
// total order: rank asc, then name asc (lexicographic) — deterministic + deadlock-free
function lockOrder(a, b) { return rankOf(a) - rankOf(b) || (a < b ? -1 : a > b ? 1 : 0); }

// Acquire a SET of named locks in the global order (Invariant L). ALL-OR-NOTHING: if any lock in the
// set is busy past its retries, release every lock already held this call and rethrow → no partial hold,
// no leaked lock dir. Returns the held dirs in acquisition order; pass them to releaseAll (reverse order).
export function acquireOrdered(locksDir, names, opts = {}) {
  const ordered = [...new Set(names)].sort(lockOrder); // dedup + impose the one global order
  const held = [];
  try {
    for (const name of ordered) held.push(acquire(locksDir, name, opts));
    return held;
  } catch (e) {
    releaseAll(held); // roll back everything already acquired this call
    throw e;
  }
}

// Release a set acquired via acquireOrdered, in REVERSE acquisition order (high rank → low).
export function releaseAll(dirs) {
  for (let i = dirs.length - 1; i >= 0; i--) release(dirs[i]);
}

// PURE: dedup + return a name set imposed into the ONE global order (Invariant L) WITHOUT acquiring
// anything. This is the exact order acquireOrdered() would take them in. schedule.mjs uses it to emit each
// planned item's lock set already in-order, and the phase-2 unit tests assert order-compliance against it
// (a planned lock list is compliant iff it equals orderedLockNames(list)). No fs, no side effects.
export function orderedLockNames(names) {
  return [...new Set(names)].sort(lockOrder);
}
// PURE: the §15c-2 rank of a single lock name (1..8, unknown→99). Exposed for assertions/introspection.
export function lockRank(name) { return rankOf(name); }
