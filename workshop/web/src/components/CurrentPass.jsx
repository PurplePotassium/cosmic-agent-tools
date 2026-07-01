import React, { useEffect, useState } from 'react';
import { fmtDur } from '../util.js';

const PHASES = {
  working: { cls: 'running', label: 'working' },
  done: { cls: 'running', label: 'done ✓' },
  reverted: { cls: 'stopped', label: 'reverted' },
  blocked: { cls: 'stopped', label: 'blocked' },
};

export default function CurrentPass({ progress, passSeconds }) {
  const [, force] = useState(0);

  const hasReport = progress && progress.task;

  // Tick once per second so "updated Ns ago" stays live while a report is shown.
  useEffect(() => {
    if (!hasReport) return;
    const id = setInterval(() => force((n) => n + 1), 1000);
    return () => clearInterval(id);
  }, [hasReport]);

  if (!hasReport) return null;

  const phase = String(progress.phase || 'working');
  const pm = PHASES[phase] || { cls: 'idle', label: phase };

  let ageSec = null;
  if (progress.updated) {
    const t = typeof progress.updated === 'number' ? progress.updated : Date.parse(progress.updated);
    if (!Number.isNaN(t)) ageSec = Math.max(0, (Date.now() - t) / 1000);
  }
  const stale = ageSec != null && passSeconds != null && ageSec > passSeconds + 30;

  let ageText = '';
  if (ageSec != null) {
    ageText = stale
      ? `⚠ last update ${fmtDur(ageSec)} ago — may be the previous pass`
      : `updated ${fmtDur(ageSec)} ago`;
  }

  return (
    <div className="card" style={{ borderLeft: '3px solid rgba(56,189,248,.5)' }}>
      <div className="row">
        <h2 style={{ margin: 0 }}>⚡ Current Pass</h2>
        <span className={'pill ' + pm.cls}>{pm.label}</span>
      </div>
      <div style={{ fontSize: '.7rem', color: 'var(--dim)', margin: '.5rem 0 .15rem' }}>Task</div>
      <div style={{ fontSize: '.84rem' }}>{progress.task || '—'}</div>
      <div style={{ fontSize: '.7rem', color: 'var(--dim)', margin: '.55rem 0 .15rem' }}>
        {progress.plan ? 'Plan' : 'Result'}
      </div>
      <div style={{ fontSize: '.78rem', color: '#cbd5e1' }}>
        {progress.plan || progress.result || '—'}
      </div>
      {progress.note && (
        <div style={{ marginTop: '.5rem' }}>
          <div style={{ fontSize: '.7rem', color: 'var(--dim)', marginBottom: '.15rem' }}>Note</div>
          <div style={{ fontSize: '.78rem', color: 'var(--amber)' }}>{progress.note}</div>
        </div>
      )}
      {ageText && (
        <div
          style={{
            fontSize: '.66rem',
            color: stale ? '#f59e0b' : 'var(--dim)',
            marginTop: '.5rem',
          }}
        >
          {ageText}
        </div>
      )}
    </div>
  );
}
