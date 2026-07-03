// Cosmo Canyon — parallel DISPATCH (§15g phase 8). The top-of-cycle piece that turns the deterministic
// schedule PLAN into live claims + isolated worktrees, ready for the host to spawn N workers into.
//
// This is the glue between the already-built contracts: schedule.mjs (pick a disjoint set) → claim.mjs (atomic
// per-agent anchor {baseSha,beadId,worktree}) → worktree.mjs (C:/Vibes-cc-wt/<id> --detach at BASE_SHA +
// node_modules junction so the worker can gate in isolation). It does NOT spawn workers (that is host-specific:
// the supervisor spawns `claude -p` processes; the Workflow host spawns agent() calls) and it does NOT commit
// (the single-committer merge.mjs does). SERIAL mode → planCycle is a no-op (the host keeps today's byte-for-byte
// single-flight path); this whole module is dormant until config.mode=parallel.
//
// Policy (15.42, minimal): agy is worktree-blind → schedule routes it to ONE serial lane; dispatch runs a
// parallel BATCH of claude workers OR (only when nothing parallel is pickable) the ONE serial agy bead via the
// host's existing main-tree serial path — NEVER both in a cycle → agy never runs concurrently with the merge.
import { readFileSync } from "node:fs";
import { randomBytes } from "node:crypto";
import { readConfig, isSerial } from "./config.mjs";
import { schedule } from "./schedule.mjs";
import { claim, releaseClaim, readClaims, claimIsLive } from "./claim.mjs";
import { create as wtCreate, linkNodeModules, remove as wtRemove, worktreePath } from "./worktree.mjs";
import { writeActive, isTerminal } from "./assets-core.mjs";
import { readIndex } from "./assets.mjs"; // §15g Finding-B — asset-state gate at plan time (skip not_ready/stale/gone asset beads)
import { bumpUsage, usageToday, headSha, gameHeadSha } from "./state.mjs";
import { pickWorker, readAllowed, agyCoolingDown } from "./agent-core.mjs"; // orchestrator worker pick (allowed set → task-fit engine+model)

const CC = "C:/Vibes/cosmo-canyon";
function controlRoot() { return process.env.CC_CONTROL || `${CC}/control`; }
function backlogPath() { return `${controlRoot()}/backlog.json`; }
function claimsDir() { return `${controlRoot()}/claims`; }
function claimPathOf(id) { return `${claimsDir()}/${id}.claim.json`; }
function gatePathOf(id) { return `${claimsDir()}/${id}.gate.json`; }
function readJson(p, d) { try { return JSON.parse(readFileSync(p, "utf8")); } catch { return d; } }

// The claim/worktree id for a bead: its assetId (asset beads) else its bead id — both match the CLAIM_ID_RE /
// WT_ID_RE charset (asset-<a-…>-r<rev> and cc-#### are safe). One id → one claim file → one worktree dir.
export function idOf(bead) { return bead.assetId || bead.id; }

