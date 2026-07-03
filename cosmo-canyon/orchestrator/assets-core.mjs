// Cosmo Canyon — assets-core: the SHARED asset-scan sense PRE-STEP + the derived-Implemented predicate
// (§15c / §15e / §15g phase 5). Factored out so it runs IDENTICALLY in both loop hosts (the detached
// supervisor imports it via computeSnapshot; the in-app Workflow gets it through `node sense.mjs`) — it is a
// sense PRE-STEP, NEVER a 3rd top-level trigger branch (15.22 parity).
//
// The projection is deterministic: a `ready+dirty` asset with no non-terminal bead becomes ONE bead
// (`asset-<assetId>-r<rev>`); a bead outcome reflects back onto the asset (bookkeep writes the Implemented
// provenance / unsure-park via the store primitives). The asset never decides pass/fail — bookkeep does.
//
// Locks (§15c-2 Invariant L): reconcileActive uses `active` (rank 1); the per-asset breaker flags go through
// the store primitives (each locks `asset-<id>` rank 3 INTERNALLY — never nested under another lock); the mint
// step holds `backlog` (rank 5) for the backlog append + the fire-latch write (both backlog-owned). We NEVER
// hold `backlog` while calling a store primitive (that would take rank 3 UNDER rank 5 → Invariant-L violation
// AND double-lock the non-reentrant lock) — the breaker flags are written in a SEPARATE pass before the mint.
//
// ROOT-parameterized via env `CC_CONTROL` (control plane) + `CC_GAME` (the manifest lives under game/) so a
// unit test points at a scratch control + a scratch manifest without touching the live plane.
import { acquire, release } from "./lock.mjs";
import { readIndex, setOperatorBlock, setManifestKey } from "./assets.mjs";
import { deriveManifestKey } from "./parse-instructions.mjs";
import { readFileSync, writeFileSync, renameSync, existsSync } from "node:fs";

const CC = "C:/Vibes/cosmo-canyon";
function controlRoot() { return process.env.CC_CONTROL || `${CC}/control`; }
function gameRoot() { return process.env.CC_GAME || `${CC}/game`; }
function locksDir() { return `${controlRoot()}/locks`; }
function backlogPath() { return `${controlRoot()}/backlog.json`; }
function latchPath() { return `${controlRoot()}/.asset-scan-latch.json`; }
function activePath() { return `${controlRoot()}/active.json`; }
function completionsPath() { return `${controlRoot()}/completions.json`; }
function manifestPath() { return `${gameRoot()}/assets/manifest.json`; }
function audioManifestPath() { return `${gameRoot()}/assets/audio-manifest.json`; }

const ASSET_ABANDON_N = 3;         // §15.3 — asset abandon ceiling (mirrors bookkeep bead ABANDON_N)
const ACTIVE_GRACE_MS = 30 * 1000; // §15c — a just-dispatched active row is not GC'd for ~30s

// beads terminal-FOR-THE-LOOP (never dispatched, never counted as open work / a live reference)
const TERMINAL = new Set(["done", "abandoned", "blocked", "parked", "superseded"]);
export function isTerminal(status) { return TERMINAL.has(status); }

const nowIso = () => new Date().toISOString();
function readJsonSafe(p, d = null) { try { return JSON.parse(readFileSync(p, "utf8")); } catch { return d; } }
function atomicWrite(p, data) { const t = `${p}.tmp`; writeFileSync(t, data); renameSync(t, p); }
function writeJson(p, o) { atomicWrite(p, JSON.stringify(o, null, 2) + "\n"); }

