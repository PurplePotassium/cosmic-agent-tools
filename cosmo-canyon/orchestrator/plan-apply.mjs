// Cosmo Canyon — deterministic planner-apply (§13.31/§13.33). The opus planner only PRODUCES
// control/.plan-result.json; THIS script validates + applies it atomically. No model judgment
// writes the backlog. Honors the WIP shared-filter (reject beads mapping to WIP sections), dedups
// adds, writes the trigger-clearing markers (.authority-consumed/.lastaudit) LAST, and latches a mode that
// produced nothing (so opus backs off until its input changes).
import { execSync, spawnSync } from "node:child_process";
import { readFileSync, writeFileSync, existsSync, renameSync, appendFileSync } from "node:fs";
import { acquire, release } from "./lock.mjs";

const REPO = "C:/Vibes";
const CC = "C:/Vibes/cosmo-canyon";
const CONTROL = `${CC}/control`;
const LOCKS = `${CONTROL}/locks`;
const LOGS = `${CC}/logs`;

const git = (c) => execSync(`git -C "${REPO}" ${c}`, { encoding: "utf8", maxBuffer: 64 * 1024 * 1024 });
const gitQ = (c) => { try { return git(c); } catch (e) { return e.stdout || ""; } };
const gitArgs = (...a) => spawnSync("git", ["-C", REPO, ...a], { encoding: "utf8", maxBuffer: 64 * 1024 * 1024 }); // argv shell:false
// §15i 15.44: strip shell metachars + newlines from interpolated untrusted text (the planner note)
const sanitize = (s) => String(s || "").replace(/[%&|<>`$\r\n]/g, " ").replace(/\s+/g, " ").trim();
const readJson = (p, d) => { try { return JSON.parse(readFileSync(p, "utf8")); } catch { return d; } };
const atomicWrite = (p, s) => { const t = `${p}.tmp`; writeFileSync(t, s); renameSync(t, p); };
const writeJson = (p, o) => atomicWrite(p, JSON.stringify(o, null, 2) + "\n");
const nowIso = () => new Date().toISOString();
const norm = (s) => String(s || "").toLowerCase().replace(/[^a-z0-9]+/g, " ").trim();
function plog(line) { try { appendFileSync(`${LOGS}/planner-log.md`, `- ${nowIso()} ${line}\n`); } catch {} }

const input = readJson(`${CONTROL}/.plan-input.json`, null);
const result = readJson(`${CONTROL}/.plan-result.json`, null);
if (!input || !result) { console.log(JSON.stringify({ applied: 0, note: "missing .plan-input/.plan-result" })); process.exit(0); }
const mode = input.mode;
const wipKeywords = (input.wipKeywords || []).map(norm).filter(Boolean);

// ── validate (schema-lite; invalid → no-op, never corrupt the backlog) ──
const backlogOps = Array.isArray(result.backlogOps) ? result.backlogOps : [];
const suggestionOps = Array.isArray(result.suggestionOps) ? result.suggestionOps : [];

const backlog = readJson(`${CONTROL}/backlog.json`, []);
const completions = readJson(`${CONTROL}/completions.json`, []);
const rejected = readJson(`${CONTROL}/rejected.json`, []);
const suggestions = readJson(`${CONTROL}/suggestions.json`, []);

// dedup corpus: titles already in backlog / done / rejected
const seenTitles = new Set([
  ...backlog.map((b) => norm(b.title)),
  ...completions.map((c) => norm(c.title)),
  ...rejected.map((r) => norm(r.title)),
]);
const seenSuggestions = new Set([...suggestions.map((s) => norm(s.title)), ...rejected.map((r) => norm(r.title))]);

// §prevent-dup-parks — exact-title dedup (seenTitles) misses the real churn: the planner RE-MINTS work that is
// already DONE or parked `needs-operator` under DIFFERENT wording, the worker re-discovers "already implemented",
// and it parks as a blocked/needs-operator bead again. Backstop: build the set of already-HANDLED beads and drop
// a new impl bead that strongly overlaps one (near-identical title, or high title-token overlap + a shared target
// file). Deterministic; every drop is logged (the bead is simply not re-added, nothing is deleted) so it is
// auditable/recoverable. Conservative thresholds keep genuinely-new work safe.
const DUP_STOP = new Set("a an the of to and or for with in on at add new set from into is are be per each all its use adds implement create make".split(" "));
function titleTokens(t) { return new Set(norm(t).split(" ").filter((w) => w.length >= 3 && !DUP_STOP.has(w))); }
// §AUDIT-2026-07-04 — dup memory must survive the §GC prune + landings: handled work lives in THREE places —
// live backlog (blocked/needsOperator/undrained done), backlog-archive.json (pruned fossils; EXCLUDE superseded —
// their successor is the real dup anchor), and completions.json (landed work; rows carry no files[], so only the
// ≥0.8 title-overlap arm can fire for them). Backlog-only scanning forgot exactly the beads most re-minted.
const archivedBeads = readJson(`${CONTROL}/backlog-archive.json`, []);
const handledSrc = [
  ...backlog.filter((b) => b && (b.needsOperator === true || b.status === "done" || b.status === "blocked")),
  ...archivedBeads.filter((b) => b && (b.needsOperator === true || ["done", "blocked", "abandoned", "parked"].includes(b.status))),
  ...completions.filter((c) => c && c.title),
];
const handledBeads = handledSrc
  .map((b) => ({ id: b.id, title: b.title, tokens: titleTokens(b.title), files: new Set((Array.isArray(b.files) ? b.files : []).map((f) => String(f).toLowerCase())) }));
function findHandledDup(b) {
  const nt = titleTokens(b.title);
  const nf = new Set((Array.isArray(b.files) ? b.files : []).map((f) => String(f).toLowerCase()));
  return handledBeads.find((h) => {
    let inter = 0; for (const w of nt) if (h.tokens.has(w)) inter++;
    const ov = (nt.size && h.tokens.size) ? inter / Math.min(nt.size, h.tokens.size) : 0;
    if (ov >= 0.8) return true;                                    // near-identical wording → same work
    const sharedFile = [...nf].some((f) => h.files.has(f));
    return sharedFile && inter >= 3;                               // ≥3 distinctive shared tokens + a shared target file
  });
}
let dupHandledDropped = 0;

// next bead id
function nextId() {
  let max = 0;
  for (const b of [...backlog, ...completions, ...suggestions, ...rejected]) { // §AUDIT-2026-07-04 — suggestion/rejected ids share the cc-#### namespace; scan them too so genId can't re-mint a colliding id
    const m = String(b.id || "").match(/cc-(\d+)/);
    if (m) max = Math.max(max, +m[1]);
  }
  let n = max;
  return () => { n += 1; return `cc-${String(n).padStart(4, "0")}`; };
}
const genId = nextId();

function mapsToWip(b) {
  const hay = norm(`${b.title} ${b.detail}`);
  return wipKeywords.some((k) => k.length >= 4 && hay.includes(k)); // word-ish forbidden keyword
}

// NO code-review suggestions (2026-07-02 user rule — see AGENTS.md "NO code-review suggestions"). The operator
// does NOT review code, so a suggestion asking a human to review / remove / audit already-shipped code is pure
// noise. Drop it here defensively — even if a planner (or a future reconcile path) ignores the prompt and emits
// one. Matches code-review / review-shipped-code / remove-shipped-code intent; genuine NEW-design suggestions
// ("author a shop spec", "new mechanic", "remove button") do NOT mention reviewing/removing CODE, so they pass.
const CODE_REVIEW_RE = /\bcode\s*review\b|\b(review|re-?review|remove|delete|audit|inspect)\b[^\n]{0,24}\bcode\b|\breview\b[^\n]{0,24}\bshipped\b/i;
function isCodeReviewSuggestion(s) {
  return CODE_REVIEW_RE.test(`${s.title || ""} ${s.body || s.detail || ""}`);
}

// §AUDIT-2026-07-03 — suggestion ids must be unique across LIVE suggestions + rejected too (two live "cc-0001"
// suggestions were observed; the GUI accept/reject keys on id). The bead-path H2 check + genId() only dedup vs
// backlog/completions, so a planner-supplied id (or a minted one) could still collide with an old suggestion.
const usedSugIds = new Set([...suggestions, ...rejected].map((x) => x && x.id).filter(Boolean));
function uniqueSugId(want) {
  let id = typeof want === "string" && /^[A-Za-z0-9_-]{1,64}$/.test(want) ? want : genId();
  while (usedSugIds.has(id)) id = genId(); // genId is monotonic → terminates
  usedSugIds.add(id);
  return id;
}

let added = 0, rejectedWip = 0, deduped = 0, other = 0, sugAdded = 0, codeReviewDropped = 0;

for (const op of backlogOps) {
  const kind = op.op || "add";
  if (kind === "add") {
    const b = op.bead || op; // tolerate flat or nested
    if (!b.title) { continue; }
    if (mapsToWip(b)) { rejectedWip++; plog(`[${mode}] REJECT WIP-section bead: "${b.title}"`); continue; }
    if (seenTitles.has(norm(b.title))) { deduped++; continue; }
    // §prevent-dup-parks — drop a re-mint of already-handled (done/parked-needs-operator) work under different
    // wording (impl beads only; design routes to suggestions with its own dedup). This is the deterministic
    // backstop for the "already-built → worker parks blocked" churn.
    if (b.kind !== "design") {
      const dup = findHandledDup(b);
      if (dup) { dupHandledDropped++; deduped++; plog(`[${mode}] DROP re-mint of already-handled bead ${dup.id} ("${dup.title}") ← new "${b.title}" (dup-of-handled; feature already done/parked)`); continue; }
    }
    const bead = {
      // §H2 — the planner id is LLM-supplied and flows into the dashboard's onclick("…") handlers; enforce a
      // safe slug charset so a prompt-injected id (e.g. `');fetch(...)//`) can never reach the DOM. Fall back to
      // a minted id otherwise. (ccEsc now also escapes quotes as the render-side backstop.)
      id: (typeof b.id === "string" && /^[A-Za-z0-9_-]{1,64}$/.test(b.id)) ? b.id : genId(),
      title: String(b.title).slice(0, 120),
      detail: String(b.detail || "").slice(0, 600),
      files: Array.isArray(b.files) ? b.files.slice(0, 8) : [],
      kind: b.kind === "design" ? "design" : "impl",
      tier: ["light", "heavy", "structural"].includes(b.tier) ? b.tier : "light",
      engine: b.engine || undefined,
      acceptance: String(b.acceptance || "").slice(0, 400),
      acceptanceCmd: b.acceptanceCmd || undefined,
      // §AUDIT-2026-07-02 HIGH-4 — a planner-authored acceptanceCmd (custom grader) MUST land DISABLED until the
      // operator confirms it (§15.15/15.17): bookkeep's confirm-gate + mutation-check + ACCEPT-PASS token all key on
      // graderNeedsConfirm. assets-core sets it for asset-spec beads; the planner path never did → a planner bead with
      // acceptanceCmd ran the grader UNGATED (self-certifying green). Force the confirm latch whenever a cmd is present.
      graderNeedsConfirm: b.acceptanceCmd ? true : undefined,
      renderOnly: b.renderOnly === true, // §15i 15.13 — render/design beads verify by feel-review (no node grader), not the node gate
      source: "planner",
      status: "ready",
      blocked_reason: "",
      attempts: 0,
      created: nowIso(),
      updated: nowIso(),
    };
    // a design-kind bead routes to suggestions, never the backlog (FC parity)
    if (bead.kind === "design") {
      if (isCodeReviewSuggestion(bead)) {
        codeReviewDropped++; plog(`[${mode}] DROP code-review suggestion (operator does not review code): "${bead.title}"`);
      } else if (!seenSuggestions.has(norm(bead.title))) {
        suggestions.push({ id: uniqueSugId(bead.id), title: bead.title, body: bead.detail, kind: "design", created: nowIso(), status: "open" });
        seenSuggestions.add(norm(bead.title)); sugAdded++;
      }
    } else {
      backlog.push(bead); seenTitles.add(norm(bead.title)); added++;
    }
  } else if (kind === "cancel" || kind === "setStatus") {
    const b = backlog.find((x) => x.id === op.id);
    // §GC3 — a needsOperator bead is an operator-gated TERMINAL human gate; the planner must NEVER reopen/rescope it
    // (that was the first-real-run block↔unblock churn). Ignore any op targeting it — only the operator resolves it.
    if (b && b.needsOperator) { plog(`[${mode}] skip ${kind} ${op.id} (needsOperator — operator-gated, not planner-reopenable)`); continue; }
    if (b) { b.status = op.status || (kind === "cancel" ? "abandoned" : b.status); b.updated = nowIso(); other++; plog(`[${mode}] ${kind} ${op.id} -> ${b.status}`); }
  } else if (kind === "update" || kind === "rescope") {
    const b = backlog.find((x) => x.id === op.id);
    if (b && b.needsOperator) { plog(`[${mode}] skip ${kind} ${op.id} (needsOperator — operator-gated, not planner-reopenable)`); continue; }
    if (b) { Object.assign(b, { title: op.title || b.title, detail: op.detail || b.detail, blocked_reason: op.blocked_reason ?? b.blocked_reason, status: op.status || b.status, updated: nowIso() }); other++; plog(`[${mode}] update ${op.id}`); }
  }
}

