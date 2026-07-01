import React, { useEffect, useRef } from 'react';

// Streams the live agent log. Auto-scrolls to the bottom unless the user has
// scrolled up to read earlier lines.
export default function LogView({ lines }) {
  const preRef = useRef(null);
  const stick = useRef(true);

  useEffect(() => {
    const el = preRef.current;
    if (!el) return;
    if (stick.current) el.scrollTop = el.scrollHeight;
  }, [lines]);

  function onScroll(e) {
    const el = e.currentTarget;
    stick.current = el.scrollHeight - el.scrollTop - el.clientHeight < 8;
  }

  const text = lines && lines.length ? lines.join('\n') : 'No log yet.';

  return (
    <div className="card">
      <h2>🖥 Live Agent Log</h2>
      <pre className="log" ref={preRef} onScroll={onScroll}>
        {text}
      </pre>
    </div>
  );
}
