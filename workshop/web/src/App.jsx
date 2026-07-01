import React, { useCallback, useEffect, useRef, useState } from 'react';
import { api } from './api.js';
import { ToastProvider, useToast } from './components/Toast.jsx';
import Header from './components/Header.jsx';
import WorkshopLoop from './components/WorkshopLoop.jsx';
import CurrentPass from './components/CurrentPass.jsx';
import ModelCard from './components/ModelCard.jsx';
import LiveActivity from './components/LiveActivity.jsx';
import EditorCard from './components/EditorCard.jsx';
import AddTask from './components/AddTask.jsx';
import Backlog from './components/Backlog.jsx';
import Completions from './components/Completions.jsx';
import LogView from './components/LogView.jsx';
import Preview from './components/Preview.jsx';

const LS_KEY = 'workshop.selectedProject';
const LOG_LIMIT = 400;

function Dashboard() {
  const toast = useToast();

  const [options, setOptions] = useState([]);
  const [projects, setProjects] = useState([]);
  const [selectedId, setSelectedId] = useState(
    () => localStorage.getItem(LS_KEY) || null
  );

  const [status, setStatus] = useState(null);
  const [backlog, setBacklog] = useState([]);
  const [completions, setCompletions] = useState([]);
  const [goal, setGoal] = useState('');
  const [prompt, setPrompt] = useState('');
  const [agentId, setAgentId] = useState(null);
  const [progress, setProgress] = useState(null);
  const [commits, setCommits] = useState([]);
  const [logLines, setLogLines] = useState([]);

  // ── initial config + project list ──────────────────────────────────────────
  const refreshProjects = useCallback(async () => {
    try {
      const list = await api.getProjects();
      setProjects(list);
      return list;
    } catch (e) {
      return null;
    }
  }, []);

  useEffect(() => {
    (async () => {
      try {
        const cfg = await api.getConfig();
        setOptions(cfg.options || []);
      } catch (e) {
        /* backend may be empty */
      }
      const list = await refreshProjects();
      if (list && list.length) {
        setSelectedId((cur) => {
          if (cur && list.some((p) => p.id === cur)) return cur;
          return list[0].id;
        });
      }
    })();
  }, [refreshProjects]);

  // Persist selection.
  useEffect(() => {
    if (selectedId) localStorage.setItem(LS_KEY, selectedId);
  }, [selectedId]);

  // ── per-project state: initial fetch + single EventSource ────────────────────
  useEffect(() => {
    if (!selectedId) return;
    const id = selectedId;
    let cancelled = false;

    // Reset view while (re)loading a project.
    setStatus(null);
    setBacklog([]);
    setCompletions([]);
    setGoal('');
    setPrompt('');
    setProgress(null);
    setCommits([]);
    setLogLines([]);

    async function loadInitial() {
      const results = await Promise.allSettled([
        api.getStatus(id),
        api.getBacklog(id),
        api.getCompletions(id),
        api.getGoal(id),
        api.getPrompt(id),
        api.getAgent(id),
        api.getProgress(id),
      ]);
      if (cancelled) return;
      const [st, bl, cp, gl, pr, ag, pg] = results;
      if (st.status === 'fulfilled') {
        setStatus(st.value);
        setCommits(st.value.commits || []);
        if (st.value.progress) setProgress(st.value.progress);
      }
      if (bl.status === 'fulfilled') setBacklog(bl.value || []);
      if (cp.status === 'fulfilled') setCompletions(cp.value || []);
      if (gl.status === 'fulfilled') setGoal((gl.value && gl.value.goal) || '');
      if (pr.status === 'fulfilled') setPrompt((pr.value && pr.value.prompt) || '');
      if (ag.status === 'fulfilled') setAgentId(ag.value && ag.value.id);
      if (pg.status === 'fulfilled' && pg.value && pg.value.task) setProgress(pg.value);
    }
    loadInitial();

    // Push-based updates. One stream per selected project.
    const es = new EventSource(`/events?projectId=${encodeURIComponent(id)}`);
    let lastAlive = null;

    const payload = (e) => {
      try {
        return JSON.parse(e.data).data;
      } catch {
        return null;
      }
    };

    es.addEventListener('status', (e) => {
      const d = payload(e);
      if (!d) return;
      setStatus((prev) => ({ ...(prev || {}), ...d }));
      if (d.alive !== undefined && d.alive !== lastAlive) {
        lastAlive = d.alive;
        refreshProjects();
      } else if (d.alive === false) {
        refreshProjects();
      }
    });

    es.addEventListener('log', (e) => {
      const d = payload(e);
      if (!d || d.line == null) return;
      setLogLines((cur) => {
        const next = cur.concat(String(d.line));
        return next.length > LOG_LIMIT ? next.slice(next.length - LOG_LIMIT) : next;
      });
    });

    es.addEventListener('commit', (e) => {
      const d = payload(e);
      if (!d) return;
      setCommits((cur) => [
        { sha: d.sha, subject: d.subject, time: d.time },
        ...cur,
      ]);
      // A pass finished — refresh the pass-derived lists.
      api.getBacklog(id).then((v) => !cancelled && setBacklog(v || [])).catch(() => {});
      api.getCompletions(id).then((v) => !cancelled && setCompletions(v || [])).catch(() => {});
      api.getProgress(id).then((v) => !cancelled && v && v.task && setProgress(v)).catch(() => {});
    });

    es.addEventListener('progress', (e) => {
      const d = payload(e);
      if (d) setProgress(d);
    });

    es.addEventListener('iteration', (e) => {
      const d = payload(e);
      if (!d) return;
      // Roll the log at the start of a new pass.
      const parts = [d.agent, d.model].filter(Boolean).join(' / ');
      setLogLines([`── iteration ${d.num}${parts ? ' · ' + parts : ''} ──`]);
      setStatus((prev) => ({ ...(prev || {}), computing: true, alive: true }));
    });

    es.addEventListener('project', () => {
      refreshProjects();
    });

    return () => {
      cancelled = true;
      es.close();
    };
  }, [selectedId, refreshProjects]);

  // ── actions ──────────────────────────────────────────────────────────────
  const selectedProject = projects.find((p) => p.id === selectedId) || null;

  async function addProject(repoPath) {
    try {
      const det = await api.detect(repoPath);
      // detect returns {isRepo, exists}: isRepo = it's a git repo; exists = already registered.
      if (det && det.isRepo === false) {
        toast('Not a git repository: ' + (det.repoPath || repoPath), 'error');
        return;
      }
      const proj = await api.addProject({ repoPath });
      await refreshProjects();
      if (proj && proj.id) setSelectedId(proj.id);
      toast('Project added', 'success');
    } catch (e) {
      toast('Add failed: ' + e.message, 'error');
    }
  }

  async function start() {
    try {
      await api.start(selectedId);
      toast('Workshop loop starting…', 'success');
      refreshProjects();
    } catch (e) {
      toast('Failed: ' + e.message, 'error');
    }
  }

  async function stop() {
    if (!window.confirm('Stop the workshop loop and kill any in-flight pass?')) return;
    try {
      await api.stop(selectedId);
      toast('Workshop loop stopped', 'success');
      refreshProjects();
    } catch (e) {
      toast('Failed: ' + e.message, 'error');
    }
  }

  async function selectModel(id) {
    try {
      const d = await api.setAgent(selectedId, id);
      setAgentId(id);
      const opt = options.find((o) => o.id === id);
      toast('Next iteration → ' + (opt ? opt.label : id), 'success');
      if (d && (d.agent || d.model)) {
        setStatus((prev) => ({ ...(prev || {}), selAgent: d.agent, selModel: d.model }));
      }
    } catch (e) {
      toast('Failed: ' + e.message, 'error');
    }
  }

  async function saveGoal(text) {
    try {
      await api.setGoal(selectedId, text);
      toast('Goal saved — applies next pass', 'success');
    } catch (e) {
      toast('Failed: ' + e.message, 'error');
    }
  }

  async function savePrompt(text) {
    try {
      await api.setPrompt(selectedId, text);
      toast('Prompt saved — applies next pass', 'success');
    } catch (e) {
      toast('Failed: ' + e.message, 'error');
    }
  }

  async function addTask({ title, detail, top }) {
    try {
      await api.addBacklog(selectedId, { title, detail, top });
      toast(top ? 'Added to top' : 'Added to backlog', 'success');
      const v = await api.getBacklog(selectedId);
      setBacklog(v || []);
      return true;
    } catch (e) {
      toast('Failed: ' + e.message, 'error');
      return false;
    }
  }

  async function deleteTask(itemId) {
    if (!window.confirm('Delete this backlog task?')) return;
    try {
      await api.deleteBacklog(selectedId, itemId);
      toast('Task deleted', 'success');
      const v = await api.getBacklog(selectedId);
      setBacklog(v || []);
    } catch (e) {
      toast('Failed: ' + e.message, 'error');
    }
  }

  async function reorderBacklog(ids) {
    const prev = backlog;
    const byId = new Map(prev.map((it) => [it.id, it]));
    setBacklog(ids.map((i) => byId.get(i)).filter(Boolean));
    try {
      await api.reorderBacklog(selectedId, ids);
    } catch (e) {
      toast('Reorder failed: ' + e.message, 'error');
      setBacklog(prev);
    }
  }

  // ── render ────────────────────────────────────────────────────────────────
  const hasPreview = !!(selectedProject && selectedProject.preview);

  return (
    <>
      <Header
        projects={projects}
        selectedId={selectedId}
        onSelect={setSelectedId}
        status={status}
        onAddProject={addProject}
      />

      {!selectedProject ? (
        <div className="grid no-preview">
          <div className="col">
            <div className="card">
              <h2>No project selected</h2>
              <p className="hint">
                Add a project above by entering an absolute path to a git repository, then
                pick it from the switcher.
              </p>
            </div>
          </div>
        </div>
      ) : (
        <div className={'grid' + (hasPreview ? '' : ' no-preview')}>
          <div className="col">
            <WorkshopLoop
              status={status}
              backlogCount={backlog.length}
              onStart={start}
              onStop={stop}
            />
            <CurrentPass progress={progress} passSeconds={status && status.passSeconds} />
            <ModelCard
              options={options}
              value={agentId}
              onSelect={selectModel}
              status={status}
            />
            <LiveActivity dirtyFiles={status && status.dirtyFiles} commits={commits} />
            <EditorCard
              title="🎯 General Goal"
              hint="Every new task the agent picks works toward this. Re-read each pass — edit any time."
              value={goal}
              rows={5}
              placeholder="e.g. Make the app a polished, fast single-page tool. Correctness and clear UX over new features."
              saveLabel="💾 Save Goal"
              onSave={saveGoal}
            />
            <EditorCard
              title="📝 Prompt"
              hint="The per-pass instructions the agent reads. Re-read each pass."
              value={prompt}
              rows={6}
              placeholder="Per-pass instructions for the agent…"
              saveLabel="💾 Save Prompt"
              onSave={savePrompt}
            />
            <AddTask onAdd={addTask} />
            <Backlog items={backlog} onDelete={deleteTask} onReorder={reorderBacklog} />
            <Completions items={completions} />
            <LogView lines={logLines} />
          </div>

          {hasPreview && <Preview url={selectedProject.preview} />}
        </div>
      )}
    </>
  );
}

export default function App() {
  return (
    <ToastProvider>
      <Dashboard />
    </ToastProvider>
  );
}
