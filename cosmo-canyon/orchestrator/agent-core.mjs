// Cosmo Canyon — WORKER SELECTION (the orchestrator's engine/model decision). PURE + deterministic.
//
// The operator checks an ALLOWED SET of models in the dashboard (control/agent.json `allowed`); the
// orchestrator then picks ONE per bead by TASK FIT, constrained to that set (fallback to the next-best
// checked one). This is the SINGLE source of truth for that decision — every host reads it the same way so
// serial and parallel never diverge:
//   • supervisor.mjs / dispatch.mjs IMPORT pickWorker to choose the model to spawn a tick/worker on;
//   • tick.md RUNS `node agent-core.mjs --pick --bead <id>` to choose the engine (agy dispatch vs claude
//     direct) — same pure fn, same inputs (bead + agent.json + cooldown) ⇒ host + tick always agree.
//
// "By task fit": hard/structural bead → opus; feel/visual → sonnet (agy is preview-blind, EXCLUDED); other
// logic/systems → agy (free). Author `bead.engine` override respected but still clamped to the allowed set.
// A transient control/.agy-cooldown (written by the §13.38 quota failover) drops agy WITHOUT touching the
// operator's checkboxes — so a temporary Gemini quota wall never silently rewrites their allowed set.
import { readFileSync, existsSync } from "node:fs";

const CC = "C:/Vibes/cosmo-canyon";
function controlRoot() { return process.env.CC_CONTROL || `${CC}/control`; }

// The three selectable workers. key = the checkbox id; engine = agy|claude; model = the claude -p / agy model.
export const WORKER_OPTIONS = Object.freeze([
  { key: "agy", engine: "agy", model: "gemini-3.5-flash", label: "agy — free Gemini (headless logic)" },
  { key: "sonnet", engine: "claude", model: "claude-sonnet-4-6", label: "sonnet — Claude (feel/visual, default work)" },
  { key: "opus", engine: "claude", model: "claude-opus-4-8", label: "opus — Claude (hard / structural beads)" },
]);
const BY_KEY = Object.fromEntries(WORKER_OPTIONS.map((o) => [o.key, o]));
export const ALL_KEYS = Object.freeze(WORKER_OPTIONS.map((o) => o.key));
const FEEL = new Set(["image", "audio", "feel", "art", "visual"]);
const COOLDOWN_MS = 45 * 60 * 1000; // agy quota failover cooldown before agy is retried

// Sanitize an allowed list → known keys, de-duped. EMPTY/invalid → ALL (the orchestrator is never left with
// zero workers → the loop can always run; the operator narrows it, never zeroes it).
export function normalizeAllowed(raw) {
  const arr = Array.isArray(raw) ? raw.filter((k) => BY_KEY[k]) : [];
  const uniq = [...new Set(arr)];
  return uniq.length ? uniq : [...ALL_KEYS];
}

// Read the allowed set off a parsed agent.json. Back-compat: a legacy {engine,model} single-pick maps to the
// matching singleton set (so an old file still constrains sensibly until the operator re-saves).
export function readAllowed(agentJson) {
  if (agentJson && Array.isArray(agentJson.allowed)) return normalizeAllowed(agentJson.allowed);
  if (agentJson && agentJson.engine) {
    const hit = WORKER_OPTIONS.find((o) => o.engine === agentJson.engine && (!agentJson.model || o.model === agentJson.model));
    if (hit) return [hit.key];
    return agentJson.engine === "agy" ? ["agy"] : ["sonnet"];
  }
  return [...ALL_KEYS];
}

// Is agy in its post-quota cooldown right now? (marker present AND fresh). Callers pass the result as
// opts.cooldownAgy so pickWorker stays a pure fn of its args (testable without fs).
export function agyCoolingDown(control = controlRoot()) {
  try {
    const j = JSON.parse(readFileSync(`${control}/.agy-cooldown`, "utf8"));
    return Date.now() - (Number(j.at) || 0) < COOLDOWN_MS;
  } catch { return false; }
}

// TASK-FIT preference order (best → worst) for a bead, BEFORE the allowed-set clamp.
function prefList(bead) {
  const feel = FEEL.has(bead.kind);
  const hard = bead.tier === "heavy" || bead.tier === "structural";
  const ex = bead.engine === "agy" || bead.engine === "claude" ? bead.engine : null;
  if (ex === "claude" || feel) return hard ? ["opus", "sonnet"] : ["sonnet", "opus"]; // claude-only (feel forces it)
  if (ex === "agy") return ["agy", "sonnet", "opus"];                                 // author insists agy; clamp still applies
  return hard ? ["opus", "sonnet", "agy"] : ["agy", "sonnet", "opus"];                // auto: hard→opus, else logic→agy
}

// THE decision. Returns {key,engine,model} — the first task-fit preference that is in `allowed` (agy dropped
// for feel work or during cooldown). {key:null,...,reason} when the allowed set can't satisfy the bead.
export function pickWorker(bead = {}, allowed, opts = {}) {
  const set = new Set(normalizeAllowed(allowed));
  const feel = FEEL.has(bead.kind);
  const dropAgy = feel || !!opts.cooldownAgy;
  for (const key of prefList(bead)) {
    if (key === "agy" && dropAgy) continue;
    if (set.has(key)) return { key, engine: BY_KEY[key].engine, model: BY_KEY[key].model };
  }
  return { key: null, engine: null, model: null,
    reason: feel ? "no Claude model enabled (agy can't verify feel/visual work)" : "no enabled model can run this bead" };
}

// Convenience for hosts: read agent.json + cooldown off disk and pick for a bead in one call.
export function pickForBead(bead, control = controlRoot()) {
  let agentJson = null;
  try { agentJson = JSON.parse(readFileSync(`${control}/agent.json`, "utf8")); } catch {}
  return pickWorker(bead, readAllowed(agentJson), { cooldownAgy: agyCoolingDown(control) });
}

// ── CLI: `node agent-core.mjs --pick --bead <id>` → prints {engine,model,key,reason} for tick.md to branch on.
import { fileURLToPath } from "node:url";
import { resolve } from "node:path";
if (process.argv[1] && fileURLToPath(import.meta.url) === resolve(process.argv[1])) {
  const args = process.argv.slice(2);
  const arg = (n, d) => { const i = args.indexOf(`--${n}`); return i >= 0 && i + 1 < args.length ? args[i + 1] : d; };
  const control = controlRoot();
  if (args.includes("--pick")) {
    const beadId = arg("bead", null);
    let backlog = []; try { backlog = JSON.parse(readFileSync(`${control}/backlog.json`, "utf8")); } catch {}
    const bead = (Array.isArray(backlog) ? backlog : []).find((b) => b && b.id === beadId) || (beadId ? { id: beadId } : {});
    process.stdout.write(JSON.stringify(pickForBead(bead, control)));
  } else {
    let agentJson = null; try { agentJson = JSON.parse(readFileSync(`${control}/agent.json`, "utf8")); } catch {}
    process.stdout.write(JSON.stringify({ allowed: readAllowed(agentJson), cooldownAgy: agyCoolingDown(control), options: WORKER_OPTIONS }));
  }
}
