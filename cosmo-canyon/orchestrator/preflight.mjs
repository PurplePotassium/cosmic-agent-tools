// Cosmo Canyon — PREFLIGHT (run once at Workflow boot, by an agent).
// Branch assert (13.24) + cross-system mutex (13.23) + reconcile a killed prior tick (13.28) +
// kill orphan agy (13.16). Prints {ok,reason} (exit 0 always, so the agent captures the verdict).
// Mirrors supervisor.mjs assertBranch/assertMutex/reconcile — the in-app path's boot guard.
import { execSync } from "node:child_process";
import { readFileSync, existsSync, rmSync } from "node:fs";
import { REPO, CONTROL, BRANCH, git, gitQuiet, ggitQuiet } from "./state.mjs";
import { readConfig, isSerial } from "./config.mjs"; // §15i 15.26 — mode-conditional reconcile (serial default)
import { reconcileParallel } from "./reconcile.mjs"; // §15i 15.26 — dormant N-agent branch (toggle OFF today)
import { reconcileActive } from "./assets-core.mjs"; // §15c — GC a killed tick's active.json row at boot

const TICKJSON = `${CONTROL}/.tick.json`;
const AGY_PID = `${CONTROL}/.agy.pid`;
// rival game-dev LOOPs only (ralph worker/lane, refinery, a loop PROMPT.md, opus planner-prompt) —
// NOT a vite/npm dev server that merely logs to a path containing the name (false-positive, fixed).
const MUTEX_RE = /ralph\.ps1|refinery\.ps1|planner-prompt|[\\/]PROMPT\.md/i;
const SELF_RE = /cosmo-canyon|agy-pass\.ps1/i; // our own runner/agy must not trip the mutex

const readJson = (p, d) => { try { return JSON.parse(readFileSync(p, "utf8")); } catch { return d; } };
function alive(pid) { if (!pid || pid <= 0) return false; try { process.kill(pid, 0); return true; } catch (e) { return e.code === "EPERM"; } }
function done(ok, reason) { process.stdout.write(JSON.stringify({ ok, reason })); process.exit(0); }
// §SPLIT — game-repo restore: reset the GAME repo to `ref` + clean (NEVER -x). Whole-repo (the game is its own repo
// now); untracked-IGNORED node_modules/dist/derived survive. A concurrent GUI upload under control/assets lives in
// C:/Vibes (a different repo) and is untouched. Mirrors bookkeep.fullRevert / supervisor.resetGameTo.
function resetGameTo(ref) { ggitQuiet(`reset --hard ${ref}`); ggitQuiet("clean -fd"); }

function killAgy() {
  if (!existsSync(AGY_PID)) return;
  const apid = Number(readFileSync(AGY_PID, "utf8").trim());
  if (alive(apid)) { try { execSync(`taskkill /PID ${apid} /T /F`); } catch {} }
  try { rmSync(AGY_PID, { force: true }); } catch {}
}

// 1. branch
const b = gitQuiet("rev-parse --abbrev-ref HEAD").trim();
if (b !== BRANCH) done(false, `branch is '${b}', expected '${BRANCH}' (13.24) — checkout ${BRANCH} first`);

// 2. cross-system mutex — refuse if a rival FC/Workshop/fleet LOOP is alive
let out = "";
try {
  out = execSync('powershell.exe -NoProfile -Command "Get-CimInstance Win32_Process | Select-Object -ExpandProperty CommandLine"', { encoding: "utf8", maxBuffer: 16 * 1024 * 1024 });
} catch {}
const hits = out.split("\n").map((l) => l.trim()).filter((l) => l && MUTEX_RE.test(l) && !SELF_RE.test(l));
if (hits.length) done(false, `rival loop alive (13.23): ${hits[0].slice(0, 120)} — stop FC/Workshop/fleet first`);

// 3. reconcile a killed/crashed prior tick from disk (13.28). §15i 15.26 — MODE-CONDITIONAL: serial (default)
//    keeps the singleton `.tick.json` reset below byte-for-byte; parallel (N>1) has no singleton anchor →
//    reconcileParallel GCs dead claims + their worktrees, then skip the singleton reset.
if (!isSerial(readConfig())) {
  reconcileParallel();
} else if (existsSync(TICKJSON)) {
  const t = readJson(TICKJSON, null);
  if (t && alive(t.pid)) done(false, `a tick (pid ${t.pid}) is already in flight — single-flight (13.5/13.28)`);
  if (t) resetGameTo(t.gameBaseSha || "HEAD"); // §SPLIT — restore the GAME repo to the killed tick's game base (or discard leftovers)
  rmSync(TICKJSON, { force: true });
}
killAgy(); // orphan agy from a killed tick (own console, not in any child tree)
try { reconcileActive(); } catch {} // §15c GC the killed tick's active.json row at boot

// 4. §SPLIT — defensive clean GAME tree before looping (the game is its own repo; a control/assets upload lives in
//    C:/Vibes and is untouched). An uncommitted control tree is fine/expected, so we only scrub the game here.
const gdirty = ggitQuiet("status --porcelain").trim();
if (gdirty) {
  // cc-safety — a dirty game tree at loop boot is almost always LEFTOVER manual WIP from a prior session
  // (uncommitted). STASH it (recoverable: `git -C cosmo-canyon/game stash list` / `stash apply`) BEFORE the
  // reset --hard, so hand-edits are never SILENTLY destroyed — this exact boot-wipe cost a full manual game
  // rebuild on 2026-07-04. If the stash fails, resetGameTo still runs (no regression vs the old behavior).
  ggitQuiet('stash push -u -m "cc-safety: dirty game tree at loop boot — recover via git -C cosmo-canyon/game stash list"');
  resetGameTo("HEAD");
}

done(true, `branch ${b}, no rival loop, tree clean`);
