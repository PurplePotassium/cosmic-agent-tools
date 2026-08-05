// Workflow UI: the intake form, the validation card, the workflow list with
// its stage stepper, and the detail view (chat pane + artifact pane +
// approval bar). One live Claude conversation per workflow — five
// human-gated stages for a task, one validate stage for a validation run.
import { h, Fragment } from "preact";
import { useState, useEffect, useRef, useCallback } from "preact/hooks";
import htm from "htm";
import { api } from "/api.js";
import { renderMarkdown, countCheckboxes } from "/markdown.js";

const html = htm.bind(h);

// STAGES is the task ladder. Validation is not a per-workflow stage anymore:
// a validation run (Kind === "validation") lives entirely in "validate".
export const STAGES = ["refine", "research", "design", "plan", "implement"];
export const VALIDATE = "validate";

// isValidation: is this workflow a validation run (vs a task workflow)?
export const isValidation = (w) => w && w.Kind === "validation";

// stagesFor: the stage list a workflow's tabs/steppers render.
const stagesFor = (w) => (isValidation(w) ? [VALIDATE] : STAGES);

// timeSet: a Go time.Time JSON value that isn't the zero value.
const timeSet = (iso) => !!iso && !iso.startsWith("0001-");

// Statuses where the ball is in the operator's court — the title-badge /
// chime set.
export const AWAITING = new Set(["awaiting-user", "awaiting-approval", "blocked"]);

const STATUS_META = {
  "turn-running": { label: "agent working", cls: "running" },
  "awaiting-user": { label: "waiting on you", cls: "user" },
  "awaiting-approval": { label: "review & approve", cls: "approve" },
  blocked: { label: "blocked", cls: "bad" },
  error: { label: "error", cls: "bad" },
  completed: { label: "completed", cls: "done" },
  abandoned: { label: "abandoned", cls: "done" },
};

// agoShort: compact age with no suffix ("32s", "5m", "2h").
export function agoShort(iso) {
  if (!iso) return "";
  const s = Math.max(0, (Date.now() - new Date(iso).getTime()) / 1000);
  if (s < 60) return `${s | 0}s`;
  if (s < 3600) return `${(s / 60) | 0}m`;
  return `${(s / 3600) | 0}h`;
}

// ago: "5m ago" — the feed/list flavor.
export function ago(iso) {
  return iso ? `${agoShort(iso)} ago` : "";
}

export function StatusBadge({ status }) {
  const meta = STATUS_META[status] || { label: status, cls: "" };
  return html`<span class=${"status-badge " + meta.cls}>${meta.label}</span>`;
}

// CLAUDE_MODELS: one representative id per claude family (workflows always
// run the interactive claude driver). Extended by [agents.claude]
// extra_models via the extras prop.
export const CLAUDE_MODELS = [
  "claude-sonnet-5", "claude-fable-5", "claude-opus-4-8", "claude-haiku-4-5-20251001",
];
export const WF_EFFORTS = ["", "low", "medium", "high", "xhigh", "max"];

const claudeModels = (extras) =>
  [...new Set([...CLAUDE_MODELS, ...((extras || {}).claude || [])])];

// DEFAULT_CLAUDE_MODEL mirrors internal/driver.DefaultClaudeModel — what a
// stage runs when neither the workflow's bundle nor [workflow.stages.<stage>]
// names a model.
export const DEFAULT_CLAUDE_MODEL = "claude-sonnet-5";

// shortModel compresses a model id for labels: "claude-sonnet-5" →
// "sonnet-5", "claude-haiku-4-5-20251001" → "haiku-4-5".
export const shortModel = (id) => (id || "").replace(/^claude-/, "").replace(/-\d{8}$/, "");

// stageDefaultInfo names what the blank model option ("stage default")
// actually evaluates to: one short name when all six stages agree, "varies"
// (with the per-stage breakdown in the tooltip) when [workflow.stages.*]
// splits them. stageCfg is the effective [workflow.stages] table from
// /api/v1/config (may be absent until the config fetch lands).
export function stageDefaultInfo(stageCfg) {
  const per = STAGES.map((s) => ((stageCfg || {})[s] || {}).Model || DEFAULT_CLAUDE_MODEL);
  const uniq = [...new Set(per)];
  if (uniq.length === 1) {
    return {
      label: `model (stage default: ${shortModel(uniq[0])})`,
      title: `'' = stage default — every stage runs ${uniq[0]}`,
    };
  }
  return {
    label: "model (stage default: varies)",
    title: "'' = stage defaults — " + STAGES.map((s, i) => `${s}: ${shortModel(per[i])}`).join(" · "),
  };
}

