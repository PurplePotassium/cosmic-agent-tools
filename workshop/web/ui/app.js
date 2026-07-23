import { h, render } from "preact";
import { useState, useEffect, useRef, useCallback } from "preact/hooks";
import htm from "htm";
import { api, subscribe, attachmentURL } from "/api.js";

const html = htm.bind(h);
const SHARED = "shared";

// Mirrors the planning branch of internal/prompt/compose.go's InventBlock.
// The manual button queues planning on demand rather than waiting for a
// pipeline to go idle.
const INVENT_TASK_TITLE = "Plan next tasks toward the goal";
const INVENT_TASK_DETAIL = "This is a PLANNING pass: assess current progress toward the GOAL by reading the GOAL, " +
  "backlog, recent completions, and relevant repository evidence. Then create concrete, high-impact, unqueued tasks " +
  "that would materially move the work closer to completing the GOAL. Do not select an imagined single highest-impact " +
  "task. Write the tasks to proposals.json with implementation-ready titles and details; check " +
  "the entire backlog first so none duplicate queued work. Do NOT implement or begin any proposed task, edit repository " +
  "files, or run an unrelated increment in this pass. The proposals are the deliverable. Record the evidence behind the " +
  "task choices and how many you created in progress.json, then finish done.";

// GOAL_EVAL_QUESTIONS are asked one at a time (as separate self-evaluator
// inquiries — see internal/app/inquiry.go) by the "evaluate goal.md" button.
// Each already sees the current Goal.md as context, so no extra wiring is
// needed to point them at it.
const GOAL_EVAL_QUESTIONS = [
  "Evaluate the clarity of the Goal.md. What’s the biggest thing I’m missing about the situation right now. What don’t I realize?",
  "Evaluate the clarity of the Goal.md. What are you least confident about right now?",
  "Evaluate the task in the Goal.md. If you could add one unrequested, industry-leading feature, what would it be?",
  "Reading through the Goal.md, what assumptions did you make that you never stated explicitly?",
];

// ---------- helpers ----------

const backlogLabel = (b) => (b === "" || b === SHARED ? SHARED : b);

function ago(iso) {
  if (!iso) return "";
  const s = Math.max(0, (Date.now() - new Date(iso).getTime()) / 1000);
  if (s < 60) return `${s | 0}s ago`;
  if (s < 3600) return `${(s / 60) | 0}m ago`;
  return `${(s / 3600) | 0}h ago`;
}

function eventTone(type) {
  if (/done|landed|resolved|commit$|classified|verified|keyed|compare$|normalized/.test(type)) return "good";
  if (/failed|halt|breaker|wedge|dropped|red|abandoned|error|missing/.test(type)) return "bad";
  if (/conflict|suspected|skipped|ignored|stuck|incomplete|unknown|unverified|forced/.test(type)) return "warn";
  return "";
}

function describeEvent(ev) {
  const p = ev.payload || {};
  switch (ev.type) {
    case "pass.started": return `pass ${p.n} started — ${p.task || ""}`;
    case "pass.finished": return `pass ${p.n} ${p.outcome}${p.sha ? " @ " + p.sha : ""}`;
    case "commit": return `${p.sha} ${p.subject}`;
    case "task.created": return `task added: ${p.title}`;
    case "task.claimed": return `claimed: ${p.title}`;
    case "task.done": return `done: ${p.title || p.task}`;
    case "task.failed": return `${p.phase || "failed"}: ${p.task} ${p.note ? "— " + p.note : ""}`;
    case "task.stuck": return `STUCK after ${p.attempts} attempts: ${p.title}`;
    case "task.classified": return `classified ${p.task} as ${p.type}`;
    case "integration.landed": return `landed: ${(p.lanes || []).join(", ")}`;
    case "integration.dropped": return `dropped from queue (${p.why})${p.blockedBy ? " — blamed: " + p.blockedBy : ""}`;
    case "integration.conflict": return `merge conflict (${p.action})`;
    case "integration.sync_conflict": return `trunk sync conflict — waiting for integration`;
    case "integration.skipped": return `queue round skipped: ${p.why}`;
    case "conflict.enqueued": return `conflict → task ${p.task} queued (attempt ${p.attempt})`;
    case "conflict.resolved": return `conflict resolved${p.trivial ? " (trivial)" : ""} on ${p.lane}`;
    case "conflict.attempt_failed": return `resolution failed: ${p.why}`;
    case "conflict.abandoned": return `resolution abandoned — lane skipped until it advances`;
    case "breaker.tripped": return `circuit breaker: ${p.consecutiveFails} consecutive failures`;
    case "integration.drain_incomplete": return `lane work still queued — re-run to finish landing it`;
    case "auth.halt": return `AUTH FAILURE — pipeline halted (${p.agent})`;
    case "auth.suspected": return `suspected auth/model problem: ${p.note}`;
    case "wedge.killed": return `wedged pass killed after ${p.timeoutMin}m`;
    case "gate.red": return `gate RED (${p.where})`;
    case "driver.effort_ignored": return `effort "${p.effort}" ignored — ${p.agent} has no effort knob`;
    case "driver.model_unknown": return `model "${p.model}" may be wrong for agent ${p.agent} — off its known families`;
    case "pipeline.bundle": return p.cleared ? "model override cleared"
      : `model override → ${[p.agent, p.model, p.effort].filter(Boolean).join(":")}`;
    case "pipeline.needs_restart": return p.action === "removed"
      ? `removed — relaunching the engine so this lane's worker actually stops`
      : `added — relaunching the engine to activate this lane; it comes up parked, so resume it to start running`;
    case "pipeline.mode": return p.cleared ? "mode override cleared" : `mode override → ${p.mode}`;
    case "pipeline.personality": return p.cleared ? "personality override cleared" : `personality override → ${p.personality}`;
    case "integration.error": return `integration error${p.op ? " (" + p.op + ")" : ""}: ${p.error || ""}`.slice(0, 140);
    case "proposals.dropped": return `${p.count} proposal${p.count === 1 ? "" : "s"} dropped over the per-pass cap (${p.cap}): ${(p.titles || []).join("; ")}`.slice(0, 160);
    case "proposals.ingest_failed": return `failed to save ${p.count ? p.count + " " : ""}proposed follow-up${p.count === 1 ? "" : "s"}: ${p.error || ""}`.slice(0, 140);
    case "integration.merge_failed": return `merge failed (will retry): ${p.error || ""}`.slice(0, 140);
    case "inquiry.asked": return `asked: ${p.question || ""}`;
    case "inquiry.answered": return p.ok ? "inquiry answered" : "inquiry FAILED";
    case "art.generated": return `art generated: ${p.target}`;
    case "art.rescreened": return `art rescreened on a ${p.key} screen: ${p.screen}`;
    case "art.keyed": return `art background keyed out (${p.remover}): ${p.target}`;
    case "art.attempt_failed": return `art pass failed: ${p.why}`;
    case "art.route_forced": return `art task rerouted to agy (was ${p.routedAgent || "unset"})`;
    case "art.remover": return p.cleared ? "keyer override cleared" : `keyers → ${(p.keyers || [p.remover]).filter(Boolean).join(", ")}`;
    case "art.normalized": return `art verified: ${p.path} held ${p.from} bytes — re-encoded as PNG`;
    case "art.keyer_compare": {
      const runs = (p.keyers || []).map((k) => `${k.keyer}${k.ok ? "" : " ✗"}${k.primary ? " (primary)" : ""}`);
      return `keyer comparison archived for ${p.target}: ${runs.join(", ")}`;
    }
    case "art.keyer_compare_failed": return `comparison keyer ${p.keyer} failed: ${p.error || ""}`.slice(0, 140);
    case "art.model_verified": return `agy art model verified: ${p.model}`;
    case "art.models_missing": return `agy offers none of the allowed art models (wanted ${(p.wanted || []).join(" or ")})`;
    case "art.models_unverified": return `could not verify agy art models: ${p.error || ""}`.slice(0, 140);
    default: return JSON.stringify(p).slice(0, 120);
  }
}