// ── projection: ready+dirty asset → bead ─────────────────────────────────────────────────────────
// image→light/claude, audio→light/claude, spec→heavy/agy (§15c / 15.42). files[] = SRC only (already SRC-only
// on the asset meta, §15.7). NO acceptanceCmd — bookkeep resolves the grader (image/audio → the SHARED
// parameterized `_image-grader`/`_audio-grader` by manifestKey; spec → the planner-authored per-bead
// game/accept/<bead.id>.ts, confirm-gated + mutation-checked + PASS-token). The bead id is path-safe
// (`asset-<a-…>-r<rev>` matches ^[A-Za-z0-9_-]+$ so accept-discovery works). §15e phase 6: image/audio now
// carry a REAL deterministic grader (NOT renderOnly), so a landed image/audio reaches Implemented for the right
// reason (render-reachability / reached-by-playback + manifest real). Only a genuine feel/visual SPEC stays
// feel-pending → routed to the human-gated FEEL-REVIEW queue.
export function projectAssetToBead(asset) {
  const rev = asset.rev;
  const kind = asset.kind;
  const key = asset.manifestKey || asset.filename || asset.id;
  const files = Array.isArray(asset.files) ? asset.files.filter((f) => typeof f === "string" && f.trim()) : [];
  const instr = (asset.instructions || "").trim();
  // acceptanceKind steers bookkeep's grader: image/audio = the shared deterministic grader; spec = "sim"
  // (hard gate: planner grader + mutation-check + PASS token) or "feel" (advisory critic → FEEL-REVIEW, 15.18).
  // A spec is feel/visual when the operator set acceptanceKind OR the instructions/FILENAME read visual. WIDENED
  // after the first real run mis-routed "Game starts with the main menu" to sim+agy: UI/menu/screen/HUD/button/
  // layout/sprite/animation/art/color/theme/font/icon are visual too, and the FILENAME counts (a "ui_*"/"*menu*"
  // spec is visual even with terse instructions). Else "sim" (the hard planner-grader path).
  const specText = `${instr} ${asset.filename || ""}`;
  const acceptanceKind = kind === "image" ? "image" : kind === "audio" ? "audio"
    : (asset.acceptanceKind || (/\b(feel|visual|juice|looks?|aesthetic|polish|vibe|readable|readability|ui|hud|menu|screen|button|title|layout|sprite|anim(?:ation)?|art|colou?r|theme|font|icon)\b/i.test(specText) ? "feel" : "sim"));
  const graderNeedsConfirm = kind === "spec"; // §15.15/15.17 — a planner-authored spec grader lands DISABLED until operator confirm
  const acceptance = kind === "image"
    ? `Image render-reachability grader (auto-minted): getTexture('${key}') referenced + manifest '${key}' real + atlas frame + flipbook differs. bookkeep derive-binds the upload before grading.`
    : kind === "audio"
    ? `Audio reached-by-playback grader (auto-minted): playSfx/playMusic('${key}') referenced + audio-manifest '${key}' real + decodable.`
    : acceptanceKind === "feel"
    ? "Feel/visual spec: snapshot + ADVISORY critic → human-gated FEEL-REVIEW queue (only operator confirm flips Implemented, 15.18)."
    : "Sim-checkable spec: planner authors game/accept/<bead.id>.ts (PROTECTED); lands DISABLED until operator confirm + mutation-check + ACCEPT-PASS token (15.15/15.17).";
  return {
    id: `asset-${asset.id}-r${rev}`,
    title: `Asset ${key}: implement ${kind}${instr ? " per instructions" : ""}`,
    detail: `Auto-projected from Ready+dirty asset ${asset.id} (key=${key}, kind=${kind}, rev=${rev}). ` +
      (instr ? `Instructions: ${instr}` : "No instructions given.") +
      ` Owns SRC files ONLY: ${files.join(", ") || "(none declared)"}. game/accept/** is PROTECTED — never edit it.`,
    kind: "impl",                    // backlog bead kind (schedule/bookkeep treat it as a work bead)
    assetKind: kind,                 // the ASSET's kind (image|audio|spec) — bookkeep routes the grader on this
    acceptanceKind,                  // image|audio|sim|feel — grader routing / FEEL-REVIEW branch
    graderNeedsConfirm,
    tier: kind === "spec" ? "heavy" : "light",
    // engine picks the AUTONOMOUS worker: agy = free headless logic, claude = feel + ALL visual work. A feel/visual
    // spec MUST route to claude — agy is preview-blind (first-real-run gotcha: a menu spec went to agy). A sim spec
    // stays agy-eligible. (In the PRIMARY Claude-Code-agent "Drive" mode the driving agent picks the subagent per
    // task; `engine` is the fallback the autonomous supervisor/Workflow hosts read — see AGENTS.md.)
    engine: kind === "spec" ? (acceptanceKind === "feel" ? "claude" : "agy") : "claude",
    files,
    assetId: asset.id, assetKey: asset.manifestKey || null, rev, contentHash: asset.contentHash || null,
    manifestKey: asset.manifestKey || null,
    acceptance,
    source: "asset-scan", status: "ready", blocked_reason: "", attempts: 0,
    created: nowIso(), updated: nowIso(),
  };
}

