// Cosmo Canyon — SENSE: print the SNAPSHOT JSON the Workflow loop branches on.
// Read-only. An agent runs `node orchestrator/sense.mjs` and returns the JSON (SNAPSHOT schema).
// Usage: node sense.mjs [--cap 200] [--audit-hours 6]
import { computeSnapshot } from "./state.mjs";
import { computeTrigger } from "./spec-core.mjs";

const args = process.argv.slice(2);
const arg = (n, d) => { const i = args.indexOf(`--${n}`); return i >= 0 && i + 1 < args.length ? args[i + 1] : d; };
const cap = Number(arg("cap", "200"));
const auditHours = Number(arg("audit-hours", "6"));

// §15b/15.22: emit the RAW pre-gate trigger INTO the SNAPSHOT. The Workflow host cannot import a .mjs
// at runtime, so its decideTrigger reads snap.trigger as a passthrough; it still applies its own
// NO_PLANNER/max-replan gates on top (computeTrigger is pure — snapshot only, no host state).
const snap = computeSnapshot({ cap, auditHours });
snap.trigger = computeTrigger(snap);
process.stdout.write(JSON.stringify(snap));
