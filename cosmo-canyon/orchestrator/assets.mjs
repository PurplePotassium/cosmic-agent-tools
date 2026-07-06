// Cosmo Canyon — folder-per-asset store (§15a / §15g phase 1). Standalone deterministic library.
//
// AUTHORITY vs INDEX. The per-asset `control/assets/<id>/meta.json` is the ONLY authority. `control/
// assets.json` is a PURE DERIVED index (list/sort/search) — rebuildIndex() reconstructs it byte-for-byte
// by scanning `assets/*/meta.json`, so it is NEVER a merge bottleneck and always self-heals: a crash
// between the meta write and the index write leaves the authority intact and the index recoverable on the
// next rebuild (rebuildIndex is a PURE function of the meta set — no wall-clock in the index → identical
// bytes across rebuilds for the same metas).
//
// MARKERS-LAST / ATOMIC (§15a). Every mutation writes meta via temp+rename LAST, under the `asset-<id>`
// lock. On REPLACE the NEW artifact is written to a NEW rev-addressed name (`file.r<rev>.<ext>`) so the
// OLD live artifact is UNTOUCHED until the single atomic meta rename flips `meta.file` — a kill between the
// artifact rename and the meta rename leaves the OLD consistent {meta, artifact} pair (the new bytes are an
// orphan the next rebuild ignores). contentHash is computed ONCE in the create/replace critical section and
// stored on meta — a scanner NEVER re-derives it (audit 15.28 flap).
//
// DIRTY (§15a, absolute-value RMW under the asset-<id> lock, never a toggle). SET true on: upload; REPLACE
// where `newHash != implementedContentHash` (NOT != stored — kills the implemented↔dirty flap, 15.29);
// instructions edit; clear-questions; not_ready→ready; agent appends a Question. CLEARED (false) ONLY on
// green-land or unsure-park — those live in bookkeep (phase 5), NOT in this library; no store primitive here
// ever clears dirty. `rev` is a monotonic authority generation, bumped by upload / REPLACE / instructions /
// answer / state (15.39) — NOT by question append/clear.
//
// 15.45 ID GUARD (load-bearing here — this module mints ids AND builds lock-names/paths from them). Every id
// is validateId()'d (regex `^a-[0-9a-z]+-[0-9a-z]{4}$` AND path-containment under every root) BEFORE any
// path or `asset-<id>` lock name is constructed. The endpoint-level guard is phase 4; this is the store
// layer of the same two-layer guard.
//
// ROOT is parameterized via env `CC_CONTROL` (default the real control/) so unit tests point at a scratch
// dir. No loop wiring, no HTTP, no endpoints, no scanner integration — those are phases 2/4/5.
import { acquire, release } from "./lock.mjs";
import {
  mkdirSync, writeFileSync, renameSync, readFileSync, readdirSync, existsSync, copyFileSync, rmSync,
} from "node:fs";
import { createHash, randomBytes } from "node:crypto";
import { resolve, sep, extname } from "node:path";
import { fileURLToPath } from "node:url";

const CC = "C:/Vibes/cosmo-canyon";

// ── ROOT-parameterized paths (read lazily so a test can set CC_CONTROL before the first call) ──────
function controlRoot() { return process.env.CC_CONTROL || `${CC}/control`; }
function assetsDir() { return `${controlRoot()}/assets`; }
function claimsDir() { return `${controlRoot()}/claims`; }
function trashDir() { return `${controlRoot()}/.trash`; }
function locksDir() { return `${controlRoot()}/locks`; }
function indexPath() { return `${controlRoot()}/assets.json`; }
function assetDir(id) { return `${assetsDir()}/${id}`; }

// ── small local fs helpers (self-contained; state.mjs hardcodes the real control root, so not reused) ──
const nowIso = () => new Date().toISOString();
function readJsonSafe(p, d = null) { try { return JSON.parse(readFileSync(p, "utf8")); } catch { return d; } }
function atomicWrite(p, data) { const t = `${p}.tmp`; writeFileSync(t, data); renameSync(t, p); }
function writeJson(p, o) { atomicWrite(p, JSON.stringify(o, null, 2) + "\n"); }

// ── test-only crash injection (deterministic markers-last / self-heal proofs) ──────────────────────
function maybeCrashBeforeMeta() { if (process.env.CC_ASSETS_CRASH_BEFORE_META) throw new Error("CRASH_INJECT: before meta rename"); }
function maybeCrashBeforeIndex() { if (process.env.CC_ASSETS_CRASH_BEFORE_INDEX) throw new Error("CRASH_INJECT: before index rebuild"); }

