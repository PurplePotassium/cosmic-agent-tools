// Thin API client: token handling + fetch helpers + the SSE stream.

const TOKEN_KEY = "workshop-token";

// The CLI opens the dashboard with #token=... — capture it once, keep it for
// the session, and clean the URL.
(function captureToken() {
  const m = location.hash.match(/token=([0-9a-f]+)/);
  if (m) {
    sessionStorage.setItem(TOKEN_KEY, m[1]);
    history.replaceState(null, "", location.pathname);
  }
})();

export function token() {
  return sessionStorage.getItem(TOKEN_KEY) || "";
}

async function req(method, path, body) {
  const headers = {};
  if (body !== undefined) headers["Content-Type"] = "application/json";
  if (method !== "GET") headers["X-Workshop-Token"] = token();
  const resp = await fetch(path, {
    method,
    headers,
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  if (!resp.ok) {
    let msg = resp.statusText;
    try { msg = (await resp.json()).error || msg; } catch { /* not json */ }
    throw new Error(msg);
  }
  const ct = resp.headers.get("Content-Type") || "";
  return ct.includes("json") ? resp.json() : resp.text();
}

export const api = {
  status: () => req("GET", "/api/v1/status"),
  config: () => req("GET", "/api/v1/config"),
  goal: () => req("GET", "/api/v1/goal"),
  setGoal: (goal) => req("PUT", "/api/v1/goal", { goal }),
  tasks: () => req("GET", "/api/v1/tasks"),
  addTask: (t) => req("POST", "/api/v1/tasks", t),
  // uploadAttachment saves a pasted/attached image ahead of task creation and
  // returns its absolute path, for embedding in the task's detail.
  uploadAttachment: (name, dataUrl) => req("POST", "/api/v1/tasks/attachments", { name, dataUrl }),
  patchTask: (id, patch) => req("PATCH", `/api/v1/tasks/${id}`, patch),
  deleteTask: (id) => req("DELETE", `/api/v1/tasks/${id}`),
  reorder: (backlog, ids) => req("POST", "/api/v1/tasks/reorder", { backlog, ids }),
  queue: () => req("GET", "/api/v1/queue"),
  runs: (pipeline) => req("GET", `/api/v1/runs${pipeline ? "?pipeline=" + pipeline : ""}`),
  runLog: (id) => req("GET", `/api/v1/runs/${id}/log`),
  setPipeline: (name, desired) => req("PATCH", `/api/v1/pipelines/${name}`, { desired }),
  // Live agent/model/effort override for the pipeline's NEXT pass; an empty
  // bundle {} clears it back to the configured routing.
  setPipelineBundle: (name, bundle) => req("PATCH", `/api/v1/pipelines/${name}`, { bundle }),
  // Live goal/discover/drain override for the pipeline's NEXT pass; an empty
  // mode "" clears it back to the configured mode.
  setPipelineMode: (name, mode) => req("PATCH", `/api/v1/pipelines/${name}`, { mode }),
  // haltServer kills every in-flight pass right now but leaves the server
  // (and every parked pipeline's ability to be resumed later) alive.
  haltServer: () => req("POST", "/api/v1/server/halt", {}),
  // pauseAfter stops every pipeline from claiming new work, letting whatever
  // they're currently running finish untouched.
  pauseAfter: () => req("POST", "/api/v1/server/pause-after", {}),
};

// subscribe wires the SSE stream; handlers: { onEvent(ev), onLog(ev), onOpen, onDown }.
export function subscribe(handlers) {
  const es = new EventSource("/api/v1/events");
  es.onopen = () => handlers.onOpen && handlers.onOpen();
  es.onerror = () => handlers.onDown && handlers.onDown();
  // The server names every SSE event by its type; a catch-all via
  // onmessage misses named events, so route the ones we render.
  const types = [
    "pass.started", "pass.finished", "pass.log",
    "task.created", "task.claimed", "task.done", "task.failed", "task.stuck", "task.classified",
    "commit", "commit.failed",
    "integration.landed", "integration.dropped", "integration.conflict",
    "integration.sync_conflict", "integration.skipped",
    "conflict.enqueued", "conflict.resolved", "conflict.attempt_failed", "conflict.abandoned",
    "breaker.tripped", "auth.halt", "auth.suspected", "wedge.killed",
    "gate.red", "driver.effort_ignored", "pass.setup_failed",
    "pipeline.bundle", "integration.merge_failed",
  ];
  for (const t of types) {
    es.addEventListener(t, (msg) => {
      let ev;
      try { ev = JSON.parse(msg.data); } catch { return; }
      if (t === "pass.log") handlers.onLog && handlers.onLog(ev);
      else handlers.onEvent && handlers.onEvent(ev);
    });
  }
  return () => es.close();
}
