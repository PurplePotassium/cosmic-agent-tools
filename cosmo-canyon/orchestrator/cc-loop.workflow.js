export const meta = {
  name: 'cosmo-canyon-loop',
  description: 'Cosmo Canyon game-dev loop hosted IN the desktop app (Workflow tool) — no external supervisor, no per-tick claude -p. Each tick is a spawned agent; deterministic Node scripts own gate/commit.',
  whenToUse: 'Drive the Cosmo Canyon autonomous game-dev loop from this chat session.',
  phases: [
    { title: 'Boot', detail: 'preflight: branch assert + cross-system mutex + reconcile a killed tick' },
    { title: 'Sense', detail: 'read control+git → SNAPSHOT (haiku)' },
    { title: 'Plan', detail: 'opus planner when a trigger fires (diff>blocked>topup>audit)' },
    { title: 'Work', detail: 'one increment per ready bead (sonnet); deterministic bookkeep gates+commits' },
  ],
}

// ── config (args override; smoke: {ticks:1, noPlanner:true}) ──
// Tolerate args arriving as an object OR a JSON string (the harness may stringify) — a bare
// `args || {}` left a string truthy, so A.ticks was undefined and the loop silently ran defaults.
let A = args || {}
if (typeof A === 'string') { try { A = JSON.parse(A) } catch { A = {} } }
if (typeof A !== 'object' || A === null) A = {}
const MAX_TICKS   = Number(A.ticks ?? 50)
const CAP         = Number(A.cap ?? 200)
const BREAKER_N   = Number(A.breaker ?? 5)
const MAX_REPLAN  = Number(A.maxReplan ?? 2)
const AGY_FAILOVER= Number(A.agyFailover ?? 2)
const AUDIT_HOURS = Number(A.auditHours ?? 6)
const NO_PLANNER  = !!A.noPlanner
const ORC = 'C:/Vibes/cosmo-canyon/orchestrator'
const CTRL = 'C:/Vibes/cosmo-canyon/control'

// ── schemas (agents return EXACTLY what the deterministic scripts print) ──
const PREFLIGHT = { type: 'object', required: ['ok'], properties: { ok: { type: 'boolean' }, reason: { type: 'string' } } }
const SNAPSHOT = {
  type: 'object', required: ['readyCount', 'authorityChanged', 'paused', 'capReached'],
  properties: {
    headSha: { type: 'string' }, readyCount: { type: 'number' },
    headReadyBead: { type: ['object', 'null'] },
    blockedIds: { type: 'array', items: { type: 'string' } },
    authorityChanged: { type: 'boolean' }, authoritySha: { type: 'string' }, auditDue: { type: 'boolean' },
    wipKeywords: { type: 'array', items: { type: 'string' } },
    paused: { type: 'boolean' }, usageToday: { type: 'number' }, capReached: { type: 'boolean' },
    latch: { type: 'object' },
    trigger: { type: ['object', 'null'] }, // 15.22 — raw pre-gate {mode,latchKey}|null precomputed by sense.mjs
    openWork: { type: 'number' },          // §15c/15.20 — unimplemented ready assets + non-terminal beads (breaker signal)
    completion: { type: ['object', 'null'] }, // §15c — {toSpec,idleBlockedOnHuman,reason}
    readySpecCount: { type: 'number' }, authorityEmpty: { type: 'boolean' }, // §15g phase 7 / 15.33 — Ready-Spec authority count + empty-authority gate
    authorityChangePending: { type: 'boolean' }, // §15.16 — a real authority change is debouncing (window not elapsed)
    concurrencyMode: { type: 'string' }, // §15g phase 8 — serial|parallel; the host branches on it (no extra config-read agent)
  },
}
// §15g phase 8 — the deterministic dispatch/merge JSON the parallel work path branches on (agents run the Node
// scripts and return EXACTLY what they printed; the scripts own claim/worktree/gate/commit — never the model).
const DISPATCH = {
  type: 'object', required: ['mode'],
  properties: { mode: { type: 'string' }, slots: { type: 'number' }, parallel: { type: 'array', items: { type: 'object' } }, serialAgy: { type: ['object', 'null'] }, deferred: { type: 'array' } },
}
const GATE_OUTCOME = { type: 'object', required: ['outcome'], properties: { outcome: { type: 'string' }, reason: { type: ['string', 'null'] }, diffLines: { type: 'number' }, gate: { type: 'boolean' } } }
// dispatch.mjs picks the worker model per bead (operator's allowed set → task-fit); map its full id → the
// Workflow agent() short model name. Parallel workers are claude-only (agy routes to the serial lane).
const WF_MODEL = { 'claude-opus-4-8': 'opus', 'claude-sonnet-4-6': 'sonnet' }
const MERGE_OUTCOME = { type: 'object', properties: { landed: { type: 'array' }, reverted: { type: 'array' }, conflicts: { type: 'array' }, red: { type: 'array' }, orphans: { type: 'number' }, dropped: { type: ['number', 'null'] }, mergedAt: { type: 'string' } } }
const WORK_OUTCOME = {
  type: 'object', required: ['outcome', 'committed'],
  properties: { outcome: { type: 'string' }, committed: { type: 'boolean' }, headSha: { type: 'string' }, agyStrikes: { type: 'number' }, failedOver: { type: 'boolean' }, sha: { type: ['string', 'null'] }, reason: { type: ['string', 'null'] } },
}
const PLAN_OUTCOME = {
  type: 'object',
  properties: { applied: { type: 'number' }, added: { type: 'number' }, sugAdded: { type: 'number' }, other: { type: 'number' }, netChange: { type: 'number' }, mode: { type: 'string' }, sha: { type: ['string', 'null'] } },
}

