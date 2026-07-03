// Cosmo Canyon — STANDALONE control-surface app (§15g phase 3; PLAN §1/§6/§15d).
//
// This is the MOVE of the Launcher's cosmo server code out of D:\Ag\launcher\server.js into the
// cosmo-canyon repo. Node built-in `http` (no express dependency — the cosmo-canyon root has no
// package.json; this runs with `node server.mjs` on Node ≥18 for global `fetch`). It:
//   • serves the standalone dashboard (ui/index.html) + the auto-snapshot PNG on :7788 (SINGLE-INSTANCE),
//   • hosts ALL /api/cosmocanyon/* routes (start/stop/status/backlog/suggestions/completions/agent/
//     focus/snapshot/aux/assets/rollback),
//   • spawns/adopts the DETACHED supervisor.mjs (cc-start) + kills its tree (cc-stop),
//   • owns ccEnsureVite(:8780) + the auto-snapshot cadence.
//
// Deterministic rails are UNCHANGED: bookkeep.mjs stays the SOLE gate/commit/revert authority. This host
// only spawns the supervisor + serves endpoints. Concurrency stays SERIAL (config read for display only —
// this host never dispatches, flips the toggle, or spawns worktrees).
//
// §15i guards carried WITH the moved code:
//   15.23 — every control-plane RMW write goes through the SAME orchestrator/lock.mjs on the §15c-2 named
//           lock (backlog POST/DELETE + suggestions accept/reject → 'backlog'; POST /agent → 'agent'; spec/
//           asset git commit → 'git-tree'). No private helper, no lockless RMW.
//   15.44 — an optional acceptanceCmd on POST /backlog is allowlisted to the exact `node accept/<id>.ts`
//           shape (mirrors bookkeep's ACCEPT_CMD_RE); shell metachars rejected 400.
//   15.45 — client ids are charset-validated (reject `../../x` traversal); the flat-manifest upload key is
//           charset-validated + path-contained under assets/source/.
//   15.51 — /assets/upload parses the PNG IHDR (dimension + pixel cap) BEFORE derive, enforces a decoded
//           byte cap, and gives the derive child a hard timeout + single-in-flight guard.
import http from "node:http";
import fs from "node:fs";
import { readFileSync, writeFileSync, existsSync, statSync, mkdirSync, renameSync, openSync, unlinkSync } from "node:fs";
import path from "node:path";
import net from "node:net";
import { exec, execSync, spawn } from "node:child_process";
import { fileURLToPath, pathToFileURL } from "node:url";
import { createHash } from "node:crypto";
// §15i 15.23 — the SAME control-plane lock the supervisor/bookkeep/plan-apply use, so browser RMW writes
// serialize with the agents' writes (no lost-update). This host lives IN cosmo-canyon (same drive) so a
// relative in-package import is correct + simplest — the Launcher needed a file:///C:/… URL only because it
// was a cross-drive (D:→C:) absolute ESM specifier. Same lock.mjs, same CC_CONTROL/locks dir, same names.
import { acquire as ccAcquire, release as ccRelease } from "./orchestrator/lock.mjs";
// §15g phase 4 — the folder-per-asset STORE (phase 1). Endpoints WIRE to it; its mutators lock INTERNALLY
// (asset-<id> for the meta write, then assets-index for the rebuild, as SEPARATE steps — never both held),
// so the endpoints call the store fns directly and DO NOT re-wrap them in ccWithLock (double-locking the
// non-reentrant asset-<id> lock would self-deadlock). The one exception is DELETE (no store primitive): it
// tombstones under an asset-<id> lock then calls rebuildIndex() as a separate step, matching the same design.
import * as ccAssets from "./orchestrator/assets.mjs";
// Worker-selection: the operator's ALLOWED model set + the orchestrator's task-fit pick (shared with the hosts).
import { WORKER_OPTIONS, readAllowed, normalizeAllowed, agyCoolingDown } from "./orchestrator/agent-core.mjs";
// Concurrency/runtime config — the SAME normalizer the loop hosts read through, so the GUI shows effective values.
import { readConfig as ccReadConfig, MAX_CONCURRENCY, MIN_PARALLEL, MAX_TICK_BUDGET } from "./orchestrator/config.mjs";
import { implemented as ccImplemented } from "./orchestrator/assets-core.mjs"; // §15e/15.32 — the SOLE derived-Implemented predicate (shared with the loop)
import { deriveManifestKey } from "./orchestrator/parse-instructions.mjs"; // §15e — deterministic manifestKey for a fresh image/audio upload (closes the keying gap)
import { reopenAsset as ccReopenAsset } from "./orchestrator/asset-reconcile.mjs"; // §15e — reopen (implemented→not_ready) cross-authority invalidation
import { compileSpecs as ccCompileSpecs } from "./orchestrator/spec-compile.mjs"; // §15b/§15g phase 7 — recompile the Ready-Spec authority (doc/index/hash) after a spec write
import { closeSatisfied as ccCloseSatisfied } from "./orchestrator/close-satisfied.mjs"; // §GC1 — operator "confirm-satisfied" for a spec already satisfied by existing code (the acceptance route the loop lacked)

const __dirname = path.dirname(fileURLToPath(import.meta.url)); // C:\Vibes\cosmo-canyon
const CC_DIR = __dirname;
const VIBES = path.resolve(CC_DIR, ".."); // C:\Vibes — the shared repo toplevel (control-plane git ops run here)
const CC_GAME_REPO = process.env.CC_GAME || path.join(CC_DIR, "game"); // §SPLIT — the game's own nested repo (rollback + known-good target)
const PORT = Number(process.env.CC_PORT) || 7788; // CC_PORT override for tests (single-instance refusal)

// ROOT-parameterized (STEP-1/2 convention): CC_CONTROL lets a test point the control plane at a scratch dir
// so it never perturbs the live backlog (the §15g gotcha). game/ paths stay real (they read, don't mutate).
const CC_CONTROL = process.env.CC_CONTROL ? path.resolve(process.env.CC_CONTROL) : path.join(CC_DIR, "control");
// §15g phase 4 gotcha (a): assets.mjs reads process.env.CC_CONTROL LAZILY (controlRoot()); server.mjs
// resolves its own CC_CONTROL const. Pin the env to the SAME resolved absolute path so the store and this
// host ALWAYS agree on the control root (esp. under a throwaway-CC_CONTROL test where a relative env value
// would otherwise diverge: server resolves it, assets used it raw). Same real dir either way; this removes
// the / vs \ + relative-path ambiguity entirely.
process.env.CC_CONTROL = CC_CONTROL;
const CC_LOCKS = path.join(CC_CONTROL, "locks"); // §15i 15.23 — same locks dir as orchestrator/lock.mjs
const CC_GAME_DIR = path.join(CC_DIR, "game");
const CC_UI = path.join(CC_DIR, "ui", "index.html");
// CC_SUPERVISOR env override = a test seam (same convention as CC_CONTROL): a verify run points it at a
// stub so cc-start/stop spawn+kill wiring is provable without a real token-burning claude -p tick.
const CC_SUPERVISOR = process.env.CC_SUPERVISOR ? path.resolve(process.env.CC_SUPERVISOR) : path.join(CC_DIR, "orchestrator", "supervisor.mjs");
const CC_BACKLOG = path.join(CC_CONTROL, "backlog.json");
const CC_SUGGESTIONS = path.join(CC_CONTROL, "suggestions.json");
const CC_REJECTED = path.join(CC_CONTROL, "rejected.json");
const CC_ACCEPTED = path.join(CC_CONTROL, "accepted.md");
const CC_COMPLETIONS = path.join(CC_CONTROL, "completions.json");
const CC_AGENT = path.join(CC_CONTROL, "agent.json");
const CC_FOCUS = path.join(CC_CONTROL, "focus.md");
const CC_STATUS = path.join(CC_CONTROL, "status.json");
const CC_PAUSED = path.join(CC_CONTROL, ".paused");
const CC_GUARD_ALERT = path.join(CC_CONTROL, ".guard-alert");
const CC_TICK = path.join(CC_CONTROL, ".tick.json");
const CC_AGY_PID = path.join(CC_CONTROL, ".agy.pid");
const CC_SUP_PID = path.join(CC_CONTROL, ".supervisor.pid");
const CC_AGY_STRIKES = path.join(CC_CONTROL, ".agy-strikes");
const CC_HOST_LOCK = path.join(CC_CONTROL, ".cc-host.lock"); // §6 single-instance pidfile (gitignored)
const CC_FEELREVIEW = path.join(CC_GAME_DIR, "docs", "FEEL-REVIEW.md");
const CC_SNAPSHOT = path.join(CC_DIR, "snapshots", "latest.png");
const CC_PLANNER_LOG = path.join(CC_DIR, "logs", "planner-log.md");
const CC_HOST_LOG = path.join(CC_DIR, "logs", "host.log");
const CC_AUTHORITY_CONSUMED = path.join(CC_CONTROL, ".authority-consumed"); // authority-consumed marker (gitignored)
const CC_AUTHORITY_SETTLE = path.join(CC_CONTROL, ".authority-settle"); // §15.16 debounce window marker (gitignored)
const CC_CONFIG = path.join(CC_CONTROL, "config.json");
const CC_ASSETS = path.join(CC_GAME_DIR, "assets");
const CC_MANIFEST = path.join(CC_ASSETS, "manifest.json");
const CC_SOURCE = path.join(CC_ASSETS, "source");
const CC_SNAP_SCRIPT = path.join(CC_DIR, "orchestrator", "snapshot.mjs");