// planCycle — schedule → claim → worktree for one cycle. Returns the dispatched worker set (the host spawns a
// worker per entry) + an optional serialAgy bead (only when nothing parallel was dispatched). PURE of git commits.
//   baseSha : §SPLIT — the GAME-repo cycle anchor (all worktrees detach at the SAME game sha); the supervisor now
//             passes the GAME head. Defaults to gameHeadSha(). The C:/Vibes CONTROL base (settings-guard anchor) is
//             computed separately from headSha() and rides the claim as baseSha; gameBaseSha rides it as the game anchor.
//   cap     : daily tick budget (capRemaining = cap - usageToday, tier-weighted inside schedule).
//   hostPid : the pid written into each claim (liveness). The supervisor calls in-process (its own long-lived
//             pid keeps claims live for reconcile); the Workflow host runs `node dispatch.mjs` one-shot — its
//             pid dies, which is fine because NO reconcile runs mid-cycle in either host (single-flight per run).
export function planCycle({ baseSha = null, cap = 200, hostPid = process.pid, now = Date.now() } = {}) {
  const cfg = readConfig();
  if (isSerial(cfg)) return { mode: "serial", slots: 0, parallel: [], serialAgy: null, deferred: [], note: "serial — no-op" };
  // §SPLIT (2026-07-03) — TWO anchors. gameBase = the GAME-repo head (worktree detach anchor + bookkeep's
  // gate/revert/commit target); the incoming baseSha param IS the game base (supervisor passes the game head).
  // controlBase = the C:/Vibes head, carried on the claim ONLY for bookkeep's .claude/settings.json tamper guard
  // (which runs `git -C C:/Vibes`). Worktrees detach at gameBase; the settings guard checks against controlBase.
  const gameBase = baseSha || gameHeadSha();
  const controlBase = headSha();
  const backlog = readJson(backlogPath(), []);
  const nonTerminal = (Array.isArray(backlog) ? backlog : []).filter((b) => b && !isTerminal(b.status));
  // orchestrator worker pick per bead (operator's allowed set → task-fit engine+model), shared with the
  // supervisor/tick so serial + parallel agree. A bead with NO enabled model (only agy checked + feel work)
  // is not dispatchable this cycle → dropped from candidates. The chosen engine drives schedule's serial(agy)/
  // parallel(claude) routing; the model rides on each dispatched record for the worker agent to run on.
  // decorate for schedule: use the ASSET kind (image/audio) for asset beads so resolveFiles infers ownership
  // + the manifest merge-lock; a non-asset bead falls back to its backlog kind + declared files[].
  let agentJson = null; try { agentJson = JSON.parse(readFileSync(`${controlRoot()}/agent.json`, "utf8")); } catch {}
  const allowed = readAllowed(agentJson);
  const cooldownAgy = agyCoolingDown(controlRoot());
  // §15g Finding-B — the scheduler must NEVER dispatch a bead whose ASSET is no longer dispatchable. Setting an
  // asset not_ready (or reopening / re-arming it → a rev bump) does NOT retract its already-queued bead, and the
  // parallel cycle does not run the asset-reconcile that the serial sense does; without this guard a not_ready /
  // stale-rev asset bead is re-dispatched every cycle only to stale-rev-REVERT at the seam (bookkeep 15.39) —
  // wasted worker, and (pre-fix) each revert fed the auto-drop. Gate here at plan time on the DERIVED index; the
  // fail-closed stale/terminal checks in bookkeep remain the land-time backstop. A non-asset (hand/planner) bead
  // has no assetId → never gated.
  let assetById = new Map();
  try { assetById = new Map((readIndex().assets || []).map((a) => [a.id, a])); } catch {}
  const skippedStale = [];
  function assetDispatchable(b) {
    if (!b.assetId) return true;
    const a = assetById.get(b.assetId);
    if (!a) { skippedStale.push({ id: b.id, reason: "asset-missing" }); return false; }               // deleted / tombstoned
    if (a.state !== "ready") { skippedStale.push({ id: b.id, reason: "asset-not-ready" }); return false; }
    if (b.rev != null && a.rev != null && String(b.rev) !== String(a.rev)) { skippedStale.push({ id: b.id, reason: "asset-stale-rev" }); return false; }
    return true;
  }

  const pickById = new Map();
  const candidates = [];
  for (const b of nonTerminal) {
    if (!assetDispatchable(b)) continue; // Finding-B: asset not_ready / stale-rev / gone → not dispatchable this cycle
    const kind = b.assetKind || b.kind;
    const pw = pickWorker({ kind, tier: b.tier, engine: b.engine }, allowed, { cooldownAgy });
    if (!pw.engine) continue; // no enabled model for this bead → skip this cycle (operator narrowed the set)
    pickById.set(b.id, pw);
    candidates.push({ id: b.id, kind, tier: b.tier, engine: pw.engine, files: b.files, manifestKey: b.manifestKey, priority: b.priority || 0, age: b.created ? Math.max(0, now - Date.parse(b.created)) : 0 });
  }
  const beadById = new Map(nonTerminal.map((b) => [b.id, b]));
  // live claims from a PRIOR cycle still hold their files/slots (a crashed cycle's dead claims are excluded so
  // their slots free up — reconcileParallel GCs them + their worktrees at the next boot).
  const activeClaims = readClaims().filter((c) => claimIsLive(c, { now, cfg }));
  const capRemaining = Math.max(0, cap - usageToday());
  const plan = schedule(candidates, { config: cfg, activeClaims, capRemaining });

  const dispatched = [], rolledBack = [];
  for (const p of plan.parallel) {
    const bead = beadById.get(p.id);
    if (!bead) continue;
    const id = idOf(bead);
    const wtPath = worktreePath(id, { root: cfg.concurrency.worktreeRoot });
    // 1. atomic claim (per-agent anchor; disjointness re-check is the linearization point inside claim())
    //    §SPLIT — carry BOTH anchors: baseSha=controlBase (C:/Vibes, settings guard) + gameBaseSha=gameBase (the game
    //    detach anchor bookkeep gates/reverts/commits against).
    const cl = claim({ assetId: id, beadId: bead.id, filesLease: p.owned, exclusive: p.exclusive, engine: p.engine, baseSha: controlBase, gameBaseSha: gameBase, worktree: wtPath, pid: hostPid, now, cfg });
    if (!cl.ok) { rolledBack.push({ id: bead.id, reason: cl.reason }); continue; }
    // 2. isolated worktree of the GAME repo, --detach at the GAME base + node_modules junction (worker gates in isolation)
    const wt = wtCreate(id, gameBase, { root: cfg.concurrency.worktreeRoot });
    if (!wt.ok) { releaseClaim(id); rolledBack.push({ id: bead.id, reason: `worktree-create: ${wt.error}` }); continue; }
    const lk = linkNodeModules(wt.path, { root: cfg.concurrency.worktreeRoot });
    if (!lk.ok) { try { wtRemove(wt.path, { root: cfg.concurrency.worktreeRoot }); } catch {} releaseClaim(id); rolledBack.push({ id: bead.id, reason: `node_modules-link: ${lk.error}` }); continue; }
    // 3. in-flight row + usage bump (each worker is one tick of daily cost)
    const runToken = randomBytes(6).toString("hex");
    try { writeActive({ runToken, beadId: bead.id, assetId: bead.assetId || null, kind: "work", engine: p.engine, tier: p.tier, title: bead.title || bead.id, baseSha: gameBase, startEpoch: now, pid: hostPid, worktree: wt.path }); } catch {}
    bumpUsage();
    // §SPLIT — the dispatched record carries gameBaseSha so the merge can read the game diff anchor off it (falls
    // back to the gate marker's gameBaseSha). baseSha stays the game anchor for the active-row's existing meaning.
    dispatched.push({ beadId: bead.id, assetId: bead.assetId || null, id, engine: p.engine, model: (pickById.get(bead.id) || {}).model || null, tier: p.tier, title: bead.title || bead.id, worktree: wt.path, claimPath: claimPathOf(id), gatePath: gatePathOf(id), runToken, baseSha: gameBase, gameBaseSha: gameBase });
  }

  // serial agy: only when NOTHING parallel was dispatched this cycle (agy never concurrent with the merge, 15.42).
  let serialAgy = null;
  if (!dispatched.length && plan.serial.length) serialAgy = beadById.get(plan.serial[0].id) || null;

  return { mode: "parallel", slots: plan.slots, base: gameBase, controlBase, parallel: dispatched, serialAgy, deferred: [...plan.deferred, ...rolledBack, ...skippedStale], picked: dispatched.length };
}

// tiny CLI for the Workflow host: `node dispatch.mjs [--base <sha>] [--cap N]` → prints the planCycle JSON.
import { fileURLToPath } from "node:url";
import { resolve } from "node:path";
if (process.argv[1] && fileURLToPath(import.meta.url) === resolve(process.argv[1])) {
  const args = process.argv.slice(2);
  const arg = (n, d) => { const i = args.indexOf(`--${n}`); return i >= 0 && i + 1 < args.length ? args[i + 1] : d; };
  const out = planCycle({ baseSha: arg("base", null), cap: Number(arg("cap", "200")) });
  process.stdout.write(JSON.stringify(out));
}