// cleanStageBundles keeps only per-stage entries with something actually set
// — the shape sent to the server and counted for the intake chip.
export function cleanStageBundles(stages) {
  const out = {};
  for (const [s, b] of Object.entries(stages || {})) {
    const model = (b && b.model) || "";
    const effort = (b && b.effort) || "";
    if (model || effort) out[s] = { model, effort };
  }
  return out;
}

// WfStageMatrix renders one model/effort row per stage — the per-flow
// overrides that beat the base pair for exactly that stage. Blank options
// name what the stage inherits (the base pick, else the [workflow.stages]
// config default). ladder defaults to the task stages; a validation run's
// popover passes ["validate"].
export function WfStageMatrix({ extras, stageCfg, ladder, baseModel, baseEffort, stages, onChange }) {
  const models = claudeModels(extras);
  const set = (stage, patch) => {
    const cur = (stages || {})[stage] || { model: "", effort: "" };
    onChange({ ...(stages || {}), [stage]: { ...cur, ...patch } });
  };
  return html`<div class="stage-matrix">
    ${(ladder || STAGES).map((s) => {
      const b = (stages || {})[s] || {};
      const cfg = (stageCfg || {})[s] || {};
      const inheritModel = baseModel || cfg.Model || DEFAULT_CLAUDE_MODEL;
      const inheritEffort = baseEffort || cfg.Effort || "";
      const offList = b.model && !models.includes(b.model);
      return html`<div class="stage-matrix-row" key=${s}>
        <span class="stage-matrix-name">${s}</span>
        <select class="model" value=${b.model || ""}
          title=${`model for the ${s} stage ('' = inherit ${inheritModel})`}
          onChange=${(e) => set(s, { model: e.target.value })}>
          <option value="">model (${shortModel(inheritModel)})</option>
          ${offList && html`<option value=${b.model}>${b.model}</option>`}
          ${models.map((m) => html`<option value=${m}>${m}</option>`)}
        </select>
        <select value=${b.effort || ""}
          title=${`effort for the ${s} stage ('' = inherit ${inheritEffort || "default"})`}
          onChange=${(e) => set(s, { effort: e.target.value })}>
          <option value="">effort (${inheritEffort || "default"})</option>
          ${WF_EFFORTS.filter(Boolean).map((ef) => html`<option value=${ef}>${ef}</option>`)}
        </select>
      </div>`;
    })}
  </div>`;
}

// WfBundlePicker is the claude model/effort pair every workflow surface
// shares (intake advanced row, per-workflow override popover, settings).
export function WfBundlePicker({ extras, stageCfg, model, effort, onModel, onEffort }) {
  const models = claudeModels(extras);
  const def = stageDefaultInfo(stageCfg);
  // A persisted pick can name an off-list model (extra_models edited since
  // it was saved) — keep it selectable instead of snapping to the default
  // (same discipline as ModelSelect).
  const offList = model && !models.includes(model);
  return html`<${Fragment}>
    <select class="model" value=${model} onChange=${(e) => onModel(e.target.value)} title=${def.title}>
      <option value="">${def.label}</option>
      ${offList && html`<option value=${model}>${model}</option>`}
      ${models.map((m) => html`<option value=${m}>${m}</option>`)}
    </select>
    <select value=${effort} onChange=${(e) => onEffort(e.target.value)} title="effort ('' = stage defaults)">
      ${WF_EFFORTS.map((ef) => html`<option value=${ef}>${ef || "effort (default)"}</option>`)}
    </select>
  <//>`;
}

// StageStepper: five segments — ✓ approved · ⊘ skipped · ● active (pulsing) ·
// ○ pending. With stage rows (the detail view) the truth is exact; from a
// bare list row, stages before the current one read as done. names overrides
// the ladder (a validation run renders its single validate segment).
export function StageStepper({ stage, stages, names }) {
  const ladder = names || STAGES;
  const cur = ladder.indexOf(stage);
  return html`<span class="stepper">
    ${ladder.map((name, i) => {
      const row = (stages || []).find((s) => s.Stage === name);
      const st = row ? row.Status : i < cur ? "approved" : i === cur ? "active" : "pending";
      let cls = "pending", sym = "○";
      if (st === "approved") { cls = "ok"; sym = "✓"; }
      else if (st === "skipped") { cls = "skip"; sym = "⊘"; }
      else if (st === "active") { cls = "active"; sym = "●"; }
      return html`<span key=${name} class=${"step " + cls} title=${`${i + 1}/${ladder.length} ${name}: ${st}`}>${sym}</span>`;
    })}
  </span>`;
}