const CC_DAILY_CAP = 200; // supervisor default cap (--cap) — for the banner "N/cap today" readout
const CC_BODY_MAX = 32 * 1024 * 1024; // request-body hard cap (base64 asset drops need headroom)
const CC_ASSET_MAX = 24 * 1024 * 1024; // decoded asset byte cap (§15d/15.51)
const CC_PNG_MAX_DIM = 8192; // reject a PNG wider/taller than this (15.51 decompression-bomb guard)
const CC_PNG_MAX_PX = 16 * 1024 * 1024; // ~16M px cap (15.51)
const CC_DERIVE_TIMEOUT_MS = 60000; // derive child hard timeout (15.51)
const PNG_MAGIC = Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]);
const ACCEPT_CMD_RE = /^(node|tsx) accept\/[A-Za-z0-9_-]+\.ts$/; // §15i 15.44 — mirrors bookkeep.mjs
const ID_RE = /^[A-Za-z0-9_-]+$/; // §15i 15.45 — bead/suggestion id charset (rejects `../../x` traversal)
const KEY_RE = /^[A-Za-z0-9_.-]+$/; // flat-manifest key charset (dots allowed, no path separators)
// §15i 15.45 — ENDPOINT-LAYER asset-id guard (the store enforces the SAME regex + path-containment; §15d
// says do BOTH). Exact mint format `a-<base36epoch>-<4×base36>`; rejects `../../x`, absolute paths, metachars
// BEFORE any path or `asset-<id>` lock name is built from a client id.
const ASSET_ID_RE = /^a-[0-9a-z]+-[0-9a-z]{4}$/;

const CC_AGY_COOLDOWN = path.join(CC_CONTROL, ".agy-cooldown");

// ── small fs/json helpers (ported; BOM-safe) ─────────────────────────────────
function readJson(p) { return JSON.parse(readFileSync(p, "utf-8").replace(/^﻿/, "")); }
function ccReadArr(p) { try { return readJson(p); } catch { return []; } }
function ccReadFile(p) { try { return readFileSync(p, "utf-8").replace(/^﻿/, ""); } catch { return ""; } }
function ccAtomicWrite(p, str) { const tmp = p + ".tmp"; writeFileSync(tmp, str, "utf-8"); renameSync(tmp, p); }
function ccWriteArr(p, arr) { ccAtomicWrite(p, JSON.stringify(arr, null, 2)); }

// Serialize a browser RMW against the agents' writes via the shared lock. acquire() is synchronous
// (Atomics.wait) so it briefly blocks the event loop under contention — acceptable for the single-user GUI;
// holds are a few ms. Stale-breakable, so a killed tick/agy never deadlocks the GUI (§15i 15.48).
function ccWithLock(name, fn) {
  const lk = ccAcquire(CC_LOCKS, name, { retries: 60, waitMs: 40 });
  try { return fn(); } finally { ccRelease(lk); }
}

function ccPidAlive(pid) { if (!pid || pid <= 0) return false; try { process.kill(pid, 0); return true; } catch (e) { return e.code === "EPERM"; } }
function ccAlive(pidFile) { try { const pid = parseInt(readFileSync(pidFile, "utf-8").trim(), 10); return ccPidAlive(pid) ? pid : 0; } catch { return 0; } }
function ccKillTree(pidFile) {
  const pid = ccAlive(pidFile);
  if (pid) { try { execSync(`taskkill /PID ${pid} /T /F`); } catch {} }
  try { unlinkSync(pidFile); } catch {}
  return pid;
}
function checkPort(port) {
  return new Promise((resolve) => {
    const socket = new net.Socket();
    socket.setTimeout(400);
    socket.on("connect", () => { socket.destroy(); resolve(true); });
    socket.on("timeout", () => { socket.destroy(); resolve(false); });
    socket.on("error", () => { socket.destroy(); resolve(false); });
    socket.connect(port, "127.0.0.1");
  });
}

// ── asset provenance note ────────────────────────────────────────────────────
// (Document import/splitter removed — authority is the Ready Spec set, created directly via the Asset Browser.)

// ── asset pipeline (flat manifest — §5) ──────────────────────────────────────
function ccReadManifest() { try { return readJson(CC_MANIFEST); } catch { return {}; } }
function ccReadAudioManifest() { try { return readJson(path.join(CC_ASSETS, "audio-manifest.json")); } catch { return {}; } }
// §15e — is this asset's real/implemented slot occupied? (replace/delete confirm-gate) kind-aware: image reads
// manifest.json, audio reads audio-manifest.json; any implementedBy provenance also counts.
function ccIsRealAsset(meta) {
  if (meta && meta.implementedBy) return true;
  if (!meta || !meta.manifestKey) return false;
  const mm = meta.kind === "audio" ? ccReadAudioManifest() : ccReadManifest();
  return (mm[meta.manifestKey] || {}).status === "real";
}
function ccAssetSummary() {
  const m = ccReadManifest(); const keys = Object.keys(m);
  const real = keys.filter((k) => m[k].status === "real");
  const wishlist = keys.filter((k) => m[k].status !== "real");
  return { manifest: m, wishlist, counts: { total: keys.length, real: real.length, placeholder: wishlist.length } };
}
let ccDeriving = false; // §15i 15.51 — single-in-flight derive guard
function ccRunDerive(cb) {
  ccDeriving = true;
  exec("node derive.mjs", { cwd: CC_GAME_DIR, windowsHide: true, maxBuffer: 4 * 1024 * 1024, timeout: CC_DERIVE_TIMEOUT_MS, killSignal: "SIGKILL" },
    (err, stdout, stderr) => { ccDeriving = false; cb(err, ((stdout || "") + (stderr || "")).trim()); });
}

// ── vite (:8780) keep-alive + auto-snapshot cadence (owned by THIS host now) ──
let ccViteStarting = false;
function ccEnsureVite() {
  if (ccViteStarting) return;
  if (!existsSync(path.join(CC_GAME_DIR, "package.json"))) return;
  checkPort(8780).then((inUse) => {
    if (inUse) return;
    ccViteStarting = true;
    const log = path.join(CC_DIR, "logs", "vite.log");
    try { mkdirSync(path.join(CC_DIR, "logs"), { recursive: true }); } catch {}
    exec(`npm run dev >> "${log}" 2>&1`, { cwd: CC_GAME_DIR, windowsHide: true }, () => { ccViteStarting = false; });
    setTimeout(() => { ccViteStarting = false; }, 15000);
    console.log("[cc] starting vite dev server on :8780");
  }).catch(() => {});
}
let ccSnapping = false;
function ccSnapshot() {
  if (ccSnapping) return;
  checkPort(8780).then((up) => { if (!up) return; ccSnapping = true;
    exec(`node "${CC_SNAP_SCRIPT}" --out "${CC_SNAPSHOT}"`, { cwd: CC_DIR, windowsHide: true, maxBuffer: 4 * 1024 * 1024 }, () => { ccSnapping = false; });
    setTimeout(() => { ccSnapping = false; }, 20000);
  }).catch(() => { ccSnapping = false; });
}

// ── daily-usage readout (for the banner) ─────────────────────────────────────
function ccUsageToday() { try { const d = new Date(); const s = `${d.getFullYear()}${String(d.getMonth() + 1).padStart(2, "0")}${String(d.getDate()).padStart(2, "0")}`; return readJson(path.join(CC_CONTROL, `.usage-${s}.json`)).ticks || 0; } catch { return 0; } }

// ════════════════════════════════════════════════════════════════════════════
// HTTP plumbing
// ════════════════════════════════════════════════════════════════════════════
function sendJson(res, code, obj) { const s = JSON.stringify(obj); res.writeHead(code, { "Content-Type": "application/json", "Cache-Control": "no-store" }); res.end(s); }
function sendText(res, code, s, type = "text/plain") { res.writeHead(code, { "Content-Type": type, "Cache-Control": "no-store" }); res.end(s); }
function sendFile(res, file, type) {
  try { const buf = readFileSync(file); res.writeHead(200, { "Content-Type": type, "Cache-Control": "no-store" }); res.end(buf); }
  catch { res.writeHead(404); res.end(); }
}
function readJsonBody(req, maxBytes) {
  return new Promise((resolve, reject) => {
    let size = 0; const chunks = []; let aborted = false;
    req.on("data", (d) => { if (aborted) return; size += d.length; if (size > maxBytes) { aborted = true; const e = new Error("payload too large"); e.code = 413; reject(e); try { req.destroy(); } catch {} return; } chunks.push(d); });
    req.on("end", () => { if (aborted) return; if (!chunks.length) return resolve({}); try { resolve(JSON.parse(Buffer.concat(chunks).toString("utf-8").replace(/^﻿/, ""))); } catch (e) { e.code = 400; reject(e); } });
    req.on("error", (e) => { if (!aborted) reject(e); });
  });
}

// ── route handlers ───────────────────────────────────────────────────────────
function hStatus(res) {
  const backlog = ccReadArr(CC_BACKLOG);
  const ready = backlog.filter((b) => !["blocked", "abandoned", "done"].includes(b.status));
  const blocked = backlog.filter((b) => b.status === "blocked");
  const abandoned = backlog.filter((b) => b.status === "abandoned");
  let lastStatus = {}; try { lastStatus = readJson(CC_STATUS); } catch {}
  let tick = null; try { tick = readJson(CC_TICK); } catch {}
  let paused = false, pausedReason = null;
  if (existsSync(CC_PAUSED)) { paused = true; try { pausedReason = JSON.parse(ccReadFile(CC_PAUSED) || "{}").reason || "manual"; } catch { pausedReason = "manual"; } }
  let guardAlert = null; if (existsSync(CC_GUARD_ALERT)) { try { guardAlert = JSON.parse(ccReadFile(CC_GUARD_ALERT)); } catch {} }
  let agyStrikes = 0; try { agyStrikes = parseInt(ccReadFile(CC_AGY_STRIKES).trim(), 10) || 0; } catch {}
  let stalled = null; if (existsSync(path.join(CC_CONTROL, ".stalled"))) { try { stalled = JSON.parse(ccReadFile(path.join(CC_CONTROL, ".stalled"))); } catch { stalled = { sinceCommit: "?" }; } }
  let knownGood = null; try { knownGood = execSync(`git -C "${CC_GAME_REPO}" rev-parse --short cc-known-good`, { encoding: "utf-8" }).trim(); } catch {} // §SPLIT — the GAME repo's known-good (what rollback restores)
  const supPid = ccAlive(CC_SUP_PID);
  let supStarted = null; if (supPid) { try { supStarted = statSync(CC_SUP_PID).mtime.toISOString(); } catch {} }
  let mode = "serial"; try { mode = readJson(CC_CONFIG).concurrency.mode || "serial"; } catch {}
  sendJson(res, 200, {
    supervisor: { alive: !!supPid, pid: supPid || null, started: supStarted },
    inFlight: tick, stage: lastStatus.stage || "idle", lastStatus,
    paused, pausedReason, guardAlert, agyStrikes, stalled, knownGood, mode,
    usage: { today: ccUsageToday(), cap: (() => { try { return ccReadConfig().tickBudget || CC_DAILY_CAP; } catch { return CC_DAILY_CAP; } })() },
    counts: { ready: ready.length, blocked: blocked.length, abandoned: abandoned.length, completions: ccReadArr(CC_COMPLETIONS).length, suggestions: ccReadArr(CC_SUGGESTIONS).filter((s) => s.status !== "closed").length },
    snapshotMtime: (() => { try { return statSync(CC_SNAPSHOT).mtimeMs; } catch { return 0; } })(),
  });
}

