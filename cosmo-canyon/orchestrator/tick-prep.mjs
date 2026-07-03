// Cosmo Canyon — TICK-PREP (run by the work agent FIRST, before any edit).
// Persists the §13.28 base anchor (control/.tick.json {baseSha:HEAD, beadId}) that bookkeep.mjs
// reads to gate/commit/revert, and bumps the daily usage counter (one bump per work tick).
// pid is null: the Workflow itself enforces single-flight (one agent at a time), and reconcile
// treats a null/dead pid as "not in flight". Usage: node tick-prep.mjs --bead <id>
import { CONTROL, headSha, gameHeadSha, writeJson, readJson, bumpUsage } from "./state.mjs";
import { writeActive } from "./assets-core.mjs"; // §15c — the active.json WRITER (this IS the workflow-host dispatch)
import { randomBytes } from "node:crypto";

const args = process.argv.slice(2);
const arg = (n, d) => { const i = args.indexOf(`--${n}`); return i >= 0 && i + 1 < args.length ? args[i + 1] : d; };
const beadId = arg("bead", null);
if (!beadId) { console.error("tick-prep: --bead <id> required"); process.exit(2); }

const baseSha = headSha();          // C:/Vibes anchor — control-plane + .claude/settings.json guard
const gameBaseSha = gameHeadSha();  // §SPLIT — the GAME repo anchor bookkeep gates/reverts/commits the game against
const startEpoch = Date.now();
const runToken = randomBytes(6).toString("hex"); // ties .tick.json ↔ the active row (removed on terminal; reconcileActive GCs a killed tick)
writeJson(`${CONTROL}/.tick.json`, { pid: null, startEpoch, baseSha, gameBaseSha, beadId, runToken });
// §15c — write the in-flight task row at DISPATCH (the work agent's first action). Removed on ANY terminal
// outcome by bookkeep + post-tick; a killed tick is GC'd by reconcileActive (30s grace).
const backlog = readJson(`${CONTROL}/backlog.json`, []);
const bead = Array.isArray(backlog) ? backlog.find((b) => b && b.id === beadId) : null;
writeActive({ runToken, beadId, assetId: (bead && bead.assetId) || null, kind: "work", engine: (bead && bead.engine) || null, tier: (bead && bead.tier) || null, title: (bead && bead.title) || beadId, baseSha, startEpoch, pid: null });
const ticks = bumpUsage();
process.stdout.write(JSON.stringify({ baseSha, gameBaseSha, beadId, runToken, ticksToday: ticks }));
