// Cosmo Canyon — POST-TICK (run by the work agent LAST, after bookkeep.mjs).
// The non-model glue the supervisor used to do in-process: read the outcome (status.json — NEVER
// the agent's prose), tag cc-known-good on a green commit (13.40), handle agy quota strikes/failover
// (13.38), kill any stale agy (13.16), clear .tick.json. Prints the outcome JSON the loop reads.
// Usage: node post-tick.mjs [--agy-failover 2]
import { execSync } from "node:child_process";
import { readFileSync, existsSync, rmSync, readdirSync, statSync } from "node:fs";
import { CONTROL, git, gitQuiet, ggitQuiet, readJson, writeJson, atomicWrite, nowIso, headSha, gameHeadSha } from "./state.mjs";
import { removeActive } from "./assets-core.mjs"; // §15c — active.json removal backstop (bookkeep already removed on terminal)
import { writeKnownGood } from "./spec-compile.mjs"; // §15.5 — snapshot the last-GREEN Ready-spec authority
import { pruneBacklog } from "./prune-backlog.mjs"; // §GC — archive terminal fossil beads out of backlog.json

const args = process.argv.slice(2);
const arg = (n, d) => { const i = args.indexOf(`--${n}`); return i >= 0 && i + 1 < args.length ? args[i + 1] : d; };
const FAILOVER_N = Number(arg("agy-failover", "2"));

const AGY_PID = `${CONTROL}/.agy.pid`;
const AGY_STRIKES = `${CONTROL}/.agy-strikes`;
const AGENT_JSON = `${CONTROL}/agent.json`;
const AGY_COOLDOWN = `${CONTROL}/.agy-cooldown`; // §13.38 quota failover — temporarily drops agy from the pick (agent-core), never touches the operator's allowed set
const GUARD_ALERT = `${CONTROL}/.guard-alert`;

function alive(pid) { if (!pid || pid <= 0) return false; try { process.kill(pid, 0); return true; } catch (e) { return e.code === "EPERM"; } }
function killAgy() {
  if (!existsSync(AGY_PID)) return;
  const apid = Number(readFileSync(AGY_PID, "utf8").trim());
  if (alive(apid)) { try { execSync(`taskkill /PID ${apid} /T /F`); } catch {} }
  try { rmSync(AGY_PID, { force: true }); } catch {}
}

const st = readJson(`${CONTROL}/status.json`, {});
const stage = st.stage || "unknown";
killAgy(); // never leave an agy alive across ticks (§13.16)

let agyStrikes = existsSync(AGY_STRIKES) ? Number(readFileSync(AGY_STRIKES, "utf8").trim()) || 0 : 0;
let failedOver = false;
if (stage === "agy-noop" && agyStrikes >= FAILOVER_N) {
  // repeated zero-diff agy passes = quota/auth wall → COOLDOWN agy (agent-core drops it from the pick) + alert
  // (§13.38). The operator's allowed set is untouched; re-saving it (or the TTL) re-enables agy.
  writeJson(AGY_COOLDOWN, { at: Date.now(), strikes: agyStrikes, note: `auto-failover from agy after ${agyStrikes} zero-diff passes` });
  atomicWrite(GUARD_ALERT, JSON.stringify({ kind: "agy-quota-failover", strikes: agyStrikes, at: nowIso() }));
  try { atomicWrite(AGY_STRIKES, "0"); } catch {}
  agyStrikes = 0; failedOver = true;
}

const committed = stage === "committed";
if (committed) {
  // §SPLIT — the rollback target is the GAME repo (that's what a bad land dirties), so cc-known-good primarily tags
  // the GAME's last green land. Also tag C:/Vibes cc-known-good (control-history marker + the dashboard banner reads it).
  ggitQuiet("tag -f cc-known-good HEAD"); // §13.40 game repo — the rollback anchor
  gitQuiet("tag -f cc-known-good HEAD");  // C:/Vibes control — banner/history marker
  try { writeKnownGood(); } catch {}     // §15.5 — persist the Ready-spec authority set the audit drifts against
}

// §15c — remove this tick's active.json row (backstop; bookkeep removed it on the terminal outcome). Read the
// runToken from .tick.json BEFORE deleting it.
const tick = readJson(`${CONTROL}/.tick.json`, {});
try { removeActive({ runToken: tick.runToken || null, beadId: tick.beadId || null }); } catch {}
rmSync(`${CONTROL}/.tick.json`, { force: true });

// §GC — archive terminal fossil beads (done/superseded/dead) out of backlog.json so it never fills with
// unremovable clutter. Fire-latch-safe (keeps live abandoned/blocked latches). Never break the tick.
try { pruneBacklog({ apply: true }); } catch {}

// §AUDIT-2026-07-04 — bounded runtime housekeeping (gitignored dirs/markers ONLY — never the durable ledgers:
// completions/feel-review/rejected stay untouched; feel-review rows back the implemented() provenance predicate).
// snapshots: keep 7 days AND the newest 50; logs: drop files >30 days old; .usage-* dailies: keep 14.
try {
  const now = Date.now(), DAY = 24 * 3600 * 1000;
  const sweep = (dir, keepMs, keepNewest = 0) => {
    let ents = [];
    try { ents = readdirSync(dir).map((n) => { const p = `${dir}/${n}`; let m = 0; try { m = statSync(p).mtimeMs; } catch {} return { p, m }; }); } catch { return; }
    ents.sort((a, b) => b.m - a.m);
    ents.forEach(({ p, m }, i) => { if (now - m > keepMs && (!keepNewest || i >= keepNewest)) { try { rmSync(p, { force: true, recursive: false }); } catch {} } });
  };
  sweep(`${CONTROL}/../snapshots`, 7 * DAY, 50);
  sweep(`${CONTROL}/../logs`, 30 * DAY);
  for (const n of readdirSync(CONTROL).filter((x) => /^\.usage-\d{8}\.json$/.test(x)).sort().reverse().slice(14)) {
    try { rmSync(`${CONTROL}/${n}`, { force: true }); } catch {}
  }
} catch {}

process.stdout.write(JSON.stringify({ outcome: stage, committed, headSha: headSha(), gameHeadSha: gameHeadSha(), agyStrikes, failedOver, sha: st.sha || null, reason: st.reason || null }));