// playChime emits a short rising two-note blip via the Web Audio API. It's
// synthesized, not a fetched asset, so the dashboard stays a single
// self-contained bundle (nothing to embed or serve). Used to notify the
// operator that a task finished. Browsers gate audio behind a user gesture,
// so the AudioContext is created/resumed lazily — the first successful play is
// the operator toggling sound on, which is itself a gesture.
let audioCtx = null;
function playChime() {
  try {
    const Ctor = window.AudioContext || window.webkitAudioContext;
    if (!Ctor) return;
    audioCtx = audioCtx || new Ctor();
    if (audioCtx.state === "suspended") audioCtx.resume();
    const now = audioCtx.currentTime;
    [660, 880].forEach((freq, i) => { // E5 → A5
      const osc = audioCtx.createOscillator();
      const gain = audioCtx.createGain();
      osc.type = "sine";
      osc.frequency.value = freq;
      const t = now + i * 0.11;
      gain.gain.setValueAtTime(0.0001, t);
      gain.gain.exponentialRampToValueAtTime(0.14, t + 0.02);
      gain.gain.exponentialRampToValueAtTime(0.0001, t + 0.17);
      osc.connect(gain).connect(audioCtx.destination);
      osc.start(t);
      osc.stop(t + 0.2);
    });
  } catch { /* audio unavailable — stay silent */ }
}

// ---------- components ----------

function TopBar({ status, connected, active, pauseAfterPending, stopped, soundOn, onSettings, onHalt, onPauseAfter, onToggleSound }) {
  // "live - stopped" once every pipeline has actually parked (stop pressed, or
  // a pause-after that has finished draining) — the server's up but no models
  // are running. Otherwise "live - N agents active", the live count of
  // pipelines mid-pass, so the operator sees at a glance how many are working.
  const liveText = connected
    ? (stopped ? "live - stopped" : `live - ${active} agent${active === 1 ? "" : "s"} active`)
    : "reconnecting…";
  return html`<div class="topbar">
    <h1>Workshop</h1>
    <span class="muted mono">${status?.repo || ""}</span>
    <span class="spacer"></span>
    <span class=${"conn" + (stopped ? " stopped" : "")}><span class=${"dot " + (connected ? "on" : "off")}></span>${liveText}</span>
    <button class="gear" onClick=${onSettings}
      title="Settings — chroma keyers, installed tools and models, transcript export, paths">⚙ settings</button>
    <button class=${"sound" + (soundOn ? " active" : "")} onClick=${onToggleSound}
      title=${soundOn
        ? "Sound on — a chime plays whenever a task completes. Click to mute."
        : "Sound off — click to play a chime whenever a task completes."}>${soundOn ? "🔔" : "🔕"}</button>
    <button class=${"pause-after" + (pauseAfterPending ? " active" : "")} onClick=${onPauseAfter} disabled=${stopped}
      title=${stopped
        ? "Everything is already stopped — nothing left to pause"
        : pauseAfterPending
        ? "Pause after is armed — every pipeline stops claiming new work once its current pass finishes"
        : "Stop every pipeline from claiming new work; whatever's running now finishes"}>pause after</button>
    <button class="danger" onClick=${onHalt} title="Kill every in-flight pass now — no models running, server stays up">stop</button>
  </div>`;
}

// ---------- the settings panel ----------

// KEYER_BLURBS mirrors internal/chroma's backend docs for the keyer pickers.
const KEYER_BLURBS = {
  ffmpeg: "ffmpeg colorkey+despill filter chain, keyed on the actual screen color. Robust, milliseconds per image; needs ffmpeg on PATH. The default.",
  corridorkey: "CorridorKey neural keyer. ML-quality edges (hair, soft shadows); needs the CorridorKey checkout and CUDA — it refuses CPU (measured 2+ hours per image).",
};

const SETTINGS_TABS = [
  ["keyers", "keyers"],
  ["tools", "tools & models"],
  ["export", "transcripts"],
  ["general", "general"],
];

// SettingsModal is the ⚙ popup: art keyers, environment/tool status,
// transcript export, and general info — the knobs and read-outs that used to
// crowd (or never fit) the topbar.
function SettingsModal({ art, extras, configView, soundOn, onToggleSound, onApplyKeyers, onVerifyModels, onClose }) {
  const [tab, setTab] = useState("keyers");
  const [env, setEnv] = useState(null);
  const [envLoading, setEnvLoading] = useState(false);
  const loadEnv = useCallback(async (fresh) => {
    setEnvLoading(true);
    try { setEnv(await api.env(fresh)); } catch { /* server briefly away */ }
    setEnvLoading(false);
  }, []);
  useEffect(() => { loadEnv(false); }, []);
  useEffect(() => {
    const onKey = (e) => { if (e.key === "Escape") onClose(); };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onClose]);
  return html`<div class="modal-backdrop" onMouseDown=${(e) => { if (e.target === e.currentTarget) onClose(); }}>
    <div class="modal settings">
      <div class="modal-head">
        <h2>Settings</h2>
        <button class="dismiss" onClick=${onClose} title="close (Esc)">✕</button>
      </div>
      <div class="tabs">
        ${SETTINGS_TABS.map(([id, label]) => html`<button key=${id}
          class=${"tab" + (tab === id ? " active" : "")} onClick=${() => setTab(id)}>${label}</button>`)}
      </div>
      <div class="modal-body">
        ${tab === "keyers" && html`<${KeyerSettings} art=${art} onApply=${onApplyKeyers} onVerify=${onVerifyModels} />`}
        ${tab === "tools" && html`<${ToolsSettings} env=${env} extras=${extras} loading=${envLoading} onRefresh=${() => loadEnv(true)} />`}
        ${tab === "export" && html`<${ExportSettings} env=${env} />`}
        ${tab === "general" && html`<${GeneralSettings} env=${env} configView=${configView} soundOn=${soundOn} onToggleSound=${onToggleSound} />`}
      </div>
    </div>
  </div>`;
}

// KeyerSettings edits the live green/blue-screen keyer set for art-gen-trans
// passes. More than one keyer = a comparison run: every selected backend keys
// the same screened intermediate; the PRIMARY's output becomes the committed
// asset while the rest are archived beside the pass log (and mirrored by
// [export]) as iter-NNNNNN.keyed-<keyer>.png, so a human can compare the
// files and settle on the most effective backend.
function KeyerSettings({ art, onApply, onVerify }) {
  const [selected, setSelected] = useState(art ? [...art.keyers] : []);
  const [dirty, setDirty] = useState(false);
  // Track the server value until the operator starts editing (same dirty
  // discipline as GoalCard).
  useEffect(() => { if (!dirty && art) setSelected([...art.keyers]); }, [art && art.keyers.join(",")]);
  if (!art) return html`<div class="muted">art settings unavailable</div>`;
  const toggle = (k) => {
    setDirty(true);
    setSelected((prev) => (prev.includes(k) ? prev.filter((x) => x !== k) : [...prev, k]));
  };
  const setPrimary = (k) => {
    setDirty(true);
    setSelected((prev) => [k, ...prev.filter((x) => x !== k)]);
  };
  const apply = async (list) => {
    const ok = await onApply(list);
    if (ok !== false) setDirty(false);
  };
  return html`<div>
    <h3>Chroma keyers <span class="chip" title=${art.override ? "live override active — applies from the next art pass" : "using the [art] config value"}>
      ${art.override ? "override ⚡" : "config"}</span></h3>
    <div class="note">Backends that remove the green/blue screen from art-gen-trans assets.
      Selecting more than one runs a <b>comparison</b>: each keyer processes the same screen,
      the <b>primary</b>'s result becomes the committed asset, and every result is archived
      beside the pass log (and mirrored to the export folder) as <code>iter-NNNNNN.keyed-«keyer».png</code> —
      compare the files, then narrow back down to the keyer that worked best.
      Changes apply from the NEXT art pass, no restart.</div>
    ${(art.removers || []).map((k) => html`<label class=${"keyer-row" + (selected.includes(k) ? " on" : "")} key=${k}>
      <input type="checkbox" checked=${selected.includes(k)} onChange=${() => toggle(k)} />
      <span>
        <b>${k}</b>${selected[0] === k && selected.length > 1 ? html` <span class="chip pin">primary</span>` : ""}
        <div class="muted">${KEYER_BLURBS[k] || ""}</div>
      </span>
    </label>`)}
    <div class="bundle-editor">
      ${selected.length > 1 && html`<label class="muted">primary
        <select value=${selected[0]} onChange=${(e) => setPrimary(e.target.value)}
          title="the keyer whose output becomes the committed asset">
          ${selected.map((k) => html`<option value=${k}>${k}</option>`)}
        </select>
      </label>`}
      <button class="primary" disabled=${!dirty || selected.length === 0}
        onClick=${() => apply(selected)}>apply</button>
      ${art.override && html`<button onClick=${() => apply([])}
        title="drop the live override, back to the [art] config value">clear override</button>`}
      <span class="muted">config: ${(art.configured || []).join(", ")}</span>
    </div>

    <h3 style="margin-top:18px">agy art model</h3>
    <div class="note">art-gen / art-gen-trans passes always run a frontier claude orchestrator
      (fable, else opus) that invokes agy with a launch-verified Gemini label
      (wanted, in order: ${(art.wanted || []).join(" → ")}).</div>
    <div class="kv-row"><span class="k">verified model</span>
      <span class="v">${art.verifying ? "verifying…" : (art.model || "not verified — passes assume the preferred default")}</span></div>
    ${art.agyModels && art.agyModels.length > 0 && html`<div class="kv-row"><span class="k">agy offers</span>
      <span class="v">${art.agyModels.join(", ")}</span></div>`}
    <div style="margin-top:8px">
      <button disabled=${art.verifying} onClick=${onVerify}
        title="Probe agy's model list again (quota-free) — e.g. right after logging agy in">
        ${art.verifying ? "verifying…" : "re-verify models"}</button>
    </div>
  </div>`;
}