// WorkflowIntake starts a new workflow from a raw ask. The advanced row
// picks a later start stage (earlier stages get stub artifacts) and the
// model/effort the workflow launches with. model/effort are the persisted
// workflow defaults owned by App (the same setting edited under Settings →
// tools & models) — picking here saves the choice for next time. The ladder
// ends at implement; validation happens in cross-workflow validation runs
// (see ValidationCard).
export function WorkflowIntake({ extras, stageCfg, defaults, onDefaults, onCreated }) {
  const [text, setText] = useState("");
  const [adv, setAdv] = useState(false);
  const [startStage, setStartStage] = useState("");
  const [autoApprove, setAutoApprove] = useState(true);
  const [busy, setBusy] = useState(false);
  const model = (defaults && defaults.model) || "";
  const effort = (defaults && defaults.effort) || "";
  const stageBundles = cleanStageBundles(defaults && defaults.stages);
  const overrideCount = Object.keys(stageBundles).length;

  const start = async () => {
    if (!text.trim() || busy) return;
    setBusy(true);
    try {
      const body = { text: text.trim(), autoApprove };
      if (startStage) body.startStage = startStage;
      if (model || effort) body.bundle = { agent: "claude", model: model || undefined, effort: effort || undefined };
      if (overrideCount > 0) body.stageBundles = stageBundles;
      const wf = await api.createWorkflow(body);
      setText(""); setStartStage(""); setAutoApprove(true);
      onCreated && onCreated(wf);
    } catch (e) {
      alert(e.message);
    }
    setBusy(false);
  };

  return html`<div class="card intake">
    <h2>New workflow</h2>
    <textarea rows="3" placeholder="What should we build / fix / understand?"
      value=${text} onInput=${(e) => setText(e.target.value)}
      onKeyDown=${(e) => { if (e.key === "Enter" && !e.shiftKey) { e.preventDefault(); start(); } }}></textarea>
    <div class="row" style="display:flex; gap:6px; margin-top:6px; align-items:center; flex-wrap:wrap;">
      <button class="primary" disabled=${busy || !text.trim()} onClick=${start}>${busy ? "starting…" : "Start"}</button>
      <button onClick=${() => setAdv((v) => !v)} title="start stage, model/effort override">${adv ? "▾ advanced" : "▸ advanced"}</button>
      <label class="muted" style="display:flex; gap:4px; align-items:center; font-size:.8rem"
        title="automatically commit and continue whenever a stage is ready; questions, blockers, errors, missing artifacts, and red validation gates still pause for you">
        <input type="checkbox" checked=${autoApprove}
          onChange=${(e) => setAutoApprove(e.target.checked)} />
        auto-approve ready stages
      </label>
      ${!adv && (model || effort || overrideCount > 0) && html`<span class="chip"
        title="saved model/effort defaults — every new workflow uses them (edit here or in Settings → tools & models)">
        ${[shortModel(model), effort, overrideCount > 0 ? `${overrideCount} per-stage` : ""].filter(Boolean).join(" · ")}</span>`}
    </div>
    ${adv && html`<div>
      <div class="bundle-editor">
        <select value=${startStage} onChange=${(e) => setStartStage(e.target.value)}
          title="Start at a later stage: every earlier stage is stub-skipped (its artifact records the skip and downstream stages treat its open questions as implementer's choice).">
          <option value="">start at refine</option>
          ${STAGES.map((s) => html`<option value=${s}>start at ${s}</option>`)}
        </select>
        <${WfBundlePicker} extras=${extras} stageCfg=${stageCfg} model=${model} effort=${effort}
          onModel=${(m) => onDefaults && onDefaults({ ...defaults, model: m })}
          onEffort=${(ef) => onDefaults && onDefaults({ ...defaults, effort: ef })} />
      </div>
      <${WfStageMatrix} extras=${extras} stageCfg=${stageCfg}
        baseModel=${model} baseEffort=${effort} stages=${(defaults && defaults.stages) || {}}
        onChange=${(stages) => onDefaults && onDefaults({ ...defaults, stages })} />
    </div>`}
  </div>`;
}