function hStart(res) {
  ccEnsureVite();
  if (ccAlive(CC_SUP_PID)) return sendJson(res, 200, { ok: true, already: true, pid: ccAlive(CC_SUP_PID) });
  try { unlinkSync(CC_PAUSED); } catch {}
  try { mkdirSync(path.join(CC_DIR, "logs"), { recursive: true }); } catch {}
  let fd; try { fd = openSync(CC_HOST_LOG, "a"); } catch { fd = "ignore"; }
  const child = spawn("node", [CC_SUPERVISOR], { cwd: CC_DIR, detached: true, windowsHide: true, stdio: ["ignore", fd, fd] });
  child.unref();
  try { ccAtomicWrite(CC_SUP_PID, String(child.pid)); } catch {}
  console.log(`[cc] supervisor started pid ${child.pid}`);
  sendJson(res, 200, { ok: true, pid: child.pid });
}
function hStop(res) {
  try { ccAtomicWrite(CC_PAUSED, JSON.stringify({ reason: "manual", at: new Date().toISOString() })); } catch {}
  const sup = ccKillTree(CC_SUP_PID);
  ccKillTree(CC_AGY_PID);
  try { const t = readJson(CC_TICK); if (t.pid) { try { execSync(`taskkill /PID ${t.pid} /T /F`); } catch {} } } catch {}
  try { unlinkSync(CC_TICK); } catch {}
  sendJson(res, 200, { ok: true, killed: sup });
}

function hBacklogPost(res, b) {
  const title = (b && b.title || "").trim();
  if (!title) return sendJson(res, 400, { error: "title is required" });
  // §15i 15.44 — if an acceptanceCmd is supplied it is untrusted; allowlist the exact grader shape.
  if (b.acceptanceCmd != null && !ACCEPT_CMD_RE.test(String(b.acceptanceCmd).trim()))
    return sendJson(res, 400, { error: 'acceptanceCmd must match "node accept/<id>.ts" (no shell metachars)' });
  const now = new Date().toISOString();
  const bead = { id: `cc-m${Date.now()}`, title, detail: (b.detail || "").trim(), files: Array.isArray(b.files) ? b.files : [],
    kind: b.kind === "design" ? "design" : "impl", tier: ["light", "heavy", "structural"].includes(b.tier) ? b.tier : "light",
    engine: b.engine || undefined, acceptance: (b.acceptance || "node gate green; change does what it says").trim(),
    ...(b.acceptanceCmd ? { acceptanceCmd: String(b.acceptanceCmd).trim() } : {}),
    renderOnly: b.renderOnly === true, source: "manual", status: "ready", blocked_reason: "", attempts: 0, created: now, updated: now };
  try { ccWithLock("backlog", () => { const arr = ccReadArr(CC_BACKLOG); if (b.top) arr.unshift(bead); else arr.push(bead); ccWriteArr(CC_BACKLOG, arr); }); sendJson(res, 200, { ok: true, bead }); }
  catch (e) { sendJson(res, 500, { error: e.message }); }
}
function hBacklogDelete(res, b) {
  const id = b && b.id; if (!id) return sendJson(res, 400, { error: "id required" });
  if (!ID_RE.test(String(id))) return sendJson(res, 400, { error: "invalid id" }); // §15i 15.45
  try { ccWithLock("backlog", () => ccWriteArr(CC_BACKLOG, ccReadArr(CC_BACKLOG).filter((x) => x.id !== id))); sendJson(res, 200, { ok: true }); } catch (e) { sendJson(res, 500, { error: e.message }); }
}
function hSuggAccept(res, b) {
  const id = b && b.id; if (!id) return sendJson(res, 400, { error: "id required" });
  if (!ID_RE.test(String(id))) return sendJson(res, 400, { error: "invalid id" }); // §15i 15.45
  // suggestions.json is written by plan-apply UNDER the 'backlog' lock → take the SAME lock here (§15i 15.23)
  try { ccWithLock("backlog", () => { const arr = ccReadArr(CC_SUGGESTIONS); const s = arr.find((x) => x.id === id);
    if (s) { fs.appendFileSync(CC_ACCEPTED, `\n## ${new Date().toISOString()} — ${s.title}\n\n${s.body || ""}\n`, "utf-8"); ccWriteArr(CC_SUGGESTIONS, arr.filter((x) => x.id !== id)); } });
    sendJson(res, 200, { ok: true, note: "Logged to accepted.md — turn it into a Ready Spec asset to make it canonical." }); } catch (e) { sendJson(res, 500, { error: e.message }); }
}
function hSuggReject(res, b) {
  const id = b && b.id; if (!id) return sendJson(res, 400, { error: "id required" });
  if (!ID_RE.test(String(id))) return sendJson(res, 400, { error: "invalid id" }); // §15i 15.45
  const reason = (b && b.reason || "").trim();
  try { ccWithLock("backlog", () => { const arr = ccReadArr(CC_SUGGESTIONS); const s = arr.find((x) => x.id === id);
    if (s) { const rej = ccReadArr(CC_REJECTED); rej.push({ id: s.id, title: s.title, reason, rejected: new Date().toISOString() }); ccWriteArr(CC_REJECTED, rej); ccWriteArr(CC_SUGGESTIONS, arr.filter((x) => x.id !== id)); } });
    sendJson(res, 200, { ok: true }); } catch (e) { sendJson(res, 500, { error: e.message }); }
}
function hAgentPost(res, b) {
  // Operator sets the ALLOWED model set (checkboxes); the orchestrator picks per bead within it (agent-core).
  // An empty/invalid set normalizes to ALL (never leave the loop with zero workers). Re-saving also clears any
  // agy quota cooldown — the operator re-affirming agy is an explicit "try it again" signal.
  if (!b || !Array.isArray(b.allowed)) return sendJson(res, 400, { error: "allowed must be an array of model keys (agy|sonnet|opus)" });
  const allowed = normalizeAllowed(b.allowed);
  try {
    ccWithLock("agent", () => ccAtomicWrite(CC_AGENT, JSON.stringify({ allowed, updated: new Date().toISOString() }, null, 2)));
    try { unlinkSync(CC_AGY_COOLDOWN); } catch {}
    sendJson(res, 200, { ok: true, allowed });
  } catch (e) { sendJson(res, 500, { error: e.message }); }
}
// Concurrency/runtime config write (serial↔parallel + the runtime knobs). Merges a partial patch onto the
// current config.json, clamping each field (maxConcurrency is hard-capped at MAX_CONCURRENCY — the GUI can
// NEVER widen past the §H7 ceiling). Responds with the NORMALIZED effective config (readConfig applies the
// serial⇒N=1 rule etc.). config.json is in bookkeep ALLOW_CONTROL so a live edit isn't reverted mid-tick.
function hConfigPost(res, b) {
  if (!b || typeof b !== "object") return sendJson(res, 400, { error: "body required" });
  let raw = {}; try { raw = readJson(CC_CONFIG); } catch {}
  if (!raw || typeof raw !== "object") raw = {};
  const cur = (raw.concurrency && typeof raw.concurrency === "object") ? raw.concurrency : {};
  const c = { ...cur };
  const clampI = (v, min, max) => { const n = Math.floor(Number(v)); if (!Number.isFinite(n) || n < min) return null; return max ? Math.min(n, max) : n; };
  if (b.mode !== undefined) c.mode = b.mode === "parallel" ? "parallel" : "serial";
  if (b.maxConcurrency !== undefined) { const n = clampI(b.maxConcurrency, MIN_PARALLEL, MAX_CONCURRENCY); if (n !== null) c.maxConcurrency = n; }
  if (b.isolation !== undefined && ["auto", "worktree", "inline"].includes(b.isolation)) c.isolation = b.isolation;
  if (b.worktreeRoot !== undefined && typeof b.worktreeRoot === "string" && /^[A-Za-z]:[\\/]\S/.test(b.worktreeRoot)) c.worktreeRoot = b.worktreeRoot.replace(/\\/g, "/").replace(/\/+$/, "");
  if (b.perAgentTimeoutMin !== undefined) { const n = clampI(b.perAgentTimeoutMin, 1); if (n !== null) c.perAgentTimeoutMin = n; }
  if (b.heavyCostReserve !== undefined) { const n = clampI(b.heavyCostReserve, 1); if (n !== null) c.heavyCostReserve = n; }
  if (b.heartbeatSec !== undefined) { const n = clampI(b.heartbeatSec, 1); if (n !== null) c.heartbeatSec = n; }
  // daily tick budget (top-level, sibling of concurrency) — the supervisor reads it LIVE each cycle. Hard-ceilinged.
  if (b.tickBudget !== undefined) { const n = clampI(b.tickBudget, 1, MAX_TICK_BUDGET); if (n !== null) raw.tickBudget = n; }
  raw.concurrency = c;
  try { ccWithLock("agent", () => ccAtomicWrite(CC_CONFIG, JSON.stringify(raw, null, 2) + "\n")); const cfg = ccReadConfig(); sendJson(res, 200, { ok: true, config: cfg.concurrency, tickBudget: cfg.tickBudget }); }
  catch (e) { sendJson(res, 500, { error: e.message }); }
}
function hFocusPost(res, b) { // blind single-field overwrite → atomic-rename, no lock needed (§6)
  try { ccAtomicWrite(CC_FOCUS, (b && b.focus || "").toString()); sendJson(res, 200, { ok: true }); } catch (e) { sendJson(res, 500, { error: e.message }); }
}
function hAux(res) {
  const tail = (p, n) => { try { return readFileSync(p, "utf-8").replace(/^﻿/, "").split("\n").slice(-n).join("\n"); } catch { return ""; } };
  sendJson(res, 200, { plannerLog: tail(CC_PLANNER_LOG, 40), feelReview: tail(CC_FEELREVIEW, 30), hostLog: tail(CC_HOST_LOG, 60) });
}
function hUpload(res, b) {
  const key = (b && b.key || "").trim(); const file = b && b.file;
  // §15i 15.45 — key charset + path containment under assets/source/ (reject `../../x` traversal).
  if (!key || !KEY_RE.test(key) || key.includes("..")) return sendJson(res, 400, { error: "valid key required" });
  const target = path.join(CC_SOURCE, `${key}.png`);
  if (path.relative(CC_SOURCE, target).startsWith("..") || path.isAbsolute(path.relative(CC_SOURCE, target))) return sendJson(res, 400, { error: "key escapes assets/source/" });
  if (!file || typeof file !== "string") return sendJson(res, 400, { error: "file (base64 PNG) required" });
  const manifest = ccReadManifest();
  if (!manifest[key]) return sendJson(res, 400, { error: `Unknown key "${key}". Declare it in the manifest first (size/anchor = positioning authority).` });
  let buf; try { buf = Buffer.from(file.replace(/^data:image\/png;base64,/, ""), "base64"); } catch { return sendJson(res, 400, { error: "bad base64" }); }
  // §15i 15.51 — decoded byte cap, magic-byte + IHDR dimension/pixel cap BEFORE any decode/derive.
  if (buf.length > CC_ASSET_MAX) return sendJson(res, 413, { error: `PNG too large (> ${(CC_ASSET_MAX / 1024 / 1024).toFixed(0)}MB)` });
  if (buf.length < 24 || !buf.subarray(0, 8).equals(PNG_MAGIC)) return sendJson(res, 422, { error: "not a PNG (magic-byte mismatch)" });
  const w = buf.readUInt32BE(16), h = buf.readUInt32BE(20);
  if (w <= 0 || h <= 0 || w > CC_PNG_MAX_DIM || h > CC_PNG_MAX_DIM || w * h > CC_PNG_MAX_PX)
    return sendJson(res, 413, { error: `PNG dimensions out of range (${w}x${h}; cap ${CC_PNG_MAX_DIM}px/side, ${(CC_PNG_MAX_PX / 1024 / 1024).toFixed(0)}M px)` });
  if (ccDeriving) return sendJson(res, 409, { error: "a derive is already in flight — retry shortly" });
  try { mkdirSync(CC_SOURCE, { recursive: true }); ccAtomicWrite(target, buf); }
  catch (e) { return sendJson(res, 500, { error: `write failed: ${e.message}` }); }
  ccRunDerive((err, out) => { if (err) return sendJson(res, 500, { error: `derive failed: ${err.message}`, log: out }); sendJson(res, 200, { ok: true, key, deriveLog: out, ...ccAssetSummary() }); });
}
function hRollback(res) {
  if (ccAlive(CC_SUP_PID)) return sendJson(res, 409, { error: "Stop the loop before rolling back." });
  try {
    // §SPLIT — roll back the GAME repo (that's what a bad land dirties) to its cc-known-good. The C:/Vibes control
    // plane (backlog/completions/asset store) is intentionally NOT reset — that would discard asset uploads + bookkeeping.
    const tag = execSync(`git -C "${CC_GAME_REPO}" rev-parse cc-known-good`, { encoding: "utf-8" }).trim();
    ccWithLock("git-tree", () => {
      execSync(`git -C "${CC_GAME_REPO}" reset --hard ${tag}`, { encoding: "utf-8" });
      try { execSync(`git -C "${CC_GAME_REPO}" clean -fd`, { encoding: "utf-8" }); } catch {} // NEVER -x → keep node_modules/dist/derived
    });
    sendJson(res, 200, { ok: true, rolledBackTo: tag.slice(0, 8) });
  } catch (e) { sendJson(res, 500, { error: "No cc-known-good tag yet (or git error): " + e.message }); }
}

