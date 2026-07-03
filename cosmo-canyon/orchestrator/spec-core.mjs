// Cosmo Canyon — spec-core: the SINGLE source of truth for the loop's SNAPSHOT + trigger decision.
//
// The trigger/snapshot logic used to be DUPLICATED across FOUR surfaces that already drifted (audit
// 15.22): supervisor.mjs (private computeSnapshot/computeTrigger), state.mjs (richer computeSnapshot,
// no trigger), cc-loop.workflow.js (inline decideTrigger that DROPPED latchKey), plan-prep.mjs (a 4th
// inline latchKey map). One edit to any of them desynced the two loop HOSTS (detached supervisor vs
// in-app Workflow) → they'd plan on different triggers = dual authority across hosts. This module owns
// the PURE logic so both hosts branch on IDENTICAL decisions.
//
// Layering: spec-core owns the pure logic and imports the fs/git PRIMITIVES it needs from state.mjs;
// state.mjs RE-EXPORTS computeSnapshot/wipKeywords from here so existing `import {computeSnapshot} from
// "./state.mjs"` callers keep working. The state↔spec-core edge is a SAFE function-level cycle:
// spec-core references state's exports ONLY inside function bodies (never at module-init), so there is
// no TDZ hazard — by the time any exported function is CALLED, both modules are fully evaluated.
//
// computeTrigger is PURE — a function of the SNAPSHOT ONLY. The NO_PLANNER short-circuit and the
// max-replan gate are HOST LOOP STATE, not snapshot; they stay in the CALLERS (supervisor + the
// Workflow script), never here. sense.mjs emits the RAW trigger into the SNAPSHOT (pre-gate); each host
// then applies its own noPlanner/replan gate on top. The Workflow host CANNOT `require`/`import` a .mjs
// at runtime → sense precomputing snap.trigger is what lets its decideTrigger become a passthrough.
import { existsSync, readFileSync, writeFileSync, renameSync } from "node:fs";
import { CONTROL, readJson, headSha, plannerLatch, usageToday, isPaused } from "./state.mjs";
// §15c/§15g phase 5 — the asset-scan sense PRE-STEP + asset-side snapshot fields. Both loop hosts branch on the
// SAME computeSnapshot, so embedding senseAssets HERE (not a 3rd top-level branch) keeps the reconcile+snapshot
// logic single-source across the detached supervisor (imports this) and the Workflow host (runs `node sense.mjs`,
// which imports this) — 15.22 parity. assets-core imports only assets.mjs + lock.mjs → no cycle back into spec-core.
import { senseAssets, computeAssetSnapshot, isTerminal } from "./assets-core.mjs";
// §15g phase 8 — the concurrency mode (serial|parallel) is emitted into the SNAPSHOT so the Workflow host (which
// cannot import a .mjs) can branch on it WITHOUT an extra config-read agent per serial cycle. Both hosts read the
// SAME config.json → the field is byte-identical across hosts (15.22 parity preserved). config.mjs imports only fs.
import { readConfig } from "./config.mjs";
// §15g phase 7 — the Ready-Spec authority. authorityHashOf/readySpecs are the SINGLE hash+set definition (shared
// with compileSpecs so the doc + the hash can't disagree, 15.24). spec-compile imports assets.mjs+lock.mjs only →
// no cycle back into spec-core/state.
import { authorityHashOf, readySpecs } from "./spec-compile.mjs";

// AUTHORITY-HASH: the loop's north-star is the SET of Ready Spec assets.
// specAuthoritySha() = sha1(sorted(readySpecs.map(id:rev:contentHash))) (authorityHashOf). Emitted as the
// SNAPSHOT field `authoritySha`; the Workflow trigger / latchKey / `.authority-consumed` marker all key off it.
// EMPTY authority → "" (falsy → authorityChanged false; authorityEmpty gates the trigger).
export function specAuthoritySha() { return authorityHashOf(); }