// ToolsSettings reads out every external dependency: installed where, what
// version answered, and the best headless login signal we have.
function ToolsSettings({ env, extras, loading, onRefresh }) {
  return html`<div>
    <h3>Installed tools
      <button style="margin-left:auto" disabled=${loading} onClick=${onRefresh}
        title="Re-run the version/install probes (bypasses the 30s cache)">${loading ? "checking…" : "re-run checks"}</button>
    </h3>
    ${!env && html`<div class="muted">probing…</div>`}
    ${env && env.tools.map((t) => html`<div class="tool-row" key=${t.name}>
      <span class=${"dot " + (t.present ? "on" : "off")}></span>
      <div style="flex:1; min-width:0">
        <b>${t.name}</b> ${t.version && html`<span class="chip">${t.version}</span>`}
        ${!t.present && html`<span class="chip stuck">not found</span>`}
        ${t.path && html`<div class="muted mono" style="font-size:.72rem; overflow-wrap:anywhere">${t.path}</div>`}
        ${t.detail && html`<div class="muted" style="font-size:.78rem">${t.detail}</div>`}
        ${t.auth && html`<div style="font-size:.78rem" class=${/FAILURE/.test(t.auth) ? "bad-text" : "muted"}>login: ${t.auth}</div>`}
        ${t.fix && !t.present && html`<div class="muted" style="font-size:.78rem">fix: ${t.fix}</div>`}
      </div>
    </div>`)}

    <h3 style="margin-top:18px">Models</h3>
    <div class="note">Curated families the dashboard offers per agent; extend them
      with <code>[agents.«agent»] extra_models</code> in the config.</div>
    ${Object.keys(MODEL_FAMILIES).map((agent) => html`<div class="kv-row" key=${agent}>
      <span class="k">${agent}</span>
      <span class="v">${modelsFor(agent, extras).join(", ")}</span>
    </div>`)}
  </div>`;
}

// ExportSettings shows where pass evidence (transcripts, logs, keyer
// comparisons) is mirrored. The destination is versioned config, not a live
// knob — the engine validates it at start — so this is a read-out with
// instructions rather than an editor.
function ExportSettings({ env }) {
  if (!env) return html`<div class="muted">loading…</div>`;
  const ex = env.export || {};
  return html`<div>
    <h3>Transcript export ${ex.enabled
      ? html`<span class="chip" style="color:var(--green)">on</span>`
      : html`<span class="chip">off</span>`}</h3>
    ${ex.error && html`<div class="alert warn" style="margin:8px 0"><div>${ex.error}</div></div>`}
    <div class="kv-row"><span class="k">configured dir</span>
      <span class="v mono">${ex.configured || "(not set — export disabled)"}</span></div>
    ${ex.resolved && html`<div class="kv-row"><span class="k">resolved destination</span>
      <span class="v mono">${ex.resolved}</span></div>`}
    <div class="kv-row"><span class="k">set by</span><span class="v">${ex.source} layer</span></div>
    <div class="kv-row"><span class="k">human-readable transcripts</span>
      <span class="v">${ex.humanReadable ? "on — a markdown rendering lands beside each .jsonl" : "off"}</span></div>
    <div class="note" style="margin-top:10px">Every finished pass mirrors its evidence into one
      subfolder per pipeline (plus <code>inquiry</code> for the self-evaluator):
      the pass log, the driver's operational log, the agent runtime's FULL transcript
      (<code>iter-NNNNNN.transcript.jsonl</code> — prompt as sent, thinking, response),
      art-gen-trans's screened intermediate (<code>.screen.png</code>) and any keyer-comparison
      images (<code>.keyed-«keyer».png</code>).
      The destination is set in <code>.workshop/config.toml</code> under <code>[export]</code> —
      it must be OUTSIDE the repository (passes commit anything dirty in the working tree) and a
      change needs a workshop restart.</div>
  </div>`;
}

// GeneralSettings: the sound preference, server/runtime info, safety knobs,
// and every path an operator may need to find.
function GeneralSettings({ env, configView, soundOn, onToggleSound }) {
  const eff = configView?.effective;
  const safety = eff?.Safety;
  const started = env?.server?.started ? new Date(env.server.started) : null;
  return html`<div>
    <h3>Preferences</h3>
    <label class="keyer-row" style="align-items:center">
      <input type="checkbox" checked=${soundOn} onChange=${onToggleSound} />
      <span>play a chime whenever a task completes</span>
    </label>

    <h3 style="margin-top:14px">Server</h3>
    ${env?.server && html`<div>
      <div class="kv-row"><span class="k">version</span><span class="v">workshop ${env.server.version} (${env.os}/${env.arch})</span></div>
      <div class="kv-row"><span class="k">pid / port</span><span class="v">${env.server.pid} / 127.0.0.1:${env.server.port}</span></div>
      ${started && html`<div class="kv-row"><span class="k">up since</span><span class="v">${started.toLocaleString()}</span></div>`}
    </div>`}

    ${safety && html`<div>
      <h3 style="margin-top:14px">Effective safety knobs <span class="chip">config — .workshop/config.toml</span></h3>
      <div class="kv-row"><span class="k">max concurrent passes</span><span class="v">${safety.MaxConcurrent}</span></div>
      <div class="kv-row"><span class="k">wedge timeout</span><span class="v">${safety.WedgeMinutes} min</span></div>
      <div class="kv-row"><span class="k">circuit breaker</span><span class="v">${safety.BreakerFailures} consecutive failures</span></div>
      <div class="kv-row"><span class="k">sleep between passes</span><span class="v">${safety.SleepSeconds}s</span></div>
      <div class="kv-row"><span class="k">worktrees</span><span class="v">${configView.worktreesEnabled ? "on" : "off"}</span></div>
      <div class="kv-row"><span class="k">skip permissions</span><span class="v">${safety.SkipPermissions ? "yes" : "no"}</span></div>
    </div>`}

    ${env && html`<div>
      <h3 style="margin-top:14px">Paths</h3>
      ${[["repo", env.paths.repo], ["state dir", env.paths.stateDir], ["pass logs", env.paths.logs],
         ["repo config", env.paths.repoConfig], ["user config", env.paths.userConfig],
         ["runtime overrides", env.paths.overrides], ["goal", env.paths.goal], ["prompts", env.paths.prompts]]
        .map(([k, v]) => html`<div class="kv-row" key=${k}><span class="k">${k}</span><span class="v mono">${v}</span></div>`)}
    </div>`}

    ${env && env.warnings && env.warnings.length > 0 && html`<div>
      <h3 style="margin-top:14px">Config warnings</h3>
      ${env.warnings.map((w, i) => html`<div class="alert warn" style="margin:6px 0" key=${i}><div>${w}</div></div>`)}
    </div>`}
  </div>`;
}

function Alerts({ alerts, dismiss }) {
  if (!alerts.length) return null;
  return html`<div>
    ${alerts.map((a) => html`<div class=${"alert " + (a.tone === "warn" ? "warn" : "")} key=${a.id}>
      <div><b>${a.pipeline ? a.pipeline + ": " : ""}</b>${a.text}</div>
      <button class="dismiss" onClick=${() => dismiss(a.id)}>✕</button>
    </div>`)}
  </div>`;
}

function GoalCard({ goal, onSave }) {
  const [text, setText] = useState(goal);
  const [dirty, setDirty] = useState(false);
  useEffect(() => { if (!dirty) setText(goal); }, [goal]);
  return html`<div class="card">
    <h2>Goal <span class="muted">(.workshop/GOAL.md — versioned)</span></h2>
    <textarea rows="8" value=${text} onInput=${(e) => { setText(e.target.value); setDirty(true); }}></textarea>
    ${dirty && html`<div style="margin-top:6px; display:flex; gap:6px;">
      <button class="primary" onClick=${async () => {
        // Same error surface as act(): a failed save (server restarted → stale
        // token, etc.) must be visible, and the edit stays dirty for a retry.
        try { await onSave(text); setDirty(false); } catch (e) { alert(e.message); }
      }}>save</button>
      <button onClick=${() => { setText(goal); setDirty(false); }}>discard</button>
    </div>`}
  </div>`;
}