// ── content addressing ──────────────────────────────────────────────────────────────────────────
function sha256hex(buf) { return createHash("sha256").update(buf).digest("hex"); }
function contentHashOf(buf) { return "sha256:" + sha256hex(buf); }
function hexOf(hash) { return hash ? String(hash).replace(/^sha256:/, "") : null; }

// ── ext / mime ────────────────────────────────────────────────────────────────────────────────
const EXT_MIME = {
  png: "image/png", jpg: "image/jpeg", jpeg: "image/jpeg", gif: "image/gif",
  wav: "audio/wav", mp3: "audio/mpeg", ogg: "audio/ogg",
  txt: "text/plain", md: "text/markdown",
};
const ALLOWED_EXT = new Set(Object.keys(EXT_MIME));
const KIND_DEFAULT_EXT = { image: "png", audio: "wav", spec: "md" };
const KINDS = new Set(["image", "audio", "spec"]);
const STATES = new Set(["not_ready", "ready"]);
const QUESTION_ROUNDS_MAX = 3; // §15.4 — after 3 unsure-park rounds the asset hard-parks `escalated`
function extFor(filename, kind) {
  const e = filename ? extname(filename).slice(1).toLowerCase() : "";
  if (e && ALLOWED_EXT.has(e)) return e;
  return KIND_DEFAULT_EXT[kind] || "bin";
}
function mimeFor(ext) { return EXT_MIME[ext] || "application/octet-stream"; }

// ── id mint + 15.45 validation ────────────────────────────────────────────────────────────────
const ID_RE = /^a-[0-9a-z]+-[0-9a-z]{4}$/;
const B36 = "0123456789abcdefghijklmnopqrstuvwxyz";
function randB36(n) { const b = randomBytes(n); let s = ""; for (let i = 0; i < n; i++) s += B36[b[i] % 36]; return s; }

// Validate a client/mint id: EXACT format AND path-containment under EVERY root (assets/claims/.trash) so
// `id=../../game/src/...` can never traverse out of the store or into a lock name. Throws on any violation.
export function validateId(id) {
  if (typeof id !== "string" || !ID_RE.test(id)) throw new Error(`bad asset id: ${JSON.stringify(id)}`);
  for (const root of [assetsDir(), claimsDir(), trashDir()]) {
    const abs = resolve(root, id);
    if (abs !== resolve(root, id) || !abs.startsWith(resolve(root) + sep)) throw new Error(`asset id escapes root: ${id}`);
  }
  return id;
}
export function mintId() {
  const epoch = Date.now().toString(36);
  return validateId(`a-${epoch}-${randB36(4)}`);
}

// ── read ────────────────────────────────────────────────────────────────────────────────────────
function readMeta(id) {
  validateId(id);
  const m = readJsonSafe(`${assetDir(id)}/meta.json`, null);
  if (!m) throw new Error(`no asset: ${id}`);
  return m;
}
export function readAsset(id) { return readMeta(id); }

// ── create ───────────────────────────────────────────────────────────────────────────────────────
// bytes=null → a placeholderOnly slot (no artifact). bytes present → an upload (dirty=true).
export function createAsset({
  kind, filename = null, bytes = null, instructions = "", manifestKey = null,
  files = [], placeholderOnly = false, state = "not_ready",
  importDocId = null, importHeading = null, source = null, // import provenance (vestigial — document-import removed; always null now)
} = {}) {
  if (!KINDS.has(kind)) throw new Error(`bad kind: ${kind}`);
  if (!STATES.has(state)) throw new Error(`bad state (human-owned not_ready|ready only): ${state}`);
  const id = mintId();
  const dir = assetDir(id);
  const ext = extFor(filename, kind);
  const rev = 1;
  const buf = bytes == null ? null : Buffer.isBuffer(bytes) ? bytes : Buffer.from(bytes);
  const file = buf ? `file.r${rev}.${ext}` : null;
  const contentHash = buf ? contentHashOf(buf) : null; // computed ONCE, here
  const now = nowIso();
  const meta = {
    id, kind, filename, file, mime: mimeFor(ext),
    contentHash, instructions: instructions || "", questions: [],
    state, dirty: buf != null, hasOpenQuestions: false,
    manifestKey: manifestKey ?? null, files: [...files], placeholderOnly: !!placeholderOnly,
    importDocId: importDocId ?? null, importHeading: importHeading ?? null, source: source ?? null, // import provenance (vestigial)
    implementedBy: null, implementedContentHash: null,
    feelConfirmed: null, placeholderStale: false, // §15e — operator feel-confirm provenance / reopen invalidation flag
    abandonCount: 0, questionRounds: 0,
    // §15.3/15.4 per-asset breaker latches (badges over state=ready, never a state; loop/bookkeep-written):
    abandonedAtContentHash: null, escalated: false, blockedNeedsOperator: false,
    created: now, updated: now, rev,
  };
  const lock = acquire(locksDir(), `asset-${id}`);
  try {
    mkdirSync(dir, { recursive: true });
    if (buf) atomicWrite(`${dir}/${file}`, buf);
    writeJson(`${dir}/meta.json`, meta); // meta LAST
  } finally { release(lock); }
  maybeCrashBeforeIndex();
  rebuildIndex();
  return meta;
}