// ── the derived-Implemented predicate (§15e / 15.32) — the SOLE Implemented source, PURE ──────────
// implemented(asset) ⟺ completion exists ∧ acceptance PASS(not skipped) ∧ (img/audio) manifest[key]=="real",
// all bound to the CURRENT contentHash+rev at the committed sha. Implemented is NEVER a stored state; this pure
// function is called by BOTH the §15c completion check (via computeAssetSnapshot) AND server.mjs /assets/list.
export function implemented(asset, { completions = [], manifest = {}, audioManifest = {} } = {}) {
  if (!asset || asset.state !== "ready") return false;    // not_ready can never be implemented
  const prov = asset.implementedBy;
  if (!prov) return false;
  if (prov.rev !== asset.rev) return false;               // re-armed/replaced/reopened → stale provenance
  if (asset.implementedContentHash == null || asset.implementedContentHash !== asset.contentHash) return false;
  const comp = (completions || []).find((c) => c && c.id === prov.beadId);
  if (!comp) return false;
  if (comp.supersededByReopen === true) return false;     // §15e reopen: the completion no longer satisfies the predicate
  if (comp.acceptanceSkipped === true) return false;      // fail-closed: skipped acceptance = NOT implemented (15.13)
  if (comp.sha != null && prov.sha != null && String(comp.sha) !== String(prov.sha)) return false;
  if (asset.kind === "image") {
    // image REQUIRES a real manifest slot (derive flips it). A null/absent manifestKey — the DEFAULT for a raw
    // upload until keyed — can NEVER be real → fail-closed. Do NOT short-circuit on a falsy key (audit false-green).
    const mk = asset.manifestKey;
    if (!mk || ((manifest || {})[mk] || {}).status !== "real") return false;
  } else if (asset.kind === "audio") {
    const mk = asset.manifestKey;
    if (!mk || ((audioManifest || {})[mk] || {}).status !== "real") return false;
  } else if (asset.kind === "spec") {
    // §15.18 — a feel/visual spec's land is an ADVISORY critic verdict: Implemented requires an OPERATOR
    // feel-confirm bound to THIS land (beadId + current rev). A model verdict NEVER lands a green.
    if (comp.feelAdvisory === true) {
      const fc = asset.feelConfirmed;
      if (!fc || fc.beadId !== prov.beadId || fc.rev !== asset.rev) return false;
    }
  }
  return true;
}

// feel-pending (phase-5 interim, NOT the phase-6 review QUEUE): the CURRENT-rev bead LANDED (provenance bound to
// contentHash+rev) but the predicate isn't satisfied (renderOnly/acceptance-skipped or manifest not-real yet).
// It is "as far as the deterministic loop can take it" → waiting on a phase-6 grader/derive or an operator; the
// completion predicate treats it as NOT to-spec (honest idle-blocked, never a false green, never re-minted).
export function feelPending(asset, ctx) {
  if (!asset || asset.state !== "ready") return false;
  const prov = asset.implementedBy;
  if (!prov || prov.rev !== asset.rev) return false;
  if (asset.implementedContentHash == null || asset.implementedContentHash !== asset.contentHash) return false;
  return !implemented(asset, ctx);
}

// ── active.json writer (§15c) — written at DISPATCH, removed on ANY terminal outcome ─────────────
export function writeActive(entry) {
  const dir = acquire(locksDir(), "active");
  try {
    let arr = readJsonSafe(activePath(), []);
    if (!Array.isArray(arr)) arr = [];
    arr = arr.filter((r) => r && r.runToken !== entry.runToken && r.beadId !== entry.beadId);
    arr.push({ ...entry, startEpoch: entry.startEpoch || Date.now(), at: nowIso() });
    writeJson(activePath(), arr);
  } finally { release(dir); }
}
export function removeActive({ runToken = null, beadId = null } = {}) {
  const dir = acquire(locksDir(), "active");
  try {
    let arr = readJsonSafe(activePath(), []);
    if (!Array.isArray(arr)) return;
    const kept = arr.filter((r) => r && !((runToken != null && r.runToken === runToken) || (beadId != null && r.beadId === beadId)));
    if (kept.length !== arr.length) writeJson(activePath(), kept);
  } finally { release(dir); }
}