// ValidationCard is the validate trigger surface: how many implementations
// await validation, the persisted auto-validate-on-completion toggle, and
// the manual Validate button. A live run is linked, not duplicated — one
// runs at a time.
export function ValidationCard({ validation, onValidate, onToggleAuto, onSelect }) {
  if (!validation) return null;
  const pending = validation.pending || [];
  const active = validation.active;
  return html`<div class="card validation-card">
    <div class="row" style="display:flex; gap:8px; align-items:center;">
      <button class="primary" disabled=${!!active}
        title=${active
          ? "a validation run is already live"
          : pending.length > 0
            ? `validate the ${pending.length} implemented workflow(s) awaiting validation (plus the verify command and the agent-play smoke test)`
            : "nothing is pending — runs a health check of the verify command and the agent-play smoke test"}
        onClick=${onValidate}>Validate</button>
      <span class="muted" style="font-size:.8rem"
        title=${pending.map((w) => w.Title).join("\n") || ""}>
        ${pending.length > 0 ? `${pending.length} awaiting validation` : "nothing awaiting validation"}</span>
      ${active && html`<button class="linkish" title="open the live validation run"
        onClick=${() => onSelect && onSelect(active.ID)}>run live →</button>`}
      <span class="spacer"></span>
      <label class="muted" style="display:flex; gap:4px; align-items:center; font-size:.8rem"
        title="when an implement approval lands and no other stage is executing, start a validation run automatically">
        <input type="checkbox" checked=${!!validation.autoValidate}
          onChange=${(e) => onToggleAuto && onToggleAuto(e.target.checked)} />
        auto-validate on completion
      </label>
    </div>
  </div>`;
}

function WorkflowCard({ w, selected, onSelect }) {
  const waiting = AWAITING.has(w.Status) || w.Status === "error";
  return html`<div class=${"card wf-card " + (selected ? "selected " : "") + (STATUS_META[w.Status]?.cls || "")}
    onClick=${() => onSelect(w.ID)}>
    <div class="wf-card-head">
      <span class="wf-title" title=${w.Brief || w.Title}>${w.Title}</span>
      <${StatusBadge} status=${w.Status} />
    </div>
    <div class="wf-card-sub">
      ${isValidation(w)
        ? html`<span class="chip" title="a validation run — sweeps every implemented-but-unvalidated workflow">validation run</span>`
        : html`<${StageStepper} stage=${w.Stage} />`}
      <span class="muted">${w.Stage}</span>
      ${!isValidation(w) && timeSet(w.Validated) && html`<span class="chip ok"
        title=${"validated by run " + (w.ValidatedBy || "")}>validated</span>`}
      ${w.AutoApprove && html`<span class="chip"
        title="ready stages auto-approve; clarification and failures still pause">auto-approve</span>`}
      ${waiting && html`<span class="chip waiting" title="how long this has been waiting on you">waiting ${agoShort(w.Updated)}</span>`}
      <span class="spacer"></span>
      <span class="muted" style="font-size:.72rem">${ago(w.Updated)}</span>
    </div>
    ${w.Status === "error" && w.Error && html`<div class="wf-card-err">${w.Error}</div>`}
  </div>`;
}

// WorkflowList: live workflows first (newest activity on top), terminal ones
// collapsed at the bottom.
export function WorkflowList({ workflows, selected, onSelect }) {
  const [showDone, setShowDone] = useState(false);
  const byUpdated = (a, b) => new Date(b.Updated) - new Date(a.Updated);
  const live = workflows.filter((w) => w.Status !== "completed" && w.Status !== "abandoned").sort(byUpdated);
  const done = workflows.filter((w) => w.Status === "completed" || w.Status === "abandoned").sort(byUpdated);
  return html`<div>
    ${live.length === 0 && html`<div class="card muted">no live workflows — start one above</div>`}
    ${live.map((w) => html`<${WorkflowCard} key=${w.ID} w=${w} selected=${w.ID === selected} onSelect=${onSelect} />`)}
    ${done.length > 0 && html`<button class="linkish" onClick=${() => setShowDone((v) => !v)}>
      ${showDone ? "▾" : "▸"} ${done.length} finished</button>`}
    ${showDone && done.map((w) => html`<${WorkflowCard} key=${w.ID} w=${w} selected=${w.ID === selected} onSelect=${onSelect} />`)}
  </div>`;
}

// ---------- detail ----------

const STEERING_HINTS = [
  "What are you least confident about right now?",
  "What assumptions did you make that you never stated?",
  "Walk me through the tradeoffs before proceeding.",
  "What's the smallest version that still satisfies the ask?",
  "Just go — use your judgment.",
];