// ════════════════════════════════════════════════════════════════════════════
// §15d/§15g phase 4 — FOLDER-PER-ASSET STORE endpoints (Asset Browser back end)
// All writes go through orchestrator/assets.mjs, whose mutators lock on the §15c-2 named locks INTERNALLY
// (asset-<id> rank 3 for the meta write, then assets-index rank 2 for the rebuild — SEPARATE steps, never
// both held → a crash between leaves authority intact + the index self-heals). Endpoints therefore call the
// store fns directly (NO extra ccWithLock — that would double-lock the non-reentrant asset-<id> lock). DELETE
// has no store primitive → it tombstones under an asset-<id> lock, then rebuildIndex() as a separate step.
// Every id-taking route validates the id at the ENDPOINT layer (ASSET_ID_RE) AND via assets.validateId (the
// store's regex + path-containment) — §15.45 two-layer guard.
// ════════════════════════════════════════════════════════════════════════════

const CC_ASSETS_DIR = path.join(CC_CONTROL, "assets");
const CC_TRASH_DIR = path.join(CC_CONTROL, ".trash");
const CC_ACTIVE = path.join(CC_CONTROL, "active.json");

// §15b/§15.16 — after a SPEC write, recompile the Ready-Spec authority (refresh spec-doc.md + spec-index.json so
// the planner north-star + the browser stay current) AND touch the debounce settle marker so the ~90s window
// starts at the browser-write moment (§15d "browser authority-writes touch the settle marker"). spec-core's
// sense-time debounce auto-starts the window on the next tick too, so this is a precision optimization, not a
// correctness dependency. Best-effort; a compile fault never fails the endpoint. Called ONLY on spec mutations.
export function ccRefreshSpecAuthority() {
  try {
    const { hash } = ccCompileSpecs();
    const consumed = ccReadFile(CC_AUTHORITY_CONSUMED).trim();
    if (hash && hash !== consumed) {
      // AUDIT FIX (step7): mirror spec-core.debouncedAuthorityChanged — only START a window on a NEW pending sha; PRESERVE
      // firstSeen when the marker already carries this hash. Unconditionally bumping firstSeen let unrelated
      // Not-Ready-spec edits (which don't change the Ready-authority hash) restart the window forever → a real
      // pending diff was suppressed indefinitely during curation.
      let cur = null; try { cur = JSON.parse(ccReadFile(CC_AUTHORITY_SETTLE)); } catch {}
      if (!cur || cur.sha !== hash) ccAtomicWrite(CC_AUTHORITY_SETTLE, JSON.stringify({ sha: hash, firstSeen: Date.now() }));
    } else { try { unlinkSync(CC_AUTHORITY_SETTLE); } catch {} } // settled/empty → clear the window
  } catch { /* compile fault never fails the endpoint */ }
}

// Multi-type magic-byte sniff (§15d ccSniffKind) → { kind:image|audio|spec|null, fmt }. Binary formats keyed
// on magic bytes; text/spec = valid UTF-8 (round-trips) OR a declared spec / .md|.txt ext. Distinct empty vs
// unrecognized signals so the UI can message precisely (empty=0-byte, unrecognized=unknown type).
function ccSniffKind(buf, { filename = "", declared = "" } = {}) {
  if (!buf || buf.length === 0) return { kind: null, fmt: "empty" };
  const at = (arr, off = 0) => buf.length >= off + arr.length && arr.every((x, i) => buf[off + i] === x);
  if (at([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a])) return { kind: "image", fmt: "png" };
  if (at([0xff, 0xd8, 0xff])) return { kind: "image", fmt: "jpg" };
  if (at([0x47, 0x49, 0x46, 0x38])) return { kind: "image", fmt: "gif" };                 // GIF8
  if (at([0x52, 0x49, 0x46, 0x46]) && at([0x57, 0x41, 0x56, 0x45], 8)) return { kind: "audio", fmt: "wav" }; // RIFF….WAVE
  if (at([0x49, 0x44, 0x33])) return { kind: "audio", fmt: "mp3" };                        // ID3
  // MPEG frame sync — validate version/layer/bitrate/samplerate bits so an arbitrary binary starting 0xFF Ex/Fx
  // isn't misclassified audio (which mints a bead whose playback grader never passes → silent churn to abandoned).
  if (buf.length >= 3 && buf[0] === 0xff && (buf[1] & 0xe0) === 0xe0
    && ((buf[1] >> 3) & 3) !== 1 && ((buf[1] >> 1) & 3) !== 0
    && ((buf[2] >> 4) & 0xf) !== 0 && ((buf[2] >> 4) & 0xf) !== 15 && ((buf[2] >> 2) & 3) !== 3) return { kind: "audio", fmt: "mp3" };
  if (at([0x4f, 0x67, 0x67, 0x53])) return { kind: "audio", fmt: "ogg" };                  // OggS
  const ext = (String(filename).split(".").pop() || "").toLowerCase();
  const isTextExt = ext === "md" || ext === "txt";
  let utf8ok = false; try { utf8ok = Buffer.from(buf.toString("utf8"), "utf8").equals(buf); } catch { utf8ok = false; }
  if (declared === "spec" || isTextExt || utf8ok) return { kind: "spec", fmt: ext === "txt" ? "txt" : "md" };
  return { kind: null, fmt: "unrecognized" };
}

