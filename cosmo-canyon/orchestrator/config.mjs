// Cosmo Canyon — control/config.json reader (§15c-2 concurrency toggle). PURE reader, NO behavior change.
//
// The single surface that selects serial (default) vs parallel runtime. Both loop hosts + schedule/claim/
// reconcile read it through here so there is ONE normalization of the shape. §15g PHASE 2 ships this reader
// with mode DEFAULTING to "serial"; the toggle is NOT flipped here (that is phase 8). Absent / unparseable /
// invalid config → serial N=1 (today's byte-for-byte path).
//
// config.json is COMMITTED (§15.46 negation `!control/config.json` keeps it committable — verified with
// `git check-ignore`). ROOT is parameterized via env `CC_CONTROL` (same convention as assets.mjs) so a unit
// test points at a scratch control dir without touching the live plane.
import { readFileSync } from "node:fs";

const CC = "C:/Vibes/cosmo-canyon";
function controlRoot() { return process.env.CC_CONTROL || `${CC}/control`; }
function configPath() { return `${controlRoot()}/config.json`; }

// The canonical serial defaults — this object is what an absent/invalid config resolves to, i.e. exactly
// today's single-flight, inline, no-worktree behavior. worktreeRoot is the §15.43 explicit-removal root.
export const SERIAL_DEFAULTS = Object.freeze({
  mode: "serial",
  maxConcurrency: 1,
  isolation: "auto",
  worktreeRoot: "C:/Vibes-cc-wt",
  perAgentTimeoutMin: 45,
  heavyCostReserve: 3,
  heartbeatSec: 30,
});

// Tier → cost weight for the tier-weighted cap (§15c-2: light 1 / heavy 3 / structural 5). Frozen so a
// caller can't mutate the shared table; schedule.mjs imports it.
export const TIER_WEIGHT = Object.freeze({ light: 1, heavy: 3, structural: 5 });

// §H7 — hard ceiling on parallelism. clampInt only floors the low end, so a VALID large int would sail
// through and open that many worktrees+workers. This stays the ONE high-side cap — a real bound so a typo /
// hostile config can't exceed it. Operator-raised to 1000 (2026-07-03): the operator wants to fan out very
// wide; the disjoint-files partition + daily tick cap still bound ACTUAL dispatch to the available work.
export const MAX_CONCURRENCY = 1000;
// Parallel-mode FLOOR: N=1 is disallowed in parallel (that's what serial mode is for) — a parallel run is clamped
// to [MIN_PARALLEL, MAX_CONCURRENCY]. Serial mode is still always N=1 (the byte-for-byte fallback path).
export const MIN_PARALLEL = 2;

// Daily tick budget (supervisor DAILY_CAP) — operator-editable in the dashboard Settings. Top-level config
// field (NOT under concurrency). Default 200; hard-ceilinged so a bad/hostile edit can't order an unbounded run.
export const DEFAULT_TICK_BUDGET = 200;
export const MAX_TICK_BUDGET = 100000;

function clampInt(v, min, def) {
  const n = Number(v);
  return Number.isFinite(n) && Math.floor(n) === n && n >= min ? n : def;
}