// §15b/15.16 authority-change DEBOUNCE. A `specAuthoritySha` change starts/extends a `control/.authority-settle`
// window (~90s) so a curation burst (10 quick Ready-toggles) is ONE coalesced `diff`, not 10 opus plans.
// `authorityChanged` fires only after the window settles; `.authority-consumed` (written by plan-apply on `diff`) coalesces
// to once-per-settled-generation. CC_AUTHORITY_SETTLE_MS overrides the window (tests use 0 = immediate). The
// SETTLE marker is a side effect written on FIRST observation of a new pending sha (the browser touches it too,
// §15d) — never written when sha===consumed (empty/settled), so parity fixtures stay byte-identical.
// settlePath() is a FUNCTION (not a module-init const) — CONTROL is imported across the state↔spec-core cycle,
// so touching it at module-init would TDZ-throw ("Cannot access 'CONTROL' before initialization"); the rails'
// rule is: reference state's exports ONLY inside function bodies. CC_AUTHORITY_SETTLE_MS is process.env → safe at init.
function settlePath() { return `${CONTROL}/.authority-settle`; }
const SETTLE_MS = process.env.CC_AUTHORITY_SETTLE_MS != null ? Number(process.env.CC_AUTHORITY_SETTLE_MS) : 90000;
function readSettle() { try { return JSON.parse(readFileSync(settlePath(), "utf8")); } catch { return null; } }
function writeSettle(o) { try { const p = settlePath(), t = `${p}.tmp`; writeFileSync(t, JSON.stringify(o)); renameSync(t, p); } catch {} }
// authority settle state: {changed, pending}. changed = the debounce window has ELAPSED (fire diff). pending = a
// real authority change EXISTS but is still debouncing (window not elapsed). `pending` is a stable bool within the
// window (two close host reads agree → 15.22 parity holds; only the elapse boundary is time-dependent, like
// auditDue). AUDIT FIX (step7): the host uses `pending` to avoid idle-EXITING inside the window (which dropped the
// diff until a manual restart). (Re)starts the window ONLY on a NEW pending sha (preserve firstSeen otherwise).
export function authoritySettleState(sha, consumed, now = Date.now()) {
  if (!sha || sha === consumed) return { changed: false, pending: false };     // no pending change (empty authority → sha="")
  if (!(SETTLE_MS > 0)) return { changed: true, pending: false };              // test/immediate mode (CC_AUTHORITY_SETTLE_MS=0)
  const s = readSettle();
  if (!s || s.sha !== sha) { writeSettle({ sha, firstSeen: now }); return { changed: false, pending: true }; } // NEW pending sha
  return now - (s.firstSeen || 0) >= SETTLE_MS ? { changed: true, pending: false } : { changed: false, pending: true };
}
export function debouncedAuthorityChanged(sha, consumed, now = Date.now()) { return authoritySettleState(sha, consumed, now).changed; }

// §13.33 deterministic WIP filter: words from any heading tagged "(WIP, DO NOT IMPLEMENT)". Reads the compiled
// `control/spec-doc.md` (Ready specs only — a Not-Ready spec is EXCLUDED from the doc = the PRIMARY WIP wall);
// this keyword grep is the SECONDARY backstop. Absent doc (pre-first-compile) → [] (safe).
export function wipKeywords() {
  let txt = ""; try { txt = readFileSync(`${CONTROL}/spec-doc.md`, "utf8"); } catch {}
  const kws = new Set();
  for (const line of txt.split("\n")) {
    if (/\(WIP[^)]*DO NOT IMPLEMENT\)/i.test(line)) {
      const cleaned = line.replace(/\(WIP[^)]*\)/i, "").replace(/[^A-Za-z ]/g, " ");
      for (const w of cleaned.split(/\s+/)) if (w.length >= 4) kws.add(w.toLowerCase());
    }
  }
  return [...kws];
}

