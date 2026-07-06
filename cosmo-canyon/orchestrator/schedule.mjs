// Cosmo Canyon — deterministic top-of-cycle SCHEDULER (§15c-2). PURE planner: NO fs, NO git, NO dispatch.
//
// Core invariant it enforces (§15c-2): at any instant the union of all in-flight agents' owned files is
// owned by AT MOST ONE agent, and every agent takes its control-plane locks in ONE global order (Invariant
// L). schedule() takes the dispatchable candidate set + the config + the currently-active claims and returns
// a PLAN — which items go to parallel slots, which agy items go to the ONE serial lane, which are deferred
// (and why) — plus, per planned item, the ORDERED lock set it will need. It does NOT claim, dispatch, or
// touch git; the caller (a later phase) turns the plan into claims + worktrees + ticks.
//
// SERIAL = N=1 of this model: maxConcurrency=1 ⇒ at most one item, inline, no worktree — today's path. The
// disjoint-files partition is vacuous at N=1. Toggle stays OFF (phase 8 flips it); this is contracts + a
// unit test only.
//
// The load-bearing rules (all deterministic, all unit-tested on a fixture):
//  • unit = an ASSET; ownership = its SRC files (`resolveFiles`), NEVER the `accept/` grader path (PROTECTED,
//    15.7) and NEVER the shared manifest (that is a MERGE lock, not an overlap token — else all image assets
//    would falsely serialize). manifest/audio-manifest are added to the item's LOCK SET, not its owned files.
//  • slots = clamp(min(maxConcurrency-active, capRemaining)) computed HERE right before dispatch (not
//    cycle-top — else a cycle starting at cap-1 overshoots by N-1, 15.21).
//  • tier-weighted cap (light1/heavy3/structural5): a heavy/structural needs capRemaining ≥ heavyCostReserve
//    (hard precondition) AND its weight ≤ remaining weight budget → N heavies can't sneak under a count ceiling.
//  • overlapping-files assets are SERIALIZED BY CONSTRUCTION — not co-scheduled (never a lock held across a
//    wait → can't deadlock); the deferred item is priority-aged so a churning exclusive can't starve it.
//  • ALL agy items → ONE serial lane (15.42: agy is worktree-blind); only claude items are parallel-eligible.
import { readConfig, TIER_WEIGHT, MAX_CONCURRENCY } from "./config.mjs";
import { orderedLockNames } from "./lock.mjs";

const TIERS = new Set(["light", "heavy", "structural"]);
function tierOf(item) { return TIERS.has(item?.tier) ? item.tier : "light"; }
function weightOf(item) { return TIER_WEIGHT[tierOf(item)]; }
function keyOf(item) { return item?.manifestKey || item?.id; }
// engine: explicit item.engine wins; else infer (image/audio feel-work → claude; spec/impl logic → agy default).
function engineOf(item) {
  if (item?.engine === "agy" || item?.engine === "claude") return item.engine;
  if (item?.kind === "image" || item?.kind === "audio") return "claude";
  return "agy";
}