// reconcileActive (§15c) — GC active rows whose tick is gone. Serial: the live tick is control/.tick.json; a row
// whose runToken no longer matches it (and is past the ~30s grace so a just-spawned entry isn't GC'd mid-start)
// is dead. pid liveness is a secondary signal (parallel claims). Best-effort; under the `active` lock (rank 1).
export function reconcileActive({ now = Date.now() } = {}) {
  if (!existsSync(activePath())) return { pruned: 0 };
  const tick = readJsonSafe(`${controlRoot()}/.tick.json`, null);
  const liveToken = tick && tick.runToken;
  const dir = acquire(locksDir(), "active");
  try {
    let arr = readJsonSafe(activePath(), []);
    if (!Array.isArray(arr)) { writeJson(activePath(), []); return { pruned: 0 }; }
    const kept = arr.filter((r) => {
      if (!r) return false;
      if ((now - (r.startEpoch || 0)) <= ACTIVE_GRACE_MS) return true;    // grace: keep a just-spawned row
      if (liveToken && r.runToken === liveToken) return true;             // matches the in-flight tick → live
      if (r.pid && aliveP(r.pid)) return true;                           // parallel: a live pid → keep
      return false;                                                      // stale → GC
    });
    if (kept.length !== arr.length) writeJson(activePath(), kept);
    return { pruned: arr.length - kept.length };
  } finally { release(dir); }
}
function aliveP(pid) { if (!pid || pid <= 0) return false; try { process.kill(pid, 0); return true; } catch (e) { return e.code === "EPERM"; } }

// ── reconcileAssets (§15c) — the mint/supersede pre-step (fire-latched, per-asset breakers) ───────
// PASS 1 (per-asset store writes, each locks asset-<id> internally, NO other lock held): apply the abandon
// breaker (15.3). PASS 2 (mint/supersede under the `backlog` lock): supersede stale-rev in-flight beads (15.39),
// honor the (assetId,rev,contentHash) fire-latch, mint one fresh bead. Idempotent; O(changed) via the latch.
export function reconcileAssets({ now = Date.now() } = {}) {
  const out = { minted: [], superseded: [], parked: [], skipped: 0 };
  const idxAssets = readIndex().assets;
  const assets = (Array.isArray(idxAssets) ? idxAssets : []).filter((a) => a.state === "ready" && a.dirty);
  if (!assets.length) return out;

  // PASS 1 — per-asset breaker flags (no shared lock held; each store primitive locks asset-<id> internally)
  const mintable = [];
  for (const a of assets) {
    if (a.escalated || a.blockedNeedsOperator) { out.skipped++; continue; }   // hard-parked → never mint
    // §15.3: refuse to mint past ASSET_ABANDON_N UNLESS contentHash changed since the abandon (metadata wiggle ≠ reset)
    if ((a.abandonCount || 0) >= ASSET_ABANDON_N && (a.abandonedAtContentHash == null || a.abandonedAtContentHash === a.contentHash)) {
      setOperatorBlock(a.id);
      out.parked.push({ id: a.id, reason: "abandon-breaker (15.3)" });
      continue;
    }
    // §15e upload-keying backstop: an image/audio asset with NO manifestKey can never reach Implemented (the
    // predicate fail-closes). Bind a deterministic key BEFORE minting (the server assigns one at create; this
    // covers migration/edge assets). setManifestKey is bind-once + NO rev bump (won't re-fire the scan latch).
    if ((a.kind === "image" || a.kind === "audio") && !a.manifestKey) {
      try {
        const key = deriveManifestKey({ filename: a.filename, instructions: a.instructions, kind: a.kind, id: a.id });
        const updated = setManifestKey(a.id, key);
        if (updated && updated.manifestKey) { a.manifestKey = updated.manifestKey; a.rev = updated.rev; }
      } catch { /* keep null → the predicate stays fail-closed; the mint still runs (grader will fail honestly) */ }
    }
    mintable.push(a);
  }
  if (!mintable.length) return out;

  // PASS 2 — mint/supersede under the backlog lock (backlog append + fire-latch write are both backlog-owned)
  const held = acquire(locksDir(), "backlog");
  try {
    let backlog = readJsonSafe(backlogPath(), []);
    if (!Array.isArray(backlog)) backlog = [];
    let latch = readJsonSafe(latchPath(), {});
    if (!latch || typeof latch !== "object") latch = {};
    let bChanged = false, lChanged = false;
    for (const a of mintable) {
      // supersede ANY prior non-terminal bead for this asset whose rev < current (15.39 stale-bytes); a
      // current-or-newer-rev non-terminal bead means one is already in flight → don't double-mint.
      let hasCurrent = false;
      for (const b of backlog) {
        if (b && b.assetId === a.id && !isTerminal(b.status)) {
          if ((b.rev || 0) < a.rev) { b.status = "superseded"; b.updated = nowIso(); bChanged = true; out.superseded.push(b.id); }
          else hasCurrent = true;
        }
      }
      if (hasCurrent) { out.skipped++; continue; }
      // fire-latch: this (assetId,rev,contentHash) already fired AND terminated (no non-terminal bead) → don't
      // re-scan/re-mint the same generation every tick (bounds sense to O(changed)).
      const L = latch[a.id];
      if (L && L.rev === a.rev && L.contentHash === a.contentHash) { out.skipped++; continue; }
      const bead = projectAssetToBead(a);
      backlog.push(bead); bChanged = true;
      latch[a.id] = { rev: a.rev, contentHash: a.contentHash, beadId: bead.id, at: nowIso() };
      lChanged = true;
      out.minted.push(bead.id);
    }
    if (bChanged) writeJson(backlogPath(), backlog);
    if (lChanged) writeJson(latchPath(), latch);
  } finally { release(held); }
  return out;
}