function MessageRow({ m }) {
  switch (m.Kind) {
    case "stage-open":
      return html`<div class="msg-divider"><span>${m.Content}</span></div>`;
    case "gate": {
      const red = m.Content.includes("RED");
      return html`<div class=${"msg-gate mono " + (red ? "red" : "green")}>${m.Content}</div>`;
    }
    case "notice":
      return html`<div class="msg-notice">${m.Content}</div>`;
    case "approval":
      return html`<div class="msg-decision ok">✓ ${m.Content}</div>`;
    case "rejection":
      return html`<div class="msg-decision warn">↺ changes requested — ${m.Content}</div>`;
  }
  const cls = m.Role === "user" ? "user" : m.Role === "assistant" ? "assistant" : "system";
  return html`<div class=${"msg " + cls}>
    ${m.Kind === "interjection" && html`<div class="msg-tag">interjected ⚡</div>`}
    <div class="msg-body">${m.Content}</div>
  </div>`;
}

function ChatPanel({ wf, messages, typing, tools, banner, onSend, onStop }) {
  const [input, setInput] = useState("");
  const scrollRef = useRef(null);
  const inputRef = useRef(null);
  const running = wf.Status === "turn-running";
  const terminal = wf.Status === "completed" || wf.Status === "abandoned";

  useEffect(() => {
    const el = scrollRef.current;
    if (el) el.scrollTop = el.scrollHeight;
  }, [messages.length, typing, tools.length]);

  const send = () => {
    const text = input.trim();
    if (!text) return;
    onSend(text, running);
    setInput("");
  };

  return html`<div class="chat-panel">
    <div class="chat-scroll" ref=${scrollRef}>
      ${messages.map((m) => html`<${MessageRow} key=${m.ID} m=${m} />`)}
      ${(running || typing || tools.length > 0) && html`<div class="msg assistant typing">
        ${tools.map((line, i) => html`<div class="msg-tool" key=${i}>▸ ${line}</div>`)}
        ${typing
          ? html`<div class="msg-body">${typing}<span class="caret">▍</span></div>`
          : html`<div class="msg-body muted">thinking…</div>`}
      </div>`}
    </div>
    ${wf.Status === "awaiting-user" && html`<div class="wf-banner accent">
      the agent is waiting on your answer${banner ? " — " + banner : ""}</div>`}
    ${wf.Status === "blocked" && html`<div class="wf-banner bad">
      blocked — the agent hit a plan/reality mismatch and needs direction${banner ? ": " + banner : ""}</div>`}
    ${wf.Status === "error" && html`<div class="wf-banner bad">
      error: ${wf.Error || "the turn failed"} — send a message to retry</div>`}
    ${!terminal && html`<div class="chat-hints">
      ${STEERING_HINTS.map((hint) => html`<button key=${hint} class="hint-chip"
        onClick=${() => { setInput((v) => (v ? v + " " : "") + hint); inputRef.current && inputRef.current.focus(); }}>${hint}</button>`)}
    </div>`}
    ${!terminal && html`<div class="chat-input">
      <textarea rows="2" ref=${inputRef}
        placeholder=${running
          ? "interject (kills the current turn, your message steers the resume)…"
          : "message the agent… (Enter sends, Shift+Enter for a new line)"}
        value=${input} onInput=${(e) => setInput(e.target.value)}
        onKeyDown=${(e) => { if (e.key === "Enter" && !e.shiftKey) { e.preventDefault(); send(); } }}></textarea>
      <div class="chat-input-side">
        ${running && html`<button class="danger" title="kill the current turn (the session resumes on your next message)" onClick=${onStop}>■ Stop</button>`}
        <button class="primary" disabled=${!input.trim()} onClick=${send}>${running ? "interject" : "send"}</button>
      </div>
    </div>`}
  </div>`;
}

