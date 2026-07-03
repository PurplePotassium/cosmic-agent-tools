import { h, render } from "preact";
import { useState, useEffect, useRef, useCallback } from "preact/hooks";
import htm from "htm";
import { api, subscribe } from "/api.js";

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
  if (/conflict|suspected|skipped|ignored|stuck/.test(type)) return "warn";
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
    case "auth.halt": return `AUTH FAILURE — pipeline halted (${p.agent})`;
    case "auth.suspected": return `suspected auth/model problem: ${p.note}`;
    case "wedge.killed": return `wedged pass killed after ${p.timeoutMin}m`;
    case "gate.red": return `gate RED (${p.where})`;
    case "driver.effort_ignored": return `effort "${p.effort}" ignored — ${p.agent} has no effort knob`;
    case "pipeline.bundle": return p.cleared ? "model override cleared"
      : `model override → ${[p.agent, p.model, p.effort].filter(Boolean).join(":")}`;
    case "integration.merge_failed": return `merge failed (will retry): ${p.error || ""}`.slice(0, 140);
    default: return JSON.stringify(p).slice(0, 120);
  }
}

// ---------- components ----------

function TopBar({ status, connected, onStopServer }) {
  return html`<div class="topbar">
    <h1>Workshop</h1>
    <span class="muted mono">${status?.repo || ""}</span>
    <span class="spacer"></span>
    <span><span class=${"dot " + (connected ? "on" : "off")}></span>${connected ? "live" : "reconnecting…"}</span>
    <button class="danger" onClick=${onStopServer} title="Stop the workshop server and all loops">stop server</button>
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
    <textarea value=${text} onInput=${(e) => { setText(e.target.value); setDirty(true); }}></textarea>
    ${dirty && html`<div style="margin-top:6px; display:flex; gap:6px;">
      <button class="primary" onClick=${async () => { await onSave(text); setDirty(false); }}>save</button>
      <button onClick=${() => { setText(goal); setDirty(false); }}>discard</button>
    </div>`}
  </div>`;
}

function AddTask({ pipelines, types, onAdd }) {
  const [title, setTitle] = useState("");
  const [type, setType] = useState("");
  const [backlog, setBacklog] = useState(SHARED);
  const submit = async (e) => {
    e.preventDefault();
    if (!title.trim()) return;
    await onAdd({ title: title.trim(), type: type || undefined, backlog });
    setTitle("");
  };
  return html`<form class="addtask" onSubmit=${submit}>
    <div class="row">
      <input name="title" placeholder="add a task…" value=${title} onInput=${(e) => setTitle(e.target.value)} />
    </div>
    <div class="row">
      <select value=${type} onChange=${(e) => setType(e.target.value)} title="task type (empty = auto-classified)">
        <option value="">auto type</option>
        ${types.map((t) => html`<option value=${t}>${t}</option>`)}
      </select>
      <select value=${backlog} onChange=${(e) => setBacklog(e.target.value)} title="which backlog">
        <option value=${SHARED}>${SHARED}</option>
        ${pipelines.map((p) => html`<option value=${p.name}>${p.name}</option>`)}
      </select>
      <button class="primary" type="submit">add</button>
    </div>
  </form>`;
}

