// Thin fetch helpers against the same-origin Go backend. All project-scoped calls
// go through /api/projects/{id}/... . Errors throw so callers can toast them.

async function jget(url) {
  const r = await fetch(url);
  if (!r.ok) throw new Error(`GET ${url} → ${r.status}`);
  return r.json();
}

async function jpost(url, body) {
  const r = await fetch(url, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body || {}),
  });
  const data = await r.json().catch(() => ({}));
  if (!r.ok) {
    const msg = data && data.error ? data.error : `POST ${url} → ${r.status}`;
    const err = new Error(msg);
    err.status = r.status;
    err.data = data;
    throw err;
  }
  return data;
}

async function jpatch(url, body) {
  const r = await fetch(url, {
    method: 'PATCH',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body || {}),
  });
  const data = await r.json().catch(() => ({}));
  if (!r.ok) throw new Error(data.error || `PATCH ${url} → ${r.status}`);
  return data;
}

async function jdelete(url) {
  const r = await fetch(url, { method: 'DELETE' });
  const data = await r.json().catch(() => ({}));
  if (!r.ok) {
    const err = new Error(data.error || `DELETE ${url} → ${r.status}`);
    err.status = r.status;
    throw err;
  }
  return data;
}

const P = (id) => `/api/projects/${encodeURIComponent(id)}`;

export const api = {
  getConfig: () => jget('/api/config'),
  getProjects: () => jget('/api/projects'),
  addProject: (body) => jpost('/api/projects', body),
  detect: (repoPath) => jpost('/api/detect', { repoPath }),
  getProject: (id) => jget(P(id)),
  patchProject: (id, body) => jpatch(P(id), body),
  deleteProject: (id) => jdelete(P(id)),

  start: (id) => jpost(`${P(id)}/start`, { iterations: 0 }),
  stop: (id) => jpost(`${P(id)}/stop`),

  getStatus: (id) => jget(`${P(id)}/status`),
  getGoal: (id) => jget(`${P(id)}/goal`),
  setGoal: (id, goal) => jpost(`${P(id)}/goal`, { goal }),
  getPrompt: (id) => jget(`${P(id)}/prompt`),
  setPrompt: (id, prompt) => jpost(`${P(id)}/prompt`, { prompt }),

  getBacklog: (id) => jget(`${P(id)}/backlog`),
  addBacklog: (id, body) => jpost(`${P(id)}/backlog`, body),
  deleteBacklog: (id, itemId) => jpost(`${P(id)}/backlog/delete`, { id: itemId }),
  reorderBacklog: (id, ids) => jpost(`${P(id)}/backlog/reorder`, { ids }),

  getCompletions: (id) => jget(`${P(id)}/completions`),
  getAgent: (id) => jget(`${P(id)}/agent`),
  setAgent: (id, agentId) => jpost(`${P(id)}/agent`, { id: agentId }),
  getProgress: (id) => jget(`${P(id)}/progress`),
};
