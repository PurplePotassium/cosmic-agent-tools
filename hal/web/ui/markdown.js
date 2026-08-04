// Tiny sanitizing markdown renderer for workflow artifacts.
//
// Rule zero: EVERY character of the input is HTML-escaped first; markup is
// then re-introduced only by this code, from the escaped text. There is no
// raw-HTML passthrough and no external library — an artifact (agent- or
// operator-written) can never smuggle script into the dashboard.
//
// Supported: headings, bold/italic, inline code + fenced code blocks,
// unordered/ordered lists, checkboxes (rendered as disabled inputs),
// links (href restricted to http(s) and #fragments), hr, blockquotes,
// and pipe tables.

const escapeHtml = (s) =>
  s.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;").replace(/"/g, "&quot;");

// safeHref admits only http(s) URLs and in-page fragments — javascript:,
// data:, file: and everything else render as plain text.
function safeHref(href) {
  const h = href.trim();
  if (/^https?:\/\//i.test(h) || h.startsWith("#")) return h;
  return null;
}

// NUL delimits the code-span placeholders below. It is stripped from the
// input before use, so content can never forge a placeholder.
const NUL = String.fromCharCode(0);
const nulRe = new RegExp(NUL, "g");
const codeSlotRe = new RegExp(NUL + "(\\d+)" + NUL, "g");

// inline runs on ALREADY-ESCAPED text. Code spans are extracted first so
// their content is immune to the emphasis/link passes, and restored last.
function inline(s) {
  const codes = [];
  s = s.replace(nulRe, "");
  s = s.replace(/`([^`]+)`/g, (_, c) => {
    codes.push(c);
    return NUL + (codes.length - 1) + NUL;
  });
  s = s.replace(/\[([^\]]+)\]\(([^()\s]+)\)/g, (m, text, href) => {
    const h = safeHref(href);
    return h ? `<a href="${h}" target="_blank" rel="noopener noreferrer">${text}</a>` : text;
  });
  s = s.replace(/\*\*([^*]+)\*\*/g, "<strong>$1</strong>");
  s = s.replace(/(^|[\s(])\*([^*]+)\*/g, "$1<em>$2</em>");
  s = s.replace(codeSlotRe, (_, i) => `<code>${codes[+i]}</code>`);
  return s;
}

const inlineEsc = (line) => inline(escapeHtml(line));

// splitRow splits one |-delimited table row into trimmed cells.
function splitRow(line) {
  let cells = line.trim();
  if (cells.startsWith("|")) cells = cells.slice(1);
  if (cells.endsWith("|")) cells = cells.slice(0, -1);
  return cells.split("|").map((c) => c.trim());
}

// countCheckboxes tallies "- [ ]" / "- [x]" list items — the plan pane's
// "7/12 done" progress chip.
export function countCheckboxes(md) {
  let done = 0;
  let total = 0;
  for (const line of String(md ?? "").split("\n")) {
    const m = line.match(/^\s*[-*+]\s+\[([ xX])\]\s/);
    if (!m) continue;
    total++;
    if (m[1].toLowerCase() === "x") done++;
  }
  return { done, total };
}

// renderMarkdown returns a sanitized HTML string for the given markdown.
export function renderMarkdown(md) {
  const lines = String(md ?? "").replace(/\r\n?/g, "\n").split("\n");
  const out = [];
  let para = [];
  let list = null; // { ordered, items: [] }
  let i = 0;

  const flushPara = () => {
    if (para.length > 0) {
      out.push(`<p>${para.map(inlineEsc).join("<br>")}</p>`);
      para = [];
    }
  };
  const flushList = () => {
    if (list) {
      const tag = list.ordered ? "ol" : "ul";
      out.push(`<${tag}>${list.items.join("")}</${tag}>`);
      list = null;
    }
  };

  while (i < lines.length) {
    const line = lines[i];

    // fenced code block
    if (/^\s*```/.test(line)) {
      flushPara();
      flushList();
      const buf = [];
      i++;
      while (i < lines.length && !/^\s*```/.test(lines[i])) {
        buf.push(lines[i]);
        i++;
      }
      i++; // closing fence (or EOF)
      out.push(`<pre class="md-code"><code>${escapeHtml(buf.join("\n"))}</code></pre>`);
      continue;
    }

    // heading
    const h = line.match(/^(#{1,6})\s+(.*)$/);
    if (h) {
      flushPara();
      flushList();
      const n = h[1].length;
      out.push(`<h${n}>${inlineEsc(h[2])}</h${n}>`);
      i++;
      continue;
    }

    // horizontal rule (a line of only -/*/_, three or more)
    if (/^\s*([-*_])(\s*\1){2,}\s*$/.test(line)) {
      flushPara();
      flushList();
      out.push("<hr>");
      i++;
      continue;
    }

    // pipe table: a |row| followed by a separator row
    if (
      /^\s*\|.*\|\s*$/.test(line) &&
      i + 1 < lines.length &&
      /^\s*\|?[\s:|-]+\|?\s*$/.test(lines[i + 1]) &&
      lines[i + 1].includes("-")
    ) {
      flushPara();
      flushList();
      const header = splitRow(line);
      i += 2;
      const rows = [];
      while (i < lines.length && /^\s*\|.*\|\s*$/.test(lines[i])) {
        rows.push(splitRow(lines[i]));
        i++;
      }
      const th = header.map((c) => `<th>${inlineEsc(c)}</th>`).join("");
      const trs = rows
        .map((r) => `<tr>${r.map((c) => `<td>${inlineEsc(c)}</td>`).join("")}</tr>`)
        .join("");
      out.push(`<table><thead><tr>${th}</tr></thead><tbody>${trs}</tbody></table>`);
      continue;
    }

    // list item (checkbox-aware)
    const li = line.match(/^\s*([-*+]|\d+[.)])\s+(.*)$/);
    if (li) {
      flushPara();
      const ordered = /^\d/.test(li[1]);
      if (!list || list.ordered !== ordered) {
        flushList();
        list = { ordered, items: [] };
      }
      const task = li[2].match(/^\[([ xX])\]\s+(.*)$/);
      if (task) {
        const checked = task[1].toLowerCase() === "x" ? " checked" : "";
        list.items.push(
          `<li class="md-task"><input type="checkbox" disabled${checked}> ${inlineEsc(task[2])}</li>`,
        );
      } else {
        list.items.push(`<li>${inlineEsc(li[2])}</li>`);
      }
      i++;
      continue;
    }

    // blockquote (single-line granularity)
    const bq = line.match(/^\s*>\s?(.*)$/);
    if (bq) {
      flushPara();
      flushList();
      out.push(`<blockquote>${inlineEsc(bq[1])}</blockquote>`);
      i++;
      continue;
    }

    // blank line ends the current block
    if (!line.trim()) {
      flushPara();
      flushList();
      i++;
      continue;
    }

    para.push(line);
    i++;
  }
  flushPara();
  flushList();
  return `<div class="md">${out.join("\n")}</div>`;
}