// ── replace artifact (new bytes) ──────────────────────────────────────────────────────────────────
// New artifact → NEW rev-addressed name; OLD artifact untouched until the atomic meta flip (markers-last).
// rev++ ALWAYS (15.39). dirty SET true iff newHash != implementedContentHash (no implemented↔dirty flap).
export function replaceArtifact(id, bytes) {
  validateId(id);
  const dir = assetDir(id);
  const buf = Buffer.isBuffer(bytes) ? bytes : Buffer.from(bytes);
  const lock = acquire(locksDir(), `asset-${id}`);
  let next;
  try {
    const meta = readMeta(id);
    const newHash = sha256hex(buf); // computed ONCE, here
    const ext = extFor(meta.filename, meta.kind);
    const newRev = meta.rev + 1;
    const newFile = `file.r${newRev}.${ext}`;
    // 1) write NEW artifact at its NEW versioned name (old live artifact left intact)
    atomicWrite(`${dir}/${newFile}`, buf);
    // 2) retain the OLD artifact bytes in history/<oldHexHash>.<ext> (MANDATORY, §15.9)
    if (meta.file && meta.contentHash && existsSync(`${dir}/${meta.file}`)) {
      mkdirSync(`${dir}/history`, { recursive: true });
      const oldExt = extname(meta.file).slice(1) || ext;
      const hp = `${dir}/history/${hexOf(meta.contentHash)}.${oldExt}`;
      if (!existsSync(hp)) copyFileSync(`${dir}/${meta.file}`, hp);
    }
    const dirty = newHash !== hexOf(meta.implementedContentHash) ? true : !!meta.dirty; // absolute value, no flap
    next = {
      ...meta, file: newFile, mime: mimeFor(ext), contentHash: "sha256:" + newHash,
      placeholderOnly: false, dirty, rev: newRev, updated: nowIso(),
    };
    maybeCrashBeforeMeta(); // test hook: kill AFTER artifact+history, BEFORE the meta flip → old pair survives
    writeJson(`${dir}/meta.json`, next); // meta LAST — atomic authority flip
    // 3) drop the prior live artifact AFTER the flip (its bytes are safe in history); a crash before this
    //    leaves a harmless orphan the next rebuild ignores (meta.file is authoritative).
    if (meta.file && meta.file !== newFile) { try { rmSync(`${dir}/${meta.file}`, { force: true }); } catch {} }
  } finally { release(lock); }
  maybeCrashBeforeIndex();
  rebuildIndex();
  return next;
}

// ── pure-meta mutators (RMW under asset-<id> lock, meta LAST, then rebuild index) ──────────────────
function checkRev(meta, rev) {
  if (rev != null && meta.rev !== rev) throw new Error(`rev mismatch: have ${meta.rev}, got ${rev}`);
}
function mutateMeta(id, fn) {
  validateId(id);
  const dir = assetDir(id);
  const lock = acquire(locksDir(), `asset-${id}`);
  let next;
  try {
    const meta = readMeta(id);
    next = fn(meta);
    if (next) { next.updated = nowIso(); writeJson(`${dir}/meta.json`, next); } // meta LAST
  } finally { release(lock); }
  if (next) { maybeCrashBeforeIndex(); rebuildIndex(); }
  return next;
}

