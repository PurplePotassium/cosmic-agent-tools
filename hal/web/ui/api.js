// Thin API client: token handling + fetch helpers + the SSE stream.

const TOKEN_KEY = "hal-token";

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
  headers["X-Hal-Token"] = token();
  const resp = await fetch(path, {
    method,
    headers,
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  if (!resp.ok) {
    let msg = resp.statusText;
    try { msg = (await resp.json()).error || msg; } catch { /* not json */ }
    const err = new Error(msg);
    // Callers branch on 409s (artifact conflicts, turn-in-flight, busy art
    // job) — carry the status through.
    err.status = resp.status;
    throw err;
  }
  const ct = resp.headers.get("Content-Type") || "";
  return ct.includes("json") ? resp.json() : resp.text();
}

export const api = {
  status: () => req("GET", "/api/v1/status"),
  config: () => req("GET", "/api/v1/config"),
  goal: () => req("GET", "/api/v1/goal"),
  setGoal: (goal) => req("PUT", "/api/v1/goal", { goal }),
  // Prompt fragments (.hal/prompts/<name>.md) — the "project" fragment
  // rides along in every workflow stage prompt.
  fragment: (name) => req("GET", `/api/v1/prompts/${name}`),
  setFragment: (name, content) => req("PUT", `/api/v1/prompts/${name}`, { content }),

  // The ideas inbox: lightweight task rows waiting to be promoted into
  // workflows (POST /workflows { fromTaskId }).
  tasks: () => req("GET", "/api/v1/tasks"),
  addTask: (t) => req("POST", "/api/v1/tasks", t),
  deleteTask: (id) => req("DELETE", `/api/v1/tasks/${id}`),
  // uploadAttachment saves a pasted/attached image ahead of idea creation and
  // returns its absolute path, for embedding in the idea's detail.
  uploadAttachment: (name, dataUrl) => req("POST", "/api/v1/tasks/attachments", { name, dataUrl }),

  // ---- workflows: one live conversation through six human-gated stages ----
  workflows: () => req("GET", "/api/v1/workflows?all=1"),
  createWorkflow: (body) => req("POST", "/api/v1/workflows", body),
  workflow: (id) => req("GET", `/api/v1/workflows/${id}`),
  workflowMessages: (id, after) =>
    req("GET", `/api/v1/workflows/${id}/messages${after ? "?after=" + after : ""}`),
  // interrupt=true kills the in-flight turn; the message steers the resume.
  postWorkflowMessage: (id, text, interrupt) =>
    req("POST", `/api/v1/workflows/${id}/messages`, { text, interrupt: !!interrupt }),
  interruptWorkflow: (id) => req("POST", `/api/v1/workflows/${id}/interrupt`, {}),
  approveStage: (id, stage, note) =>
    req("POST", `/api/v1/workflows/${id}/approve`, { stage, note: note || undefined }),
  rejectStage: (id, stage, feedback) =>
    req("POST", `/api/v1/workflows/${id}/reject`, { stage, feedback }),
  skipStage: (id, stage) => req("POST", `/api/v1/workflows/${id}/skip`, { stage }),
  abandonWorkflow: (id) => req("POST", `/api/v1/workflows/${id}/abandon`, {}),
  artifact: (id, stage) => req("GET", `/api/v1/workflows/${id}/artifacts/${stage}`),
  // baseHash is the hash the editor loaded; the server 409s if the file
  // changed since (agent or IDE) instead of silently clobbering.
  saveArtifact: (id, stage, content, baseHash) =>
    req("PUT", `/api/v1/workflows/${id}/artifacts/${stage}`, { content, baseHash }),
  workflowDiff: (id) => req("GET", `/api/v1/workflows/${id}/diff`),
  // Per-workflow model/effort override ({} clears back to stage defaults).
  setWorkflowBundle: (id, bundle) => req("PUT", `/api/v1/workflows/${id}/bundle`, bundle),

  runs: (pipeline) => req("GET", `/api/v1/runs${pipeline ? "?pipeline=" + pipeline : ""}`),
  runLog: (id) => req("GET", `/api/v1/runs/${id}/log`),
  // The self-evaluator: ask a read-only forensics agent WHY the hal did
  // something. One inquiry runs at a time (409 while busy); the answer
  // streams as inquiry.log events and lands in the inquiries list.
  inquiries: () => req("GET", "/api/v1/inquiries"),
  // bundle ({agent, model, effort}, all optional) picks the agent/model/effort
  // for this one question; omitted fields fall back to the configured route.
  ask: (question, bundle) => req("POST", "/api/v1/inquiries", { question, bundle }),
  stopInquiry: () => req("POST", "/api/v1/inquiries/stop", {}),
  // haltServer kills every in-flight turn right now but leaves the server
  // (and every workflow's ability to resume) alive.
  haltServer: () => req("POST", "/api/v1/server/halt", {}),
  // stopServer shuts the whole process down (the CLI `hal stop` twin).
  stopServer: () => req("POST", "/api/v1/server/stop", {}),
  // Start one art-generation job (claude orchestrates agy; see the art card).
  // 409 while one is already running.
  artJob: (job) => req("POST", "/api/v1/art/jobs", job),
  // Art-generation settings: the live green/blue-screen keyer list used by
  // transparent art jobs (ordered — the first keyer's output becomes the
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
  // onmessage misses named events, so route the ones we render. Anything not
  // listed here is invisible to the dashboard — extend this when the server
  // grows a new event type.
  const types = [
    // workflow lifecycle (persisted; the event's `pipeline` field carries the
    // workflow id)
    "workflow.created", "workflow.status", "workflow.stage_started",
    "workflow.turn_started", "workflow.turn_finished", "workflow.message",
    "workflow.question", "workflow.ready", "workflow.blocked", "workflow.error",
    "workflow.approved", "workflow.rejected", "workflow.skipped", "workflow.abandoned",
    "workflow.gate", "workflow.tree_violation", "workflow.commit",
    // workflow live stream (ephemeral, seq 0): coalesced assistant text +
    // one-line tool summaries for the active turn
    "workflow.delta", "workflow.tool",
    // art jobs ride the pass vocabulary
    "pass.started", "pass.finished", "pass.setup_failed", "pass.log",
    "commit", "commit.failed", "wedge.killed", "export.failed",
    "art.generated", "art.rescreened", "art.keyed", "art.attempt_failed",
    "art.blocked", "art.done", "art.remover", "art.model_verified",
    "art.models_missing", "art.models_unverified", "art.normalized",
    "art.keyer_compare", "art.keyer_compare_failed",
    "driver.model_unknown",
    "inquiry.log", "inquiry.asked", "inquiry.answered",
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
