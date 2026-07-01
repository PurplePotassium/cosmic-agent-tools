import React from 'react';

export default function Backlog({ items, onDelete, onReorder }) {
  const list = items || [];

  function move(index, dir) {
    const to = index + dir;
    if (to < 0 || to >= list.length) return;
    const ids = list.map((it) => it.id);
    [ids[index], ids[to]] = [ids[to], ids[index]];
    onReorder(ids);
  }

  return (
    <div className="card">
      <div className="row" style={{ marginBottom: '.7rem' }}>
        <h2 style={{ margin: 0 }}>📋 Backlog</h2>
        <span className="pill idle">
          {list.length} task{list.length === 1 ? '' : 's'}
        </span>
      </div>
      <p className="hint">
        Drained TOP-first. Top item = the agent's next task. Delete anything you don't want.
      </p>
      <div style={{ display: 'flex', flexDirection: 'column', gap: '.45rem' }}>
        {list.length === 0 && (
          <div className="empty">
            Backlog empty — the agent will invent the next task toward the goal.
          </div>
        )}
        {list.map((it, i) => (
          <div
            key={it.id}
            className="listitem"
            style={{
              display: 'flex',
              gap: '.5rem',
              alignItems: 'flex-start',
              ...(i === 0 ? { borderColor: 'rgba(56,189,248,.4)' } : {}),
            }}
          >
            <span
              style={{
                fontSize: '.62rem',
                fontWeight: 700,
                color: i === 0 ? 'var(--accent)' : 'var(--dim)',
                minWidth: '1.5rem',
                paddingTop: '.15rem',
              }}
            >
              {i === 0 ? 'NEXT' : '#' + (i + 1)}
            </span>
            <div style={{ flex: 1, minWidth: 0 }}>
              <div style={{ fontSize: '.82rem' }}>{it.title}</div>
              {it.detail && (
                <div style={{ fontSize: '.72rem', color: 'var(--muted)', marginTop: '.2rem' }}>
                  {it.detail}
                </div>
              )}
            </div>
            <div style={{ display: 'flex', flexDirection: 'column', gap: '.15rem' }}>
              <button
                className="arrow"
                title="Move up"
                onClick={() => move(i, -1)}
                disabled={i === 0}
              >
                ▲
              </button>
              <button
                className="arrow"
                title="Move down"
                onClick={() => move(i, 1)}
                disabled={i === list.length - 1}
              >
                ▼
              </button>
            </div>
            <button className="x" title="Delete" onClick={() => onDelete(it.id)}>
              🗑
            </button>
          </div>
        ))}
      </div>
    </div>
  );
}