// ── trigger decision: PASSTHROUGH. The precedence is precomputed by sense.mjs INTO snap.trigger via
// spec-core.computeTrigger (single source across both hosts — 15.22). The Workflow host CANNOT
// import a .mjs at runtime, so decideTrigger only applies the HOST-STATE gates (NO_PLANNER +
// max-replan drain) that are NOT part of the pure snapshot, then returns the raw {mode,latchKey}. ──
function decideTrigger(snap, replan) {
  if (NO_PLANNER) return null
  if (replan >= MAX_REPLAN) return null
  return snap.trigger || null
}

// §15.20 REDEFINED consecutive-fail breaker — REPLICATED inline (the Workflow host cannot import a .mjs at
// runtime, same as decideTrigger). MUST match assets-core.breakerStep byte-for-byte in behavior: trip on M
// cycles with NO net openWork reduction; benign (parked/unsure/infra-kill/idle/paused/productive-planner) don't
// increment; a strict openWork reduction resets. openWork = unimplemented ready assets + non-terminal beads (snap).
const BREAKER_BENIGN = new Set(['parked', 'unsure', 'idle', 'timeout', 'agy-noop', 'paused', 'planned', null])
function breakerStep({ breaker = 0, prevOpenWork = null, openWork = 0, outcome = null, breakerN = 5 }) {
  const reduced = prevOpenWork != null && openWork < prevOpenWork
  let b = breaker
  if (reduced) b = 0
  else if (!BREAKER_BENIGN.has(outcome)) b++
  return { breaker: b, prevOpenWork: openWork, reduced, tripped: b >= breakerN }
}

// ── agent prompts (thin wrappers; tick.md/planner.md remain the source of "how") ──
const preflightPrompt = `Run EXACTLY this one command and nothing else:\n  node ${ORC}/preflight.mjs\nReturn EXACTLY the JSON it printed to stdout (an object {ok,reason}).`

const sensePrompt = `Run EXACTLY this one command and nothing else:\n  node ${ORC}/sense.mjs --cap ${CAP} --audit-hours ${AUDIT_HOURS}\nReturn EXACTLY the JSON object it printed to stdout. Do not add fields.`

function workPrompt(bead) {
  return `You are ONE work tick of the Cosmo Canyon loop, running as a Workflow agent inside the desktop app (NOT a CLI). Caveman-terse. ALL commands run from cwd C:/Vibes/cosmo-canyon. Do ONE increment for bead ${bead.id} ("${bead.title}"), then return.

Steps (in order):
1. Run: node orchestrator/tick-prep.mjs --bead ${bead.id}   (persists control/.tick.json base anchor — required before any edit)
2. Read orchestrator/tick.md and follow it EXACTLY for bead ${bead.id}: decide the engine per its rules (it reads control/agent.json), implement the increment (or dispatch agy per the recipe), and honor every scope / protected-file / determinism rule. Your ONLY bead is ${bead.id}.
   - tick.md's bookkeep step — node orchestrator/bookkeep.mjs --result work|blocked|agy-noop — is REQUIRED and is the deterministic gate/commit authority. Run it exactly as tick.md instructs. Do NOT run git or the gate yourself.
3. FINAL action: node orchestrator/post-tick.mjs --agy-failover ${AGY_FAILOVER}
4. Return EXACTLY the JSON that post-tick.mjs printed to stdout (an object with outcome, committed, ...).`
}