export function setInstructions(id, rev, instructions) {
  return mutateMeta(id, (m) => { checkRev(m, rev); return { ...m, instructions: instructions ?? "", dirty: true, rev: m.rev + 1 }; });
}

// §AUDIT-2026-07-04 — RETIRE a SATISFIED spec from the compiled authority (or restore it). Distinct from the
// reopen not_ready path: state + Implemented provenance stay intact; the spec just stops compiling into
// spec-doc.md (spec-compile filters the flag), so landed one-shot fix-specs stop taxing every planner tick.
// NO rev bump — implemented() binds contentHash+rev, so a bump would un-Implement the asset.
export function setAuthorityRetired(id, rev, retired) {
  return mutateMeta(id, (m) => {
    checkRev(m, rev);
    if (m.kind !== "spec") throw new Error(`authorityRetired is spec-only (got kind=${m.kind})`);
    return { ...m, authorityRetired: !!retired };
  });
}

// Human-owned not_ready↔ready ONLY. `implemented` is a DERIVED projection, never a stored state (15.32) →
// reject any other value (the phase-4 endpoint's 400).
export function setState(id, rev, state) {
  if (!STATES.has(state)) throw new Error(`illegal human state (implemented is derived, rejected): ${state}`);
  return mutateMeta(id, (m) => {
    checkRev(m, rev);
    const dirty = m.state === "not_ready" && state === "ready" ? true : !!m.dirty; // only not_ready→ready sets dirty
    return { ...m, state, dirty, rev: m.rev + 1 };
  });
}

// Agent appends a clarifying Question → hasOpenQuestions badge (over state=ready, never a needs_answer
// state — 15.34), dirty=true per §15a, NO rev bump. Dedup by text. NOTE: the unsure-PARK path (phase-5,
// bookkeep) composes this with an explicit dirty=false clear — that clear is not a store primitive.
export function appendQuestion(id, { text, by = "agent" } = {}) {
  return mutateMeta(id, (m) => {
    const t = String(text || "").trim();
    if (!t) throw new Error("empty question");
    if (m.questions.some((q) => q.text === t)) return { ...m, hasOpenQuestions: true, dirty: true };
    const q = { id: `q-${randB36(6)}`, text: t, by, at: nowIso() };
    return { ...m, questions: [...m.questions, q], hasOpenQuestions: true, dirty: true };
  });
}
export function clearQuestions(id) {
  return mutateMeta(id, (m) => ({ ...m, questions: [], hasOpenQuestions: false, dirty: true }));
}

// §15.6 — NO-CLOBBER import question (VESTIGIAL: document-import removed, so nothing calls this now). An incoming
// body change to a `ready`/Implemented spec is surfaced as a Question (operator reviews + applies manually), NEVER
// an overwrite. UNLIKE appendQuestion this does NOT set dirty (a ready spec must not be re-minted into a build bead
// by a review flag) and does NOT bump rev (not an authority-generation change). Dedup by text. Idempotent.
export function appendImportQuestion(id, { text, by = "import" } = {}) {
  return mutateMeta(id, (m) => {
    const t = String(text || "").trim();
    if (!t) throw new Error("empty import question");
    if (m.questions.some((q) => q.text === t)) return { ...m, hasOpenQuestions: true }; // idempotent (no dirty/rev)
    const q = { id: `q-${randB36(6)}`, text: t, by, at: nowIso() };
    return { ...m, questions: [...m.questions, q], hasOpenQuestions: true }; // NO dirty, NO rev bump
  });
}

// Atomic human answer (§15c): verify rev, set instructions, clear ONLY the listed qids (a concurrently-
// appended new question survives), set ready+dirty, bump rev.
export function answer(id, rev, instructions, clearQids = []) {
  return mutateMeta(id, (m) => {
    checkRev(m, rev);
    const clr = new Set(clearQids);
    const remaining = m.questions.filter((q) => !clr.has(q.id));
    return {
      ...m,
      instructions: instructions ?? m.instructions,
      questions: remaining,
      hasOpenQuestions: remaining.length > 0,
      state: "ready",
      dirty: true,
      rev: m.rev + 1,
    };
  });
}

// ── §15g phase-5 loop-written primitives (green-land flip / unsure-park / per-asset breakers) ─────
// These are the ONLY dirty-clears (§15a): green-land (markImplemented/writeImplementedProvenanceHeld) and
// unsure-park (parkUnsure). Everything else here NEVER bumps rev (provenance/questions/breakers are not
// authority-generation changes — a rev bump would re-fire the (assetKey,rev) scan latch).