// ── asset-side snapshot fields (§15c completion predicate + redefined breaker input) ──────────────
// Bucket EVERY ready asset into exactly one of: implemented | waitingOnHuman | dispatchable | anomaly (defensive
// — a ready asset in none means corrupted meta → park+log). openWork = unimplemented-non-waiting ready assets +
// non-terminal beads (the redefined breaker's signal, 15.20). Returns the fields computeSnapshot merges in.
export function computeAssetSnapshot({ now = Date.now() } = {}) {
  const idx = readIndex();
  const assets = Array.isArray(idx.assets) ? idx.assets : []; // guard: computeAssetSnapshot runs OUTSIDE senseAssets' try/catch (spec-core), so a corrupt derived index must degrade, not crash sensing
  const backlog = readJsonSafe(backlogPath(), []) || [];
  const completions = readJsonSafe(completionsPath(), []) || [];
  const manifest = readJsonSafe(manifestPath(), {}) || {};
  const audioManifest = readJsonSafe(audioManifestPath(), {}) || {};
  const ctx = { completions, manifest, audioManifest };

  const nonTerminalBeads = (Array.isArray(backlog) ? backlog : []).filter((b) => b && !isTerminal(b.status));
  const ready = assets.filter((a) => a.state === "ready");

  // §15g phase 7 — an AUTHORITY-ONLY spec (a Ready spec that is clean, never built, no open questions/breakers —
  // e.g. the spec-legacy monolith) is the COMPILED north-star, NOT an "asset to implement". The planner decomposes
  // it into cc-#### beads; it must not be bucketed as an anomaly (it's not dispatchable) NOR block the to-spec stop
  // (it will never be "implemented" as one unit). A spec toggled Ready by the operator is ready+DIRTY → not
  // authority-only → mints a build bead normally (unchanged). Excluded ONLY from the implement accounting below.
  const isAuthorityOnlySpec = (a) => a.kind === "spec" && !a.dirty && !a.implementedBy &&
    !a.hasOpenQuestions && !a.escalated && !a.blockedNeedsOperator;
  const buildableReady = ready.filter((a) => !isAuthorityOnlySpec(a));
  const authoritySpecsN = ready.length - buildableReady.length;

  let implementedN = 0, waitingN = 0, dispatchableN = 0, dirtyReadyN = 0, openQ = 0, feelPend = 0, escalatedN = 0, operatorN = 0;
  const anomalies = [];
  for (const a of buildableReady) {
    if (a.dirty) dirtyReadyN++;
    const waiting = a.hasOpenQuestions || a.escalated || a.blockedNeedsOperator || feelPending(a, ctx);
    if (a.hasOpenQuestions) openQ++;
    if (a.escalated) escalatedN++;
    if (a.blockedNeedsOperator) operatorN++;
    if (implemented(a, ctx)) { implementedN++; continue; }
    if (feelPending(a, ctx)) feelPend++;
    if (waiting) { waitingN++; continue; }
    const inFlight = nonTerminalBeads.some((b) => b.assetId === a.id);
    if (a.dirty || inFlight) { dispatchableN++; continue; }
    anomalies.push(a.id); // ready, not implemented, not waiting, not dispatchable → corrupted meta
  }
  // defensive: park + log anomalies so a ready asset is never silently spun (invariant: dispatchable ∪
  // implemented ∪ waiting-on-human = all ready assets).
  for (const id of anomalies) { try { setOperatorBlock(id); } catch {} }

  // openWork (15.20): still-buildable ready assets (unimplemented, not waiting-on-human) + non-terminal beads.
  const openWork = dispatchableN + nonTerminalBeads.length;

  // completion (§15c): to-spec ⟺ every ready asset implemented ∧ no ready+dirty ∧ no ready beads ∧ no open
  // questions ∧ no feel-pending/escalated/operator-block. (Gate-green is an invariant of the loop — only green
  // commits land — so it is not re-run here.) idle-blocked-on-human ⟺ nothing dispatchable but work remains.
  const toSpec = buildableReady.length > 0 && implementedN === buildableReady.length && dirtyReadyN === 0 &&
    nonTerminalBeads.length === 0 && openQ === 0 && feelPend === 0 && escalatedN === 0 && operatorN === 0;
  const idleBlockedOnHuman = !toSpec && dispatchableN === 0 && nonTerminalBeads.length === 0 &&
    (openQ > 0 || feelPend > 0 || escalatedN > 0 || operatorN > 0 || anomalies.length > 0);
  const reason = toSpec ? "to-spec"
    : idleBlockedOnHuman ? `idle-blocked-on-human (openQ=${openQ} feelPending=${feelPend} escalated=${escalatedN} operator=${operatorN})`
    : "in-progress";

  return {
    assetsReady: ready.length, assetsAuthoritySpecs: authoritySpecsN, assetsImplemented: implementedN, assetsDispatchable: dispatchableN,
    assetsWaitingHuman: waitingN, assetsDirtyReady: dirtyReadyN, assetsOpenQuestions: openQ,
    assetsFeelPending: feelPend, assetsEscalated: escalatedN, assetsBlockedOperator: operatorN,
    assetAnomalies: anomalies, nonTerminalBeads: nonTerminalBeads.length,
    openWork,
    completion: { toSpec, idleBlockedOnHuman, reason },
  };
}

