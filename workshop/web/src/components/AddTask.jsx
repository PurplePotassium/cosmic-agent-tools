import React, { useState } from 'react';

export default function AddTask({ onAdd }) {
  const [title, setTitle] = useState('');
  const [detail, setDetail] = useState('');
  const [busy, setBusy] = useState(false);

  async function add(top) {
    if (!title.trim()) return;
    setBusy(true);
    try {
      const ok = await onAdd({ title: title.trim(), detail: detail.trim(), top });
      if (ok) {
        setTitle('');
        setDetail('');
      }
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="card">
      <h2>➕ Add Task to Backlog</h2>
      <input
        value={title}
        onChange={(e) => setTitle(e.target.value)}
        placeholder="Task title (e.g. Add keyboard shortcuts to the editor)"
        style={{ marginBottom: '.5rem' }}
      />
      <textarea
        rows={2}
        value={detail}
        onChange={(e) => setDetail(e.target.value)}
        placeholder="Optional detail / acceptance notes"
      />
      <div style={{ display: 'flex', gap: '.5rem', marginTop: '.6rem' }}>
        <button
          className="btn exec"
          style={{ flex: 1 }}
          onClick={() => add(false)}
          disabled={busy || !title.trim()}
        >
          ＋ Add to Bottom
        </button>
        <button
          className="btn exec"
          style={{ flex: 1 }}
          onClick={() => add(true)}
          disabled={busy || !title.trim()}
        >
          ↑ Add to Top
        </button>
      </div>
    </div>
  );
}
