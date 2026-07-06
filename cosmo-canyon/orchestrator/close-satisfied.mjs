// Cosmo Canyon — CONFIRM-SATISFIED (§GC1). The operator (or the present driving Claude Code agent, after
// VERIFYING the feature renders/works) attests a SPEC is already satisfied by existing code — the one acceptance
// route the autonomous machinery lacked (worker blocks "already implemented"; a sim grader would be tautological;
// feel-confirm has no advisory land to bind). It writes the FULL provenance implemented() needs for a feel-advisory
// spec (a completion row + implementedBy + feelConfirmed, all pinned to the current HEAD) and commits, so the
// asset flips derived-Implemented HONESTLY (an operator attestation, never a model self-attest green — §15.18).
//
// Two callers, ONE core (closeSatisfied): the CLI (`node orchestrator/close-satisfied.mjs --id <assetId>`) that a
// driving CC agent runs via Bash, and server.mjs's POST /assets/confirm-satisfied (dashboard). Roots honor the
// CC_REPO/CC_CC/CC_CONTROL env seams (default = the real repo) so it is testable against a throwaway plane.
//
// SAFETY: only call when NO autonomous tick/merge is mid-flight (it commits directly, like grader-confirm). The
// present-agent "Drive" flow controls this; the dashboard button is a single-user loopback action.
import { execSync } from "node:child_process";
import { readFileSync, writeFileSync, renameSync, existsSync } from "node:fs";
import { acquire, release } from "./lock.mjs";
import { readAsset, markSatisfied } from "./assets.mjs";

const REPO = process.env.CC_REPO || "C:/Vibes";
const CC = process.env.CC_CC || `${REPO}/cosmo-canyon`;
const GAME = process.env.CC_GAME || `${CC}/game`; // §SPLIT — the game's own nested repo (provenance sha lives HERE)
const CONTROL = process.env.CC_CONTROL || `${CC}/control`;
const LOCKS = `${CONTROL}/locks`;

const nowIso = () => new Date().toISOString();
function readJson(p, d) { try { return JSON.parse(readFileSync(p, "utf8")); } catch { return d; } }
function atomicWrite(p, s) { const t = `${p}.tmp`; writeFileSync(t, s); renameSync(t, p); }
function writeJson(p, o) { atomicWrite(p, JSON.stringify(o, null, 2) + "\n"); }
// §SPLIT — the feature the operator is attesting lives in the GAME repo, so pin provenance to the GAME repo HEAD.
function headSha() { return execSync(`git -C "${GAME}" rev-parse HEAD`, { encoding: "utf8" }).trim(); }

// Close a Ready SPEC as satisfied-by-existing-code. Returns { ok, assetId, beadId, sha }.
export function closeSatisfied(assetId, { by = "operator" } = {}) {
  // §AUDIT-2026-07-04 — operator/present-agent ONLY. A WORKER must never self-attest Implemented (the whole
  // anti-false-green stance); asset Instructions are untrusted text a prompt-injected worker could obey. Worker
  // ticks always carry RALPH_PASS=<beadId> (serial) or CC_WORKER_NO_COMMIT=1 (parallel worktree) in env — refuse
  // under either. Also refuse while ANY tick/claim is in flight (this commits directly; SAFETY note above).
  if (process.env.CC_WORKER_NO_COMMIT || process.env.RALPH_PASS)
    throw new Error("refused: worker-tick context (RALPH_PASS/CC_WORKER_NO_COMMIT set) — confirm-satisfied is an operator/driving-agent gate");
  if (existsSync(`${CONTROL}/.tick.json`))
    throw new Error("refused: a tick is in flight (control/.tick.json exists) — stop/finish the tick first");
  if (existsSync(`${CONTROL}/claims/${assetId}.claim.json`))
    throw new Error(`refused: a live parallel claim exists for ${assetId} — let it finish or reconcile first`);
  const meta = readAsset(assetId); // throws on unknown id
  if (meta.kind !== "spec") throw new Error(`confirm-satisfied is for SPEC assets only (got kind=${meta.kind}); image/audio have deterministic graders`);
  if (meta.state !== "ready") throw new Error(`asset ${assetId} is not Ready (state=${meta.state}) — nothing to confirm`);
  const rev = meta.rev;
  const beadId = `asset-${assetId}-r${rev}`;                 // the projected bead id (matches projectAssetToBead)
  const sha = headSha();                                     // the feature exists as of current HEAD — pin provenance to it

  // 1. completion row (feelAdvisory so implemented() routes through the feelConfirmed check; alreadySatisfied is a
  //    provenance marker for the dashboard/audit). acceptanceSkipped:false so the fail-closed predicate accepts it.
  {
    const dir = acquire(LOCKS, "completions");
    try {
      const comp = readJson(`${CONTROL}/completions.json`, []);
      if (!comp.some((c) => c && c.id === beadId)) {
        comp.unshift({
          id: beadId, title: `Asset ${meta.manifestKey || meta.filename || assetId}: confirmed satisfied by existing code`,
          acceptance: "operator confirm-satisfied (spec already satisfied by existing code)",
          result: `operator confirm-satisfied by ${by}: spec satisfied by existing code`, ts: nowIso(),
          sha, assetKey: meta.manifestKey ?? null, contentHash: meta.contentHash ?? null, rev,
          acceptanceSkipped: false, feelAdvisory: true, alreadySatisfied: true,
        });
        writeJson(`${CONTROL}/completions.json`, comp);
      }
    } finally { release(dir); }
  }

  // 2. asset provenance (implementedBy WITH sha + feelConfirmed) — the derived Implemented flip (markSatisfied
  //    rebuilds the index internally). No rev bump (not an authority-generation change).
  markSatisfied(assetId, { beadId, sha, contentHash: meta.contentHash, rev, by });

  // 3. close the projecting backlog bead if present (needsOperator/blocked → done) so it isn't surfaced as open.
  {
    const dir = acquire(LOCKS, "backlog");
    try {
      const backlog = readJson(`${CONTROL}/backlog.json`, []);
      const b = backlog.find((x) => x && x.id === beadId);
      if (b) { b.status = "done"; b.needsOperator = false; b.blocked_reason = ""; b.updated = nowIso(); writeJson(`${CONTROL}/backlog.json`, backlog); }
    } finally { release(dir); }
  }

  // 4. commit the operator attestation so it sits clean at BASE (mirrors grader-confirm/feel-confirm committers).
  //    git add respects .gitignore → only tracked control changes (meta.json/completions.json/backlog.json) stage.
  try { execSync(`git -C "${REPO}" add cosmo-canyon/control`, { encoding: "utf8" }); } catch {}
  try { execSync(`git -C "${REPO}" commit -q -m "cosmo-canyon: confirm-satisfied ${beadId} (${assetId})"`, { encoding: "utf8" }); } catch {}

  return { ok: true, assetId, beadId, sha };
}

// CLI: node orchestrator/close-satisfied.mjs --id <assetId>
if (import.meta.url === `file://${process.argv[1].replace(/\\/g, "/")}` || process.argv[1]?.endsWith("close-satisfied.mjs")) {
  const args = process.argv.slice(2);
  const i = args.indexOf("--id");
  const id = i >= 0 && i + 1 < args.length ? args[i + 1] : null;
  if (!id) { console.error("usage: node orchestrator/close-satisfied.mjs --id <assetId>"); process.exit(2); }
  try { console.log(JSON.stringify(closeSatisfied(id, { by: "agent" }))); }
  catch (e) { console.error("close-satisfied failed:", e.message); process.exit(1); }
}