// pngjs lives in game/node_modules (cosmo-canyon root has no package.json) — lazy file-URL import, cached;
// best-effort (a missing decoder degrades to dims-only, never crashes the endpoint).
let ccPngLib = null, ccPngTried = false;
async function ccGetPng() {
  if (ccPngTried) return ccPngLib;
  ccPngTried = true;
  try { const m = await import(pathToFileURL(path.join(CC_GAME_DIR, "node_modules", "pngjs", "lib", "png.js")).href); ccPngLib = m.PNG || (m.default && m.default.PNG) || m.default; } catch { ccPngLib = null; }
  return ccPngLib;
}
// §15.51 PNG IHDR dim/pixel/byte cap (reuse of hUpload's parse) + §15d degenerate-image reject (dims>0, not
// fully-transparent, decodes). Dim/byte caps are checked BEFORE decode so a bomb never reaches PNG.sync.read.
async function ccValidateImage(buf, fmt) {
  if (!buf || buf.length === 0) return { ok: false, code: 422, error: "empty image (0 bytes)" };
  if (buf.length > CC_ASSET_MAX) return { ok: false, code: 413, error: `image too large (> ${(CC_ASSET_MAX / 1048576).toFixed(0)}MB)` };
  if (fmt === "png") {
    if (buf.length < 24 || !buf.subarray(0, 8).equals(PNG_MAGIC)) return { ok: false, code: 422, error: "not a PNG (magic-byte mismatch)" };
    const w = buf.readUInt32BE(16), h = buf.readUInt32BE(20);
    if (w <= 0 || h <= 0) return { ok: false, code: 422, error: `degenerate PNG dimensions (${w}x${h})` };
    if (w > CC_PNG_MAX_DIM || h > CC_PNG_MAX_DIM || w * h > CC_PNG_MAX_PX)
      return { ok: false, code: 413, error: `PNG dimensions out of range (${w}x${h}; cap ${CC_PNG_MAX_DIM}px/side, ${(CC_PNG_MAX_PX / 1048576).toFixed(0)}M px)` };
    const PNG = await ccGetPng();
    if (PNG) { // decode (safe now — dims capped ≤8192/side, ≤16M px) + fully-transparent reject
      let png; try { png = PNG.sync.read(buf); } catch (e) { return { ok: false, code: 422, error: "PNG failed to decode: " + e.message }; }
      let anyOpaque = false; const d = png.data;
      for (let i = 3; i < d.length; i += 4) { if (d[i] !== 0) { anyOpaque = true; break; } }
      if (!anyOpaque) return { ok: false, code: 422, error: "degenerate image: fully transparent (all-alpha) — nothing to render" };
    }
    return { ok: true };
  }
  // (audit) the derive pipeline is PNG-ONLY (derive.mjs --bind asserts the PNG magic). A JPEG/GIF would sail
  // through create, mint a bead, then FAIL derive-bind every tick → silent churn to abandoned. Reject it here
  // with a clear message so the operator re-exports as PNG instead of hitting an invisible abandon loop.
  return { ok: false, code: 422, error: `images must be PNG (derive is PNG-only) — got ${fmt}; re-export as PNG` };
}

// A labeled placeholder SVG (never 404 for /assets/file — audit 15.31). Rendered by <img> like any image.
function ccPlaceholderSvg(label, sub) {
  const esc = (s) => String(s == null ? "" : s).replace(/[<>&]/g, (c) => ({ "<": "&lt;", ">": "&gt;", "&": "&amp;" }[c]));
  return '<svg xmlns="http://www.w3.org/2000/svg" width="120" height="120" viewBox="0 0 120 120">'
    + '<rect width="120" height="120" fill="#0b0f1a"/>'
    + '<rect x="6" y="6" width="108" height="108" rx="10" fill="none" stroke="#334155" stroke-dasharray="6 5"/>'
    + '<text x="60" y="55" fill="#64748b" font-family="system-ui,sans-serif" font-size="10" text-anchor="middle">' + esc(label) + '</text>'
    + '<text x="60" y="72" fill="#475569" font-family="system-ui,sans-serif" font-size="7" text-anchor="middle">' + esc(sub) + '</text></svg>';
}

// GET /assets/list — readIndex + §15.25 rev-check rows-vs-meta → rebuild-on-drift. Returns {assets,counts}.
function hAssetsList(res) {
  let idx = ccAssets.readIndex();
  try {
    const rowRev = new Map((idx.assets || []).map((r) => [r.id, r.rev]));
    let names = []; try { names = fs.readdirSync(CC_ASSETS_DIR, { withFileTypes: true }).filter((d) => d.isDirectory()).map((d) => d.name); } catch { names = []; }
    const diskRev = new Map();
    let drift = false;
    for (const id of names) {
      if (!ASSET_ID_RE.test(id)) continue;
      let m = null; try { m = readJson(path.join(CC_ASSETS_DIR, id, "meta.json")); } catch { m = null; }
      if (!m || m.id !== id) continue;
      diskRev.set(id, m.rev);
      if (rowRev.get(id) !== m.rev) drift = true;
    }
    if (diskRev.size !== rowRev.size) drift = true;
    for (const id of rowRev.keys()) if (!diskRev.has(id)) drift = true;
    if (drift) idx = ccAssets.rebuildIndex();
  } catch {}
  // §15e/15.32 — compute the DERIVED `implemented` flag per row via the SAME pure predicate the loop's completion
  // check uses (completions ∧ acceptance PASS-not-skipped ∧ img/audio manifest real, bound to contentHash+rev at
  // the committed sha). Implemented is NEVER stored — the UI's read-only "provisional" slot becomes real here.
  try {
    const completions = ccReadArr(CC_COMPLETIONS);
    const manifest = ccReadManifest();
    const audioManifest = ccReadAudioManifest();
    if (idx && Array.isArray(idx.assets)) {
      let implN = 0;
      for (const r of idx.assets) { r.implemented = ccImplemented(r, { completions, manifest, audioManifest }); if (r.implemented) implN++; }
      if (idx.counts) idx.counts.implemented = implN;
    }
  } catch {}
  sendJson(res, 200, idx);
}

// GET /assets/file?id= — serve the asset's REAL stored bytes: image → its art, SPEC → its text, audio → its
// sound. Only a placeholderOnly / missing / no-file asset falls back to a labeled placeholder SVG (never 404,
// audit 15.31). BUGFIX: was image-only, so clicking a spec (or audio) thumb previewed the placeholder card
// ("<name> / spec file") instead of the actual spec text.
function hAssetsFile(res, id) {
  if (!ASSET_ID_RE.test(id)) return sendText(res, 400, "invalid id");
  let meta = null;
  try { ccAssets.validateId(id); meta = ccAssets.readAsset(id); } catch { meta = null; }
  if (meta && meta.file && !meta.placeholderOnly) {
    try {
      const buf = readFileSync(path.join(CC_ASSETS_DIR, id, meta.file));
      // A spec is served as text/plain regardless of its stored mime (a .md/.txt must NEVER render as inline
      // HTML → stored-XSS); nosniff stops the browser second-guessing it. image/audio use their sniffed mime.
      const type = meta.kind === "spec" ? "text/plain; charset=utf-8" : (meta.mime || (meta.kind === "image" ? "image/png" : "application/octet-stream"));
      res.writeHead(200, { "Content-Type": type, "Cache-Control": "no-store", "X-Content-Type-Options": "nosniff" });
      return res.end(buf);
    } catch {} // fall through to placeholder
  }
  const label = meta ? (meta.manifestKey || meta.filename || meta.kind) : "no asset";
  const sub = !meta ? id : meta.kind === "image" ? "positioning slot — drop art" : meta.kind + (meta.file ? " file" : " — placeholder");
  res.writeHead(200, { "Content-Type": "image/svg+xml", "Cache-Control": "no-store" });
  res.end(ccPlaceholderSvg(label, sub));
}

// POST /assets/reveal {id} — open the asset's on-disk folder in the OS file manager (local-only host). Opens a
// DIRECTORY only; the strict ASSET_ID_RE + validateId make path traversal impossible. explorer.exe exits 1 even
// on success, so this is fire-and-forget — never gate on the child's exit code.
function hAssetsReveal(res, b) {
  const id = String((b && b.id) || "");
  if (!ASSET_ID_RE.test(id)) return sendJson(res, 400, { error: "invalid id" });
  try { ccAssets.validateId(id); } catch { return sendJson(res, 400, { error: "invalid id" }); }
  const dir = path.join(CC_ASSETS_DIR, id);
  if (!existsSync(dir)) return sendJson(res, 404, { error: "asset folder not found" });
  try {
    if (process.platform === "win32") spawn("explorer.exe", [dir], { detached: true, windowsHide: false }).unref();
    else spawn(process.platform === "darwin" ? "open" : "xdg-open", [dir], { detached: true }).unref();
    return sendJson(res, 200, { ok: true, dir });
  } catch (e) { return sendJson(res, 500, { error: e.message }); }
}

// POST /assets/open {id} — open the asset FILE in the OS default app (OUTSIDE the browser). Same strict id
// validation + local-only guards as reveal. Opens the stored bytes (meta.file); 404 if the asset has none.
function hAssetsOpen(res, b) {
  const id = String((b && b.id) || "");
  if (!ASSET_ID_RE.test(id)) return sendJson(res, 400, { error: "invalid id" });
  let meta = null;
  try { ccAssets.validateId(id); } catch { return sendJson(res, 400, { error: "invalid id" }); }
  try { meta = ccAssets.readAsset(id); } catch { meta = null; }
  if (!meta || !meta.file) return sendJson(res, 404, { error: "asset has no file to open" });
  const file = path.join(CC_ASSETS_DIR, id, meta.file);
  if (!existsSync(file)) return sendJson(res, 404, { error: "asset file not found" });
  try {
    // explorer.exe <file> launches the file's default handler (image viewer / audio player / editor); it exits
    // 1 even on success, so fire-and-forget — never gate on the child's exit code.
    if (process.platform === "win32") spawn("explorer.exe", [file], { detached: true, windowsHide: false }).unref();
    else spawn(process.platform === "darwin" ? "open" : "xdg-open", [file], { detached: true }).unref();
    return sendJson(res, 200, { ok: true });
  } catch (e) { return sendJson(res, 500, { error: e.message }); }
}

// Decode a base64/data-URL string → Buffer (or null on bad/absent input). MUST reject a non-string: a bare
// Buffer.from(String(undefined)="undefined","base64") yields 6 non-empty garbage bytes → a missing `file`
// field would otherwise pass a `!buf`/length check and (for a spec, which sniffs by `declared` not bytes)
// corrupt the asset. Null here + the endpoint's explicit presence guard are the two-layer defense.
function ccDecodeB64(s) { if (typeof s !== "string") return null; try { return Buffer.from(s.replace(/^data:[^;]+;base64,/, ""), "base64"); } catch { return null; } }

