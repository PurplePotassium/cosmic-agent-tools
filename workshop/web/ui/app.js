import { h, render } from "preact";
import { useState, useEffect, useRef, useCallback } from "preact/hooks";
import htm from "htm";
import { api, subscribe, attachmentURL } from "/api.js";

const html = htm.bind(h);
const SHARED = "shared";

// Mirrors internal/prompt/compose.go's InventBlock — the instruction a pass
// gets automatically when its backlog is empty and Invent is on. Queuing it
// as a real task via the "invent" button gives operators the same choice
// on demand, without waiting for a pipeline to go idle.
const INVENT_TASK_TITLE = "Invent the next task toward the goal";
const INVENT_TASK_DETAIL = "INVENT the single highest-impact task that moves the GOAL forward, " +
  "then do exactly that one increment. Record what you chose in progress.json's task field. " +
  "If the last few completions are all the same KIND of work, pick a different kind.";

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
  if (/done|landed|resolved|commit$|classified/.test(type)) return "good";
  if (/failed|halt|breaker|wedge|dropped|red|abandoned/.test(type)) return "bad";
  if (/conflict|suspected|skipped|ignored|stuck|incomplete|unknown/.test(type)) return "warn";
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
    case "pipeline.needs_restart": return `added — relaunching the engine to activate this lane; it comes up parked, so resume it to start running`;
    case "integration.merge_failed": return `merge failed (will retry): ${p.error || ""}`.slice(0, 140);
    case "inquiry.asked": return `asked: ${p.question || ""}`;
    case "inquiry.answered": return p.ok ? "inquiry answered" : "inquiry FAILED";
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

function TopBar({ status, connected, active, pauseAfterPending, stopped, soundOn, onHalt, onPauseAfter, onToggleSound }) {
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
      <button class="primary" onClick=${async () => { await onSave(text); setDirty(false); }}>save</button>
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
    for (const file of [...files].filter((f) => f.type.startsWith("image/"))) {
      const dataUrl = await readAsDataURL(file);
      setAttachments((prev) => [...prev, { name: file.name || "pasted-image.png", dataUrl }]);
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
const AGENTS = ["", "claude", "agy"];

// MODEL_FAMILIES mirrors internal/domain's curated prefixes (ClaudeModels,
// AgyModels) with one representative id per family, for the model dropdown
// below. The dropdown is extended at runtime by the user's
// [agents.<agent>] extra_models (fetched from /api/v1/config) — that's how
// off-list ids become selectable now that the field is a select, not a
// free-text input.
const MODEL_FAMILIES = {
  claude: ["claude-sonnet-5", "claude-fable-5", "claude-opus-4-8", "claude-haiku-4-5-20251001"],
  agy: ["gemini-3-flash"],
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
    await onAdd({ name: name.trim(), agent: agent || undefined, model: model.trim() || undefined, effort: effort || undefined });
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

function PipelineCard({ p, log, extras, onDesired, onBundle, onMode, onDelete }) {
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
      last: iter ${p.lastPass.N} ${p.lastPass.Outcome}${p.lastPass.CommitSHA ? " @ " + p.lastPass.CommitSHA : ""}
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
  // extraModels: per-agent [agents.<agent>] extra_models from the config, so
  // the model dropdowns list the user's own additions next to the curated ids.
  const [extraModels, setExtraModels] = useState({});
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
      const [st, ts, q, inqs] = await Promise.all([api.status(), api.tasks(), api.queue(), api.inquiries()]);
      setStatus(st);
      setTasks(ts);
      setQueue(q || []);
      setInquiries(inqs || []);
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
        setFeed((prev) => [ev, ...prev].slice(0, 120));
        if (ev.type === "task.done" && soundOnRef.current) playChime();
        const tone = ALERT_TYPES[ev.type];
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

  const act = async (fn) => { try { await fn(); } catch (e) { alert(e.message); } await refresh(); };

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
      onHalt=${() => act(() => api.haltServer())}
      onPauseAfter=${() => act(() => api.pauseAfter())}
      onToggleSound=${toggleSound} />
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
              title="Queue a task that tells the AI to invent and complete the next highest-impact step toward the goal — the same choice a pipeline makes automatically when its backlog is empty, triggered on demand"
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
          log=${logs[p.name]} onDesired=${(name, desired) => act(() => api.setPipeline(name, desired))}
          onBundle=${(name, bundle) => act(() => api.setPipelineBundle(name, bundle))}
          onMode=${(name, mode) => act(() => api.setPipelineMode(name, mode))}
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