// The full SNAPSHOT the loop branches on (auditHours/cap supplied by the caller; defaults mirror the
// supervisor). Field names are STABLE (authorityChanged/authoritySha/wipKeywords/blockedIds/readyCount/
// auditDue/latch/…) — the authority swap changes specAuthoritySha's body, not this schema.
export function computeSnapshot({ auditHours = 6, cap = 200 } = {}) {
  // §15c asset-scan PRE-STEP (both hosts): reconcileActive + reconcileAssets BEFORE reading the backlog, so a
  // freshly-minted asset bead is already reflected in readyCount/headReadyBead below. Wrapped in senseAssets so
  // a scan fault can never break sensing. On a control plane with no Ready+dirty assets this is a pure no-op →
  // the SNAPSHOT stays byte-identical to pre-phase-5 (parity/regression preserved).
  senseAssets();
  const backlog = readJson(`${CONTROL}/backlog.json`, []);
  // §15c — exclude EVERY terminal-for-loop status (isTerminal: blocked/abandoned/done + the phase-5 parked/
  // superseded, which STAY in the backlog). A parked/superseded bead left in readyCount/headReadyBead would be
  // re-dispatched forever (spin, bypassing the human-clarification gate + blocking the honest to-spec/idle stop).
  const ready = backlog.filter((b) => !isTerminal(b.status));
  // §13.32 — blocked (NOT abandoned). EXCLUDE needsOperator beads: those are TERMINAL human gates (the worker
  // found the bead un-actionable by a code edit — feature already implemented / spec confirm-only / mis-classified),
  // NOT rescope-able failures. Feeding them to the planner's `blocked` mode caused the first-real-run block↔unblock
  // CHURN (the planner kept reopening them, ~1 opus call / 2 min). Excluded here → the planner never sees them → no
  // reopen; the operator resolves via confirm-satisfied / reclassify / reopen. See docs/KNOWLEDGE.md 2026-07-02.
  const blocked = backlog.filter((b) => b.status === "blocked" && !b.needsOperator);
  const sha = specAuthoritySha();
  const consumed = existsSync(`${CONTROL}/.authority-consumed`) ? readFileSync(`${CONTROL}/.authority-consumed`, "utf8").trim() : "";
  const settle = authoritySettleState(sha, consumed); // §15.16 debounce — {changed (window elapsed), pending (change debouncing)}
  const lastaudit = existsSync(`${CONTROL}/.lastaudit`) ? Number(readFileSync(`${CONTROL}/.lastaudit`, "utf8").trim()) || 0 : 0;
  const head = ready[0] || null;
  // §15c asset-side fields (openWork drives the redefined breaker; completion drives the honest stop) computed ONCE.
  const assetSnap = computeAssetSnapshot();
  // §15g phase 7 / 15.33 — the Ready-Spec authority count + the empty-authority flag (shared SNAPSHOT fields).
  const readySpecCount = readySpecs().length;
  const authorityEmpty = readySpecCount === 0;
  // §15.33 — empty authority (no Ready specs) is an honest idle-blocked-on-human, NOT to-spec. Override the asset
  // completion so BOTH hosts report the SAME reason (parity); a genuine toSpec is left untouched.
  let completion = assetSnap.completion;
  if (authorityEmpty && !(completion && completion.toSpec)) {
    completion = { toSpec: false, idleBlockedOnHuman: true, reason: "empty-authority: no Ready specs — mark a Spec asset Ready to resume planning (§15.33)" };
  }
  return {
    headSha: headSha(),
    readyCount: ready.length,
    headReadyBead: head ? { id: head.id, title: head.title, kind: head.kind, tier: head.tier, engine: head.engine || null } : null,
    blockedIds: blocked.map((b) => b.id),
    authorityChanged: settle.changed,        // §15.16 debounced (~90s settle) — not raw sha!=consumed
    authorityChangePending: settle.pending,  // a real authority change is DEBOUNCING → host must not idle-EXIT (audit fix)
    authoritySha: sha,
    auditDue: lastaudit > 0 && Date.now() - lastaudit > auditHours * 3600 * 1000, // only after a prior audit
    wipKeywords: wipKeywords(),
    paused: isPaused(),
    usageToday: usageToday(),
    capReached: usageToday() >= cap,
    latch: plannerLatch(),
    ...assetSnap,
    completion,                        // override (empty-authority idle reason; else the asset completion)
    readySpecCount, authorityEmpty,    // §15g phase 7 — shared SNAPSHOT (authorityEmpty is the FIRST computeTrigger branch)
    concurrencyMode: readConfig().concurrency.mode, // §15g phase 8 — serial|parallel (the Workflow host branches on it)
  };
}