// GoalEvaluation shows the answers to GOAL_EVAL_QUESTIONS, matched by exact
// question text against the inquiries list (newest first, so a re-run's
// answer replaces the previous one in place). Inquiries run one at a time on
// the server, so `running` also covers the wait between questions.
//
// The agent/model/effort picker mirrors main's per-pass ${BundleEditor}: an
// empty field falls back to the configured [types.inquiry] route, same as an
// unset pipeline override does.
function GoalEvaluation({ inquiries, running, extras, onEvaluate }) {
  const answers = GOAL_EVAL_QUESTIONS.map((q) => inquiries.find((i) => i.question === q));
  const [editBundle, setEditBundle] = useState(false);
  const [agent, setAgent] = useState("");
  const [model, setModel] = useState("");
  const [effort, setEffort] = useState("");
  const pickAgent = (a) => {
    setAgent(a);
    if (a && model && !modelsFor(a, extras).includes(model)) setModel("");
  };
  const pickModel = (m, fam) => {
    setModel(m);
    if (m && fam && fam !== agent) setAgent(fam);
  };
  const bundle = [agent, model, effort].filter(Boolean).join(" · ") || "default route";
  return html`<div class="card">
    <h2>Goal.md evaluation
      <span class="chip" title="agent/model/effort used for the evaluation questions">${bundle}</span>
      <button title="switch agent/model/effort for this evaluation" onClick=${() => setEditBundle((v) => !v)}>⚙</button>
      <button class="primary" style="margin-left:auto" disabled=${running}
        onClick=${() => onEvaluate({ agent: agent || undefined, model: model.trim() || undefined, effort: effort || undefined })}
        title="Ask the AI four fixed self-evaluation questions about the current Goal.md, one at a time">
        ${running ? "evaluating…" : "evaluate goal.md"}
      </button>
    </h2>
    ${editBundle && html`<div class="bundle-editor">
      <select value=${agent} onChange=${(e) => pickAgent(e.target.value)} title="agent ('' = configured)">
        ${AGENTS.map((a) => html`<option value=${a}>${a || "agent (config)"}</option>`)}
      </select>
      <${ModelSelect} agent=${agent} extras=${extras} value=${model} onChange=${pickModel} />
      <select value=${effort} onChange=${(e) => setEffort(e.target.value)} title="effort ('' = default)">
        ${EFFORTS.map((ef) => html`<option value=${ef}>${ef || "effort (default)"}</option>`)}
      </select>
      <button onClick=${() => setEditBundle(false)}>✕</button>
    </div>`}
    ${answers.every((a) => !a) && html`<div class="muted">not evaluated yet</div>`}
    ${GOAL_EVAL_QUESTIONS.map((q, i) => answers[i] && html`<div class="completion" key=${i}>
      <div>${q}</div>
      ${answers[i].state === "running" && html`<div class="muted">thinking…</div>`}
      ${answers[i].state === "failed" && html`<div class="result">failed: ${answers[i].error}</div>`}
      ${answers[i].state === "done" && html`<div class="result">${answers[i].answer}</div>`}
    </div>`)}
  </div>`;
}

// readAsDataURL wraps FileReader in a promise so paste/attach handlers can await it.
function readAsDataURL(file) {
  return new Promise((resolve, reject) => {
    const reader = new FileReader();
    reader.onload = () => resolve(reader.result);
    reader.onerror = () => reject(reader.error || new Error("failed to read file"));
    reader.readAsDataURL(file);
  });
}

function AddTask({ pipelines, types, onAdd }) {
  const [title, setTitle] = useState("");
  const [type, setType] = useState("");
  const [backlog, setBacklog] = useState(SHARED);
  const [attachments, setAttachments] = useState([]); // [{name, dataUrl}], uploaded lazily on submit
  const fileInput = useRef(null);

  const addFiles = async (files) => {
    // Callers (paste handler, file input) are fire-and-forget, so a FileReader
    // failure must be surfaced here or it vanishes as an unhandled rejection.
    try {
      for (const file of [...files].filter((f) => f.type.startsWith("image/"))) {
        const dataUrl = await readAsDataURL(file);
        setAttachments((prev) => [...prev, { name: file.name || "pasted-image.png", dataUrl }]);
      }
    } catch (err) {
      alert(`attaching image failed: ${err.message}`);
    }
  };
  const onPaste = (e) => {
    const files = [...e.clipboardData.items]
      .filter((i) => i.kind === "file" && i.type.startsWith("image/"))
      .map((i) => i.getAsFile());
    if (files.length === 0) return;
    e.preventDefault();
    addFiles(files);
  };
  const removeAttachment = (i) => setAttachments((prev) => prev.filter((_, j) => j !== i));

  const submit = async (e) => {
    e.preventDefault();
    if (!title.trim()) return;
    try {
      const lines = [];
      for (const att of attachments) {
        const { path } = await api.uploadAttachment(att.name, att.dataUrl);
        lines.push(`![${att.name}](${path})`);
      }
      await onAdd({
        title: title.trim(), type: type || undefined, backlog,
        detail: lines.length > 0 ? lines.join("\n") : undefined,
      });
      setTitle("");
      setAttachments([]);
    } catch (err) {
      alert(err.message);
    }
  };
  return html`<form class="addtask" onSubmit=${submit}>
    <div class="row">
      <textarea name="title" rows="8" placeholder="add a task… (paste or attach an image below)" value=${title}
        onInput=${(e) => setTitle(e.target.value)}
        onPaste=${onPaste}
        onKeyDown=${(e) => { if (e.key === "Enter" && !e.shiftKey) { e.preventDefault(); submit(e); } }}></textarea>
    </div>
    ${attachments.length > 0 && html`<div class="row attachments">
      ${attachments.map((att, i) => html`<span class="attachment-chip" key=${i}>
        <img src=${att.dataUrl} alt=${att.name} />
        <button type="button" class="danger" title="remove" onClick=${() => removeAttachment(i)}>✕</button>
      </span>`)}
    </div>`}
    <div class="row">
      <select value=${type} onChange=${(e) => setType(e.target.value)} title="task type (empty = auto-classified)">
        <option value="">auto type</option>
        ${types.map((t) => html`<option value=${t}>${t}</option>`)}
      </select>
      <select value=${backlog} onChange=${(e) => setBacklog(e.target.value)} title="which backlog">
        <option value=${SHARED}>${SHARED}</option>
        ${pipelines.map((p) => html`<option value=${p.name}>${p.name}</option>`)}
      </select>
      <input type="file" accept="image/*" multiple ref=${fileInput} style="display:none"
        onChange=${(e) => { addFiles(e.target.files); e.target.value = ""; }} />
      <button type="button" title="attach image" onClick=${() => fileInput.current?.click()}>📎</button>
      <button class="primary" type="submit">add</button>
    </div>
  </form>`;
}

// attachmentRefRe matches the markdown image lines SaveAttachment's callers
// write into a task's detail (see internal/app/surface.go's attachmentRefRe),
// e.g. ![name](<StateDir>/attachments/xxx.png) — the path is an absolute host
// path, so only its basename is usable to fetch it back over HTTP.
const attachmentRefRe = /!\[([^\]]*)\]\(([^)]+)\)/g;

function parseAttachments(detail) {
  if (!detail) return [];
  const out = [];
  for (const m of detail.matchAll(attachmentRefRe)) {
    const name = m[2].trim().split(/[\\/]/).pop();
    if (name) out.push({ alt: m[1] || name, name });
  }
  return out;
}