// POST /assets/create — sniff + degenerate/cap reject → createAsset (state not_ready). Collision WARN (non-blocking).
async function hAssetsCreate(res, b) {
  const fileB64 = b && b.file;
  const filename = String((b && b.filename) || "").slice(0, 200);
  const declared = String((b && b.kind) || "");
  const instructions = String((b && b.instructions) || "");
  if (!fileB64 || typeof fileB64 !== "string") return sendJson(res, 400, { error: "file (base64) required" });
  const buf = ccDecodeB64(fileB64);
  if (!buf) return sendJson(res, 400, { error: "bad base64" });
  if (buf.length === 0) return sendJson(res, 422, { error: "empty file (0 bytes)" });
  if (buf.length > CC_ASSET_MAX) return sendJson(res, 413, { error: `file too large (> ${(CC_ASSET_MAX / 1048576).toFixed(0)}MB)` });
  const sniff = ccSniffKind(buf, { filename, declared });
  if (!sniff.kind) return sendJson(res, 422, { error: "unrecognized file type — need PNG/JPG/GIF (image), WAV/MP3/OGG (audio), or UTF-8 text (spec)" });
  if (sniff.kind === "image") { const v = await ccValidateImage(buf, sniff.fmt); if (!v.ok) return sendJson(res, v.code, { error: v.error }); }
  const ch = "sha256:" + createHash("sha256").update(buf).digest("hex");
  let warn = null;
  try {
    const rows = ccAssets.readIndex().assets || [];
    const dupHash = rows.find((r) => r.contentHash && r.contentHash === ch);
    const dupName = filename && rows.find((r) => r.filename === filename);
    if (dupHash) warn = `identical bytes already stored as ${dupHash.id}`;
    else if (dupName) warn = `an asset named "${filename}" already exists (${dupName.id})`;
  } catch {}
  // §15e — an image/audio upload gets a DETERMINISTIC manifestKey at create (from a `key:` directive, the
  // filename, or a kind.tail fallback), so the DERIVED predicate can flip it Implemented. A spec has no key.
  const manifestKey = (sniff.kind === "image" || sniff.kind === "audio")
    ? deriveManifestKey({ filename, instructions, kind: sniff.kind, id: "" }) : null;
  let meta;
  try { meta = ccAssets.createAsset({ kind: sniff.kind, filename: filename || `drop.${sniff.fmt}`, bytes: buf, instructions, manifestKey, state: "not_ready" }); }
  catch (e) { return sendJson(res, 500, { error: "create failed: " + e.message }); }
  if (meta.kind === "spec") ccRefreshSpecAuthority(); // §15b — refresh spec-index so the browser sees the new (Not-Ready) spec
  sendJson(res, 200, { ok: true, asset: meta, warn });
}

// POST /assets/replace {id,rev,file,confirm} — rev-guarded; on an implemented/real slot forces not_ready +
// requires confirm; prior bytes copied to history/ by the store (§15.2). 15.51 cap. Sniff must match kind.
async function hAssetsReplace(res, b) {
  const id = String((b && b.id) || "");
  if (!ASSET_ID_RE.test(id)) return sendJson(res, 400, { error: "invalid id" });
  let meta; try { ccAssets.validateId(id); meta = ccAssets.readAsset(id); } catch { return sendJson(res, 404, { error: "no such asset" }); }
  const rev = b && b.rev;
  if (typeof rev !== "number" || meta.rev !== rev) return sendJson(res, 409, { error: `rev mismatch (have ${meta.rev}, got ${rev == null ? "none" : rev})`, rev: meta.rev });
  const fileB64 = b && b.file;
  if (!fileB64 || typeof fileB64 !== "string") return sendJson(res, 400, { error: "file (base64) required" }); // presence BEFORE decode (a missing field must not decode to garbage bytes)
  const buf = ccDecodeB64(fileB64);
  if (!buf) return sendJson(res, 400, { error: "bad base64" });
  if (buf.length === 0) return sendJson(res, 422, { error: "empty file (0 bytes)" });
  const sniff = ccSniffKind(buf, { filename: meta.filename || "", declared: meta.kind });
  if (sniff.kind !== meta.kind) return sendJson(res, 422, { error: `replacement is ${sniff.kind || "unrecognized"}, asset is ${meta.kind}` });
  if (sniff.kind === "image") { const v = await ccValidateImage(buf, sniff.fmt); if (!v.ok) return sendJson(res, v.code, { error: v.error }); }
  const isReal = ccIsRealAsset(meta);
  if (isReal && !(b && b.confirm)) return sendJson(res, 409, { needsConfirm: true, error: "replacing an implemented/real asset — resubmit confirm:true (prior bytes kept in history/, slot forced not_ready)" });
  let next;
  try { next = ccAssets.replaceArtifact(id, buf); } catch (e) { return sendJson(res, 500, { error: "replace failed: " + e.message }); }
  if (isReal && next.state === "ready") { try { next = ccAssets.setState(id, next.rev, "not_ready"); } catch {} }
  if (next.kind === "spec") ccRefreshSpecAuthority(); // §15b — a spec body REPLACE changes authority (if Ready) → refresh
  sendJson(res, 200, { ok: true, asset: next });
}

// POST /assets/instructions {id,rev,instructions} — rev-guarded PATCH of the field only (409 on rev mismatch).
function hAssetsInstructions(res, b) {
  const id = String((b && b.id) || "");
  if (!ASSET_ID_RE.test(id)) return sendJson(res, 400, { error: "invalid id" });
  const rev = b && b.rev;
  if (typeof rev !== "number") return sendJson(res, 400, { error: "rev (number) required" });
  if (typeof (b && b.instructions) !== "string") return sendJson(res, 400, { error: "instructions (string) required" });
  try { ccAssets.validateId(id); const next = ccAssets.setInstructions(id, rev, b.instructions); if (next.kind === "spec") ccRefreshSpecAuthority(); sendJson(res, 200, { ok: true, asset: next }); }
  catch (e) { sendJson(res, (/rev mismatch/.test(e.message) || /^no asset/.test(e.message)) ? 409 : 400, { error: e.message }); } // deleted-out-from-under → 409 so the client resyncs (it only reloads on 409), not a dead-end 400 toast
}

// POST /assets/answer {id,rev,instructions,clearQids[]} — atomic set instructions + clear ONLY listed Qs +
// ready+dirty + rev++ (store.answer; a concurrently-appended new question survives).
function hAssetsAnswer(res, b) {
  const id = String((b && b.id) || "");
  if (!ASSET_ID_RE.test(id)) return sendJson(res, 400, { error: "invalid id" });
  const rev = b && b.rev;
  if (typeof rev !== "number") return sendJson(res, 400, { error: "rev (number) required" });
  const instructions = typeof (b && b.instructions) === "string" ? b.instructions : undefined;
  const clearQids = Array.isArray(b && b.clearQids) ? b.clearQids.map(String) : [];
  try { ccAssets.validateId(id); const next = ccAssets.answer(id, rev, instructions, clearQids); if (next.kind === "spec") ccRefreshSpecAuthority(); sendJson(res, 200, { ok: true, asset: next }); }
  catch (e) { sendJson(res, (/rev mismatch/.test(e.message) || /^no asset/.test(e.message)) ? 409 : 400, { error: e.message }); } // deleted-out-from-under → 409 so the client resyncs
}

// POST /assets/state {id,rev,state} — human not_ready↔ready (implemented→not_ready = ready→not_ready, since
// implemented is DERIVED, never stored). REJECT a human `implemented` write 400 (15.32).
function hAssetsState(res, b) {
  const id = String((b && b.id) || "");
  if (!ASSET_ID_RE.test(id)) return sendJson(res, 400, { error: "invalid id" });
  const rev = b && b.rev;
  if (typeof rev !== "number") return sendJson(res, 400, { error: "rev (number) required" });
  const state = String((b && b.state) || "");
  const confirm = !!(b && b.confirm);
  if (state === "implemented") return sendJson(res, 400, { error: "'implemented' is a DERIVED projection, not a settable state (15.32)" });
  if (state !== "not_ready" && state !== "ready") return sendJson(res, 400, { error: "state must be not_ready|ready" });
  // §15e — a not_ready transition on a DERIVED-Implemented asset is a RE-OPEN → full cross-authority invalidation
  // (clear provenance, supersede the completion, bump rev, flag placeholderStale, suggest code cleanup) rather
  // than a plain state flip. NEVER auto-deletes shipped code.
  if (state === "not_ready") {
    let meta = null; try { ccAssets.validateId(id); meta = ccAssets.readAsset(id); } catch { return sendJson(res, 404, { error: "no such asset" }); }
    if (typeof rev === "number" && meta.rev !== rev) return sendJson(res, 409, { error: `rev mismatch (have ${meta.rev}, got ${rev})`, rev: meta.rev });
    const isImpl = ccImplemented(meta, { completions: ccReadArr(CC_COMPLETIONS), manifest: ccReadManifest(), audioManifest: ccReadAudioManifest() });
    // §15.5 — retiring a Ready SPEC (authority) requires confirm when it would flag built work as drift OR drain
    // the authority. AUDIT FIX (step7): the old guard counted ONLY assetId-cited beads + isImpl, but an authority
    // monolith (spec-legacy) is decomposed into assetId-LESS planner cc-#### beads and has implementedBy=null →
    // citing=0 && isImpl=false → the sole north-star was silently retirable (drains readySpecCount to 0 → the loop
    // idles: the exact 15.5 fat-finger regression). Also require confirm on drain-to-empty (last Ready spec) and
    // on retiring an authority monolith (source spec-legacy).
    if (meta.kind === "spec" && meta.state === "ready" && !confirm) {
      const citing = ccReadArr(CC_BACKLOG).filter((x) => x && x.assetId === id && x.status !== "abandoned" && x.status !== "superseded").length;
      const readySpecN = (ccAssets.readIndex().assets || []).filter((a) => a && a.kind === "spec" && a.state === "ready").length;
      const drainsToEmpty = readySpecN <= 1;                 // this is the last Ready spec → retiring empties authority
      const isMonolith = meta.source === "spec-legacy";      // the decomposed authority monolith (no assetId citations)
      if (citing > 0 || isImpl || drainsToEmpty || isMonolith) return sendJson(res, 409, { needsConfirm: true, citingBeads: citing, implemented: isImpl, drainsToEmpty, monolith: isMonolith,
        error: `Retiring this Ready spec — ${drainsToEmpty ? "it is the LAST Ready spec (this empties the authority → the loop idles)" : isMonolith ? "it is the authority monolith (spec-legacy) the whole game is built from" : `${citing} bead(s)${isImpl ? " + an Implemented projection" : ""} cite it`}. Resubmit confirm:true.` });
    }
    if (isImpl) {
      try { const r = ccReopenAsset(id, { reason: "operator reopen" }); if (meta.kind === "spec") ccRefreshSpecAuthority(); return sendJson(res, 200, { ok: true, reopened: true, ...r }); }
      catch (e) { return sendJson(res, 500, { error: "reopen failed: " + e.message }); }
    }
  }
  try {
    ccAssets.validateId(id); const next = ccAssets.setState(id, rev, state);
    if (next.kind === "spec") ccRefreshSpecAuthority(); // §15b — a spec Ready-toggle changes authority → refresh doc/index + touch the debounce window
    sendJson(res, 200, { ok: true, asset: next });
  } catch (e) { sendJson(res, /rev mismatch/.test(e.message) ? 409 : 400, { error: e.message }); }
}