for (const op of suggestionOps) {
  const s = op.suggestion || op;
  if (!s.title || seenSuggestions.has(norm(s.title))) { continue; }
  if (isCodeReviewSuggestion(s)) { codeReviewDropped++; plog(`[${mode}] DROP code-review suggestion (operator does not review code): "${s.title}"`); continue; }
  if (mapsToWip(s)) { rejectedWip++; continue; }
  suggestions.push({ id: uniqueSugId(null), title: String(s.title).slice(0, 120), body: String(s.body || s.detail || "").slice(0, 600), kind: "design", created: nowIso(), status: "open" });
  seenSuggestions.add(norm(s.title)); sugAdded++;
}

// ── persist (locked, atomic). Beads/suggestions FIRST, trigger markers LAST (§13.44). ──
const lk = acquire(LOCKS, "backlog");
try {
  writeJson(`${CONTROL}/backlog.json`, backlog);
  writeJson(`${CONTROL}/suggestions.json`, suggestions);
} finally { release(lk); }

const netChange = added + sugAdded + other;

// trigger-clearing markers go LAST, only after beads are durably written. §AUDIT-2026-07-04: tmp+rename
// (atomicWrite) so a crash mid-write can never leave a truncated marker a later sense misparses.
if (mode === "diff" && input.authoritySha) { atomicWrite(`${CONTROL}/.authority-consumed`, input.authoritySha); }
if (mode === "audit") { atomicWrite(`${CONTROL}/.lastaudit`, String(Date.now())); }