// LOW-LEVEL provenance write assuming the caller ALREADY HOLDS the asset-<id> lock (bookkeep's §15e land
// critical section, acquireOrdered(['asset-<id>','backlog'])). Meta-last single write; does NOT lock and does
// NOT rebuild the index — the caller releases then rebuilds under assets-index as a SEPARATE step. This is the
// no-double-lock companion to markImplemented (lock.mjs is non-reentrant → calling markImplemented under a held
// asset-<id> lock would self-deadlock). `sha` may be null at land (the commit sha doesn't exist yet) → bound
// post-commit by bindImplementedSha (mirrors patchCompletionSha).
export function writeImplementedProvenanceHeld(id, { beadId, sha = null, contentHash, rev }) {
  validateId(id);
  const dir = assetDir(id);
  const m = readMeta(id);
  const next = {
    ...m,
    implementedBy: { beadId, sha: sha ?? null, contentHash: contentHash ?? null, rev: rev ?? null },
    implementedContentHash: contentHash ?? null,
    dirty: false,                                                     // §15a — THE green-land dirty clear
    questionRounds: 0, escalated: false, blockedNeedsOperator: false, // a clean land closes per-asset breakers
    updated: nowIso(),
  };
  writeJson(`${dir}/meta.json`, next); // meta LAST
  return next;
}

// Standalone provenance flip: acquire asset-<id>, write, rebuild index. For tests / non-bookkeep callers only —
// bookkeep uses writeImplementedProvenanceHeld under its own acquireOrdered land section (no double-lock).
export function markImplemented(id, prov) {
  validateId(id);
  const lock = acquire(locksDir(), `asset-${id}`);
  let next; try { next = writeImplementedProvenanceHeld(id, prov); } finally { release(lock); }
  maybeCrashBeforeIndex(); rebuildIndex();
  return next;
}

// Post-commit: bind the landing sha into an existing null-sha provenance record (idempotent). Leaves meta.json
// patched-dirty (the Stop hook folds it — meta.json is a committable control path). Mirrors patchCompletionSha.
export function bindImplementedSha(id, sha) {
  return mutateMeta(id, (m) => {
    if (!m.implementedBy || m.implementedBy.sha) return null; // only a null-sha prov; already-bound → no-op
    return { ...m, implementedBy: { ...m.implementedBy, sha } };
  });
}

// UNSURE-PARK (§15c / 15.34): worker returned unsure → append+dedup the clarifying Questions, raise the
// hasOpenQuestions BADGE, CLEAR dirty (never re-fires the scan), bump questionRounds; ≥ MAX → escalate. state
// STAYS ready; NO rev bump (questions don't bump rev, §15a). bookkeep composes this with the git revert + the
// bead park (status=parked, NO attempts++) — those are bookkeep's authority, not this store primitive's.
export function parkUnsure(id, { questions = [] } = {}) {
  return mutateMeta(id, (m) => {
    const qs = m.questions.slice();
    for (const raw of questions) {
      const t = String((raw && raw.text) || "").trim();
      if (!t || qs.some((q) => q.text === t)) continue; // dedup by text
      qs.push({ id: `q-${randB36(6)}`, text: t, by: (raw && raw.by) || "agent", at: nowIso() });
    }
    const questionRounds = (m.questionRounds || 0) + 1;
    const escalated = questionRounds >= QUESTION_ROUNDS_MAX ? true : !!m.escalated;
    return { ...m, questions: qs, hasOpenQuestions: true, dirty: false, questionRounds, escalated };
  });
}

// §15.3 — bookkeep records an asset-bead ABANDON on the ASSET (moves the ceiling to the asset, not the bead):
// bump abandonCount + record the contentHash at which it abandoned (reconcileAssets refuses to re-mint past
// ASSET_ABANDON_N UNLESS that hash changes). NO rev/dirty change.
export function bumpAbandon(id, { contentHash = null } = {}) {
  return mutateMeta(id, (m) => ({
    ...m, abandonCount: (m.abandonCount || 0) + 1, abandonedAtContentHash: contentHash ?? m.contentHash ?? null,
  }));
}

// §15.3 — hard-park `blocked_needs_operator` (a badge over ready; reconcileAssets sets it when the abandon
// breaker trips). Idempotent (null → no rewrite/rebuild if already set).
export function setOperatorBlock(id) {
  return mutateMeta(id, (m) => (m.blockedNeedsOperator ? null : { ...m, blockedNeedsOperator: true }));
}

