import React from 'react';

export default function LiveActivity({ dirtyFiles, commits }) {
  const dirty = dirtyFiles || [];
  const list = commits || [];

  return (
    <div className="card">
      <h2>📈 Live Activity</h2>
      <div style={{ fontSize: '.7rem', color: 'var(--dim)', marginBottom: '.3rem' }}>
        Files edited this pass (uncommitted)
      </div>
      <div
        className="mono"
        style={{
          fontSize: '.74rem',
          color: 'var(--amber)',
          lineHeight: 1.5,
          minHeight: '1.2rem',
          whiteSpace: 'pre-wrap',
          wordBreak: 'break-word',
        }}
      >
        {dirty.length ? dirty.join('\n') : 'none'}
      </div>
      <div style={{ fontSize: '.7rem', color: 'var(--dim)', margin: '.8rem 0 .3rem' }}>
        Recent commits
      </div>
      <div style={{ display: 'flex', flexDirection: 'column', gap: '.25rem' }}>
        {list.length === 0 && <div className="empty">No commits yet.</div>}
        {list.map((c, i) => {
          const isPass = /^ralph iter/.test(c.subject || '');
          return (
            <div
              key={c.sha || i}
              style={{ display: 'flex', gap: '.5rem', fontSize: '.72rem', alignItems: 'baseline' }}
            >
              <span className="mono" style={{ color: 'var(--dim)', flexShrink: 0 }}>
                {c.time}
              </span>
              <span
                className="mono"
                style={{ color: isPass ? 'var(--green)' : 'var(--dim)', flexShrink: 0 }}
              >
                {c.sha}
              </span>
              <span
                style={{
                  color: 'var(--muted)',
                  overflow: 'hidden',
                  textOverflow: 'ellipsis',
                  whiteSpace: 'nowrap',
                }}
              >
                {c.subject}
              </span>
            </div>
          );
        })}
      </div>
    </div>
  );
}
