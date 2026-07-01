import React, { useEffect, useRef, useState } from 'react';
import { fmtDur } from '../util.js';

export default function WorkshopLoop({ status, backlogCount, onStart, onStop }) {
  const alive = status && status.alive;
  const base = useRef({ sec: null, at: 0 });
  const [, force] = useState(0);

  // Reset the local ticker base whenever the server pushes a fresh passSeconds.
  useEffect(() => {
    if (alive && status && status.passSeconds != null) {
      base.current = { sec: status.passSeconds, at: Date.now() };
    } else {
      base.current = { sec: null, at: 0 };
    }
    force((n) => n + 1);
  }, [alive, status && status.passSeconds]);

  // Local per-second re-render — a display ticker only, not a network poll.
  useEffect(() => {
    if (!alive) return;
    const id = setInterval(() => force((n) => n + 1), 1000);
    return () => clearInterval(id);
  }, [alive]);

  let elapsed;
  if (alive && base.current.sec != null) {
    elapsed = fmtDur(base.current.sec + (Date.now() - base.current.at) / 1000);
  } else if (alive) {
    elapsed = 'between passes';
  } else {
    elapsed = 'idle';
  }

  const lastIter = status && status.lastIter != null ? `iter ${status.lastIter}` : '—';
  const currentTask =
    (status && status.currentTask) || (alive ? '(agent choosing next task)' : '—');

  return (
    <div className="card">
      <div className="row">
        <h2 style={{ margin: 0 }}>Workshop Loop</h2>
      </div>
      <p className="hint">
        One agent at a time (no fleet). Each pass starts cold, reads the goal + backlog,
        makes ONE verified increment, drains the top item, logs a completion.
      </p>
      <div className="stats">
        <div className="stat">
          <div className="lbl">This Pass</div>
          <div className="val">{elapsed}</div>
        </div>
        <div className="stat">
          <div className="lbl">Completed</div>
          <div className="val">{lastIter}</div>
        </div>
        <div className="stat">
          <div className="lbl">Backlog</div>
          <div className="val">{backlogCount != null ? backlogCount : 0}</div>
        </div>
      </div>
      <div style={{ fontSize: '.72rem', color: 'var(--dim)', marginTop: '.7rem' }}>
        Working on: <span style={{ color: '#cbd5e1' }}>{currentTask}</span>
      </div>
      <div className="actions">
        <button className="btn exec" onClick={onStart} disabled={alive}>
          ▶ Start Loop
        </button>
        <button className="btn stop" onClick={onStop} disabled={!alive}>
          ■ Stop Loop
        </button>
      </div>
    </div>
  );
}
