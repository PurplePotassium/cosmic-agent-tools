// Small formatting helpers shared across components.

export function fmtDur(s) {
  if (s == null) return 'idle';
  s = Math.max(0, Math.floor(s));
  const m = Math.floor(s / 60);
  const sec = s % 60;
  return m > 0 ? `${m}m ${String(sec).padStart(2, '0')}s` : `${sec}s`;
}

// Human "N ago" from a timestamp (ms epoch, ISO string, or Date).
export function relativeTime(when) {
  if (!when) return '';
  const t = typeof when === 'number' ? when : Date.parse(when);
  if (Number.isNaN(t)) return '';
  const diff = Math.max(0, (Date.now() - t) / 1000);
  if (diff < 60) return `${Math.floor(diff)}s ago`;
  if (diff < 3600) return `${Math.floor(diff / 60)}m ago`;
  if (diff < 86400) return `${Math.floor(diff / 3600)}h ago`;
  return `${Math.floor(diff / 86400)}d ago`;
}

export function localTime(when) {
  if (!when) return '';
  const t = typeof when === 'number' ? when : Date.parse(when);
  if (Number.isNaN(t)) return '';
  return new Date(t).toLocaleString();
}
