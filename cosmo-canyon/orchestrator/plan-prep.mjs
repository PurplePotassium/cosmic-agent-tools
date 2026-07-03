// Cosmo Canyon — PLAN-PREP (run by the plan agent FIRST). Writes control/.plan-input.json (the bounded
// input planner.md reads + plan-apply.mjs honors) from the SAME deterministic snapshot the loop used to
// pick the mode. latchKey per mode comes from the SHARED spec-core.latchKeyFor (the same helper
// computeTrigger uses) so plan-apply's no-change latch lines up — single source, audit 15.22. Bumps the
// daily usage counter (one bump per plan tick). Usage: node plan-prep.mjs --mode <mode>
import { CONTROL, computeSnapshot, writeJson, bumpUsage } from "./state.mjs";
import { latchKeyFor } from "./spec-core.mjs";
import { compileSpecs, readKnownGood } from "./spec-compile.mjs"; // §15b — refresh the north-star + the audit drift baseline

const args = process.argv.slice(2);
const arg = (n, d) => { const i = args.indexOf(`--${n}`); return i >= 0 && i + 1 < args.length ? args[i + 1] : d; };
const mode = arg("mode", null);
if (!["diff", "blocked", "topup", "audit"].includes(mode)) { console.error("plan-prep: --mode diff|blocked|topup|audit required"); process.exit(2); }

// §15b — recompile spec-doc.md (+ spec-index.json) from the Ready set FIRST so the planner's north-star + the
// snapshot's wipKeywords (which reads spec-doc.md) reflect the current authority. The hash it emits equals
// snap.authoritySha (both are authorityHashOf over the same index) — single source, no doc/hash disagreement (15.24).
compileSpecs();
const snap = computeSnapshot();
// latchKey for the fired mode (NOT computeTrigger(snap).latchKey — that returns the highest-precedence
// mode, which may differ from the mode plan-prep was invoked with, or be null; latchKeyFor is exact).
const latchKey = latchKeyFor(mode, snap);

const input = { mode, latchKey, readyCount: snap.readyCount, blockedIds: snap.blockedIds, wipKeywords: snap.wipKeywords, authoritySha: snap.authoritySha };
// §15.5 — the audit compares drift against the LAST-GREEN Ready-spec authority, not the live set (a just-toggled
// spec is not proof of drift). Give the planner that baseline as bounded input.
if (mode === "audit") input.authorityKnownGood = readKnownGood();
writeJson(`${CONTROL}/.plan-input.json`, input);
const ticks = bumpUsage();
process.stdout.write(JSON.stringify({ ...input, ticksToday: ticks }));