// Normalize a file token for OVERLAP comparison. Windows FS is case-insensitive, so lowercase to be
// conservative (two leases differing only by case would collide on disk). Forward-slash, strip a leading
// `./`. Kept local so overlap semantics are one place.
function normFile(f) { return String(f || "").replace(/\\/g, "/").replace(/^\.\//, "").replace(/\/+$/, "").toLowerCase(); }

// §15c-2/15.7 SRC-ONLY ownership: worker beads partition on SOURCE files ONLY. The gate tests + the per-bead
// grader are PROTECTED (tamper guard / PROTECTED_PREFIX) — a worker CANNOT write them, so they are NEVER a real
// write-conflict and MUST NOT be ownership/partition tokens. Two beads that merely both list `test/sim.ts` (as
// the current planner-emitted beads do) would otherwise FALSELY serialize on a file neither can touch, killing
// parallelism for zero safety. Strip these prefixes from declared ownership before partitioning. Matched on the
// game-relative form the bead schema uses (`test/…`, `accept/…`); source art (`assets/source/…`) is NOT stripped
// — for image assets it is the legitimate per-key ownership unit (the merge/derive step writes it).
const PROTECTED_OWN = ["test/", "accept/"];
function isProtectedOwn(f) { return PROTECTED_OWN.some((p) => f === p.slice(0, -1) || f.startsWith(p)); }

// resolveFiles(item) → { owned:string[], mergeLocks:string[], exclusive:bool } (§15c-2).
//  1) declared files[] → strip PROTECTED (test/accept) → remaining SRC files are the owned tokens;
//  2) if nothing SRC-declared, infer by kind: image → assets/source/<key>.* (+manifest lock); audio →
//     assets/audio/<key>.* (+audio-manifest lock);
//  3) spec/unknown with no SRC surface → GLOBAL-EXCLUSIVE (owned=[], exclusive=true): conflicts with everything,
//     capped to a small dispatch budget, scheduler continues past it when lighter work exists (priority-aged).
export function resolveFiles(item) {
  const kind = item?.kind;
  const mergeLocks = kind === "image" ? ["manifest"] : kind === "audio" ? ["audio-manifest"] : [];
  const declared = Array.isArray(item?.files) ? item.files.filter((f) => typeof f === "string" && f.trim()) : [];
  const srcOwned = declared.map(normFile).filter((f) => !isProtectedOwn(f)); // SRC only (15.7)
  if (srcOwned.length) return { owned: srcOwned, mergeLocks, exclusive: false };
  const key = keyOf(item);
  if (kind === "image") return { owned: [normFile(`assets/source/${key}.*`)], mergeLocks, exclusive: false };
  if (kind === "audio") return { owned: [normFile(`assets/audio/${key}.*`)], mergeLocks, exclusive: false };
  // no SRC surface declared/inferred (e.g. a spec bead, or a bead that listed ONLY protected files) → exclusive
  return { owned: [], mergeLocks, exclusive: true };
}

// The ORDERED control-plane lock set an item needs across claim → work → single-committer merge, in the ONE
// global order (Invariant L). active+claims (claim under 15c-2 linearization), asset-<id> (its own meta),
// backlog+completions (merge bookkeeping), kind merge locks (manifest/audio-manifest), git-tree (rank 8, the
// single committer). Emitted already-ordered so a caller passes it straight to acquireOrdered and a test can
// assert equality with orderedLockNames(list).
export function lockSetFor(item) {
  const { mergeLocks } = resolveFiles(item);
  return orderedLockNames(["active", "claims", `asset-${item.id}`, "backlog", "completions", ...mergeLocks, "git-tree"]);
}

// two leases overlap iff they share any normalized owned token; an EXCLUSIVE item overlaps everything.
function overlaps(a, b) {
  if (a.exclusive || b.exclusive) return true;
  const set = new Set(a.owned);
  return b.owned.some((f) => set.has(f));
}

// Priority-age sort: higher priority first, then OLDER first (age = ms-since-created or an explicit rank the
// caller supplies via item.age — larger = older = earlier). Stable for equal keys (id tiebreak) so the plan
// is fully deterministic run-to-run.
function byPriorityAge(a, b) {
  const pa = Number(a.priority || 0), pb = Number(b.priority || 0);
  if (pa !== pb) return pb - pa;
  const aa = Number(a.age || 0), ab = Number(b.age || 0);
  if (aa !== ab) return ab - aa;
  return a.id < b.id ? -1 : a.id > b.id ? 1 : 0;
}

// schedule(candidates, opts) — the deterministic planner.
//   candidates: dispatchable items [{id, kind?, tier?, engine?, files?, manifestKey?, priority?, age?}]
//   opts.config: a readConfig() result (defaults to reading it); opts.activeClaims: in-flight claims
//     [{assetId|id, filesLease[], exclusive?, engine?}] whose owned files are already taken this instant —
//     pass claim.readClaims() straight through: each claim record now carries filesLease + exclusive + engine
//     (the cross-cycle agy-serial guard reads engine; the disjointness check reads exclusive — audit HIGH-1/2).
//     opts.capRemaining: tier-weighted daily budget left (default Infinity → count-limited only).
// Returns { slots, mode, parallel:[...], serial:[...], deferred:[{id,reason}], picked, weightUsed }.
export function schedule(candidates = [], opts = {}) {
  const cfg = opts.config || readConfig();
  const { mode, maxConcurrency, autoConcurrency, heavyCostReserve } = cfg.concurrency;
  const activeClaims = Array.isArray(opts.activeClaims) ? opts.activeClaims : [];
  const capRemaining = opts.capRemaining == null ? Infinity : Number(opts.capRemaining);

  const active = activeClaims.length;
  // autoConcurrency (operator "decide for yourself") ⇒ fan out to the safety ceiling; the disjoint-files
  // partition below + capRemaining still bound actual picks to the available work.
  const effMax = autoConcurrency ? MAX_CONCURRENCY : maxConcurrency;
  // §15.21: slots computed HERE, right before dispatch — min(concurrency headroom, cap headroom), floored 0.
  const slots = Math.max(0, Math.min(effMax - active, capRemaining === Infinity ? effMax - active : Math.floor(capRemaining)));

  // Files already owned by in-flight claims → the new plan must stay disjoint from them.
  const activeLeases = activeClaims.map((c) => ({
    owned: (Array.isArray(c.filesLease) ? c.filesLease : []).map(normFile),
    exclusive: !!c.exclusive,
  }));

  const parallel = [], serial = [], deferred = [];
  const claimedThisCycle = [...activeLeases]; // grows as we pick — enforces disjointness by construction
  let picked = 0, weightUsed = 0, weightBudget = capRemaining;
  // ONE agy at a time (15.42): the serial lane is already taken if an agy claim is in flight. Both lanes'
  // picks count against `slots` (conservative — never over-dispatches; phase 8 may give agy separate capacity).
  let serialTaken = activeClaims.some((c) => c.engine === "agy");

  const ranked = [...candidates].sort(byPriorityAge);
  for (const item of ranked) {
    if (!item || !item.id) continue;
    const rf = resolveFiles(item);
    const eng = engineOf(item);
    const w = weightOf(item);

    // count ceiling (§15.21 — the hard slot clamp)
    if (picked >= slots) { deferred.push({ id: item.id, reason: "no-slot" }); continue; }
    // overlap → SERIALIZE by construction (defer; priority-aged next cycle). Never co-schedule; no lock held across a wait.
    if (claimedThisCycle.some((c) => overlaps(rf, c))) { deferred.push({ id: item.id, reason: "files-conflict" }); continue; }
    // tier-weighted cap: heavy/structural needs the reserve headroom; every pick must fit the weight budget.
    if ((tierOf(item) === "heavy" || tierOf(item) === "structural") && weightBudget < heavyCostReserve) {
      deferred.push({ id: item.id, reason: "heavy-reserve" }); continue;
    }
    if (w > weightBudget) { deferred.push({ id: item.id, reason: "weight-budget" }); continue; }

    const planned = { id: item.id, engine: eng, tier: tierOf(item), owned: rf.owned, exclusive: rf.exclusive, locks: lockSetFor(item) };
    if (eng === "agy") {
      // 15.42: agy is worktree-blind → ONE serial lane, at most one agy scheduled per cycle.
      if (serialTaken) { deferred.push({ id: item.id, reason: "agy-serial-lane-taken" }); continue; }
      planned.lane = "serial"; serial.push(planned); serialTaken = true;
    } else {
      planned.lane = "parallel"; parallel.push(planned);
    }
    claimedThisCycle.push({ owned: rf.owned, exclusive: rf.exclusive });
    picked++; weightUsed += w; weightBudget -= w;
  }

  return { slots, mode, active, parallel, serial, deferred, picked, weightUsed };
}