// DELETE /assets {id,confirm} — state-aware confirm for ready/implemented; TOMBSTONE to .trash/ (never
// unlink); remove dir + index row atomically (15.25/15.10). No store primitive → tombstone under the
// asset-<id> lock, then rebuildIndex() as a SEPARATE assets-index step (never both held).
function hAssetsDelete(res, b) {
  const id = String((b && b.id) || "");
  if (!ASSET_ID_RE.test(id)) return sendJson(res, 400, { error: "invalid id" });
  let meta; try { ccAssets.validateId(id); meta = ccAssets.readAsset(id); } catch { return sendJson(res, 404, { error: "no such asset" }); }
  const isReal = ccIsRealAsset(meta);
  const gated = meta.state === "ready" || isReal;
  if (gated && !(b && b.confirm)) return sendJson(res, 409, { needsConfirm: true, error: `deleting a ${isReal ? "implemented/real" : "ready"} asset — resubmit confirm:true (tombstoned to .trash/, not unlinked)` });
  try {
    const src = path.join(CC_ASSETS_DIR, id);
    if (!existsSync(src)) return sendJson(res, 404, { error: "asset dir missing" });
    let dst = path.join(CC_TRASH_DIR, id);
    ccWithLock(`asset-${id}`, () => {
      mkdirSync(CC_TRASH_DIR, { recursive: true });
      if (existsSync(dst)) dst = path.join(CC_TRASH_DIR, `${id}-${Date.now()}`);
      renameSync(src, dst);
    });
    ccAssets.rebuildIndex(); // separate assets-index step: dir gone → row dropped
    if (meta.kind === "spec") ccRefreshSpecAuthority(); // §15b — deleting a Ready spec changes authority → refresh + touch settle
    sendJson(res, 200, { ok: true, tombstoned: path.basename(dst) });
  } catch (e) { sendJson(res, 500, { error: "delete failed: " + e.message }); }
}

// GET /assets/get?id= — PURE read of ONE asset's full meta (incl. the `questions` array the derived index
// row omits) so the Answer surface can list Qs + their qids. Id-guarded; 404 if absent.
function hAssetsGet(res, id) {
  if (!ASSET_ID_RE.test(id)) return sendJson(res, 400, { error: "invalid id" });
  try { ccAssets.validateId(id); return sendJson(res, 200, ccAssets.readAsset(id)); }
  catch { return sendJson(res, 404, { error: "no such asset" }); }
}

// GET /active — PURE read of active.json (the WRITER is phase 5). Absent → [] (render gracefully now).
function hActive(res) {
  let arr = [];
  try { const j = readJson(CC_ACTIVE); arr = Array.isArray(j) ? j : (j && typeof j === "object" ? Object.values(j) : []); } catch { arr = []; }
  sendJson(res, 200, arr);
}

// ── §15.18 FEEL-REVIEW queue + §15.15 grader-confirm — the HUMAN gates that flip a feel spec / arm a spec grader.
const CC_FEEL_REVIEW = path.join(CC_CONTROL, "feel-review.json");
const CC_GRADER_CONFIRM = path.join(CC_CONTROL, "grader-confirm.json");

// GET /assets/feel-review — the human-gated queue: feel/visual spec lands whose critic verdict is ADVISORY.
// A model verdict NEVER flips Implemented — only POST /assets/feel-confirm does (§15.18).
function hFeelReview(res) {
  let arr = []; try { const j = readJson(CC_FEEL_REVIEW); if (Array.isArray(j)) arr = j; } catch {}
  sendJson(res, 200, arr.filter((e) => e && !e.confirmed));
}

// POST /assets/feel-confirm {id} — OPERATOR confirms a feel/visual spec: stamps feelConfirmed provenance (the
// predicate requires it) + marks the queue entry confirmed. This is the ONLY path that flips a feel spec
// Implemented — a model critic verdict is advisory only (§15.18).
function hFeelConfirm(res, b) {
  const id = String((b && b.id) || "");
  if (!ASSET_ID_RE.test(id)) return sendJson(res, 400, { error: "invalid id" });
  let meta; try { ccAssets.validateId(id); meta = ccAssets.readAsset(id); } catch { return sendJson(res, 404, { error: "no such asset" }); }
  // (audit) require an ACTUAL pending advisory land — the newest unconfirmed queue entry for this asset. Do NOT
  // trust an operator-supplied beadId (that would let a future, not-yet-reviewed land be PRE-confirmed). beadId+rev
  // come from the queue entry, binding the confirm to the specific land whose snapshot the operator reviewed.
  let queue = []; try { const j = readJson(CC_FEEL_REVIEW); if (Array.isArray(j)) queue = j; } catch {}
  const entry = queue.filter((e) => e && e.assetId === id && !e.confirmed).sort((a, c) => (a.at < c.at ? 1 : -1))[0];
  if (!entry) return sendJson(res, 409, { error: "no pending feel-review land for this asset (nothing to confirm)" });
  // §AUDIT-2026-07-02 HIGH-9 — REFUSE a stale confirm: the operator reviewed the snapshot for entry.rev, but the
  // asset may have been edited since (rev bumped). Confirming anyway stamps feelConfirmed{rev:old} onto the new meta
  // (predicate never matches → not Implemented) AND clears dirty=true — killing the pending rework mint and parking
  // the asset in the operator-block anomaly bucket with no way out. Make the operator re-review the current rev.
  if (entry.rev != null && meta.rev !== entry.rev) {
    return sendJson(res, 409, { error: `asset edited since this land (reviewed rev ${entry.rev}, now rev ${meta.rev}) — re-review the current version before confirming` });
  }
  const beadId = entry.beadId, rev = entry.rev != null ? entry.rev : meta.rev;
  try { ccAssets.feelConfirm(id, { beadId, rev, by: "operator" }); } catch (e) { return sendJson(res, 500, { error: "feelConfirm failed: " + e.message }); }
  // mark ONLY this specific land confirmed (scoped by beadId, not blanket by assetId) so a concurrent NEWER
  // advisory re-land appended between our read and write is not swept away + stays surfaced in the queue.
  try { ccWithLock("completions", () => { let q = []; try { const j = readJson(CC_FEEL_REVIEW); if (Array.isArray(j)) q = j; } catch {} for (const e of q) if (e && e.beadId === beadId) e.confirmed = true; ccAtomicWrite(CC_FEEL_REVIEW, JSON.stringify(q, null, 2)); }); } catch {}
  sendJson(res, 200, { ok: true, id, beadId, rev });
}

// POST /assets/confirm-satisfied {id} — OPERATOR (or the driving CC agent after VERIFYING it renders/works) attests
// a SPEC is already satisfied by existing code: the acceptance route the autonomous loop lacked (§GC1 — a worker
// blocks "already implemented", a sim grader would be tautological, feel-confirm has no advisory land). Writes the
// full implemented() provenance + commits (close-satisfied.mjs). A human/present-agent attestation, like feel-confirm.
function hConfirmSatisfied(res, b) {
  const id = String((b && b.id) || "");
  if (!ASSET_ID_RE.test(id)) return sendJson(res, 400, { error: "invalid id" });
  try { const r = ccCloseSatisfied(id, { by: "operator" }); return sendJson(res, 200, r); }
  catch (e) { return sendJson(res, 500, { error: "confirm-satisfied failed: " + e.message }); }
}

// POST /assets/grader-confirm {beadId} — OPERATOR confirms a planner-authored spec grader so bookkeep will let
// it gate (it lands DISABLED-until-confirm, §15.15). Confirm alone is not enough — bookkeep STILL runs the
// mutation-check + PASS-token deterministically; this only lifts the human gate.
function hGraderConfirm(res, b) {
  const beadId = String((b && b.beadId) || "");
  if (!ID_RE.test(beadId)) return sendJson(res, 400, { error: "invalid beadId" });
  try {
    ccWithLock("completions", () => {
      let m = {}; try { const j = readJson(CC_GRADER_CONFIRM); if (j && typeof j === "object") m = j; } catch {}
      m[beadId] = { confirmed: true, by: "operator", at: new Date().toISOString() };
      ccAtomicWrite(CC_GRADER_CONFIRM, JSON.stringify(m, null, 2));
    });
    // (audit) COMMIT the confirm so it sits at BASE (clean) for the next tick — grader-confirm.json is NOT an
    // ALLOW_CONTROL worker-write, so a worker that dirties it is tamper-reverted; only a committed operator confirm
    // survives to be read by bookkeep.graderConfirmed. Same git-tree-locked commit pattern used by every committer.
    try { ccWithLock("git-tree", () => {
      execSync(`git -C "${VIBES}" add cosmo-canyon/control/grader-confirm.json`, { encoding: "utf-8" });
      try { execSync(`git -C "${VIBES}" commit -q -m "cosmo-canyon: grader confirm ${beadId}"`, { encoding: "utf-8" }); } catch {}
    }); } catch {}
    sendJson(res, 200, { ok: true, beadId });
  } catch (e) { sendJson(res, 500, { error: e.message }); }
}

// Startup index reconcile (§15.25) — rebuild the derived index once on boot so a stale/hand-edited/corrupt
// assets.json is repaired before the first /assets/list.
function reconcileAssetIndex() { try { ccAssets.rebuildIndex(); return true; } catch (e) { console.error("[cc] asset index reconcile failed:", e.message); return false; } }

