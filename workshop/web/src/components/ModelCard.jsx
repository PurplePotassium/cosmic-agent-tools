import React from 'react';

export default function ModelCard({ options, value, onSelect, status }) {
  const opts = options || [];
  const alive = status && status.alive;
  const runningModel = status && status.runningModel;
  const selModel =
    (status && status.selModel) ||
    (opts.find((o) => o.id === value) || {}).model;

  let note = '';
  if (selModel === 'auto') {
    note =
      alive && runningModel
        ? `Auto — running ${runningModel} (picked per task)`
        : 'Auto — model picked per task each pass';
  } else if (alive && runningModel && selModel && runningModel !== selModel) {
    note = `Running ${runningModel} now · switches to ${selModel} next pass`;
  } else if (alive && runningModel) {
    note = `Running ${runningModel}`;
  }

  return (
    <div className="card">
      <h2>🧠 Model</h2>
      <p className="hint">
        Switches the agent for the <b>next</b> iteration — the in-flight pass keeps its
        model. No restart.
      </p>
      <div style={{ display: 'flex', flexDirection: 'column', gap: '.4rem' }}>
        {opts.length === 0 && <div className="empty">No agent options configured.</div>}
        {opts.map((o) => {
          const active = o.id === value;
          return (
            <button
              key={o.id}
              className={'btn ' + (active ? 'exec' : '')}
              style={{ justifyContent: 'flex-start', opacity: active ? 1 : 0.7 }}
              onClick={() => onSelect(o.id)}
            >
              {active ? '◉' : '○'} {o.label}
            </button>
          );
        })}
      </div>
      {note && (
        <div style={{ fontSize: '.72rem', color: 'var(--amber)', marginTop: '.55rem' }}>
          {note}
        </div>
      )}
    </div>
  );
}
