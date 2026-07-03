// Cosmo Canyon — agent-core worker-selection unit test. Run: node orchestrator/agent-core.test.mjs
import { pickWorker, normalizeAllowed, readAllowed, ALL_KEYS } from "./agent-core.mjs";
let fail = 0;
const eq = (got, exp, msg) => { const g = JSON.stringify(got), e = JSON.stringify(exp); if (g !== e) { console.log("FAIL", msg, "\n  got", g, "\n  exp", e); fail++; } else console.log("ok  ", msg); };
const k = (b, a, o) => pickWorker(b, a, o).key;
const ALL = ["agy", "sonnet", "opus"];

// task-fit selection, full allowed set
eq(k({ kind: "impl", tier: "light" }, ALL), "agy", "logic light -> agy");
eq(k({ kind: "image", tier: "light" }, ALL), "sonnet", "feel light -> sonnet");
eq(k({ kind: "impl", tier: "structural" }, ALL), "opus", "hard logic -> opus");
eq(k({ kind: "image", tier: "heavy" }, ALL), "opus", "feel+hard -> opus");
// clamp to the checked set (fallback to next-best)
eq(k({ kind: "impl", tier: "light" }, ["sonnet"]), "sonnet", "logic, only sonnet");
eq(pickWorker({ kind: "image", tier: "light" }, ["agy"]).key, null, "feel, only agy -> unsatisfiable");
eq(k({ kind: "impl", tier: "light" }, ["sonnet", "opus"]), "sonnet", "logic prefers sonnet over opus");
eq(k({ kind: "impl", tier: "light" }, ["opus"]), "opus", "logic, only opus");
eq(k({ kind: "image", tier: "light" }, ["agy", "opus"]), "opus", "feel excludes agy -> opus");
eq(k({ kind: "impl", tier: "structural" }, ["agy"]), "agy", "hard logic, only agy -> agy");
// agy cooldown (quota failover) drops agy without touching the checkboxes
eq(k({ kind: "impl", tier: "light" }, ALL, { cooldownAgy: true }), "sonnet", "logic + agy cooldown -> sonnet");
// author engine override, still clamped
eq(k({ kind: "impl", tier: "light", engine: "claude" }, ALL), "sonnet", "explicit claude logic -> sonnet");
// model ids
eq(pickWorker({ kind: "impl", tier: "light" }, ALL).model, "gemini-3.5-flash", "agy model id");
eq(pickWorker({ kind: "impl", tier: "structural" }, ALL).model, "claude-opus-4-8", "opus model id");
// allowed-set normalization (never zero)
eq(normalizeAllowed([]), [...ALL_KEYS], "empty -> all");
eq(normalizeAllowed(["bogus"]), [...ALL_KEYS], "invalid -> all");
eq(normalizeAllowed(["agy", "agy", "sonnet"]), ["agy", "sonnet"], "dedupe");
eq(readAllowed({ engine: "claude", model: "claude-opus-4-8" }), ["opus"], "legacy single-pick -> singleton");
eq(readAllowed({ allowed: ["agy"] }), ["agy"], "allowed passthrough");
eq(readAllowed(null), [...ALL_KEYS], "absent -> all");

console.log(fail ? `\n${fail} FAILED` : "\nALL PASS");
process.exit(fail ? 1 : 0);
