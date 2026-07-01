import React, { useEffect, useRef } from 'react';

export default function Preview({ url }) {
  const frameRef = useRef(null);

  // Point the iframe at the project's preview URL when it changes.
  useEffect(() => {
    if (frameRef.current && url) frameRef.current.src = url;
  }, [url]);

  function reload() {
    const f = frameRef.current;
    if (f && url) f.src = url + (url.includes('?') ? '&' : '?') + 't=' + Date.now();
  }

  return (
    <div className="col">
      <div className="card">
        <div className="row">
          <h2 style={{ margin: 0 }}>🎮 Project Preview</h2>
          <button className="btn" style={{ padding: '.3rem .6rem' }} onClick={reload}>
            ⟳ Reload
          </button>
        </div>
        <iframe ref={frameRef} title="Project preview" src="about:blank" style={{ marginTop: '.7rem' }} />
        <p className="hint" style={{ marginTop: '.6rem' }}>
          The loop commits straight to your repo. Reload to pick up the latest pass.
        </p>
      </div>
    </div>
  );
}