function TaskRow({ task, pipelines, onTop, onMove, onDelete }) {
  const pinLabel = task.pin && (task.pin.agent || task.pin.model)
    ? [task.pin.agent, task.pin.model, task.pin.effort].filter(Boolean).join(":") : null;
  const thumbs = parseAttachments(task.detail);
  return html`<div class="task-row">
    <span class="title" title=${task.detail || task.title}>${task.title}</span>
    <div class="side">
      <span class="chips">
        ${thumbs.map((a, i) => html`<span class="attachment-chip thumb" key=${i}>
          <img src=${attachmentURL(a.name)} alt=${a.alt} />
        </span>`)}
        ${task.type && html`<span class="chip type">${task.type}</span>`}
        ${pinLabel && html`<span class="chip pin" title="pinned bundle">${pinLabel}</span>`}
        ${task.status === "claimed" && html`<span class="chip claimed">▶ ${task.claimedBy}</span>`}
        ${task.status === "stuck" && html`<span class="chip stuck">stuck ×${task.attempts}</span>`}
      </span>
      <span class="actions">
        <button title="move to top" onClick=${() => onTop(task)}>↑</button>
        <select title="move to backlog" onChange=${(e) => { if (e.target.value) onMove(task, e.target.value); e.target.value = ""; }}>
          <option value="">⇢</option>
          <option value=${SHARED}>${SHARED}</option>
          ${pipelines.map((p) => html`<option value=${p.name}>${p.name}</option>`)}
        </select>
        <button class="danger" title="delete" onClick=${() => onDelete(task)}>✕</button>
      </span>
    </div>
  </div>`;
}

function BacklogBoard({ tasks, pipelines, onTop, onMove, onDelete }) {
  const groups = new Map([["", []]]);
  for (const p of pipelines) groups.set(p.name, []);
  for (const t of tasks) {
    if (!groups.has(t.backlog || "")) groups.set(t.backlog || "", []);
    groups.get(t.backlog || "").push(t);
  }
  const multi = pipelines.length > 1;
  const sections = [...groups.entries()].filter(([name, list]) => name === "" || list.length > 0 || multi);
  return html`<div class="card">
    <h2>Backlog <span class="count">${tasks.length}</span></h2>
    ${sections.map(([name, list]) => html`<div class="backlog-section" key=${name}>
      ${(multi || name !== "") && html`<h3>${backlogLabel(name)}${name !== "" ? " (exclusive)" : ""}</h3>`}
      ${list.length === 0 && html`<div class="muted" style="font-size:0.8rem; padding:4px;">empty</div>`}
      ${list.map((t) => html`<${TaskRow} key=${t.id} task=${t} pipelines=${pipelines}
          onTop=${onTop} onMove=${onMove} onDelete=${onDelete} />`)}
    </div>`)}
  </div>`;
}

const EFFORTS = ["", "low", "medium", "high", "xhigh", "max"];
const AGENTS = ["", "claude", "agy", "codex"];

// MODEL_FAMILIES mirrors internal/domain's curated prefixes (ClaudeModels,
// AgyModels, CodexModels) with one representative id per family, for the model dropdown
// below. The dropdown is extended at runtime by the user's
// [agents.<agent>] extra_models (fetched from /api/v1/config) — that's how
// off-list ids become selectable now that the field is a select, not a
// free-text input.
const MODEL_FAMILIES = {
  claude: ["claude-sonnet-5", "claude-fable-5", "claude-opus-4-8", "claude-haiku-4-5-20251001"],
  agy: ["gemini-3-flash"],
  codex: ["gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna"],
};

// modelsFor unions an agent's curated representatives with the user's
// configured extra_models for it.
const modelsFor = (agent, extras) =>
  [...new Set([...(MODEL_FAMILIES[agent] || []), ...((extras || {})[agent] || [])])];

// ModelSelect is the model dropdown. With an agent pinned it lists that
// agent's models; unpinned it lists every agent's models grouped by driver,
// and onChange reports which family the pick belongs to so the caller can pin
// the matching agent (a claude driver can't run a gemini id, and vice versa).
function ModelSelect({ agent, extras, value, onChange }) {
  const families = agent ? [agent] : Object.keys(MODEL_FAMILIES);
  const owner = (m) => families.find((a) => modelsFor(a, extras).includes(m));
  // A stored override can name an off-list model (e.g. extra_models edited
  // since it was applied) — keep it selectable instead of snapping to default.
  const offList = value && !owner(value);
  return html`<select class="model" value=${value} title="model ('' = agent default)"
    onChange=${(e) => onChange(e.target.value, owner(e.target.value))}>
    <option value="">model (agent default)</option>
    ${offList && html`<option value=${value}>${value}</option>`}
    ${agent
      ? modelsFor(agent, extras).map((m) => html`<option value=${m}>${m}</option>`)
      : families.map((a) => html`<optgroup label=${a}>
          ${modelsFor(a, extras).map((m) => html`<option value=${m}>${m}</option>`)}
        </optgroup>`)}
  </select>`;
}

// BundleEditor is the live agent/model dial: it writes a store-backed
// override the worker re-reads every pass, so the NEXT pass switches with no
// restart (the successor of the old agent.json workflow).
function BundleEditor({ p, extras, onApply, onClear, onClose }) {
  const o = p.override || {};
  const [agent, setAgent] = useState(o.agent || "");
  const [model, setModel] = useState(o.model || "");
  const [effort, setEffort] = useState(o.effort || "");
  // Keep agent and model consistent: repinning the agent drops a model the
  // new driver can't run; picking a model from the other family pins its agent.
  const pickAgent = (a) => {
    setAgent(a);
    if (a && model && !modelsFor(a, extras).includes(model)) setModel("");
  };
  const pickModel = (m, fam) => {
    setModel(m);
    if (m && fam && fam !== (agent || p.agent)) setAgent(fam);
  };
  return html`<div class="bundle-editor">
    <select value=${agent} onChange=${(e) => pickAgent(e.target.value)} title="agent ('' = configured)">
      ${AGENTS.map((a) => html`<option value=${a}>${a || "agent (config)"}</option>`)}
    </select>
    <${ModelSelect} agent=${agent} extras=${extras} value=${model} onChange=${pickModel} />
    <select value=${effort} onChange=${(e) => setEffort(e.target.value)} title="effort ('' = default)">
      ${EFFORTS.map((ef) => html`<option value=${ef}>${ef || "effort (default)"}</option>`)}
    </select>
    <button class="primary" onClick=${() => onApply({
      agent: agent || undefined, model: model.trim() || undefined, effort: effort || undefined,
    })}>apply</button>
    ${p.override && html`<button onClick=${onClear} title="back to configured routing">clear</button>`}
    <button onClick=${onClose}>✕</button>
  </div>`;
}

// MODES mirrors domain.Modes: goal invents idle work and accepts proposed
// follow-ups; discover only accepts follow-ups; drain only drains the backlog.
const MODES = ["goal", "discover", "drain"];

// AddPipelineForm adds a new parallel-worktree agent lane. It writes to the
// runtime config layer only — the lane's worker/worktree comes alive on the
// next `workshop up`/`run`, so the confirmation copy says so up front rather
// than implying the agent starts working immediately.
function AddPipelineForm({ extras, onAdd }) {
  const [open, setOpen] = useState(false);
  const [name, setName] = useState("");
  const [agent, setAgent] = useState("");
  const [model, setModel] = useState("");
  const [effort, setEffort] = useState("");
  const pickAgent = (a) => {
    setAgent(a);
    if (a && model && !modelsFor(a, extras).includes(model)) setModel("");
  };
  const pickModel = (m, fam) => {
    setModel(m);
    if (m && fam && fam !== (agent || "claude")) setAgent(fam);
  };
  if (!open) {
    return html`<button style="margin-bottom:10px" onClick=${() => setOpen(true)}
      title="Add a new agent that works in parallel on its own git worktree/branch">+ agent</button>`;
  }
  const submit = async () => {
    if (!name.trim()) return;
    const ok = await onAdd({ name: name.trim(), agent: agent || undefined, model: model.trim() || undefined, effort: effort || undefined });
    if (ok === false) return; // add failed — keep the form open with its values for a fix-and-retry
    setName(""); setAgent(""); setModel(""); setEffort(""); setOpen(false);
  };
  return html`<div class="card bundle-editor" style="margin-bottom:10px">
    <input placeholder="name" value=${name} onInput=${(e) => setName(e.target.value)} style="width:8rem" />
    <select value=${agent} onChange=${(e) => pickAgent(e.target.value)} title="agent ('' = claude)">
      ${AGENTS.map((a) => html`<option value=${a}>${a || "agent (claude)"}</option>`)}
    </select>
    <${ModelSelect} agent=${agent} extras=${extras} value=${model} onChange=${pickModel} />
    <select value=${effort} onChange=${(e) => setEffort(e.target.value)} title="effort ('' = default)">
      ${EFFORTS.map((ef) => html`<option value=${ef}>${ef || "effort (default)"}</option>`)}
    </select>
    <button class="primary" onClick=${submit}
      title="Adds a new worktree/branch lane — takes effect on the next \`workshop up\`/\`run\`, not live">add</button>
    <button onClick=${() => setOpen(false)}>✕</button>
  </div>`;
}

