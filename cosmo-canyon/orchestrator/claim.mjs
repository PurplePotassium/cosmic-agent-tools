// Cosmo Canyon — atomic per-asset CLAIM store (§15c-2). Exactly-one ownership + heartbeat liveness.
//
// A claim is `control/claims/<assetId>.claim.json` (GITIGNORED) holding the parallel token
// `{beadId,assetId,pid,startToken,epoch,filesLease[],baseSha,gameBaseSha,worktree}`. It is the per-agent tick
// ANCHOR (the anchors/beadId/worktree live HERE, not a singleton .tick.json — 15.26) and the file-ownership lease
// the scheduler's disjointness invariant is built on. §SPLIT: baseSha = C:/Vibes control head (settings guard);
// gameBaseSha = the GAME-repo head = the worktree detach anchor bookkeep gates/reverts/commits against.
//
// EXACTLY-ONE under a TOCTOU guard (§15c-2): claim() runs under acquireOrdered(['active','claims']) and, as
// its LINEARIZATION POINT, re-checks disjointness against the CURRENTLY-LIVE claim set inside the lock — two
// schedulers racing the same asset (or overlapping files) can't both win. Staleness is a HEARTBEAT signal,
// NOT the 5-min control TTL: the owning agent re-stamps `epoch` every ~heartbeatSec; a claim that missed ~3×
// that interval (and exceeds the per-agent timeout) is steal-able, so a wedged agent frees its files and two
// schedulers never race a live long claim (§15.27). pid reuse is guarded by `startToken` (OS process
// start-identity) — alive(pid) counts only if the token still matches; a recycled pid ⇒ token mismatch ⇒ dead.
//
// Toggle stays OFF: no live caller yet (phase 5 wires reconcileAssets/dispatch). This is the contract + a
// unit test on a scratch control root (CC_CONTROL). ROOT-parameterized like assets.mjs.
import { acquireOrdered, releaseAll, acquire, release } from "./lock.mjs";
import { readConfig } from "./config.mjs";
import { remove as wtRemove } from "./worktree.mjs";
import { execFileSync } from "node:child_process";
import { mkdirSync, writeFileSync, renameSync, readFileSync, readdirSync, existsSync, rmSync } from "node:fs";
import { randomBytes } from "node:crypto";
import { resolve, sep } from "node:path";

const CC = "C:/Vibes/cosmo-canyon";
function controlRoot() { return process.env.CC_CONTROL || `${CC}/control`; }
function claimsDir() { return `${controlRoot()}/claims`; }
function locksDir() { return `${controlRoot()}/locks`; }

