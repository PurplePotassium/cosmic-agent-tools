import React, { useEffect, useRef, useState } from 'react';

// Textarea bound to a server-side markdown file. It refuses to overwrite the
// user's in-progress edits: the incoming server value only replaces local text
// while the textarea is NOT focused.
export default function EditorCard({
  title,
  hint,
  value,
  rows = 5,
  placeholder,
  saveLabel = '💾 Save',
  onSave,
}) {
  const [text, setText] = useState(value || '');
  const focused = useRef(false);
  const [saving, setSaving] = useState(false);

  useEffect(() => {
    if (!focused.current) setText(value || '');
  }, [value]);

  async function save() {
    setSaving(true);
    try {
      await onSave(text);
    } finally {
      setSaving(false);
    }
  }

  return (
    <div className="card">
      <h2>{title}</h2>
      {hint && <p className="hint">{hint}</p>}
      <textarea
        rows={rows}
        value={text}
        placeholder={placeholder}
        onFocus={() => {
          focused.current = true;
        }}
        onBlur={() => {
          focused.current = false;
        }}
        onChange={(e) => setText(e.target.value)}
      />
      <button
        className="btn exec full"
        style={{ marginTop: '.6rem' }}
        onClick={save}
        disabled={saving}
      >
        {saving ? 'Saving…' : saveLabel}
      </button>
    </div>
  );
}