function TaskRow({ task, pipelines, onTop, onMove, onDelete }) {
  const pinLabel = task.pin && (task.pin.agent || task.pin.model)
    ? [task.pin.agent, task.pin.model, task.pin.effort].filter(Boolean).join(":") : null;
  return html`<div class="task-row">
    <span class="title" title=${task.detail || task.title}>${task.title}</span>
    ${task.type && html`<span class="chip type">${task.type}</span>`}
    ${pinLabel && html`<span class="chip pin" title="pinned bundle">${pinLabel}</span>`}
    ${task.status === "claimed" && html`<span class="chip claimed">▶ ${task.claimedBy}</span>`}
    ${task.status === "stuck" && html`<span class="chip stuck">stuck ×${task.attempts}</span>`}
    <span class="actions">
      <button title="move to top" onClick=${() => onTop(task)}>↑</button>
      <select title="move to backlog" onChange=${(e) => { if (e.target.value) onMove(task, e.target.value); e.target.value = ""; }}>
        <option value="">⇢</option>
        <option value=${SHARED}>${SHARED}</option>
        ${pipelines.map((p) => html`<option value=${p.name}>${p.name}</option>`)}
      </select>
      <button class="danger" title="delete" onClick=${() => onDelete(task)}>✕</button>
    </span>
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

// BundleEditor is the live agent/model dial: it writes a store-backed
// override the worker re-reads every pass, so the NEXT pass switches with no
// restart (the successor of the old agent.json workflow).
function BundleEditor({ p, onApply, onClear, onClose }) {
  const o = p.override || {};
  const [agent, setAgent] = useState(o.agent || "");
  const [model, setModel] = useState(o.model || "");
  const [effort, setEffort] = useState(o.effort || "");
  return html`<div class="bundle-editor">
    <select value=${agent} onChange=${(e) => setAgent(e.target.value)} title="agent ('' = configured)">
      ${AGENTS.map((a) => html`<option value=${a}>${a || "agent (config)"}</option>`)}
    </select>
    <input placeholder="model (agent default)" value=${model} onInput=${(e) => setModel(e.target.value)} />
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

function PipelineCard({ p, log, onDesired, onBundle }) {
  const [editBundle, setEditBundle] = useState(false);
  const running = !!p.running;
  const stopped = p.halted === "operator";
  const halted = p.halted && p.halted !== "operator";
  const stateClass = halted ? "halted" : stopped ? "stopped" : "";
  const pill = running
    ? html`<span class="pill running">pass ${p.running.N} · ${elapsed(p.running.Started)}</span>`
    : halted ? html`<span class="pill halted">HALTED: ${p.halted}</span>`
    : stopped ? html`<span class="pill stopped">stopped</span>`
    : html`<span class="pill idle">idle</span>`;
  const bundle = [p.agent, p.model, p.effort].filter(Boolean).join(" · ");
  const blind = p.agent === "agy";
  const staleReport = p.progressAgeSec > 300 && running;
  return html`<div class=${"card pipeline-card " + stateClass}>
    <div class="pipeline-head">
      <span class="name">${p.name}</span>
      ${pill}
      <span class="chip" title=${p.override ? "live override active — applies from the next pass" : "configured bundle"}>
        ${bundle}${p.override ? " ⚡" : ""}</span>
      ${p.backlogExclusive > 0 && html`<span class="chip">own backlog: ${p.backlogExclusive}</span>`}
      <span class="spacer"></span>
      <button title="switch agent/model for the next pass" onClick=${() => setEditBundle((v) => !v)}>⚙</button>
      ${stopped || halted
        ? html`<button class="primary" onClick=${() => onDesired(p.name, "running")}>resume</button>`
        : html`<button onClick=${() => onDesired(p.name, "stopped")}>stop</button>`}
    </div>
    ${editBundle && html`<${BundleEditor} p=${p}
      onApply=${async (b) => { await onBundle(p.name, b); setEditBundle(false); }}
      onClear=${async () => { await onBundle(p.name, {}); setEditBundle(false); }}
      onClose=${() => setEditBundle(false)} />`}
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

// ---------- root ----------

const ALERT_TYPES = {
  "auth.halt": "bad",
  "auth.suspected": "warn",
  "breaker.tripped": "bad",
  "wedge.killed": "warn",
  "task.stuck": "warn",
  "gate.red": "warn",
};

function App() {
  const [status, setStatus] = useState(null);
  const [tasks, setTasks] = useState([]);
  const [goal, setGoal] = useState("");
  const [queue, setQueue] = useState([]);
  const [feed, setFeed] = useState([]);
  const [logs, setLogs] = useState({});
  const [alerts, setAlerts] = useState([]);
  const [connected, setConnected] = useState(false);
  const refreshTimer = useRef(null);

  const refresh = useCallback(async () => {
    try {
      const [st, ts, q] = await Promise.all([api.status(), api.tasks(), api.queue()]);
      setStatus(st);
      setTasks(ts);
      setQueue(q || []);
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
        const tone = ALERT_TYPES[ev.type];
        if (tone) {
          setAlerts((prev) => [...prev, {
            id: `${ev.seq}-${ev.type}`, tone, pipeline: ev.pipeline, text: describeEvent(ev),
          }].slice(-6));
        }
        if (ev.type === "pass.started") {
          setLogs((prev) => ({ ...prev, [ev.pipeline]: [] }));
        }
        scheduleRefresh();
      },
    });
    return () => { clearInterval(poll); close(); };
  }, []);

  const pipelines = status?.pipelines || [];
  const cfgTypes = [...new Set([
    ...(tasks.map((t) => t.type).filter(Boolean)),
    "code", "tests", "docs", "art", "audio",
  ])];

  const act = async (fn) => { try { await fn(); } catch (e) { alert(e.message); } await refresh(); };

  return html`<div>
    <${TopBar} status=${status} connected=${connected}
      onStopServer=${() => act(() => api.stopServer())} />
    <div class="columns">
      <div>
        <${Alerts} alerts=${alerts} dismiss=${(id) => setAlerts((a) => a.filter((x) => x.id !== id))} />
        <${GoalCard} goal=${goal} onSave=${async (text) => { await api.setGoal(text); setGoal(text); }} />
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
        <${Completions} completions=${status?.completions} />
      </div>
      <div>
        ${pipelines.map((p) => html`<${PipelineCard} key=${p.name} p=${p}
          log=${logs[p.name]} onDesired=${(name, desired) => act(() => api.setPipeline(name, desired))}
          onBundle=${(name, bundle) => act(() => api.setPipelineBundle(name, bundle))} />`)}
        ${pipelines.length === 0 && html`<div class="card muted">no pipelines configured</div>`}
      </div>
      <div>
        <${QueuePanel} queue=${queue} />
        <${ActivityFeed} feed=${feed} />
        <${Commits} commits=${status?.recentCommits} />
      </div>
    </div>
  </div>`;
}

render(html`<${App} />`, document.getElementById("app"));