// opus backoff: a mode that changed nothing latches on its input until that input changes (§13.31).
// §AUDIT-2026-07-04 — RMW under the backlog lock: two planner hosts should never run concurrently (mutex),
// but an operator-error overlap could interleave this read-modify-write and silently drop a latch.
{
  const lkL = acquire(LOCKS, "backlog");
  try {
    const latch = readJson(`${CONTROL}/.plan-latch.json`, {});
    if (netChange === 0) {
      latch[mode] = input.latchKey ?? true;
      plog(`[${mode}] produced no change → latched on ${JSON.stringify(input.latchKey)}`);
    } else {
      delete latch[mode];
    }
    writeJson(`${CONTROL}/.plan-latch.json`, latch);
  } finally { release(lkL); }
}

plog(`[${mode}] applied: +${added} beads, +${sugAdded} suggestions, ${other} ops, ${deduped} deduped (${dupHandledDropped} dup-of-handled), ${rejectedWip} WIP-rejected, ${codeReviewDropped} code-review-dropped. note: ${String(result.note || "").slice(0, 160)}`);

// commit the plan (labeled; bookkeep-style — Stop hook then no-ops on the clean tree)
// §15i 15.47: take the SAME git-tree lock as bookkeep.commit() so plan-apply-vs-tick can't race on .git/index.lock.
// NOTE: the ingest committer (D:\Ag\launcher\server.js — today a `for(i<3)` index.lock retry band-aid) and any
// future merge committer MUST also acquire(LOCKS,'git-tree'); DELETE that retry loop when ingest moves to the
// standalone server.mjs and takes this same lock.
let sha = null;
const gk = acquire(LOCKS, "git-tree");
try {
  // §AUDIT-2026-07-02 HIGH-3 — stage ONLY the two durable plan outputs, NOT `-A cosmo-canyon`. The planner runs
  // `claude -p --dangerously-skip-permissions` (prompt-injectable via spec bodies) and has NO tamper/scope guard on
  // this commit path. A blanket `add -A` would commit any stray planner write — `game/accept/*.ts` (a tautological
  // grader), an edit to an `orchestrator/*.mjs` rail — as a legit `ralph planner-*` commit with no gate. Explicit
  // paths mean the planner can only ever land backlog/suggestions here; any game/orchestrator dirt it left is caught
  // by the next worker tick's bookkeep scope guard (or the supervisor's game-tree reconcile), never committed here.
  gitArgs("add", "cosmo-canyon/control/backlog.json", "cosmo-canyon/control/suggestions.json");
  const staged = gitQ("diff --cached --name-only").trim();
  if (staged) {
    // §15i 15.44: message via stdin (git commit -F -), argv shell:false — the planner note never reaches a shell
    // (no injection / %VAR% expansion). sanitize() strips metachars/newlines for tidy logs.
    const msg = sanitize(`ralph planner-${mode}: +${added}b +${sugAdded}s ${other}u (${String(result.note || "").slice(0, 60)})`);
    const r = spawnSync("git", ["-C", REPO, "commit", "-q", "-F", "-"], { input: msg, encoding: "utf8", maxBuffer: 64 * 1024 * 1024 });
    if (r.status !== 0) throw new Error(`git commit failed: ${((r.stderr || "") + (r.stdout || "")).slice(0, 200)}`);
    sha = git("rev-parse HEAD").trim();
  }
} finally { release(gk); }

writeJson(`${CONTROL}/status.json`, { stage: `planned-${mode}`, added, sugAdded, other, deduped, rejectedWip, codeReviewDropped, netChange, sha, updated: nowIso() });
console.log(JSON.stringify({ applied: netChange, added, sugAdded, other, deduped, rejectedWip, codeReviewDropped, mode, sha }));
