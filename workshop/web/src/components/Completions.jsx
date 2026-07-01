import React from 'react';
import { localTime } from '../util.js';

export default function Completions({ items }) {
  // Newest first — sort by completed timestamp when available.
  const list = [...(items || [])].sort((a, b) => {
    const ta = a.completed ? Date.parse(a.completed) : 0;
    const tb = b.completed ? Date.parse(b.completed) : 0;
    return tb - ta;
  });

  return (
    <div className="card">
      <div className="row" style={{ marginBottom: '.7rem' }}>
        <h2 style={{ margin: 0 }}>✅ Completed Tasks</h2>
        <span className="pill idle">{list.length} done</span>
      </div>
      <div style={{ display: 'flex', flexDirection: 'column', gap: '.45rem' }}>
        {list.length === 0 && <div className="empty">No completed passes yet.</div>}
        {list.map((it, i) => (
          <div key={it.id || i} className="listitem" style={{ borderColor: 'rgba(34,197,94,.18)' }}>
            <div style={{ display: 'flex', alignItems: 'center', gap: '.4rem' }}>
              <span style={{ color: 'var(--green)', flexShrink: 0 }}>✓</span>
              <span style={{ fontSize: '.82rem', flex: 1, minWidth: 0 }}>{it.title}</span>
              <span style={{ fontSize: '.66rem', color: 'var(--dim)', flexShrink: 0 }}>
                {localTime(it.completed)}
              </span>
            </div>
            {it.result && (
              <div style={{ fontSize: '.72rem', color: 'var(--muted)', marginTop: '.25rem' }}>
                {it.result}
              </div>
            )}
          </div>
        ))}
      </div>
    </div>
  );
}