// §15e RE-OPEN (implemented→not_ready) — the store half of the cross-authority invalidation (asset-reconcile.mjs
// does the completions/backlog half). Clears ALL provenance (implementedBy/implementedContentHash/feelConfirmed)
// so the predicate stops seeing it Implemented; sets state=not_ready + dirty=true (§15a: implemented→not_ready
// sets dirty); BUMPS rev so a regenerated grader binds the NEW rev (grader filename is rev-addressed, NEVER
// reused); flags placeholderStale for image/audio (the shipped manifest slot is now suspect, to be re-derived on
// rework); clears per-asset breakers (a clean slate). Does NOT touch shipped game code (§13.36 — operator-gated).
export function reopenForRework(id) {
  return mutateMeta(id, (m) => ({
    ...m,
    state: "not_ready",
    dirty: true,
    rev: m.rev + 1,
    implementedBy: null, implementedContentHash: null, feelConfirmed: null,
    placeholderStale: (m.kind === "image" || m.kind === "audio") ? true : !!m.placeholderStale,
    questionRounds: 0, escalated: false, blockedNeedsOperator: false,
  }));
}

// §15e — BIND-ONCE deterministic manifestKey for an image/audio asset (closes the upload-keying gap: a
// GUI upload defaults manifestKey=null → the DERIVED predicate could never flip it Implemented). Set only
// when currently null; NO rev bump (not new bytes / an authority-generation change — a rev bump would
// needlessly re-fire the scan latch). Idempotent (already-keyed → no-op). Charset-guarded (game manifest key).
export function setManifestKey(id, key) {
  return mutateMeta(id, (m) => {
    if (m.manifestKey) return null; // bind-once
    const k = String(key || "").trim();
    if (!k || !/^[A-Za-z0-9_.-]+$/.test(k) || k.includes("..")) throw new Error(`bad manifestKey: ${JSON.stringify(key)}`);
    return { ...m, manifestKey: k }; // no rev bump, dirty unchanged
  });
}

// §15e FEEL-REVIEW (15.18) — OPERATOR confirm of a feel/visual spec whose grader was an ADVISORY critic verdict
// (never a model self-attest green). This is the ONLY thing that flips a feel spec Implemented: it stamps the
// feelConfirmed provenance the predicate requires + clears dirty (a confirmed feel land is a green-land). A model
// verdict never reaches here. beadId/rev bind the confirm to the specific advisory land (a later rev un-confirms).
export function feelConfirm(id, { beadId = null, rev = null, by = "operator" } = {}) {
  return mutateMeta(id, (m) => ({
    ...m,
    feelConfirmed: { beadId: beadId ?? (m.implementedBy && m.implementedBy.beadId) ?? null, rev: rev ?? m.rev, by, at: nowIso() },
    dirty: false, questionRounds: 0, escalated: false, blockedNeedsOperator: false,
  }));
}

// §GC1 — CONFIRM-SATISFIED: a spec whose feature ALREADY EXISTS in the code (nothing to implement → no land can
// happen → a sim grader would be tautological, and feel-confirm has no advisory land to bind to). The operator —
// or the present driving Claude Code agent, after VERIFYING the feature renders/works — attests it's satisfied.
// Sets the FULL provenance the implemented() predicate needs for a feel-advisory spec (implementedBy WITH the
// landing sha + feelConfirmed), clears dirty + hard-park flags. The completion row + the git commit are written by
// close-satisfied.mjs (the caller). No rev bump. Mirrors feelConfirm but ALSO writes implementedBy (no prior land).
export function markSatisfied(id, { beadId, sha = null, contentHash = null, rev = null, by = "operator" } = {}) {
  return mutateMeta(id, (m) => ({
    ...m,
    implementedBy: { beadId, sha: sha ?? null, contentHash: contentHash ?? m.contentHash ?? null, rev: rev ?? m.rev },
    implementedContentHash: contentHash ?? m.contentHash ?? null,
    feelConfirmed: { beadId, rev: rev ?? m.rev, by, at: nowIso() },
    dirty: false, questionRounds: 0, escalated: false, blockedNeedsOperator: false,
  }));
}

