import { h, render } from "preact";
import { useState, useEffect, useRef, useCallback } from "preact/hooks";
import htm from "htm";
import { api, subscribe, attachmentURL } from "/api.js";
import {
  WorkflowIntake, WorkflowList, WorkflowDetail, WfBundlePicker, ValidationCard,
  AWAITING, CLAUDE_MODELS, WF_EFFORTS, ago,
} from "/workflow.js";

const html = htm.bind(h);

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

function eventTone(type) {
  if (/approved|validated|\.ready$|\.done$|generated|keyed|verified|normalized|commit$|\.created$/.test(type)) return "good";
  if (/failed|error|blocked|abandoned|missing|killed/.test(type)) return "bad";
  if (/skipped|rejected|violation|unknown|unverified|attempt/.test(type)) return "warn";
  return "";
}

function describeEvent(ev) {
  const p = ev.payload || {};
  switch (ev.type) {
    case "workflow.created": return `workflow created — ${p.title || ""} (starting at ${p.stage})`;
    case "workflow.status": return `status → ${p.status}${p.stage ? " · " + p.stage : ""}`;
    case "workflow.stage_started": return `stage started: ${p.stage}`;
    case "workflow.turn_started": return `turn ${p.n} started (${p.stage})`;
    case "workflow.turn_finished": return `turn finished: ${p.state}${p.costUsd ? ` — $${p.costUsd.toFixed(2)}` : ""}`;
    case "workflow.question": return `waiting on you${p.note ? " — " + p.note : ""}`;
    case "workflow.ready": return `ready for review${p.note ? " — " + p.note : ""}`;
    case "workflow.blocked": return `BLOCKED${p.note ? " — " + p.note : ""}`;
    case "workflow.error": return `turn error: ${p.reason || ""}`.slice(0, 160);
    case "workflow.approved": return `${p.stage} approved${p.commit ? " @ " + p.commit : ""}`;
    case "workflow.rejected": return `${p.stage}: changes requested`;
    case "workflow.skipped": return `${p.stage} skipped`;
    case "workflow.abandoned": return "workflow abandoned";
    case "workflow.gate": return p.green ? "verify command green" : "verify command RED — approval gated";
    case "workflow.tree_violation": return `reverted out-of-scope changes: ${(p.paths || []).join(", ")}`.slice(0, 160);
    case "workflow.commit": return `implementation committed @ ${p.sha}`;
    case "workflow.validated": return `validated by run ${p.run || ""} — artifacts archived to the recycle bin`;
    case "commit": return `${p.sha} ${p.subject}`;
    case "commit.failed": return `commit failed: ${p.error || ""}`.slice(0, 140);
    case "pass.started": return `art job started${p.title ? " — " + p.title : ""}`;
    case "pass.finished": return `art job ${p.outcome || "finished"}${p.sha ? " @ " + p.sha : ""}`;
    case "pass.setup_failed": return `art job setup failed: ${p.error || ""}`.slice(0, 140);
    case "wedge.killed": return `wedged art job killed after ${p.timeoutMin}m`;
    case "export.failed": return `evidence export failed: ${p.error || ""}`.slice(0, 140);
    case "driver.model_unknown": return `model "${p.model}" may be wrong for agent ${p.agent} — off its known families`;
    case "inquiry.asked": return `asked: ${p.question || ""}`;
    case "inquiry.answered": return p.ok ? "inquiry answered" : "inquiry FAILED";
    case "art.generated": return `art generated: ${p.target}`;
    case "art.rescreened": return `art rescreened on a ${p.key} screen: ${p.screen}`;
    case "art.keyed": return `art background keyed out (${p.remover}): ${p.target}`;
    case "art.attempt_failed": return `art attempt failed: ${p.why}`;
    case "art.blocked": return `art job blocked: ${p.why || p.note || ""}`.slice(0, 140);
    case "art.done": return `art job done: ${p.target || ""}`;
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
// self-contained bundle (nothing to embed or serve). freqs picks the flavor:
// the default rise for "ready for review", a lower pair for "the agent is
// waiting on your answer". Browsers gate audio behind a user gesture, so the
// AudioContext is created/resumed lazily — the first successful play is the
// operator toggling sound on, which is itself a gesture.
let audioCtx = null;
function playChime(freqs = [660, 880]) { // E5 → A5
  try {
    const Ctor = window.AudioContext || window.webkitAudioContext;
    if (!Ctor) return;
    audioCtx = audioCtx || new Ctor();
    if (audioCtx.state === "suspended") audioCtx.resume();
    const now = audioCtx.currentTime;
    freqs.forEach((freq, i) => {
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

function TopBar({ status, connected, turnsRunning, soundOn, onSettings, onHalt, onShutdown, onToggleSound }) {
  // The live counter is the number of agent turns in flight across every
  // workflow — the operator's one-glance "is anything working right now".
  const liveText = connected
    ? turnsRunning > 0
      ? `live - ${turnsRunning} turn${turnsRunning === 1 ? "" : "s"} running`
      : "live - idle"
    : "reconnecting…";
  return html`<div class="topbar">
    <h1>Hal</h1>
    <span class="muted mono">${status?.repo || ""}</span>
    <span class="spacer"></span>
    <span class="conn"><span class=${"dot " + (connected ? "on" : "off")}></span>${liveText}</span>
    <button class="gear" onClick=${onSettings}
      title="Settings — chroma keyers, installed tools and models, transcript export, paths">⚙ settings</button>
    <button class=${"sound" + (soundOn ? " active" : "")} onClick=${onToggleSound}
      title=${soundOn
        ? "Sound on — a chime plays when a workflow needs you. Click to mute."
        : "Sound off — click to play a chime whenever a workflow needs you."}>${soundOn ? "🔔" : "🔕"}</button>
    <button class="danger" onClick=${onHalt}
      title="Kill every in-flight turn now — workflows drop to waiting-on-you and resume on your next message; the server stays up">stop turns</button>
    <button class="danger" onClick=${() => { if (confirm("Shut the hal server down?")) onShutdown(); }}
      title="Stop the whole server process (same as the CLI's hal stop)">shut down</button>
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
function SettingsModal({ art, extras, configView, wfDefaults, onWfDefaults, soundOn, onToggleSound, onApplyKeyers, onVerifyModels, onClose }) {
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
        ${tab === "tools" && html`<${ToolsSettings} env=${env} extras=${extras}
          stageCfg=${configView?.effective?.Workflow?.Stages} wfDefaults=${wfDefaults} onWfDefaults=${onWfDefaults}
          loading=${envLoading} onRefresh=${() => loadEnv(true)} />`}
        ${tab === "export" && html`<${ExportSettings} env=${env} />`}
        ${tab === "general" && html`<${GeneralSettings} env=${env} configView=${configView} soundOn=${soundOn} onToggleSound=${onToggleSound} />`}
      </div>
    </div>
  </div>`;
}

// KeyerSettings edits the live green/blue-screen keyer set for transparent
// art jobs. More than one keyer = a comparison run: every selected backend
// keys the same screened intermediate; the PRIMARY's output becomes the
// committed asset while the rest are archived beside the job log (and
// mirrored by [export]) as iter-NNNNNN.keyed-<keyer>.png, so a human can
// compare the files and settle on the most effective backend.
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
    <h3>Chroma keyers <span class="chip" title=${art.override ? "live override active — applies from the next art job" : "using the [art] config value"}>
      ${art.override ? "override ⚡" : "config"}</span></h3>
    <div class="note">Backends that remove the green/blue screen from transparent art-job assets.
      Selecting more than one runs a <b>comparison</b>: each keyer processes the same screen,
      the <b>primary</b>'s result becomes the committed asset, and every result is archived
      beside the job log (and mirrored to the export folder) as <code>iter-NNNNNN.keyed-«keyer».png</code> —
      compare the files, then narrow back down to the keyer that worked best.
      Changes apply from the NEXT art job, no restart.</div>
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
    <div class="note">Art jobs always run a frontier claude orchestrator
      (fable, else opus) that invokes agy with a launch-verified Gemini label
      (wanted, in order: ${(art.wanted || []).join(" → ")}).</div>
    <div class="kv-row"><span class="k">verified model</span>
      <span class="v">${art.verifying ? "verifying…" : (art.model || "not verified — jobs assume the preferred default")}</span></div>
    ${art.agyModels && art.agyModels.length > 0 && html`<div class="kv-row"><span class="k">agy offers</span>
      <span class="v">${art.agyModels.join(", ")}</span></div>`}
    <div style="margin-top:8px">
      <button disabled=${art.verifying} onClick=${onVerify}
        title="Probe agy's model list again (quota-free) — e.g. right after logging agy in">
        ${art.verifying ? "verifying…" : "re-verify models"}</button>
    </div>
  </div>`;
}

// ToolsSettings reads out every external dependency (installed where, what
// version answered, the best headless login signal we have) and edits the
// persisted workflow model/effort defaults.
function ToolsSettings({ env, extras, stageCfg, wfDefaults, onWfDefaults, loading, onRefresh }) {
  return html`<div>
    <h3>Workflow defaults</h3>
    <div class="note">The model/effort every new workflow starts with. This is the same
      setting as the New workflow form's advanced row — changing either place updates both,
      and the choice is saved in this browser. The blank model option shows what the
      per-stage config default actually resolves to.</div>
    <div class="bundle-editor">
      <${WfBundlePicker} extras=${extras} stageCfg=${stageCfg}
        model=${(wfDefaults && wfDefaults.model) || ""} effort=${(wfDefaults && wfDefaults.effort) || ""}
        onModel=${(m) => onWfDefaults({ ...wfDefaults, model: m })}
        onEffort=${(ef) => onWfDefaults({ ...wfDefaults, effort: ef })} />
    </div>

    <h3 style="margin-top:18px">Installed tools
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

// ExportSettings shows where turn/job evidence (transcripts, logs, keyer
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
    <div class="note" style="margin-top:10px">Every finished workflow turn and art job mirrors its
      evidence there: the raw stream log, and the agent runtime's FULL session transcript
      (<code>*.transcript.jsonl</code> — prompt as sent, thinking, response, tool calls),
      plus transparent art jobs' screened intermediate (<code>.screen.png</code>) and any
      keyer-comparison images (<code>.keyed-«keyer».png</code>).
      The destination is set in <code>.hal/config.toml</code> under <code>[export]</code> —
      it must be OUTSIDE the repository (implement turns commit anything dirty in the working
      tree) and a change needs a hal restart.</div>
  </div>`;
}

// GeneralSettings: the sound preference, server/runtime info, safety knobs,
// and every path an operator may need to find.
function GeneralSettings({ env, configView, soundOn, onToggleSound }) {
  const eff = configView?.effective;
  const safety = eff?.Safety;
  const wfCfg = eff?.Workflow;
  const started = env?.server?.started ? new Date(env.server.started) : null;
  return html`<div>
    <h3>Preferences</h3>
    <label class="keyer-row" style="align-items:center">
      <input type="checkbox" checked=${soundOn} onChange=${onToggleSound} />
      <span>play a chime whenever a workflow needs you</span>
    </label>

    <h3 style="margin-top:14px">Server</h3>
    ${env?.server && html`<div>
      <div class="kv-row"><span class="k">version</span><span class="v">hal ${env.server.version} (${env.os}/${env.arch})</span></div>
      <div class="kv-row"><span class="k">pid / port</span><span class="v">${env.server.pid} / 127.0.0.1:${env.server.port}</span></div>
      ${started && html`<div class="kv-row"><span class="k">up since</span><span class="v">${started.toLocaleString()}</span></div>`}
    </div>`}

    ${safety && html`<div>
      <h3 style="margin-top:14px">Effective knobs <span class="chip">config — .hal/config.toml</span></h3>
      <div class="kv-row"><span class="k">max concurrent turns</span><span class="v">${safety.MaxConcurrent}</span></div>
      ${wfCfg && html`<div class="kv-row"><span class="k">turn timeout</span><span class="v">${wfCfg.TurnMinutes} min</span></div>`}
      <div class="kv-row"><span class="k">art-job wedge timeout</span><span class="v">${safety.WedgeMinutes} min</span></div>
      <div class="kv-row"><span class="k">skip permissions (implement/validate)</span><span class="v">${safety.SkipPermissions ? "yes" : "no"}</span></div>
      ${wfCfg && html`<div class="kv-row"><span class="k">artifact dir</span><span class="v mono">${wfCfg.ArtifactDir}</span></div>`}
    </div>`}

    ${env && html`<div>
      <h3 style="margin-top:14px">Paths</h3>
      ${[["repo", env.paths.repo], ["state dir", env.paths.stateDir], ["turn logs", env.paths.logs],
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
    <h2>Goal <span class="muted">(.hal/GOAL.md — versioned)</span></h2>
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

// PromptFragmentCard edits .hal/prompts/project.md — the per-repo
// reading-list/guardrails fragment injected into every workflow stage
// prompt. Same dirty-state discipline as GoalCard.
function PromptFragmentCard() {
  const [content, setContent] = useState("");
  const [text, setText] = useState("");
  const [dirty, setDirty] = useState(false);
  useEffect(() => {
    api.fragment("project").then((f) => {
      setContent(f.content || "");
      setText((t) => (dirty ? t : f.content || ""));
    }).catch(() => {});
  }, []);
  useEffect(() => { if (!dirty) setText(content); }, [content]);
  return html`<div class="card">
    <h2>Project prompt fragment <span class="muted">(.hal/prompts/project.md)</span></h2>
    <div class="note" style="color:var(--dim); font-size:.8rem; margin-bottom:6px">
      Reading list, guardrails, conventions — injected into every workflow stage prompt.</div>
    <textarea rows="6" placeholder="(empty — no project fragment yet)"
      value=${text} onInput=${(e) => { setText(e.target.value); setDirty(true); }}></textarea>
    ${dirty && html`<div style="margin-top:6px; display:flex; gap:6px;">
      <button class="primary" onClick=${async () => {
        try { await api.setFragment("project", text); setContent(text); setDirty(false); } catch (e) { alert(e.message); }
      }}>save</button>
      <button onClick=${() => { setText(content); setDirty(false); }}>discard</button>
    </div>`}
  </div>`;
}

// GoalEvaluation shows the answers to GOAL_EVAL_QUESTIONS, matched by exact
// question text against the inquiries list (newest first, so a re-run's
// answer replaces the previous one in place). Inquiries run one at a time on
// the server, so `running` also covers the wait between questions.
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

// ---------- the ideas inbox ----------

function IdeaForm({ onAdd }) {
  const [title, setTitle] = useState("");
  const [detail, setDetail] = useState("");
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
      const fullDetail = [detail.trim(), lines.join("\n")].filter(Boolean).join("\n\n");
      await onAdd({ title: title.trim(), detail: fullDetail || undefined });
      setTitle("");
      setDetail("");
      setAttachments([]);
    } catch (err) {
      alert(err.message);
    }
  };
  return html`<form class="addtask" onSubmit=${submit}>
    <div class="row">
      <input name="title" placeholder="idea title…" value=${title} style="flex:1"
        onInput=${(e) => setTitle(e.target.value)} onPaste=${onPaste} />
    </div>
    <div class="row">
      <textarea name="detail" rows="3" placeholder="detail (optional — paste or attach images)"
        value=${detail} onInput=${(e) => setDetail(e.target.value)} onPaste=${onPaste}></textarea>
    </div>
    ${attachments.length > 0 && html`<div class="row attachments">
      ${attachments.map((att, i) => html`<span class="attachment-chip" key=${i}>
        <img src=${att.dataUrl} alt=${att.name} />
        <button type="button" class="danger" title="remove" onClick=${() => removeAttachment(i)}>✕</button>
      </span>`)}
    </div>`}
    <div class="row">
      <input type="file" accept="image/*" multiple ref=${fileInput} style="display:none"
        onChange=${(e) => { addFiles(e.target.files); e.target.value = ""; }} />
      <button type="button" title="attach image" onClick=${() => fileInput.current?.click()}>📎</button>
      <button class="primary" type="submit" disabled=${!title.trim()}>add idea</button>
    </div>
  </form>`;
}

// attachmentRefRe matches the markdown image lines SaveAttachment's callers
// write into an idea's detail (see internal/app/surface.go's attachmentRefRe),
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

function IdeaRow({ task, onPromote, onDelete }) {
  const thumbs = parseAttachments(task.detail);
  return html`<div class="task-row">
    <span class="title" title=${task.detail || task.title}>${task.title}</span>
    <span class="chips">
      ${thumbs.map((a, i) => html`<span class="attachment-chip thumb" key=${i}>
        <img src=${attachmentURL(a.name)} alt=${a.alt} />
      </span>`)}
    </span>
    <span class="actions">
      <button class="primary" title="promote this idea into a workflow (the idea closes with a pointer at it)"
        onClick=${() => onPromote(task)}>→ workflow</button>
      <button class="danger" title="delete" onClick=${() => onDelete(task)}>✕</button>
    </span>
  </div>`;
}

function IdeasPanel({ tasks, onAdd, onPromote, onDelete }) {
  return html`<div>
    <div class="card">
      <h2>Ideas inbox <span class="count">${tasks.length}</span></h2>
      <${IdeaForm} onAdd=${onAdd} />
    </div>
    <div class="card">
      ${tasks.length === 0 && html`<div class="muted" style="font-size:0.8rem; padding:4px;">no ideas parked — promote one into a workflow when it's ready</div>`}
      ${tasks.map((t) => html`<${IdeaRow} key=${t.id} task=${t} onPromote=${onPromote} onDelete=${onDelete} />`)}
    </div>
  </div>`;
}

const EFFORTS = ["", "low", "medium", "high", "xhigh", "max"];
const AGENTS = ["", "claude", "agy", "codex"];

// MODEL_FAMILIES mirrors internal/domain's curated prefixes (ClaudeModels,
// AgyModels, CodexModels) with one representative id per family, for the
// model dropdowns. Extended at runtime by the user's [agents.<agent>]
// extra_models (fetched from /api/v1/config).
const MODEL_FAMILIES = {
  claude: CLAUDE_MODELS,
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

function Commits({ commits }) {
  if (!commits || commits.length === 0) return null;
  return html`<div class="card">
    <h2>Recent commits</h2>
    ${commits.map((c) => html`<div class="commit-row mono" key=${c.sha}>
      <span class="sha">${c.sha}</span> ${c.subject}
    </div>`)}
  </div>`;
}

// ArtJobCard fires one art-generation job: a frontier claude orchestrator
// invokes agy (the Gemini image model) via `hal agy-run`, verifies what
// it wrote, and commits the asset. transparent adds the green-screen +
// chroma-key flow. One job runs at a time (the server 409s while busy); its
// log streams into the tail below.
function ArtJobCard({ log }) {
  const [prompt, setPrompt] = useState("");
  const [target, setTarget] = useState("");
  const [transparent, setTransparent] = useState(false);
  const [busy, setBusy] = useState(false);
  const submit = async (e) => {
    e.preventDefault();
    if (!prompt.trim() || busy) return;
    setBusy(true);
    try {
      await api.artJob({ prompt: prompt.trim(), target: target.trim(), transparent });
      setPrompt("");
    } catch (err) {
      alert(err.status === 409 ? "an art job is already running — wait for it to finish" : err.message);
    }
    setBusy(false);
  };
  return html`<div class="card">
    <h2>Generate art <span class="muted">(claude orchestrates agy)</span></h2>
    <form onSubmit=${submit}>
      <textarea rows="2" placeholder="describe the asset… (subject, style, palette; first line doubles as the title)"
        value=${prompt} onInput=${(e) => setPrompt(e.target.value)}></textarea>
      <div style="display:flex; gap:6px; margin-top:6px; align-items:center; flex-wrap:wrap;">
        <input placeholder="target path (optional — assets/art/«slug».png)" style="flex:1; min-width:180px"
          value=${target} onInput=${(e) => setTarget(e.target.value)} />
        <label class="muted" style="display:flex; gap:4px; align-items:center; font-size:.8rem"
          title="rescreen + chroma-key flow: the background is keyed away, leaving a transparent PNG">
          <input type="checkbox" checked=${transparent} onChange=${(e) => setTransparent(e.target.checked)} />
          transparent
        </label>
        <button class="primary" type="submit" disabled=${busy || !prompt.trim()}>${busy ? "starting…" : "generate"}</button>
      </div>
    </form>
    ${log && log.length > 0 && html`<${LogTail} lines=${log} />`}
  </div>`;
}

// InquiryCard is the self-evaluator: ask WHY the hal did something
// ("why are the coin pickups so big?") and a read-only forensics agent
// answers from commit trailers, turn logs, archived session transcripts, and
// the project docs. It investigates the past — it never edits anything.
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
      <textarea rows="2" placeholder="why did the hal… (e.g. why are the coin pickups so big?)"
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

// Feed noise filter: per-token/message traffic renders inside the workflow
// detail, not the activity feed.
const FEED_SKIP = new Set(["workflow.delta", "workflow.tool", "workflow.message", "workflow.status"]);

const ALERT_TYPES = {
  "workflow.error": "bad",
  "workflow.blocked": "bad",
  "workflow.tree_violation": "warn",
  "commit.failed": "warn",
  "export.failed": "warn",
  "wedge.killed": "warn",
  "driver.model_unknown": "warn",
  "art.models_missing": "warn",
  "pass.setup_failed": "warn",
};

// loadWfDefaults reads the persisted New Workflow model/effort defaults,
// dropping anything malformed (a hand-edited key or a retired effort name
// must not ride into a create request).
function loadWfDefaults() {
  try {
    const d = JSON.parse(localStorage.getItem("hal.wfDefaults")) || {};
    return {
      model: typeof d.model === "string" ? d.model : "",
      effort: WF_EFFORTS.includes(d.effort) ? d.effort : "",
    };
  } catch {
    return { model: "", effort: "" };
  }
}

// Events older than this cutoff (~10s before this page load) are history —
// the SSE stream replays a recent tail on every fresh connection so the feed
// has context, but a replayed event must not re-fire the "something just
// happened" side effects (chime, alert banner).
const HISTORY_CUTOFF_MS = Date.now() - 10_000;
const isHistorical = (ev) => {
  const t = ev.ts ? new Date(ev.ts).getTime() : NaN;
  return Number.isFinite(t) && t < HISTORY_CUTOFF_MS;
};

function App() {
  const [status, setStatus] = useState(null);
  const [workflows, setWorkflows] = useState([]);
  const [selected, setSelected] = useState(null);
  // wfLive wraps the latest workflow.* SSE event with a counter so the detail
  // view sees every one (ephemeral deltas carry no seq of their own).
  const [wfLive, setWfLive] = useState(null);
  const wfLiveSeq = useRef(0);
  const [tasks, setTasks] = useState([]);
  const [goal, setGoal] = useState("");
  const [feed, setFeed] = useState([]);
  const [logs, setLogs] = useState({});
  const [alerts, setAlerts] = useState([]);
  const [inquiries, setInquiries] = useState([]);
  // art: the art-generation settings view (greenscreen keyers + verified agy
  // model) for the settings panel.
  const [art, setArt] = useState(null);
  // validation: the validate card's view model — the auto toggle, pending
  // implementations, and the live validation run (if any).
  const [validation, setValidation] = useState(null);
  // extraModels: per-agent [agents.<agent>] extra_models from the config, so
  // the model dropdowns list the user's own additions next to the curated ids.
  const [extraModels, setExtraModels] = useState({});
  // configView: the whole /api/v1/config payload (effective values +
  // provenance) for the settings panel's read-outs.
  const [configView, setConfigView] = useState(null);
  const [showSettings, setShowSettings] = useState(false);
  const [connected, setConnected] = useState(false);
  const [evaluatingGoal, setEvaluatingGoal] = useState(false);
  const [leftTab, setLeftTab] = useState("workflows");
  // Goal-eval answer ids the operator has already viewed on the eval tab, so
  // the tab can badge answers that landed while it was closed.
  const [seenEvalIds, setSeenEvalIds] = useState(() => new Set());
  // Attention keys (workflow id|status|updated) the operator has already had
  // open in the detail view — the seen-set behind the "(N) Hal" title
  // badge, same pattern as seenEvalIds.
  const [attnSeen, setAttnSeen] = useState(() => new Set());
  // Sound preference, persisted so it survives reloads. A ref shadows it
  // because the SSE onEvent closure is created once (empty-deps effect) and
  // would otherwise capture the initial value forever.
  const [soundOn, setSoundOn] = useState(() => localStorage.getItem("hal.sound") === "1");
  // Workflow model/effort defaults: what the New Workflow form (and thus
  // every workflow started from it) launches with. Persisted like hal.sound;
  // editable both from the intake form's advanced row and Settings →
  // tools & models — one shared value, so the two places never disagree.
  const [wfDefaults, setWfDefaultsRaw] = useState(loadWfDefaults);
  const setWfDefaults = (d) => {
    setWfDefaultsRaw(d);
    localStorage.setItem("hal.wfDefaults", JSON.stringify(d));
  };
  const soundOnRef = useRef(soundOn);
  useEffect(() => { soundOnRef.current = soundOn; }, [soundOn]);
  const refreshTimer = useRef(null);

  const refresh = useCallback(async () => {
    try {
      const [st, wfs, ts, inqs, artSt, val] = await Promise.all([
        api.status(), api.workflows(), api.tasks(), api.inquiries(), api.art(), api.validation(),
      ]);
      setStatus(st);
      setWorkflows(wfs || []);
      setTasks(ts || []);
      setInquiries(inqs || []);
      setArt(artSt || null);
      setValidation(val || null);
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
        // Every workflow event streams into the open detail view.
        if (ev.type.startsWith("workflow.")) {
          wfLiveSeq.current += 1;
          setWfLive({ n: wfLiveSeq.current, ev });
        }
        if (!FEED_SKIP.has(ev.type)) setFeed((prev) => [ev, ...prev].slice(0, 120));
        // Replayed history fills the feed only — chimes and alert banners are
        // "right now" signals and must not resurrect for stale events.
        const fresh = !isHistorical(ev);
        if (fresh && soundOnRef.current) {
          if (ev.type === "workflow.question" || ev.type === "workflow.blocked") playChime([523, 415]); // C5 → G#4: "your move"
          else if (ev.type === "workflow.ready") playChime(); // rising: "review time"
        }
        const tone = fresh && ALERT_TYPES[ev.type];
        if (tone) {
          setAlerts((prev) => [...prev, {
            id: `${ev.seq}-${ev.type}`, tone, pipeline: ev.pipeline, text: describeEvent(ev),
          }].slice(-6));
        }
        if (ev.type === "inquiry.asked") setLogs((prev) => ({ ...prev, inquiry: [] }));
        if (ev.type === "pass.started") setLogs((prev) => ({ ...prev, [ev.pipeline]: [] }));
        scheduleRefresh();
      },
    });
    return () => { clearInterval(poll); close(); };
  }, []);

  // GoalEvaluation answers are the inquiries whose question matches a fixed
  // GOAL_EVAL_QUESTIONS prompt. A "done" answer the operator hasn't yet viewed
  // on the eval tab is "unread" and badges the eval tab button.
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

  // The title badge: workflows idle on the operator's move whose current
  // waiting spell hasn't been opened in the detail view yet.
  const attnKey = (w) => `${w.ID}|${w.Status}|${w.Updated}`;
  const attention = workflows.filter((w) => AWAITING.has(w.Status) && !attnSeen.has(attnKey(w)));

  // Having a workflow's detail open marks its current waiting spell as seen.
  useEffect(() => {
    if (!selected) return;
    const w = workflows.find((x) => x.ID === selected);
    if (!w || !AWAITING.has(w.Status)) return;
    const key = attnKey(w);
    setAttnSeen((prev) => {
      if (prev.has(key)) return prev;
      const next = new Set(prev);
      next.add(key);
      return next;
    });
  }, [selected, workflows]);

  useEffect(() => {
    document.title = attention.length > 0 ? `(${attention.length}) Hal` : "Hal";
  }, [attention.length]);

  const turnsRunning = workflows.filter((w) => w.Status === "turn-running").length;

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
    localStorage.setItem("hal.sound", next ? "1" : "0");
    if (next) playChime();
    return next;
  });

  // Fires GOAL_EVAL_QUESTIONS as separate inquiries, all routed through the
  // same agent/model/effort bundle picked in the evaluation card. The server
  // runs only one inquiry at a time, so each question is asked only once the
  // previous one has an answer (polled — there's no per-question completion
  // event to await).
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

  const promoteIdea = async (task) => {
    try {
      const wf = await api.createWorkflow({ fromTaskId: task.id });
      setSelected(wf.ID);
      setLeftTab("workflows");
    } catch (e) {
      alert(e.message);
    }
    await refresh();
  };

  const LEFT_TABS = [
    ["workflows", "workflows"],
    ["ideas", "ideas"],
    ["goal", "goal"],
    ["eval", "eval"],
  ];

  return html`<div>
    <${TopBar} status=${status} connected=${connected} turnsRunning=${turnsRunning}
      soundOn=${soundOn}
      onSettings=${() => setShowSettings(true)}
      onHalt=${() => act(() => api.haltServer())}
      onShutdown=${() => act(() => api.stopServer())}
      onToggleSound=${toggleSound} />
    ${showSettings && html`<${SettingsModal} art=${art} extras=${extraModels} configView=${configView}
      wfDefaults=${wfDefaults} onWfDefaults=${setWfDefaults}
      soundOn=${soundOn} onToggleSound=${toggleSound}
      onApplyKeyers=${(keyers) => act(async () => setArt(await api.setArtKeyers(keyers)))}
      onVerifyModels=${() => act(async () => { await api.verifyArtModels(); setArt(await api.art()); })}
      onClose=${() => setShowSettings(false)} />`}
    <div class="columns">
      <div>
        <${Alerts} alerts=${alerts} dismiss=${(id) => setAlerts((a) => a.filter((x) => x.id !== id))} />
        <div class="tabs">
          ${LEFT_TABS.map(([id, label]) => html`<button key=${id}
            class=${"tab" + (leftTab === id ? " active" : "")} onClick=${() => setLeftTab(id)}>
            ${label}
            ${id === "workflows" && attention.length > 0 && html`<span class="tab-badge"
              title=${`${attention.length} workflow${attention.length === 1 ? "" : "s"} waiting on you`}>${attention.length}</span>`}
            ${id === "eval" && unreadEval > 0 && html`<span class="tab-badge"
              title=${`${unreadEval} new evaluation answer${unreadEval === 1 ? "" : "s"}`}>${unreadEval}</span>`}
          </button>`)}
        </div>
        ${leftTab === "workflows" && html`<div>
          <${WorkflowIntake} extras=${extraModels} stageCfg=${configView?.effective?.Workflow?.Stages}
            defaults=${wfDefaults} onDefaults=${setWfDefaults}
            onCreated=${async (wf) => { setSelected(wf.ID); await refresh(); }} />
          <${ValidationCard} validation=${validation}
            onValidate=${async () => {
              try { const run = await api.startValidation(); setSelected(run.ID); } catch (e) { alert(e.message); }
              await refresh();
            }}
            onToggleAuto=${async (on) => {
              setValidation((v) => v ? { ...v, autoValidate: on } : v);
              try { await api.setAutoValidate(on); } catch (e) { alert(e.message); }
              await refresh();
            }}
            onSelect=${setSelected} />
          <${WorkflowList} workflows=${workflows} selected=${selected} onSelect=${setSelected} />
        </div>`}
        ${leftTab === "ideas" && html`<${IdeasPanel} tasks=${tasks}
          onAdd=${(t) => act(() => api.addTask(t))}
          onPromote=${promoteIdea}
          onDelete=${(t) => act(() => api.deleteTask(t.id))} />`}
        ${leftTab === "goal" && html`<div>
          <${GoalCard} goal=${goal} onSave=${async (text) => { await api.setGoal(text); setGoal(text); }} />
          <${PromptFragmentCard} />
        </div>`}
        ${leftTab === "eval" && html`<${GoalEvaluation} inquiries=${inquiries} running=${evaluatingGoal}
          extras=${extraModels} onEvaluate=${onEvaluateGoal} />`}
      </div>
      <div>
        ${selected
          ? html`<${WorkflowDetail} id=${selected} extras=${extraModels}
              stageCfg=${configView?.effective?.Workflow?.Stages} live=${wfLive}
              onClose=${() => setSelected(null)} />`
          : html`<div>
              <${ActivityFeed} feed=${feed} />
              <${Commits} commits=${status?.recentCommits} />
              <${ArtJobCard} log=${logs["art"]} />
              <${InquiryCard} inquiries=${inquiries} log=${logs["inquiry"]} extras=${extraModels}
                onAsk=${(question, bundle) => act(() => api.ask(question, bundle))}
                onStop=${() => act(() => api.stopInquiry())} />
            </div>`}
      </div>
    </div>
  </div>`;
}

render(html`<${App} />`, document.getElementById("app"));