const now0 = () => Date.now();
function atomicWrite(p, data) { const t = `${p}.tmp`; writeFileSync(t, data); renameSync(t, p); }
function readJsonSafe(p, d = null) { try { return JSON.parse(readFileSync(p, "utf8")); } catch { return d; } }
// same normalization schedule.mjs uses for overlap (fwd-slash, drop ./, lowercase — Windows FS is case-insensitive)
function normFile(f) { return String(f || "").replace(/\\/g, "/").replace(/^\.\//, "").replace(/\/+$/, "").toLowerCase(); }

// Claim-id guard: charset + no "..", + resolve-CONTAINMENT under claimsDir (a claim id can be an asset id
// `a-…-xxxx` OR a bead id `cc-0009`; containment is the load-bearing anti-traversal guard for the FILE path).
const CLAIM_ID_RE = /^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$/;
export function validateClaimId(id) {
  if (typeof id !== "string" || !CLAIM_ID_RE.test(id) || id.includes("..")) throw new Error(`bad claim id: ${JSON.stringify(id)}`);
  const root = resolve(claimsDir());
  const abs = resolve(root, `${id}.claim.json`);
  if (!abs.startsWith(root + sep)) throw new Error(`claim id escapes claims root: ${id}`);
  return id;
}
function claimPath(id) { return `${claimsDir()}/${validateClaimId(id)}.claim.json`; }

function alive(pid) {
  if (!pid || pid <= 0) return false;
  try { process.kill(pid, 0); return true; } catch (e) { return e.code === "EPERM"; }
}

// Best-effort OS process start-identity for the pid-reuse guard. On Windows the process CreationDate
// changes when a pid is recycled, so a mismatch vs the stored startToken means a DIFFERENT process now holds
// that pid. Returns null on any failure → callers treat null as "unknown" and DO NOT falsely kill a claim.
export function osStartToken(pid) {
  if (!pid || pid <= 0) return null;
  try {
    const out = execFileSync("powershell.exe", ["-NoProfile", "-Command",
      `$p=Get-Process -Id ${Number(pid)} -ErrorAction SilentlyContinue; if($p){$p.StartTime.Ticks}`],
      { encoding: "utf8", timeout: 8000 }).trim();
    return out || null;
  } catch { return null; }
}
// A fresh claimer's own token: its OS start-identity, else a random nonce (still unique per process run).
export function mkStartToken(pid = process.pid) { return osStartToken(pid) || `rnd:${randomBytes(8).toString("hex")}`; }

// staleness window: exceed the per-agent timeout AND ~3 missed heartbeats, so the watchdog frees a wedged
// agent but never races a live long claim (§15.27). Derived from config; overridable for tests.
function staleMsFrom(cfg) {
  const c = cfg.concurrency;
  return Math.max(3 * c.heartbeatSec * 1000, c.perAgentTimeoutMin * 60 * 1000 + c.heartbeatSec * 1000);
}

// Is a claim still LIVE? dead if: pid not alive; OR heartbeat stale (wedged); OR pid reused (startToken
// mismatch). startTokenOf(pid) injectable for tests; defaults to osStartToken (null ⇒ don't falsely kill).
export function claimIsLive(claim, { now = now0(), staleMs, startTokenOf = osStartToken, cfg = readConfig() } = {}) {
  if (!claim || !claim.pid) return false;
  const window = staleMs == null ? staleMsFrom(cfg) : staleMs;
  if (!alive(claim.pid)) return false;                          // process gone
  if (now - (claim.epoch || 0) > window) return false;          // heartbeat missed → wedged/stale
  // pid-reuse guard — ONLY when the stored token is a real OS start-identity. A `rnd:` nonce (osStartToken
  // was unavailable at CREATE) carries NO OS identity, so it can NEVER equal a later real-ticks reading →
  // comparing them would wrongly steal a LIVE claim (audit HIGH-3). Skip the check for rnd: tokens; heartbeat
  // staleness remains the correctness backstop (a truly-dead owner stops re-stamping → GC'd within the window).
  if (claim.startToken && !String(claim.startToken).startsWith("rnd:")) {
    const live = startTokenOf(claim.pid);
    if (live != null && String(live) !== String(claim.startToken)) return false; // recycled pid = different process
  }
  return true;
}

export function readClaim(id) { return readJsonSafe(claimPath(id), null); }
export function readClaims() {
  let names = [];
  try { names = readdirSync(claimsDir()).filter((n) => n.endsWith(".claim.json")); } catch { return []; }
  // DROP any record whose INTERNAL assetId is malformed/traversing (validateClaimId) — an adversarial worker
  // (§15.41 threat model) can plant control/claims/evil.claim.json with assetId="x/../secret"; validateClaimId
  // only guards the FILENAME via claimPath, so without this filter a tainted assetId reaches reconcile's
  // .agy-<id>.pid path build → arbitrary-file read → taskkill (audit MEDIUM-1). Inert malformed files are left
  // on disk (their path can't be safely derived to remove) but NEVER acted upon.
  return names.map((n) => readJsonSafe(`${claimsDir()}/${n}`, null)).filter(Boolean)
    .filter((c) => { try { validateClaimId(c.assetId); return true; } catch { return false; } });
}

// Atomic exactly-one claim under acquireOrdered(['active','claims']). Disjointness RE-CHECK vs the LIVE claim
// set inside the lock = the TOCTOU linearization point. Stale claims are GC'd here so they don't block.
// Returns {ok:true, claim} or {ok:false, reason}. NEVER holds a lock across a wait.
export function claim({ assetId, beadId = null, filesLease = [], exclusive = false, engine = null,
                        baseSha = null, gameBaseSha = null, worktree = null,
                        pid = process.pid, startToken, now = now0(), cfg = readConfig(), startTokenOf = osStartToken } = {}) {
  validateClaimId(assetId);
  const lease = (Array.isArray(filesLease) ? filesLease : []).map(normFile);
  const excl = !!exclusive; // global-exclusive (spec/unknown surface) — conflicts with EVERY other claim
  const token = startToken || mkStartToken(pid);
  mkdirSync(claimsDir(), { recursive: true });
  const held = acquireOrdered(locksDir(), ["active", "claims"]);
  try {
    // GC dead claims + collect the live set (single scan)
    const live = [];
    for (const c of readClaims()) {
      if (claimIsLive(c, { now, cfg, startTokenOf })) { live.push(c); continue; }
      // §AUDIT-2026-07-02 HIGH-6 — GC the stale claim's WORKTREE before dropping the record: the claim file is the
      // ONLY reference to the worktree path, so removing it first would orphan C:/Vibes-cc-wt/<id> + its
      // .git/worktrees/<id> admin dir forever → future wtCreate(id) fails "already exists" (that asset never
      // dispatches parallel again) + unbounded disk. stealStale() defers this to its caller; claim()'s inline GC has
      // no downstream caller, so it must clean the worktree itself. (wtRemove is explicit-path-only, 15.43.)
      if (c.worktree) { try { wtRemove(c.worktree, { root: cfg.concurrency.worktreeRoot }); } catch {} }
      try { rmSync(claimPath(c.assetId), { force: true }); } catch {} // steal-and-remove stale
    }
    // exactly-one per asset
    if (live.some((c) => c.assetId === assetId)) return { ok: false, reason: "already-claimed" };
    // disjointness re-check (linearization point): MIRRORS schedule.mjs overlaps() — a global-exclusive claim
    // (this one OR a live one) conflicts with EVERYTHING; otherwise the requested lease must be disjoint from
    // every live lease (audit HIGH-1: without the exclusive arm, two empty-lease specs — or an exclusive spec
    // + any asset — both won, breaking the §15c-2 at-most-one-owner invariant under N>1).
    for (const c of live) {
      if (excl || c.exclusive) return { ok: false, reason: "files-conflict", conflictWith: c.assetId };
      const other = new Set((c.filesLease || []).map(normFile));
      if (lease.some((f) => other.has(f))) return { ok: false, reason: "files-conflict", conflictWith: c.assetId };
    }
    // engine + exclusive PERSISTED on the record so a later schedule()/reconcile cycle reads the real values
    // off the in-flight claim (audit HIGH-2: schedule's cross-cycle agy-serial guard reads activeClaims[].engine;
    // audit HIGH-1 round-trip: schedule reads activeClaims[].exclusive). Both were absent → dead/false.
    // §SPLIT (2026-07-03) — TWO anchors per tick: baseSha = the C:/Vibes CONTROL head (for the .claude/settings.json
    // tamper guard, which runs `git -C C:/Vibes`); gameBaseSha = the GAME-repo head = the worktree detach anchor +
    // what bookkeep --gate-only gates/reverts/commits against. The claim MUST carry BOTH (bookkeep reads tick.baseSha
    // AND tick.gameBaseSha; a missing gameBaseSha would fall back to the worktree's HEAD, which is the same detach
    // commit, but persist it explicitly so the two-repo contract is authoritative, not incidental).
    const rec = { beadId, assetId, engine: engine || null, pid, startToken: token, epoch: now, filesLease: lease, exclusive: excl, baseSha, gameBaseSha, worktree };
    atomicWrite(claimPath(assetId), JSON.stringify(rec, null, 2) + "\n");
    return { ok: true, claim: rec };
  } finally { releaseAll(held); }
}

// Re-stamp the heartbeat epoch. Verifies token (a foreign process can't restamp someone else's claim).
export function heartbeat(assetId, { token, now = now0() } = {}) {
  const dir = acquire(locksDir(), "claims");
  try {
    const rec = readClaim(assetId);
    if (!rec) return { ok: false, reason: "no-claim" };
    if (token && rec.startToken && String(token) !== String(rec.startToken)) return { ok: false, reason: "token-mismatch" };
    rec.epoch = now;
    atomicWrite(claimPath(assetId), JSON.stringify(rec, null, 2) + "\n");
    return { ok: true, claim: rec };
  } finally { release(dir); }
}

// Release a claim (owner exit). token optional — a reconciler steal releases without it.
export function releaseClaim(assetId, { token } = {}) {
  const dir = acquire(locksDir(), "claims");
  try {
    const rec = readClaim(assetId);
    if (!rec) return { ok: true, already: true };
    if (token && rec.startToken && String(token) !== String(rec.startToken)) return { ok: false, reason: "token-mismatch" };
    try { rmSync(claimPath(assetId), { force: true }); } catch {}
    return { ok: true, released: rec };
  } finally { release(dir); }
}

// GC all stale claims (watchdog/reconcile). Returns the stolen records so the caller can GC each one's
// worktree by EXPLICIT path (worktree.mjs) + kill its per-agent agy pid. Under the claims lock.
export function stealStale({ now = now0(), cfg = readConfig(), startTokenOf = osStartToken } = {}) {
  const dir = acquire(locksDir(), "claims");
  try {
    const stolen = [];
    for (const c of readClaims()) {
      if (!claimIsLive(c, { now, cfg, startTokenOf })) { stolen.push(c); try { rmSync(claimPath(c.assetId), { force: true }); } catch {} }
    }
    return stolen;
  } finally { release(dir); }
}