// The sense PRE-STEP: reconcileActive (GC dead in-flight rows) + reconcileAssets (mint/park). Side-effecting;
// computeSnapshot calls this BEFORE reading the backlog so newly-minted beads are reflected. Wrapped so a scan
// fault NEVER breaks the loop's sensing. Returns the two summaries for logging.
export function senseAssets({ now = Date.now() } = {}) {
  let active = { pruned: 0 }, scan = { minted: [], superseded: [], parked: [], skipped: 0 };
  try { active = reconcileActive({ now }); } catch (e) { active = { pruned: 0, error: String((e && e.message) || e) }; }
  try { scan = reconcileAssets({ now }); } catch (e) { scan = { minted: [], superseded: [], parked: [], skipped: 0, error: String((e && e.message) || e) }; }
  return { active, scan };
}

// ── redefined consecutive-fail breaker (§15c / 15.20) — PURE step, used by the supervisor + tests ─────────────
// Trip on M cycles with NO NET REDUCTION in openWork (unimplemented ready assets + non-terminal beads), NOT on
// "any green somewhere" — one trivial land per cycle that re-mints (openWork flat) must NOT reset it. Benign
// outcomes (parked/unsure/infra-kill) do NOT increment. Reset ONLY on a strict openWork reduction. cc-loop.
// workflow.js REPLICATES this inline (the Workflow host cannot import a .mjs at runtime — same as decideTrigger).
// Benign = never increments the breaker: parked/unsure (Questions surfaced), idle, infra-kill (timeout/agy-noop),
// paused, AND a PRODUCTIVE planner ('planned' — it grows openWork on purpose, that is not a stall). NON-benign
// (bump if no net reduction): reverted, blocked, plan-empty, and 'committed' (a land that did NOT net-reduce
// openWork is masked thrash, 15.20). A strict openWork reduction resets regardless.
const BREAKER_BENIGN = new Set(["parked", "unsure", "idle", "timeout", "agy-noop", "paused", "planned", null]);
export function breakerStep({ breaker = 0, prevOpenWork = null, openWork = 0, outcome = null, breakerN = 5 } = {}) {
  const reduced = prevOpenWork != null && openWork < prevOpenWork;
  let b = breaker;
  if (reduced) b = 0;
  else if (!BREAKER_BENIGN.has(outcome)) b++;
  return { breaker: b, prevOpenWork: openWork, reduced, tripped: b >= breakerN };
}