// ── dispatch ─────────────────────────────────────────────────────────────────
async function route(req, res, p, u) {
  const m = req.method;
  // static
  if (m === "GET" && (p === "/" || p === "/index.html" || p === "/ui/index.html")) return sendFile(res, CC_UI, "text/html; charset=utf-8");
  if (m === "GET" && p === "/api/cosmocanyon/snapshot") { if (existsSync(CC_SNAPSHOT)) return sendFile(res, CC_SNAPSHOT, "image/png"); res.writeHead(404); return res.end(); }
  // GETs
  if (m === "GET" && p === "/api/cosmocanyon/status") return hStatus(res);
  if (m === "GET" && p === "/api/cosmocanyon/backlog") return sendJson(res, 200, ccReadArr(CC_BACKLOG));
  if (m === "GET" && p === "/api/cosmocanyon/suggestions") return sendJson(res, 200, ccReadArr(CC_SUGGESTIONS).filter((s) => s.status !== "closed"));
  if (m === "GET" && p === "/api/cosmocanyon/completions") return sendJson(res, 200, ccReadArr(CC_COMPLETIONS));
  if (m === "GET" && p === "/api/cosmocanyon/agent") { let a = null; try { a = readJson(CC_AGENT); } catch {} return sendJson(res, 200, { allowed: readAllowed(a), options: WORKER_OPTIONS, cooldownAgy: agyCoolingDown(CC_CONTROL) }); }
  if (m === "GET" && p === "/api/cosmocanyon/config") { const cfg = ccReadConfig(); return sendJson(res, 200, { config: cfg.concurrency, tickBudget: cfg.tickBudget, max: MAX_CONCURRENCY }); }
  if (m === "GET" && p === "/api/cosmocanyon/aux") return hAux(res);
  if (m === "GET" && p === "/api/cosmocanyon/assets") return sendJson(res, 200, ccAssetSummary());
  // §15d folder-per-asset store (Asset Browser)
  if (m === "GET" && p === "/api/cosmocanyon/assets/list") return hAssetsList(res);
  if (m === "GET" && p === "/api/cosmocanyon/assets/file") return hAssetsFile(res, u.searchParams.get("id") || "");
  if (m === "GET" && p === "/api/cosmocanyon/assets/get") return hAssetsGet(res, u.searchParams.get("id") || "");
  if (m === "GET" && p === "/api/cosmocanyon/active") return hActive(res);
  if (m === "GET" && p === "/api/cosmocanyon/assets/feel-review") return hFeelReview(res);
  // POST/DELETE (body-bearing)
  if (m === "POST" && p === "/api/cosmocanyon/start") return hStart(res);
  if (m === "POST" && p === "/api/cosmocanyon/stop") return hStop(res);
  if (m === "POST" && p === "/api/cosmocanyon/rollback") return hRollback(res);
  if ((m === "POST" || m === "DELETE") && p.startsWith("/api/cosmocanyon/")) {
    const body = await readJsonBody(req, CC_BODY_MAX);
    if (p === "/api/cosmocanyon/suggestions/accept") return hSuggAccept(res, body);
    if (p === "/api/cosmocanyon/suggestions/reject") return hSuggReject(res, body);
    if (p === "/api/cosmocanyon/agent") return hAgentPost(res, body);
    if (p === "/api/cosmocanyon/config") return hConfigPost(res, body);
    if (p === "/api/cosmocanyon/assets/upload") return hUpload(res, body);
    // §15d folder-per-asset store writes
    if (p === "/api/cosmocanyon/assets/create" && m === "POST") return hAssetsCreate(res, body);
    if (p === "/api/cosmocanyon/assets/replace" && m === "POST") return hAssetsReplace(res, body);
    if (p === "/api/cosmocanyon/assets/instructions" && m === "POST") return hAssetsInstructions(res, body);
    if (p === "/api/cosmocanyon/assets/answer" && m === "POST") return hAssetsAnswer(res, body);
    if (p === "/api/cosmocanyon/assets/state" && m === "POST") return hAssetsState(res, body);
    if (p === "/api/cosmocanyon/assets/open" && m === "POST") return hAssetsOpen(res, body);
    if (p === "/api/cosmocanyon/assets/reveal" && m === "POST") return hAssetsReveal(res, body);
    if (p === "/api/cosmocanyon/assets/feel-confirm" && m === "POST") return hFeelConfirm(res, body);
    if (p === "/api/cosmocanyon/assets/confirm-satisfied" && m === "POST") return hConfirmSatisfied(res, body);
    if (p === "/api/cosmocanyon/grader-confirm" && m === "POST") return hGraderConfirm(res, body);
    if (p === "/api/cosmocanyon/assets" && m === "DELETE") return hAssetsDelete(res, body);
  }
  res.writeHead(404, { "Content-Type": "application/json" }); res.end(JSON.stringify({ error: "not found" }));
}

// ════════════════════════════════════════════════════════════════════════════
// Single-instance guard + boot
// ════════════════════════════════════════════════════════════════════════════
function preCheckHostLock() {
  // §6 single-instance: refuse if another host is alive. The port bind (below) is the authoritative mutex;
  // this pidfile is the friendly pre-check + the banner's data source. Stale-breakable: a dead pid is broken.
  try {
    if (existsSync(CC_HOST_LOCK)) {
      const j = JSON.parse(readFileSync(CC_HOST_LOCK, "utf-8"));
      if (j.pid && j.pid !== process.pid && ccPidAlive(j.pid)) {
        console.error(`[cc-server] REFUSE: another cosmo host is alive (pid ${j.pid}, since ${j.started || "?"}). Single-instance — exiting.`);
        process.exit(1);
      }
    }
  } catch {}
}
function writeHostLock() { try { mkdirSync(CC_CONTROL, { recursive: true }); ccAtomicWrite(CC_HOST_LOCK, JSON.stringify({ pid: process.pid, kind: "server", started: new Date().toISOString() })); } catch {} }
function clearHostLock() { try { const j = JSON.parse(readFileSync(CC_HOST_LOCK, "utf-8")); if (j.pid === process.pid) unlinkSync(CC_HOST_LOCK); } catch {} }

// §C2 — this control plane spawns processes + runs destructive git (rollback = reset --hard + clean -fd), so it
// MUST NOT be reachable off-box. The socket binds dual-stack (the Windows `localhost`→::1 reachability the bind
// comment below explains), so loopback is enforced HERE by the client address instead: any non-loopback peer is
// refused before routing. Cheaper + more reliable than a single-family bind that breaks `localhost` resolution.
function remoteIsLoopback(req) {
  const a = (req.socket && req.socket.remoteAddress) || "";
  return a === "127.0.0.1" || a === "::1" || a === "::ffff:127.0.0.1" || a.startsWith("127.");
}
// §C3 — CSRF: legitimate browser callers are the dashboard (same-origin :7788) and the Launcher (:3333). A
// state-changing request carrying ANY other Origin is a cross-site attack from a page in the operator's browser
// (which passes the loopback guard because the browser IS local). Non-browser clients (curl, the Launcher's
// server-side fetch) send no Origin and are allowed (already loopback-gated). Also used to reflect ACAO (no wildcard).
const CC_ALLOWED_ORIGINS = new Set([
  `http://localhost:${PORT}`, `http://127.0.0.1:${PORT}`,
  "http://localhost:3333", "http://127.0.0.1:3333", // Launcher
]);

function startServer() {
  preCheckHostLock();

  const server = http.createServer(async (req, res) => {
    if (!remoteIsLoopback(req)) { res.writeHead(403, { "Content-Type": "application/json" }); return res.end(JSON.stringify({ error: "loopback only" })); }
    const origin = req.headers.origin;
    if (origin && CC_ALLOWED_ORIGINS.has(origin)) res.setHeader("Access-Control-Allow-Origin", origin); // reflect allowlisted origin; NO wildcard
    res.setHeader("Vary", "Origin");
    res.setHeader("Access-Control-Allow-Methods", "GET,POST,DELETE,OPTIONS");
    res.setHeader("Access-Control-Allow-Headers", "Content-Type");
    if (req.method === "OPTIONS") { res.writeHead(204); return res.end(); }
    // §C3 CSRF gate: a mutating request with a present-but-not-allowlisted Origin is refused.
    if ((req.method === "POST" || req.method === "DELETE") && origin && !CC_ALLOWED_ORIGINS.has(origin)) {
      return sendJson(res, 403, { error: "cross-origin state change refused" });
    }
    try { await route(req, res, new URL(req.url, "http://localhost").pathname, new URL(req.url, "http://localhost")); }
    catch (e) { if (!res.headersSent) sendJson(res, e.code === 413 ? 413 : e.code === 400 ? 400 : 500, { error: e.message }); }
  });

  server.on("error", (e) => {
    if (e.code === "EADDRINUSE") { console.error(`[cc-server] REFUSE: port ${PORT} already in use — another cosmo host is up. Single-instance — exiting.`); process.exit(1); }
    console.error(`[cc-server] FATAL ${e.stack || e}`); process.exit(1);
  });

  // Bind with NO explicit host (dual-stack) — a 127.0.0.1-only bind is unreachable via `localhost` when
  // Windows resolves it to ::1 first, which is exactly how the browser + Launcher probe reach :7788.
  server.listen(PORT, () => {
    writeHostLock();
    reconcileAssetIndex();                                            // §15.25 rebuild the derived index once on boot
    console.log(`\n  Cosmo Canyon standalone app → http://localhost:${PORT}/\n`);
    ccEnsureVite(); setInterval(ccEnsureVite, 20000);                 // keep the game dev server (:8780) up
    setInterval(ccSnapshot, 180000); setTimeout(ccSnapshot, 14000);   // auto-snapshot cadence (fix 10)
  });

  for (const sig of ["SIGINT", "SIGTERM"]) process.on(sig, () => { clearHostLock(); process.exit(0); });
  process.on("exit", clearHostLock);
  return server;
}

// Boot only when run directly (`node server.mjs`); a test can `import` this module for its exported
// helpers without binding the port. process.argv[1] resolves to this file iff invoked as the entrypoint.
const invokedDirectly = process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url);
if (invokedDirectly) startServer();