function PipelineCard({ p, log, extras, personalityConfig, onDesired, onBundle, onMode, onPersonality, onDelete }) {
  const [editBundle, setEditBundle] = useState(false);
  const running = !!p.running;
  const operatorHalt = p.halted === "operator";
  const halted = p.halted && p.halted !== "operator";
  // A pause-after (operator halt) while a pass is still in flight is only
  // ARMED, not stopped: the pass keeps working and only parks once it finishes.
  // Until then we show it as "pausing…" and hide the resume button — the agent
  // is still doing real work, so offering "resume" would be a lie.
  const pausePending = operatorHalt && running;
  const stopped = operatorHalt && !running;
  const stateClass = halted ? "halted" : stopped ? "stopped" : pausePending ? "pausing" : "";
  const pill = running
    ? html`<span class="pill running">pass ${p.running.N} · ${elapsed(p.running.Started)}${pausePending ? " · pausing" : ""}</span>`
    : halted ? html`<span class="pill halted">HALTED: ${p.halted}</span>`
    : stopped ? html`<span class="pill stopped">stopped</span>`
    : html`<span class="pill idle">idle</span>`;
  const bundle = [p.agent, p.model, p.effort].filter(Boolean).join(" · ");
  const blind = p.agent === "agy";
  const staleReport = p.progressAgeSec > 300 && running;
  const thinking = running && !blind ? currentActivity(log) : "";
  return html`<div class=${"card pipeline-card " + stateClass}>
    <div class="pipeline-head">
      <span class="name">${p.name}</span>
      ${pill}
      <span class="chip" title=${p.override ? "live override active — applies from the next pass" : "configured bundle"}>
        ${bundle}${p.override ? " ⚡" : ""}</span>
      <select class="chip" value=${p.mode} onChange=${(e) => onMode(p.name, e.target.value)}
        title=${"goal = invents idle work and accepts proposed follow-ups; discover = only accepts follow-ups; drain = only drains the backlog" + (p.modeOverride ? " — live override, applies from the next pass" : " — click to override the configured mode")}>
        ${MODES.map((m) => html`<option value=${m}>${m}</option>`)}
      </select>
      ${p.modeOverride && html`<button title="clear override, back to the configured mode" onClick=${() => onMode(p.name, "")}>✕</button>`}
      ${editBundle && personalityConfig.enabled && html`<select class="chip" value=${p.personality || "none"}
        onChange=${(e) => onPersonality(p.name, e.target.value)}
        title=${"personality flavor injected into the prompt" + (p.personalityOverride ? " — live override, applies from the next pass" : " — click to override the configured personality")}>
        <option value="none">none</option>
        <option value="random">random</option>
        ${personalityConfig.list.map((name) => html`<option value=${name}>${name}</option>`)}
      </select>`}
      ${editBundle && p.personalityOverride && html`<button title="clear override, back to the configured personality" onClick=${() => onPersonality(p.name, "")}>✕</button>`}
      ${p.backlogExclusive > 0 && html`<span class="chip">own backlog: ${p.backlogExclusive}</span>`}
      <span class="spacer"></span>
      <button title="switch agent/model for the next pass" onClick=${() => setEditBundle((v) => !v)}>⚙</button>
      ${stopped || halted
        ? html`<button class="primary" onClick=${() => onDesired(p.name, "running")}>resume</button>`
        : pausePending
        ? html`<button class="pausing" onClick=${() => onDesired(p.name, "running")}
            title="Pause-after armed — this pass finishes, then the pipeline parks. Click to cancel and keep claiming work.">pausing…</button>`
        : html`<button onClick=${() => onDesired(p.name, "stopped")}
            title="Pause after idle: stop this pipeline from claiming new work; if it's mid-pass, that pass finishes first">stop</button>`}
      ${p.name.toLowerCase() !== "main" && html`<button class="danger" onClick=${() => onDelete(p.name)}
        title="Remove this agent lane from the config — takes effect on the next \`workshop up\`/\`run\`, not live">remove</button>`}
    </div>
    ${editBundle && html`<${BundleEditor} p=${p} extras=${extras}
      onApply=${async (b) => { await onBundle(p.name, b); setEditBundle(false); }}
      onClear=${async () => { await onBundle(p.name, {}); setEditBundle(false); }}
      onClose=${() => setEditBundle(false)} />`}
    ${thinking && html`<div class="thinking" title=${thinking}><span class="dot pulse"></span>${thinking}</div>`}
    ${p.progress && p.progress.phase && html`<div class="selfreport">
      <span class=${"age" + (staleReport ? " stale" : "")}>${p.progressAgeSec}s ago${staleReport ? " (stale?)" : ""}</span>
      <span class="phase">${p.progress.phase}</span> — ${p.progress.task || ""}
      ${p.progress.plan && html`<div class="muted">${p.progress.plan}</div>`}
      ${p.progress.note && html`<div class="muted">note: ${p.progress.note}</div>`}
      ${p.progress.result && html`<div class="muted">${p.progress.result}</div>`}
      ${blind && html`<div class="blindnote">agy output is not capturable headless — this self-report is the agent's own view of the pass.</div>`}
    </div>`}
    ${!blind && log && log.length > 0 && html`<${LogTail} lines=${log} />`}
    ${p.lastPass && !running && html`<div class="muted" style="margin-top:8px; font-size:0.8rem;">
      last: iter ${p.lastPass.N} ${p.lastPass.Outcome}${p.lastPass.CommitSHA ? " @ " + p.lastPass.CommitSHA : ""}${p.lastPass.Personality ? " · persona: " + p.lastPass.Personality : ""}${p.lastPass.Spice ? " · spice: " + p.lastPass.Spice : ""}
    </div>`}
  </div>`;
}

// currentActivity picks the most recent non-blank line out of a pipeline's
// streamed stdout/stderr — claude -p's raw, human-readable narration of what
// it's doing (tool calls, file reads, its own commentary). It's a live
// preview, not a summary: just "what did the model print last."
function currentActivity(lines) {
  if (!lines) return "";
  for (let i = lines.length - 1; i >= 0; i--) {
    const line = lines[i].trim();
    if (line) return line.length > 200 ? line.slice(0, 200) + "…" : line;
  }
  return "";
}

function elapsed(startISO) {
  const s = Math.max(0, (Date.now() - new Date(startISO).getTime()) / 1000);
  return s < 60 ? `${s | 0}s` : `${(s / 60) | 0}m${(s % 60) | 0}s`;
}

function LogTail({ lines }) {
  const ref = useRef(null);
  useEffect(() => {
    if (ref.current) ref.current.scrollTop = ref.current.scrollHeight;
  }, [lines]);
  return html`<div class="logtail" ref=${ref}>${lines.join("\n")}</div>`;
}

function QueuePanel({ queue }) {
  if (!queue || queue.length === 0) return null;
  return html`<div class="card">
    <h2>Merge queue</h2>
    ${queue.map((l) => html`<div class="queue-row" key=${l.pipeline}>
      <span class="mono">${l.branch}</span>
      <span class="chip">${l.ahead} ahead</span>
      ${l.blocked && html`<span class="chip stuck" title=${l.provenCulprit ? "failed the gate alone on trunk" : "suspected against: " + l.blockedBy}>
        blocked${l.provenCulprit ? " (proven)" : l.blockedBy ? ": " + l.blockedBy : ""}</span>`}
      ${l.conflictTaskId && html`<span class="chip pin" title="a merge-conflict task is queued for an agent">conflict → task</span>`}
    </div>`)}
  </div>`;
}

function ActivityFeed({ feed }) {
  return html`<div class="card">
    <h2>Activity</h2>
    <div class="feed">
      ${feed.length === 0 && html`<div class="muted">nothing yet</div>`}
      ${feed.map((ev) => html`<div class=${"feed-item " + eventTone(ev.type)} key=${ev.seq}>
        <span class="when">${ago(ev.ts)}</span>
        <span class="etype">${ev.pipeline ? ev.pipeline + " · " : ""}${ev.type}</span>
        <span>${describeEvent(ev)}</span>
      </div>`)}
    </div>
  </div>`;
}

function Completions({ completions }) {
  if (!completions || completions.length === 0) return null;
  return html`<div class="card">
    <h2>Completed <span class="count">${completions.length}</span></h2>
    ${completions.map((c) => html`<div class="completion" key=${c.id}>
      <div>${c.title} ${c.pipeline && html`<span class="chip">${c.pipeline}</span>`}</div>
      ${c.result && html`<div class="result">${c.result}</div>`}
    </div>`)}
  </div>`;
}

function Commits({ commits }) {
  if (!commits || commits.length === 0) return null;
  return html`<div class="card">
    <h2>Recent commits</h2>
    ${commits.map((c) => html`<div class="commit-row mono" key=${c.sha}>
      <span class="sha">${c.sha}</span> ${c.subject}
    </div>`)}
  </div>`;
}

