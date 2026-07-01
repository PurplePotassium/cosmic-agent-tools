import React, { useState } from 'react';

// Activity badge derives from live status: computing vs waiting vs wedged.
function activity(status) {
  if (!status || !status.alive) return null;
  if (status.error) return { cls: 'stopped', text: 'Wedged?' };
  if (status.computing) return { cls: 'running', text: 'Computing' };
  return { cls: 'idle', text: 'Waiting on model' };
}

export default function Header({
  projects,
  selectedId,
  onSelect,
  status,
  onAddProject,
}) {
  const [path, setPath] = useState('');
  const [busy, setBusy] = useState(false);

  const selected = projects.find((p) => p.id === selectedId);
  const running = selected ? selected.running : false;
  const act = activity(status);

  async function submitAdd(e) {
    e.preventDefault();
    const repoPath = path.trim();
    if (!repoPath) return;
    setBusy(true);
    try {
      await onAddProject(repoPath);
      setPath('');
    } finally {
      setBusy(false);
    }
  }

  return (
    <header>
      <h1>🔨 Workshop</h1>
      <span className="sub">single agent · fresh context each pass · drains a backlog toward GOAL.md</span>

      <span className="proj-switch">
        {projects.length > 0 ? (
          <select
            value={selectedId || ''}
            onChange={(e) => onSelect(e.target.value)}
            aria-label="Select project"
          >
            {projects.map((p) => (
              <option key={p.id} value={p.id}>
                {p.running ? '● ' : '○ '}
                {p.name}
              </option>
            ))}
          </select>
        ) : (
          <span className="sub">No projects yet</span>
        )}
      </span>

      <form className="add-proj" onSubmit={submitAdd}>
        <input
          value={path}
          onChange={(e) => setPath(e.target.value)}
          placeholder="Absolute repo path…"
        />
        <button className="btn" type="submit" disabled={busy || !path.trim()}>
          {busy ? '…' : '＋ Add'}
        </button>
      </form>

      <span style={{ flex: 1 }} />

      {selected && (
        <span className={'pill ' + (running ? 'running' : 'stopped')}>
          <span className="dot" /> {running ? 'Running' : 'Stopped'}
        </span>
      )}
      {act && (
        <span className={'pill ' + act.cls}>
          <span className="dot" /> {act.text}
        </span>
      )}
    </header>
  );
}