function ApprovalBar({ wf, artifactExists, onApprove, onReject, onSkip, onAbandon }) {
  const [note, setNote] = useState("");
  const [rejecting, setRejecting] = useState(false);
  const [feedback, setFeedback] = useState("");
  if (wf.Status === "completed" || wf.Status === "abandoned") return null;
  const running = wf.Status === "turn-running";
  const blockTitle = running ? "a turn is running — stop it or wait" : null;
  return html`<div class="approval-bar">
    ${wf.Status === "awaiting-approval" && html`<div class="approve-row">
      <input placeholder="approval note (optional)" value=${note} onInput=${(e) => setNote(e.target.value)} />
      <button class="primary approve" onClick=${() => { onApprove(note); setNote(""); }}>✓ Approve & continue</button>
    </div>`}
    <div class="approval-actions">
      ${wf.Status !== "awaiting-approval" && html`<button disabled=${running || !artifactExists}
        title=${blockTitle || (artifactExists ? `approve the ${wf.Stage} artifact as-is` : `the ${wf.Stage} artifact does not exist yet`)}
        onClick=${() => onApprove("")}>approve</button>`}
      <button disabled=${running || !artifactExists}
        title=${blockTitle || (artifactExists ? "ask for a revision of this stage's artifact" : `the ${wf.Stage} artifact does not exist yet`)}
        onClick=${() => setRejecting((v) => !v)}>request changes…</button>
      <button disabled=${running} title=${blockTitle || "skip this stage — a stub artifact records the skip"}
        onClick=${() => { if (confirm(`Skip the ${wf.Stage} stage?`)) onSkip(); }}>skip stage</button>
      <button class="danger" title="abandon this workflow — kills any turn; artifacts stay on disk"
        onClick=${() => { if (confirm("Abandon this workflow? Artifacts stay on disk.")) onAbandon(); }}>abandon</button>
    </div>
    ${rejecting && html`<div class="reject-row">
      <textarea rows="3" placeholder="what should change?" value=${feedback}
        onInput=${(e) => setFeedback(e.target.value)}></textarea>
      <button class="primary" disabled=${!feedback.trim() || running}
        onClick=${() => { onReject(feedback.trim()); setFeedback(""); setRejecting(false); }}>send</button>
      <button onClick=${() => setRejecting(false)}>✕</button>
    </div>`}
  </div>`;
}

// ArtifactPane: stage tabs over the rendered (or raw/edited) artifact, the
// implement diff, and the approval bar.
function ArtifactPane({ id, wf, stages, refreshTick, onApprove, onReject, onSkip, onAbandon }) {
  const [tab, setTab] = useState(wf.Stage);
  const [art, setArt] = useState(null);
  const [missing, setMissing] = useState(false);
  const [raw, setRaw] = useState(false);
  const [edit, setEdit] = useState(false);
  const [editText, setEditText] = useState("");
  const [conflict, setConflict] = useState(false);
  const [diff, setDiff] = useState(null); // null = hidden, string = shown

  // Stage advances pull the pane along.
  useEffect(() => { setTab(wf.Stage); }, [wf.Stage]);
  // Leaving edit mode or switching tabs drops edit state.
  useEffect(() => { setEdit(false); setConflict(false); setDiff(null); setRaw(false); }, [id, tab]);

  const load = useCallback(() => {
    api.artifact(id, tab)
      .then((a) => { setArt(a); setMissing(false); })
      .catch(() => { setArt(null); setMissing(true); });
  }, [id, tab]);
  useEffect(() => { if (!edit) load(); }, [load, refreshTick, edit]);

  const rowFor = (name) => (stages || []).find((s) => s.Stage === name);
  const curRow = rowFor(tab);
  const enabled = (name) => {
    const row = rowFor(name);
    return !!row && (row.ArtifactExists || row.Artifact);
  };

  const save = async () => {
    try {
      const saved = await api.saveArtifact(id, tab, editText, art ? art.hash : "");
      setArt(saved);
      setEdit(false);
      setConflict(false);
    } catch (e) {
      if (e.status === 409) setConflict(true);
      else alert(e.message);
    }
  };

  const progress = tab === "plan" && art ? countCheckboxes(art.content) : null;

  const ladder = stagesFor(wf);

  return html`<div class="artifact-pane">
    <div class="artifact-tabs">
      ${ladder.map((name, i) => {
        const row = rowFor(name);
        const skipped = row && row.Status === "skipped";
        return html`<button key=${name}
          class=${"artifact-tab" + (tab === name ? " active" : "")}
          disabled=${!enabled(name)}
          title=${`${i + 1}. ${name}${skipped ? " (skipped)" : ""}${enabled(name) ? "" : " — no artifact yet"}`}
          onClick=${() => setTab(name)}>${i + 1}${skipped ? "⊘" : ""} ${name}</button>`;
      })}
    </div>
    <div class="artifact-toolbar">
      <span class="mono muted" style="font-size:.72rem; overflow:hidden; text-overflow:ellipsis;">${art ? art.path : ""}</span>
      ${progress && progress.total > 0 && html`<span class="chip progress">${progress.done}/${progress.total} done</span>`}
      <span class="spacer"></span>
      ${(tab === "implement" || tab === VALIDATE) && html`<button class="linkish" onClick=${async () => {
        if (diff !== null) { setDiff(null); return; }
        try { setDiff((await api.workflowDiff(id)).stat || "(no changes)"); } catch (e) { alert(e.message); }
      }}>${diff !== null ? "hide diff" : "diff"}</button>`}
      ${art && !edit && html`<button class="linkish" onClick=${() => setRaw((v) => !v)}>${raw ? "rendered" : "raw"}</button>`}
      ${!edit && html`<button class="linkish" disabled=${!art}
        onClick=${() => { if (art) { setEditText(art.content); setEdit(true); setConflict(false); } }}>edit</button>`}
    </div>
    ${conflict && html`<div class="wf-banner bad">changed on disk since you loaded it — reload?
      <button onClick=${() => { load(); setConflict(false); setEdit(false); }}>reload (drops your edit)</button>
      <button onClick=${() => setConflict(false)}>keep editing</button>
    </div>`}
    <div class="artifact-body">
      ${diff !== null && html`<pre class="diff-view mono">${diff}</pre>`}
      ${diff === null && missing && html`<div class="muted" style="padding:14px">no artifact for ${tab} yet — the agent writes it during the stage.</div>`}
      ${diff === null && art && edit && html`<div class="artifact-edit">
        <textarea rows="20" value=${editText} onInput=${(e) => setEditText(e.target.value)}></textarea>
        <div style="display:flex; gap:6px; margin-top:6px;">
          <button class="primary" onClick=${save}>save</button>
          <button onClick=${() => { setEdit(false); setConflict(false); }}>discard</button>
        </div>
      </div>`}
      ${diff === null && art && !edit && raw && html`<pre class="md-raw mono">${art.content}</pre>`}
      ${diff === null && art && !edit && !raw &&
        html`<div class="md-view" dangerouslySetInnerHTML=${{ __html: renderMarkdown(art.content) }}></div>`}
    </div>
    <${ApprovalBar} wf=${wf} artifactExists=${!!(curRow && curRow.ArtifactExists) && tab === wf.Stage}
      onApprove=${onApprove} onReject=${onReject} onSkip=${onSkip} onAbandon=${onAbandon} />
  </div>`;
}