// Read + NORMALIZE the concurrency block. Any malformed field falls back to its serial default individually
// (a partially-broken config can never widen concurrency past what it validly declares). Returns a frozen,
// fully-populated `{concurrency:{...}}` — callers never see undefined fields.
export function readConfig() {
  let raw = null;
  try { raw = JSON.parse(readFileSync(configPath(), "utf8")); } catch { raw = null; }
  const c = (raw && typeof raw === "object" && raw.concurrency && typeof raw.concurrency === "object") ? raw.concurrency : {};

  const mode = c.mode === "parallel" ? "parallel" : "serial"; // unknown/absent → serial (fail-safe)
  // serial is ALWAYS N=1 no matter what the file says (defense: a serial config can't request concurrency).
  // parallel is clamped to [MIN_PARALLEL, MAX_CONCURRENCY] — N=1 is disallowed in parallel (use serial for that);
  // an invalid/low/absent value → MAX_CONCURRENCY (the default parallel width), floored to MIN_PARALLEL, capped at MAX.
  const maxConcurrency = mode === "serial"
    ? 1
    : Math.min(MAX_CONCURRENCY, Math.max(MIN_PARALLEL, clampInt(c.maxConcurrency, 1, MAX_CONCURRENCY)));
  // autoConcurrency (operator toggle, 2026-07-03): "ignore the number — decide for yourself how many agents
  // to run to finish fastest". Only meaningful in parallel (serial is always N=1). Consumers (schedule.mjs)
  // treat it as "use the MAX_CONCURRENCY ceiling" → fan out as wide as the available disjoint work allows.
  const autoConcurrency = mode === "parallel" && c.autoConcurrency === true;
  const isolation = c.isolation === "worktree" || c.isolation === "inline" || c.isolation === "auto" ? c.isolation : "auto";
  // §AUDIT-2026-07-02 CRIT-1 — worktreeRoot is PINNED to the constant, NOT read from the file. config.json is in
  // bookkeep's ALLOW_CONTROL (a worker can land an edit to it through the normal gate), and this value flows into
  // wtRemove()'s explicit-path root (merge.cleanupClaim → worktree.remove → rmSync recursive:force). A worker-landed
  // `worktreeRoot:"C:/Users/alway"` would relocate worktree creation/removal to an attacker-chosen root and, combined
  // with a poisoned marker.worktree, enable removal of arbitrary `<root>/<child>` dirs. Pinning removes the whole
  // class: the explicit-removal root can never move. (Tests still override via worktree.mjs opts.root.)
  const worktreeRoot = SERIAL_DEFAULTS.worktreeRoot;
  const perAgentTimeoutMin = clampInt(c.perAgentTimeoutMin, 1, SERIAL_DEFAULTS.perAgentTimeoutMin);
  const heavyCostReserve = clampInt(c.heavyCostReserve, 1, SERIAL_DEFAULTS.heavyCostReserve);
  const heartbeatSec = clampInt(c.heartbeatSec, 1, SERIAL_DEFAULTS.heartbeatSec);
  // top-level (sibling of concurrency): the operator-editable daily tick budget. Clamp to [1, MAX_TICK_BUDGET];
  // any invalid/absent value → DEFAULT_TICK_BUDGET (200) so the loop always has a sane, bounded cap.
  const tbRaw = clampInt(raw && raw.tickBudget, 1, DEFAULT_TICK_BUDGET);
  const tickBudget = Math.min(tbRaw, MAX_TICK_BUDGET);

  // §AUDIT-2026-07-04 — optional model overrides ({models:{work,planner}}), so a model bump is a config edit,
  // not a supervisor.mjs code change (the hardcoded defaults were going stale). Strict charset (model-id shaped);
  // anything else → null → the supervisor's built-in default. Never worker-widened: config.json edits still land
  // only through ALLOW_CONTROL-gated commits, and a bogus id just fails the spawn loudly.
  const mRaw = (raw && typeof raw === "object" && raw.models && typeof raw.models === "object") ? raw.models : {};
  const modelStr = (v) => (typeof v === "string" && /^[A-Za-z0-9][A-Za-z0-9._:-]{2,63}$/.test(v) ? v : null);
  const models = Object.freeze({ work: modelStr(mRaw.work), planner: modelStr(mRaw.planner) });

  return Object.freeze({
    concurrency: Object.freeze({ mode, maxConcurrency, autoConcurrency, isolation, worktreeRoot, perAgentTimeoutMin, heavyCostReserve, heartbeatSec }),
    tickBudget,
    models,
  });
}

// Convenience predicate — the mode-conditional reconcile branch (15.26) + schedule slot math key on this.
export function isSerial(cfg = readConfig()) { return cfg.concurrency.mode === "serial"; }

// tiny CLI: `node config.mjs` prints the normalized config (ops introspection)
import { fileURLToPath } from "node:url";
import { resolve } from "node:path";
if (process.argv[1] && fileURLToPath(import.meta.url) === resolve(process.argv[1])) {
  process.stdout.write(JSON.stringify(readConfig(), null, 2) + "\n");
}