// ── derived index (pure function of the meta set → byte-equivalent across rebuilds) ────────────────
// Row carries meta fields ONLY (incl. `implementedBy` PROVENANCE); it does NOT compute a derived
// `implemented` flag — that predicate needs completions/acceptance/manifest (phases 5-6), out of scope here.
function projectRow(m) {
  return {
    id: m.id, kind: m.kind, filename: m.filename ?? null, file: m.file ?? null, mime: m.mime ?? null,
    contentHash: m.contentHash ?? null,
    state: m.state, dirty: !!m.dirty, hasOpenQuestions: !!m.hasOpenQuestions,
    instructions: m.instructions ?? "", // the Asset Browser row textarea reads a.instructions — list rows MUST carry it or it renders blank + a force-rebuild wipes saved text
    manifestKey: m.manifestKey ?? null, placeholderOnly: !!m.placeholderOnly,
    importDocId: m.importDocId ?? null, importHeading: m.importHeading ?? null, source: m.source ?? null, // import provenance (vestigial — document-import removed)
    files: Array.isArray(m.files) ? m.files : [],
    implementedBy: m.implementedBy ?? null, implementedContentHash: m.implementedContentHash ?? null,
    feelConfirmed: m.feelConfirmed ?? null, placeholderStale: !!m.placeholderStale,
    acceptanceKind: m.acceptanceKind ?? null, // §15e — operator/parse-set: "feel" routes a spec to FEEL-REVIEW
    questionRounds: m.questionRounds ?? 0, abandonCount: m.abandonCount ?? 0,
    // §15.3/15.4 breaker badges (over state=ready) — surfaced so /assets/list + the predicate read them from the index
    escalated: !!m.escalated, blockedNeedsOperator: !!m.blockedNeedsOperator,
    authorityRetired: !!m.authorityRetired, // §AUDIT-2026-07-04 — satisfied spec retired from the compiled authority (state/provenance untouched)
    created: m.created ?? null, updated: m.updated ?? null, rev: m.rev,
  };
}
function countsOf(rows) {
  const c = { total: rows.length, ready: 0, not_ready: 0, image: 0, audio: 0, spec: 0, dirty: 0, openQuestions: 0, placeholderOnly: 0 };
  for (const r of rows) {
    if (r.state === "ready") c.ready++; else if (r.state === "not_ready") c.not_ready++;
    if (r.kind === "image") c.image++; else if (r.kind === "audio") c.audio++; else if (r.kind === "spec") c.spec++;
    if (r.dirty) c.dirty++;
    if (r.hasOpenQuestions) c.openQuestions++;
    if (r.placeholderOnly) c.placeholderOnly++;
  }
  return c;
}

// Reconstruct assets.json by scanning assets/*/meta.json. The ONLY authority is the meta set → a corrupt/
// missing/stale index is fully repaired here (self-heal). No wall-clock → identical bytes for identical
// metas. Held under the assets-index lock (rank 2) for the whole scan+write.
export function rebuildIndex() {
  const lock = acquire(locksDir(), "assets-index");
  try {
    let names = [];
    try { names = readdirSync(assetsDir(), { withFileTypes: true }).filter((d) => d.isDirectory()).map((d) => d.name); } catch { names = []; }
    const rows = [];
    for (const id of names) {
      if (!ID_RE.test(id)) continue; // ignore stray non-asset dirs
      const m = readJsonSafe(`${assetsDir()}/${id}/meta.json`, null);
      if (!m || m.id !== id) continue; // skip absent/torn/mismatched — authority-only reconstruction
      rows.push(projectRow(m));
    }
    rows.sort((a, b) => (a.id < b.id ? -1 : a.id > b.id ? 1 : 0));
    const index = { assets: rows, counts: countsOf(rows) };
    mkdirSync(controlRoot(), { recursive: true });
    writeJson(indexPath(), index); // atomic; deterministic
    return index;
  } finally { release(lock); }
}
export function readIndex() { return readJsonSafe(indexPath(), { assets: [], counts: countsOf([]) }); }

// ── tiny ops CLI: `node assets.mjs rebuild` (self-heal the index by hand) ──────────────────────────
if (process.argv[1] && fileURLToPath(import.meta.url) === resolve(process.argv[1])) {
  const cmd = process.argv[2];
  if (cmd === "rebuild") { const idx = rebuildIndex(); console.log(JSON.stringify(idx.counts)); }
  else { console.error("usage: node assets.mjs rebuild"); process.exit(2); }
}