// InquiryCard is the self-evaluator: ask WHY the workshop did something
// ("why are the coin pickups so big?") and a read-only forensics agent
// answers from commit trailers, pass logs, archived session transcripts, and
// the project docs. It investigates the past — it never edits anything.
//
// The agent/model/effort picker mirrors ${GoalEvaluation}: an empty field
// falls back to the configured [types.inquiry] route, same as main's per-pass
// bundle override does.
function InquiryCard({ inquiries, log, extras, onAsk, onStop }) {
  const [q, setQ] = useState("");
  const [editBundle, setEditBundle] = useState(false);
  const [agent, setAgent] = useState("");
  const [model, setModel] = useState("");
  const [effort, setEffort] = useState("");
  const pickAgent = (a) => {
    setAgent(a);
    if (a && model && !modelsFor(a, extras).includes(model)) setModel("");
  };
  const pickModel = (m, fam) => {
    setModel(m);
    if (m && fam && fam !== agent) setAgent(fam);
  };
  const bundle = [agent, model, effort].filter(Boolean).join(" · ") || "default route";
  const running = inquiries.some((i) => i.state === "running");
  const submit = async (e) => {
    e.preventDefault();
    const question = q.trim();
    if (!question || running) return;
    await onAsk(question, { agent: agent || undefined, model: model.trim() || undefined, effort: effort || undefined });
    setQ("");
  };
  return html`<div class="card">
    <h2>Ask why <span class="muted">(read-only forensics)</span>
      <span class="chip" title="agent/model/effort used for the inquiry">${bundle}</span>
      <button title="switch agent/model/effort for the inquiry" onClick=${() => setEditBundle((v) => !v)}>⚙</button>
    </h2>
    ${editBundle && html`<div class="bundle-editor">
      <select value=${agent} onChange=${(e) => pickAgent(e.target.value)} title="agent ('' = configured)">
        ${AGENTS.map((a) => html`<option value=${a}>${a || "agent (config)"}</option>`)}
      </select>
      <${ModelSelect} agent=${agent} extras=${extras} value=${model} onChange=${pickModel} />
      <select value=${effort} onChange=${(e) => setEffort(e.target.value)} title="effort ('' = default)">
        ${EFFORTS.map((ef) => html`<option value=${ef}>${ef || "effort (default)"}</option>`)}
      </select>
      <button onClick=${() => setEditBundle(false)}>✕</button>
    </div>`}
    <form onSubmit=${submit}>
      <textarea rows="2" placeholder="why did the workshop… (e.g. why are the coin pickups so big?)"
        value=${q} onInput=${(e) => setQ(e.target.value)}
        onKeyDown=${(e) => { if (e.key === "Enter" && !e.shiftKey) { e.preventDefault(); submit(e); } }}></textarea>
      <div style="display:flex; gap:6px; margin-top:6px;">
        <button class="primary" type="submit" disabled=${running || !q.trim()}>
          ${running ? "investigating…" : "ask"}</button>
        ${running && html`<button type="button" class="danger" onClick=${onStop}
          title="Kill the running inquiry">stop</button>`}
      </div>
    </form>
    ${running && log && log.length > 0 && html`<${LogTail} lines=${log} />`}
    ${inquiries.map((i) => html`<div class="inquiry" key=${i.id}>
      <div class="inquiry-q">${i.auto && html`<span class="muted">[auto-diagnosis] </span>`}${i.question}</div>
      ${i.state === "running" && html`<div class="muted">investigating… ${elapsed(i.started)}</div>`}
      ${i.state === "failed" && html`<div class="inquiry-a bad">${i.error}</div>`}
      ${i.state === "done" && html`<div class="inquiry-a">${i.answer}</div>`}
    </div>`)}
  </div>`;
}

// ---------- root ----------

const ALERT_TYPES = {
  "auth.halt": "bad",
  "auth.suspected": "warn",
  "breaker.tripped": "bad",
  "integration.drain_incomplete": "warn",
  "driver.model_unknown": "warn",
  "wedge.killed": "warn",
  "task.stuck": "warn",
  "gate.red": "warn",
  "pipeline.needs_restart": "warn",
  "integration.error": "warn",
  "proposals.ingest_failed": "warn",
};

// Events older than this cutoff (~10s before this page load) are history —
// the SSE stream replays a recent tail on every fresh connection so the feed
// has context, but a replayed event must not re-fire the "something just
// happened" side effects (completion chime, alert banner).
const HISTORY_CUTOFF_MS = Date.now() - 10_000;
const isHistorical = (ev) => {
  const t = ev.ts ? new Date(ev.ts).getTime() : NaN;
  return Number.isFinite(t) && t < HISTORY_CUTOFF_MS;
};