function plannerPrompt(mode) {
  return `You are the Cosmo Canyon PLANNER tick (mode=${mode}), running as a Workflow agent. Caveman-terse. ALL commands run from cwd C:/Vibes/cosmo-canyon. You PLAN ONLY — never edit game/, never run git or the gate.

Steps (in order):
1. Run: node orchestrator/plan-prep.mjs --mode ${mode}   (writes control/.plan-input.json)
2. Read orchestrator/planner.md and follow it EXACTLY for mode ${mode}: produce control/.plan-result.json (typed ops only).
3. FINAL action: node orchestrator/plan-apply.mjs   (validates, dedups, WIP-rejects, applies atomically, commits)
4. Return EXACTLY the JSON that plan-apply.mjs printed to stdout.`
}

const pausePrompt = `Run EXACTLY this one command and nothing else, then return the literal text ok:\n  node -e "require('fs').writeFileSync('${CTRL}/.paused', JSON.stringify({reason:'consecutive-failure-breaker',at:new Date().toISOString()}))"`

// §15g phase 8 — PARALLEL work path (config mode=parallel). dispatch plans+claims+creates worktrees; each worker
// agent gates IN its worktree (commits nothing); the merge agent (single committer) lands each green onto HEAD.
const dispatchPrompt = `Run EXACTLY this one command and nothing else:\n  node ${ORC}/dispatch.mjs --cap ${CAP}\nReturn EXACTLY the JSON object it printed to stdout (it plans + claims + creates the isolated worktrees). Do not add fields.`

function workerParallelPrompt(d) {
  return `You are ONE PARALLEL WORKER of the Cosmo Canyon loop, running in an ISOLATED git worktree. Caveman-terse. Your worktree = ${d.worktree}; your claim anchor = ${d.claimPath}; your bead = ${d.beadId} ("${d.title}"). Do ONE increment, gate IN the worktree, land NOTHING (a single-committer merge lands green work — §13.30).

Steps (in order):
1. cd "${d.worktree}/cosmo-canyon"   (ALL commands run from here — your edits live in ITS game/).
2. Read orchestrator/tick.parallel.md and follow it EXACTLY for bead ${d.beadId}: implement the increment WITHIN the bead's SRC files only. game/accept/**, game/test/sim*.ts, game/assets/manifest.json, game/assets/source/** are PROTECTED — never edit. For an image/audio asset bead, only WIRE the getTexture/playSfx('${d.beadId}') key; do NOT run derive and do NOT touch the manifest (the merge derive-binds + grades at post-merge HEAD).
3. FINAL action (gate in the worktree; commit NOTHING):  node orchestrator/bookkeep.mjs --gate-only --tick "${d.claimPath}"
   Do NOT run git commit/add/push and do NOT run npm run gate yourself.
4. Return EXACTLY the JSON that bookkeep --gate-only printed ({outcome, reason, diffLines, gate}).`
}

const mergePrompt = `You are the Cosmo Canyon MERGE step (single committer). Run EXACTLY this one command and nothing else:\n  node ${ORC}/merge.mjs\nIt holds the git-tree lock, applies each GREEN worktree's diff onto current HEAD, re-runs gate+acceptance+derive at post-merge HEAD, commits via bookkeep (serialized), moves cc-known-good, and GC's every worktree by explicit path. Return EXACTLY the JSON it printed ({landed, reverted, conflicts, red, orphans, dropped, mergedAt}).`

// ── boot ──
log(`cc-loop config: MAX_TICKS=${MAX_TICKS} NO_PLANNER=${NO_PLANNER} CAP=${CAP} BREAKER=${BREAKER_N} (args=${JSON.stringify(args)})`)
phase('Boot')
const pf = await agent(preflightPrompt, { schema: PREFLIGHT, model: 'haiku', phase: 'Boot', label: 'preflight' })
if (!pf || !pf.ok) { log(`preflight REFUSED: ${pf ? pf.reason : 'agent returned null'}`); return { aborted: true, reason: pf ? pf.reason : 'preflight-null' } }
log(`preflight ok: ${pf.reason}`)

