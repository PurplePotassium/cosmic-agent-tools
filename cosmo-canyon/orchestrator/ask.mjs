// Cosmo Canyon — ASK (worker helper for the §15c unsure/Questions gate). Run by a work agent when it CANNOT
// proceed on an ASSET bead and needs the operator to clarify: it records the clarifying Question(s) to
// control/.unsure.json, then the agent runs `node orchestrator/bookkeep.mjs --result unsure` — the DETERMINISTIC
// authority that reverts partials, appends the Questions to the asset (parkUnsure), and parks the bead (NO
// attempts++). This helper NEVER touches the asset meta or git itself (bookkeep owns those); it only records the
// intent so the deterministic park is single-source.
//
// Usage: node ask.mjs --bead <beadId> --text "question 1" [--text "question 2" ...]
import { CONTROL, readJson, writeJson } from "./state.mjs";

const args = process.argv.slice(2);
function arg(n, d = null) { const i = args.indexOf(`--${n}`); return i >= 0 && i + 1 < args.length ? args[i + 1] : d; }
const beadId = arg("bead", null);
if (!beadId) { console.error("ask: --bead <id> required"); process.exit(2); }

// collect ALL --text occurrences (repeatable)
const questions = [];
for (let i = 0; i < args.length; i++) if (args[i] === "--text" && args[i + 1]) { const t = String(args[i + 1]).trim(); if (t) questions.push({ text: t, by: "agent" }); }

const backlog = readJson(`${CONTROL}/backlog.json`, []);
const bead = Array.isArray(backlog) ? backlog.find((b) => b && b.id === beadId) : null;
if (!bead) { console.error(`ask: bead ${beadId} not in backlog`); process.exit(2); }
if (!bead.assetId) { console.error(`ask: bead ${beadId} has no assetId — unsure/Questions apply to ASSET beads only`); process.exit(2); }

writeJson(`${CONTROL}/.unsure.json`, { beadId, assetId: bead.assetId, questions, at: new Date().toISOString() });
process.stdout.write(JSON.stringify({ ok: true, beadId, assetId: bead.assetId, questions: questions.length, next: "node orchestrator/bookkeep.mjs --result unsure" }));