// BundlePopover: the per-workflow model/effort override (agent is always the
// interactive claude driver) — the all-stages base pair plus per-stage rows
// that beat it for their stage.
function BundlePopover({ wf, extras, stageCfg, onClose }) {
  const b = wf.Bundle || {};
  const [model, setModel] = useState(b.model || "");
  const [effort, setEffort] = useState(b.effort || "");
  const [stages, setStages] = useState(() => ({ ...(wf.StageBundles || {}) }));
  const apply = async () => {
    try {
      const clean = cleanStageBundles(stages);
      const any = model || effort || Object.keys(clean).length > 0;
      const body = any
        ? { agent: "claude", model: model || undefined, effort: effort || undefined, stages: clean }
        : {};
      await api.setWorkflowBundle(wf.ID, body);
      onClose(true);
    } catch (e) {
      alert(e.message);
    }
  };
  return html`<div>
    <div class="bundle-editor">
      <${WfBundlePicker} extras=${extras} stageCfg=${stageCfg} model=${model} effort=${effort}
        onModel=${setModel} onEffort=${setEffort} />
      <button class="primary" onClick=${apply}>apply</button>
      <button onClick=${() => onClose(false)}>✕</button>
    </div>
    <${WfStageMatrix} extras=${extras} stageCfg=${stageCfg} ladder=${stagesFor(wf)}
      baseModel=${model} baseEffort=${effort} stages=${stages} onChange=${setStages} />
  </div>`;
}