// The per-mode latchKey (§13.31) — a PURE function of (mode, snap). Extracted so computeTrigger AND
// plan-prep.mjs derive the latchKey from ONE place (single source, audit 15.22). plan-prep needs the
// key for its GIVEN mode (the mode the loop already fired), which is NOT necessarily the currently
// highest-precedence mode, so it calls this directly rather than computeTrigger(snap).latchKey (which
// could pick a different mode or return null → wrong key / crash).
export function latchKeyFor(mode, snap) {
  return mode === "diff" ? snap.authoritySha
    : mode === "blocked" ? snap.blockedIds.join(",")
    // topup latches on the AUTHORITY sha, NOT the volatile readyCount (hardening 2026-07-02). A no-change
    // topup ("nothing spec-authored to add") used to latch on readyCount, so as an ASSET-bead queue drains
    // through 2→1→0 each new readyCount missed the latch → a fresh (useless) opus topup fired PER value
    // (observed: 3 empty topups draining a 7-asset settings-menu build). Keying on authoritySha makes a
    // no-change topup STICKY per authority-generation (asked once, never re-asked until the authority
    // changes). Incremental topup is UNaffected: plan-apply DELETES the latch on a productive plan
    // (netChange>0), so a topup that DID add beads still re-fires as the queue drains.
    : mode === "topup" ? snap.authoritySha
    : "due"; // audit
}

// PURE trigger precedence (§13.31): diff > blocked > topup > audit; ONE trigger per fire; a per-mode
// latch suppresses an already-fired empty-result mode. Returns {mode,latchKey} or null. NO host-state
// gates here (NO_PLANNER / max-replan live in the callers) — a pure function of the SNAPSHOT so both
// hosts agree bit-for-bit.
export function computeTrigger(snap) {
  // §15.33 — EMPTY AUTHORITY is the FIRST branch: no Ready specs → NO planner trigger at all (never topup/diff/
  // audit → no junk beads invented against a nonexistent north-star). The loop then reports idle-blocked-on-human
  // (computeSnapshot set that completion). Runtime-empty (operator toggles ALL specs Not-Ready) fires zero opus.
  if (snap.authorityEmpty) return null;
  const latch = snap.latch || {};
  if (snap.authorityChanged && latch.diff !== snap.authoritySha) return { mode: "diff", latchKey: latchKeyFor("diff", snap) };
  if (snap.blockedIds.length) { const k = latchKeyFor("blocked", snap); if (latch.blocked !== k) return { mode: "blocked", latchKey: k }; }
  // topup: low ready queue → ask the planner to author more toward the spec. Compare against the SHARED
  // latchKeyFor (now authoritySha) so a no-change topup is suppressed per authority-generation, not re-fired
  // once per draining readyCount value (hardening 2026-07-02 — see latchKeyFor).
  if (snap.readyCount < 3) { const k = latchKeyFor("topup", snap); if (latch.topup !== k) return { mode: "topup", latchKey: k }; }
  if (snap.auditDue && latch.audit !== "due") return { mode: "audit", latchKey: latchKeyFor("audit", snap) };
  return null;
}