// ── loop (state lives HERE — replaces the supervisor's in-memory counters) ──
let breaker = 0, replan = 0, sinceCommit = 0, prevOpenWork = null, lastOutcome = null
const summary = { ticks: 0, committed: 0, reverted: 0, blocked: 0, parked: 0, planned: 0, idle: false, toSpec: false, brokenBy: null, cfg: { MAX_TICKS, NO_PLANNER, argsType: typeof args, argsRaw: args } }

for (let i = 0; i < MAX_TICKS; i++) {
  phase('Sense')
  const snap = await agent(sensePrompt, { schema: SNAPSHOT, model: 'haiku', phase: 'Sense', label: `sense#${i + 1}` })
  if (!snap) { log('sense returned null → stop'); break }
  if (snap.paused) { log('paused flag set → stop'); summary.brokenBy = 'paused'; break }
  if (snap.capReached) { log(`daily cap ${CAP} reached (usageToday=${snap.usageToday}) → stop`); summary.brokenBy = 'cap'; break }

  // §15.20 redefined breaker at cycle top from the openWork delta since last cycle, attributed to lastOutcome.
  const bstep = breakerStep({ breaker, prevOpenWork, openWork: Number(snap.openWork || 0), outcome: lastOutcome, breakerN: BREAKER_N })
  breaker = bstep.breaker; prevOpenWork = bstep.prevOpenWork
  if (bstep.tripped) { log(`BREAKER ${breaker} cycles, no net openWork reduction (openWork=${snap.openWork}) → auto-pause (13.39/15.20)`); await agent(pausePrompt, { model: 'haiku', phase: 'Sense', label: 'pause' }); summary.brokenBy = 'breaker'; break }

  const trig = decideTrigger(snap, replan)
  if (trig) {
    phase('Plan')
    log(`── tick ${i + 1} ── PLANNER mode=${trig.mode} readyCount=${snap.readyCount} openWork=${snap.openWork}`)
    const plan = await agent(plannerPrompt(trig.mode), { schema: PLAN_OUTCOME, model: 'opus', phase: 'Plan', label: `plan:${trig.mode}` })
    replan++; summary.ticks++; summary.planned++
    const net = plan && Number(plan.netChange || plan.applied || 0)
    lastOutcome = net > 0 ? 'planned' : 'plan-empty' // benign if productive; a no-op planner bumps the breaker next cycle
    log(`tick ${i + 1} planner done → mode=${trig.mode} net=${net ?? '?'} breaker=${breaker}`)
    continue // re-sense with the new backlog
  }

  // §15g phase 8 — PARALLEL work cycle (config mode=parallel). SERIAL mode skips this entirely → the single-flight
  // path below is byte-for-byte unchanged. An agent runs dispatch.mjs (plan+claim+worktree); if it dispatched
  // workers, spawn N worker agents CONCURRENTLY (each gates in its worktree) then the single-committer merge agent
  // lands each green onto HEAD. A lone agy bead (serialAgy) falls through to the serial path (main tree, one at a
  // time — never concurrent with a merge); nothing dispatchable → idle.
  let forcedBead = null
  if (snap.concurrencyMode === 'parallel') {
    phase('Work')
    const disp = await agent(dispatchPrompt, { schema: DISPATCH, model: 'haiku', phase: 'Work', label: `dispatch#${i + 1}` })
    const picks = (disp && Array.isArray(disp.parallel)) ? disp.parallel : []
    if (picks.length) {
      replan = 0
      log(`── tick ${i + 1} ── PARALLEL dispatch ${picks.length} [${picks.map((d) => d.beadId).join(', ')}] (deferred ${disp.deferred ? disp.deferred.length : 0})`)
      const wr = await Promise.all(picks.map((d) => agent(workerParallelPrompt(d), { schema: GATE_OUTCOME, model: WF_MODEL[d.model] || 'sonnet', phase: 'Work', label: `work:${d.beadId}` })))
      log(`workers: ${picks.map((d, k) => `${d.beadId}=${wr[k] ? wr[k].outcome : 'null'}`).join(' ')}`)
      const mg = await agent(mergePrompt, { schema: MERGE_OUTCOME, model: 'haiku', phase: 'Work', label: `merge#${i + 1}` })
      const landed = mg && Array.isArray(mg.landed) ? mg.landed.length : 0
      // §H6 — mirror supervisor.mjs:350 anyRevert: an orphan-only cycle (all workers crashed/timed out, so
      // landed=0 AND reverted/conflicts/red are all empty) is a BENIGN infra-kill the merge already sweeps
      // without bumping attempts. Marking it 'reverted' (non-benign) would trip the §15.20 breaker on flakiness
      // that the supervisor host idle-retries. 'idle' is in BREAKER_BENIGN → parity restored.
      const anyRevert = mg && (((mg.reverted || []).length) + ((mg.conflicts || []).length) + ((mg.red || []).length)) > 0
      summary.ticks += picks.length
      if (landed > 0) { sinceCommit = 0; summary.committed += landed; lastOutcome = 'committed'; log(`tick ${i + 1} MERGE landed ${landed}${mg && mg.dropped ? ` — AUTO-DROP maxConcurrency→${mg.dropped}` : ''}`) }
      else { sinceCommit++; lastOutcome = anyRevert ? 'reverted' : 'idle'; log(`tick ${i + 1} merge landed 0 (${anyRevert ? 'reverted/conflict' : `orphan-sweep ${(mg && mg.orphans) || 0}, infra-kill → idle`})`) }
      continue // re-sense
    }
    if (disp && disp.serialAgy) { forcedBead = disp.serialAgy; log(`── tick ${i + 1} ── serial agy bead ${forcedBead.id} (nothing parallel this cycle)`) }
    else { // nothing dispatchable → SAME idle/settle decision as the serial !bead branch
      const comp = snap.completion || {}
      if (comp.toSpec) { log('to-spec — every ready asset implemented, no open work → stop'); summary.toSpec = true }
      else if (snap.authorityChangePending) { log('authority change is settling (debounce) → re-run after ~90s to fire the coalesced diff'); summary.settlePending = true }
      else log(`idle: ${comp.idleBlockedOnHuman ? comp.reason : 'no dispatchable work and no planner trigger'} → stop`)
      summary.idle = true; break
    }
  }

  const bead = forcedBead || snap.headReadyBead
  if (!bead) {
    // §15c honest stop: to-spec vs idle-blocked-on-human. AUDIT FIX (step7 / §15.16): if a real authority change is
    // DEBOUNCE-PENDING, say so LOUDLY — the coalesced `diff` fires on the next run once the ~90s window elapses (the
    // .authority-settle marker persists on disk). This host is human-present, so re-running picks it up; the detached
    // supervisor poll-waits in-place instead (it is the lights-out host that must not silently drop the change).
    const comp = snap.completion || {}
    if (comp.toSpec) { log('to-spec — every ready asset implemented, no open work → stop'); summary.toSpec = true }
    else if (snap.authorityChangePending) { log('authority change is settling (debounce) → re-run the loop after ~90s to fire the coalesced diff (the .authority-settle marker persists)'); summary.settlePending = true }
    else log(`idle: ${comp.idleBlockedOnHuman ? comp.reason : 'no ready bead and no planner trigger'} → stop`)
    summary.idle = true; break
  }
  replan = 0

  phase('Work')
  log(`── tick ${i + 1} ── WORK bead=${bead.id} "${bead.title}" tier=${bead.tier || '?'} openWork=${snap.openWork}`)
  const work = await agent(workPrompt(bead), { schema: WORK_OUTCOME, model: 'sonnet', phase: 'Work', label: `work:${bead.id}` })
  summary.ticks++
  const oc = work ? work.outcome : 'null'
  lastOutcome = oc // the breaker consumes this at the NEXT cycle top (15.20 openWork-based)
  if (work && work.committed) { sinceCommit = 0; summary.committed++; log(`tick ${i + 1} COMMITTED ${work.sha ? work.sha.slice(0, 8) : ''} (${bead.id})`) }
  else {
    sinceCommit++
    if (oc === 'reverted') summary.reverted++; else if (oc === 'blocked') summary.blocked++; else if (oc === 'parked') summary.parked++
    log(`tick ${i + 1} no-progress outcome=${oc} reason=${work ? work.reason : '?'} breaker=${breaker}`)
  }
  if (work && work.failedOver) log(`agy → claude failover fired (13.38)`)
}

log(`loop ended: ${JSON.stringify(summary)}`)
return summary
