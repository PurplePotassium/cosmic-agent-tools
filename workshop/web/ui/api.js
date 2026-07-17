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
  // Reads are token-gated too (repo intelligence lives behind them), so send
  // the token on every request, not just mutations.
  headers["X-Workshop-Token"] = token();
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
  // addPipeline adds a new parallel-worktree agent lane to the runtime
  // config; deletePipeline removes one. Neither takes effect until the next
  // `workshop up`/`run` — the engine only builds worker/worktree at startup.
  addPipeline: (p) => req("POST", "/api/v1/pipelines", p),
  deletePipeline: (name) => req("DELETE", `/api/v1/pipelines/${name}`),
  // Live agent/model/effort override for the pipeline's NEXT pass; an empty
  // bundle {} clears it back to the configured routing.
  setPipelineBundle: (name, bundle) => req("PATCH", `/api/v1/pipelines/${name}`, { bundle }),
  // Live goal/discover/drain override for the pipeline's NEXT pass; an empty
  // mode "" clears it back to the configured mode.
  setPipelineMode: (name, mode) => req("PATCH", `/api/v1/pipelines/${name}`, { mode }),
  // Live personality override for the pipeline's NEXT pass ("none"/"random"/a
  // roster entry); an empty personality "" clears it back to the configured
  // selector.
  setPipelinePersonality: (name, personality) => req("PATCH", `/api/v1/pipelines/${name}`, { personality }),
  // The self-evaluator: ask a read-only forensics agent WHY the workshop did
  // something. One inquiry runs at a time (409 while busy); the answer
  // streams as inquiry.log events and lands in the inquiries list.
  inquiries: () => req("GET", "/api/v1/inquiries"),
  // bundle ({agent, model, effort}, all optional) picks the agent/model/effort
  // for this one question; omitted fields fall back to the configured route.
  ask: (question, bundle) => req("POST", "/api/v1/inquiries", { question, bundle }),
  stopInquiry: () => req("POST", "/api/v1/inquiries/stop", {}),
  // haltServer kills every in-flight pass right now but leaves the server
  // (and every parked pipeline's ability to be resumed later) alive.
  haltServer: () => req("POST", "/api/v1/server/halt", {}),
  // pauseAfter stops every pipeline from claiming new work, letting whatever
  // they're currently running finish untouched.
  pauseAfter: () => req("POST", "/api/v1/server/pause-after", {}),
  // Art-generation settings: the live green/blue-screen keyer list used by
  // art-gen-trans passes (ordered — the first keyer's output becomes the
  // committed asset, the rest key archived comparison copies) plus the
  // verified agy art model. setArtKeyers([]) clears the override back to the
  // configured default.
  art: () => req("GET", "/api/v1/art"),
  setArtKeyers: (keyers) => req("PUT", "/api/v1/art/keyers", { keyers }),
  // Re-run the agy art-model verification (after logging agy in); progress
  // shows as art.verifying, the result arrives as an art.model_verified /
  // art.models_missing event.
  verifyArtModels: () => req("POST", "/api/v1/art/verify-models", {}),
  // Environment snapshot for the settings panel: tool installs/versions/
  // login signals, resolved paths, transcript-export destination. fresh=true
  // bypasses the server's probe cache.
  env: (fresh) => req("GET", "/api/v1/env" + (fresh ? "?fresh=1" : "")),
};

// attachmentURL builds the token-carrying URL for an attachment thumbnail. An
// <img> can't set a header, so the read guard accepts the token as a query
// parameter for this route (and the SSE route) only.
export function attachmentURL(name) {
  return `/api/v1/attachments/${encodeURIComponent(name)}?token=${encodeURIComponent(token())}`;
}

// subscribe wires the SSE stream; handlers: { onEvent(ev), onLog(ev), onOpen, onDown }.
export function subscribe(handlers) {
  // EventSource can't set headers, so the token rides a query parameter here.
  const es = new EventSource("/api/v1/events?token=" + encodeURIComponent(token()));
  es.onopen = () => handlers.onOpen && handlers.onOpen();
  es.onerror = () => handlers.onDown && handlers.onDown();
  // The server names every SSE event by its type; a catch-all via
  // onmessage misses named events, so route the ones we render.
  const types = [
    "pass.started", "pass.finished", "pass.log",
    "task.created", "task.claimed", "task.done", "task.failed", "task.stuck", "task.classified",
    "commit", "commit.failed",
    "integration.landed", "integration.dropped", "integration.conflict",
    "integration.sync_conflict", "integration.skipped", "integration.drain_incomplete",
    "conflict.enqueued", "conflict.resolved", "conflict.attempt_failed", "conflict.abandoned",
    "breaker.tripped", "auth.halt", "auth.suspected", "wedge.killed",
    "gate.red", "driver.effort_ignored", "driver.model_unknown", "pass.setup_failed",
    "pipeline.bundle", "integration.merge_failed",
    "pipeline.needs_restart", "pipeline.mode", "pipeline.personality",
    "integration.error", "proposals.dropped", "proposals.ingest_failed",
    "inquiry.log", "inquiry.asked", "inquiry.answered",
    "art.generated", "art.rescreened", "art.keyed", "art.attempt_failed",
    "art.route_forced", "art.remover", "art.model_verified",
    "art.models_missing", "art.models_unverified", "art.normalized",
    "art.keyer_compare", "art.keyer_compare_failed",
  ];
  for (const t of types) {
    es.addEventListener(t, (msg) => {
      let ev;
      try { ev = JSON.parse(msg.data); } catch { return; }
      if (t === "pass.log" || t === "inquiry.log") handlers.onLog && handlers.onLog(ev);
      else handlers.onEvent && handlers.onEvent(ev);
    });
  }
  return () => es.close();
}