function App() {
  const [status, setStatus] = useState(null);
  const [tasks, setTasks] = useState([]);
  const [goal, setGoal] = useState("");
  const [queue, setQueue] = useState([]);
  const [feed, setFeed] = useState([]);
  const [logs, setLogs] = useState({});
  const [alerts, setAlerts] = useState([]);
  const [inquiries, setInquiries] = useState([]);
  // art: the art-generation settings view (greenscreen remover + verified
  // agy model); the topbar's keyer select edits it live.
  const [art, setArt] = useState(null);
  // extraModels: per-agent [agents.<agent>] extra_models from the config, so
  // the model dropdowns list the user's own additions next to the curated ids.
  const [extraModels, setExtraModels] = useState({});
  const [personalityConfig, setPersonalityConfig] = useState({ enabled: false, list: [] });
  // configView: the whole /api/v1/config payload (effective values +
  // provenance) for the settings panel's read-outs.
  const [configView, setConfigView] = useState(null);
  const [showSettings, setShowSettings] = useState(false);
  const [connected, setConnected] = useState(false);
  const [evaluatingGoal, setEvaluatingGoal] = useState(false);
  const [leftTab, setLeftTab] = useState("main");
  // Goal-eval answer ids the operator has already viewed on the eval tab, so
  // the 'goal evaluation' tab can badge answers that landed while it was closed.
  const [seenEvalIds, setSeenEvalIds] = useState(() => new Set());
  // Sound-on-completion preference, persisted so it survives reloads. A ref
  // shadows it because the SSE onEvent closure is created once (empty-deps
  // effect) and would otherwise capture the initial value forever.
  const [soundOn, setSoundOn] = useState(() => localStorage.getItem("workshop.sound") === "1");
  const soundOnRef = useRef(soundOn);
  useEffect(() => { soundOnRef.current = soundOn; }, [soundOn]);
  const refreshTimer = useRef(null);

  const refresh = useCallback(async () => {
    try {
      const [st, ts, q, inqs, artSt] = await Promise.all([api.status(), api.tasks(), api.queue(), api.inquiries(), api.art()]);
      setStatus(st);
      setTasks(ts);
      setQueue(q || []);
      setInquiries(inqs || []);
      setArt(artSt || null);
    } catch { /* server briefly away */ }
  }, []);

  // Coalesce bursts of events into one refetch.
  const scheduleRefresh = useCallback(() => {
    if (refreshTimer.current) return;
    refreshTimer.current = setTimeout(() => {
      refreshTimer.current = null;
      refresh();
    }, 400);
  }, [refresh]);

  useEffect(() => {
    refresh();
    api.goal().then((g) => setGoal(g.goal)).catch(() => {});
    // Config is fetched once: extra_models only changes with a config edit,
    // which means a server restart anyway.
    api.config().then((c) => {
      setConfigView(c || null);
      const agents = (c && c.effective && c.effective.Agents) || {};
      const extras = {};
      for (const [name, ac] of Object.entries(agents)) extras[name] = ac.ExtraModels || [];
      setExtraModels(extras);
      const pc = (c && c.effective && c.effective.Personality) || {};
      setPersonalityConfig({ enabled: !!pc.Enabled, list: pc.List || [] });
    }).catch(() => {});
    const poll = setInterval(refresh, 7000);
    const close = subscribe({
      onOpen: () => { setConnected(true); scheduleRefresh(); },
      onDown: () => setConnected(false),
      onLog: (ev) => {
        const line = ev.payload && ev.payload.line;
        if (line === undefined) return;
        setLogs((prev) => {
          const cur = (prev[ev.pipeline] || []).concat([line]).slice(-80);
          return { ...prev, [ev.pipeline]: cur };
        });
      },
      onEvent: (ev) => {
        setFeed((prev) => [ev, ...prev].slice(0, 120));
        // Replayed history fills the feed only — chimes and alert banners are
        // "right now" signals and must not resurrect for stale events.
        const fresh = !isHistorical(ev);
        if (fresh && ev.type === "task.done" && soundOnRef.current) playChime();
        const tone = fresh && ALERT_TYPES[ev.type];
        if (tone) {
          setAlerts((prev) => [...prev, {
            id: `${ev.seq}-${ev.type}`, tone, pipeline: ev.pipeline, text: describeEvent(ev),
          }].slice(-6));
        }
        if (ev.type === "pass.started" || ev.type === "inquiry.asked") {
          setLogs((prev) => ({ ...prev, [ev.pipeline]: [] }));
        }
        scheduleRefresh();
      },
    });
    return () => { clearInterval(poll); close(); };
  }, []);

  // GoalEvaluation answers are the inquiries whose question matches a fixed
  // GOAL_EVAL_QUESTIONS prompt. A "done" answer the operator hasn't yet viewed
  // on the eval tab is "unread" and badges the 'goal evaluation' tab button.
  const doneEvalIds = GOAL_EVAL_QUESTIONS
    .map((q) => inquiries.find((i) => i.question === q))
    .filter((a) => a && a.state === "done")
    .map((a) => a.id);
  const unreadEval = leftTab === "eval"
    ? 0
    : doneEvalIds.filter((id) => !seenEvalIds.has(id)).length;

  // Opening the eval tab marks every answer shown there as seen, so the badge
  // only ever counts answers that landed while the tab was closed.
  useEffect(() => {
    if (leftTab !== "eval" || doneEvalIds.length === 0) return;
    setSeenEvalIds((prev) => {
      const next = new Set(prev);
      for (const id of doneEvalIds) next.add(id);
      return next.size === prev.size ? prev : next;
    });
  }, [leftTab, doneEvalIds.join(",")]);

  const pipelines = status?.pipelines || [];
  const enabled = pipelines.filter((p) => p.enabled);
  // Armed once a pause-after is requested (halted for "operator"), and only while
  // some pipeline is still finishing its in-flight pass — once everything is
  // actually idle, pausing is a done deal and the button drops back to normal.
  const pauseAfterPending = enabled.some((p) => p.halted === "operator" && p.running);
  // "live - stopped": every enabled pipeline is parked (operator/breaker/auth
  // halt) and nothing is mid-pass. This is what a completed pause-after drains
  // into, and what the stop button forces immediately.
  const allStopped = enabled.length > 0 && enabled.every((p) => p.halted && !p.running);
  // How many agents are actually working right now: enabled pipelines with an
  // in-flight pass (p.running is the live Pass, nil between passes).
  const activeCount = enabled.filter((p) => p.running).length;
  const cfgTypes = [...new Set([
    ...(tasks.map((t) => t.type).filter(Boolean)),
    "code", "tests", "docs", "art", "audio",
  ])];

  // act wraps a mutation: surface failure, refetch either way. Returns whether
  // the mutation succeeded so callers with local state to clear (forms) can
  // keep it on failure.
  const act = async (fn) => {
    let ok = true;
    try { await fn(); } catch (e) { ok = false; alert(e.message); }
    await refresh();
    return ok;
  };

  // Toggling on both persists the choice and previews the chime — the click is
  // the user gesture browsers require to unlock the AudioContext, so later
  // event-driven plays work without further interaction.
  const toggleSound = () => setSoundOn((v) => {
    const next = !v;
    localStorage.setItem("workshop.sound", next ? "1" : "0");
    if (next) playChime();
    return next;
  });

  // Fires GOAL_EVAL_QUESTIONS as separate inquiries, all routed through the
  // same agent/model/effort bundle picked in the evaluation card (empty
  // fields fall back to the configured route, same as an unset pipeline
  // override). The server runs only one inquiry at a time, so each question
  // is asked only once the previous one has an answer (polled — there's no
  // per-question completion event to await).
  const onEvaluateGoal = async (bundle) => {
    if (evaluatingGoal) return;
    setEvaluatingGoal(true);
    try {
      for (const q of GOAL_EVAL_QUESTIONS) {
        const started = await api.ask(q, bundle);
        for (;;) {
          await new Promise((r) => setTimeout(r, 1200));
          const list = await api.inquiries();
          setInquiries(list || []);
          const found = (list || []).find((x) => x.id === started.id);
          if (found && found.state !== "running") break;
        }
      }
    } catch (e) {
      alert(e.message);
    } finally {
      setEvaluatingGoal(false);
    }
  };

  return html`<div>
    <${TopBar} status=${status} connected=${connected} active=${activeCount} pauseAfterPending=${pauseAfterPending} stopped=${allStopped}
      soundOn=${soundOn}
      onSettings=${() => setShowSettings(true)}
      onHalt=${() => act(() => api.haltServer())}
      onPauseAfter=${() => act(() => api.pauseAfter())}
      onToggleSound=${toggleSound} />
    ${showSettings && html`<${SettingsModal} art=${art} extras=${extraModels} configView=${configView}
      soundOn=${soundOn} onToggleSound=${toggleSound}
      onApplyKeyers=${(keyers) => act(async () => setArt(await api.setArtKeyers(keyers)))}
      onVerifyModels=${() => act(async () => { await api.verifyArtModels(); setArt(await api.art()); })}
      onClose=${() => setShowSettings(false)} />`}
    <div class="columns">
      <div>
        <${Alerts} alerts=${alerts} dismiss=${(id) => setAlerts((a) => a.filter((x) => x.id !== id))} />
        <div class="tabs">
          <button class=${"tab" + (leftTab === "main" ? " active" : "")} onClick=${() => setLeftTab("main")}>goal</button>
          <button class=${"tab" + (leftTab === "eval" ? " active" : "")} onClick=${() => setLeftTab("eval")}>goal evaluation${unreadEval > 0 && html`<span class="tab-badge" title=${`${unreadEval} new evaluation answer${unreadEval === 1 ? "" : "s"}`}>${unreadEval}</span>`}</button>
        </div>
        ${leftTab === "main" && html`<${GoalCard} goal=${goal} onSave=${async (text) => { await api.setGoal(text); setGoal(text); }} />
        <div class="card">
          <h2>Add task
            <button class="primary" style="margin-left:auto"
              title="Queue a planning pass that evaluates goal progress and creates high-impact follow-up tasks without implementing them"
              onClick=${() => act(() => api.addTask({ title: INVENT_TASK_TITLE, detail: INVENT_TASK_DETAIL }))}>invent</button>
          </h2>
          <${AddTask} pipelines=${pipelines} types=${cfgTypes}
            onAdd=${(t) => act(() => api.addTask(t))} />
        </div>
        <${BacklogBoard} tasks=${tasks} pipelines=${pipelines}
          onTop=${(t) => act(() => api.reorder(backlogLabel(t.backlog || ""), [t.id]))}
          onMove=${(t, backlog) => act(() => api.patchTask(t.id, { backlog }))}
          onDelete=${(t) => act(() => api.deleteTask(t.id))} />
        <${Completions} completions=${status?.completions} />`}
        ${leftTab === "eval" && html`<${GoalEvaluation} inquiries=${inquiries} running=${evaluatingGoal} extras=${extraModels} onEvaluate=${onEvaluateGoal} />`}
      </div>
      <div>
        <${AddPipelineForm} extras=${extraModels} onAdd=${(p) => act(() => api.addPipeline(p))} />
        ${pipelines.map((p) => html`<${PipelineCard} key=${p.name} p=${p} extras=${extraModels}
          personalityConfig=${personalityConfig}
          log=${logs[p.name]} onDesired=${(name, desired) => act(() => api.setPipeline(name, desired))}
          onBundle=${(name, bundle) => act(() => api.setPipelineBundle(name, bundle))}
          onMode=${(name, mode) => act(() => api.setPipelineMode(name, mode))}
          onPersonality=${(name, personality) => act(() => api.setPipelinePersonality(name, personality))}
          onDelete=${(name) => act(() => api.deletePipeline(name))} />`)}
        ${pipelines.length === 0 && html`<div class="card muted">no pipelines configured</div>`}
      </div>
      <div>
        <${QueuePanel} queue=${queue} />
        <${ActivityFeed} feed=${feed} />
        <${Commits} commits=${status?.recentCommits} />
        <${InquiryCard} inquiries=${inquiries} log=${logs["inquiry"]} extras=${extraModels}
          onAsk=${(question, bundle) => act(() => api.ask(question, bundle))}
          onStop=${() => act(() => api.stopInquiry())} />
      </div>
    </div>
  </div>`;
}

render(html`<${App} />`, document.getElementById("app"));