// WorkflowDetail: header + chat pane + artifact pane for one workflow.
// `live` is the app's last workflow SSE event, wrapped {n, ev} so every
// event (including ephemeral deltas) changes the prop identity.
export function WorkflowDetail({ id, extras, stageCfg, live, onClose }) {
  const [detail, setDetail] = useState(null);
  const [messages, setMessages] = useState([]);
  const [typing, setTyping] = useState("");
  const [tools, setTools] = useState([]);
  const [banner, setBanner] = useState("");
  const [editBundle, setEditBundle] = useState(false);
  const [refreshTick, setRefreshTick] = useState(0);
  const refetchTimer = useRef(null);

  const refetch = useCallback(async () => {
    try {
      const [d, msgs] = await Promise.all([api.workflow(id), api.workflowMessages(id)]);
      setDetail(d);
      setMessages(msgs || []);
      setRefreshTick((t) => t + 1);
    } catch { /* server briefly away */ }
  }, [id]);

  const scheduleRefetch = useCallback(() => {
    if (refetchTimer.current) return;
    refetchTimer.current = setTimeout(() => {
      refetchTimer.current = null;
      refetch();
    }, 250);
  }, [refetch]);

  useEffect(() => {
    setDetail(null); setMessages([]); setTyping(""); setTools([]); setBanner("");
    refetch();
    return () => { if (refetchTimer.current) clearTimeout(refetchTimer.current); };
  }, [id]);

  // Live SSE events for THIS workflow: deltas stream into the typing bubble,
  // messages append (dedup by id), everything else triggers a coalesced
  // refetch.
  useEffect(() => {
    const ev = live && live.ev;
    if (!ev || ev.pipeline !== id) return;
    const p = ev.payload || {};
    switch (ev.type) {
      case "workflow.delta":
        setTyping((t) => t + (p.text || ""));
        break;
      case "workflow.tool":
        setTools((ts) => [...ts, p.line || ""].slice(-40));
        break;
      case "workflow.message":
        if (p.role === "assistant") setTyping(""); // the settled row replaces the typing bubble
        setMessages((ms) => ms.some((m) => m.ID === p.id) ? ms : [...ms, {
          ID: p.id, WorkflowID: id, Stage: p.stage, Role: p.role, Kind: p.kind,
          Content: p.content, Created: ev.ts,
        }]);
        break;
      case "workflow.turn_started":
        setTyping(""); setTools([]); scheduleRefetch();
        break;
      case "workflow.turn_finished":
        setTyping(""); setTools([]); scheduleRefetch();
        break;
      case "workflow.question":
      case "workflow.blocked":
        setBanner(p.note || ""); scheduleRefetch();
        break;
      case "workflow.ready":
        setBanner(""); scheduleRefetch();
        break;
      default:
        scheduleRefetch();
    }
  }, [live]);

  if (!detail) return html`<div class="card muted">loading workflow…</div>`;
  const wf = detail.workflow;
  const stages = detail.stages || [];
  const cost = (detail.turns || []).reduce((sum, t) => sum + (t.CostUSD || 0), 0);
  const ladder = stagesFor(wf);
  const stageN = ladder.indexOf(wf.Stage) + 1;

  const act = (fn) => async (...args) => {
    try { await fn(...args); } catch (e) { alert(e.message); }
    scheduleRefetch();
  };

  return html`<div class="wf-detail">
    <div class="wf-detail-head">
      <button class="linkish" title="back to the feed" onClick=${onClose}>←</button>
      <span class="wf-title" title=${wf.Brief}>${wf.Title}</span>
      ${isValidation(wf)
        ? html`<span class="muted" title="a validation run — sweeps every implemented-but-unvalidated workflow">validation run</span>`
        : html`<span class="muted">stage ${stageN}/${ladder.length} · ${wf.Stage}</span>`}
      <${StageStepper} stage=${wf.Stage} stages=${stages} names=${ladder} />
      <${StatusBadge} status=${wf.Status} />
      ${!isValidation(wf) && timeSet(wf.Validated) && html`<span class="chip ok"
        title=${"validated by run " + (wf.ValidatedBy || "")}>validated</span>`}
      ${wf.AutoApprove && html`<span class="chip"
        title="ready stages auto-approve; clarification and failures still pause">auto-approve</span>`}
      ${cost > 0 && html`<span class="chip" title="total cost of this workflow's turns so far">$${cost.toFixed(2)}</span>`}
      <span class="spacer"></span>
      <button title="per-workflow model/effort override" onClick=${() => setEditBundle((v) => !v)}>⚙</button>
    </div>
    ${editBundle && html`<${BundlePopover} wf=${wf} extras=${extras} stageCfg=${stageCfg}
      onClose=${(applied) => { setEditBundle(false); if (applied) scheduleRefetch(); }} />`}
    <div class="wf-panes">
      <${ChatPanel} wf=${wf} messages=${messages} typing=${typing} tools=${tools} banner=${banner}
        onSend=${act((text, interrupt) => api.postWorkflowMessage(id, text, interrupt))}
        onStop=${act(() => api.interruptWorkflow(id))} />
      <${ArtifactPane} id=${id} wf=${wf} stages=${stages} refreshTick=${refreshTick}
        onApprove=${act((note) => api.approveStage(id, wf.Stage, note))}
        onReject=${act((feedback) => api.rejectStage(id, wf.Stage, feedback))}
        onSkip=${act(() => api.skipStage(id, wf.Stage))}
        onAbandon=${act(() => api.abandonWorkflow(id))} />
    </div>
  </div>`;
}
