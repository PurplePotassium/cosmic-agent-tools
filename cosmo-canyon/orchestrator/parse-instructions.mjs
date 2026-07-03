// Cosmo Canyon — parse-instructions (§15e / §15g phase 6). DETERMINISTIC, never the model.
//
// Maps a human-written asset `Instructions` string ("24x24, 6 frames, horizontal, 8fps") → a manifest
// config the derive pipeline consumes, AND derives a deterministic `manifestKey` for a fresh GUI upload —
// closing the §15e/§15a upload-keying gap (createAsset defaults manifestKey=null → the DERIVED predicate
// could never flip an uploaded image/audio Implemented, because a null manifestKey can never be "real").
//
// PURE: no fs, no model judgment, no side effects. The parse is a best-effort regex read with SAFE defaults —
// a garbled Instructions string yields the defaults (a placeholder-sized single frame), never a throw.
//
// The manifest key is the positioning-authority key (source filename `<key>.png`, atlas frame id). It MUST
// satisfy the game manifest key charset (`^[A-Za-z0-9_.-]+$`, no path separators / `..`) — sanitize enforces it.

// ── the manifest AssetEntry kinds (mirrors game/src/assets/types.ts AssetKind) ──────────────────────
const MANIFEST_KINDS = new Set(["player", "enemy", "boss", "projectile", "pickup", "fx", "ui"]);
const KEY_CHAR_RE = /[^a-z0-9._-]/g;

// SAFE parse defaults — a Ready+dirty image with an empty Instructions is a single 32×32 frame.
const DEFAULTS = { size: [32, 32], frames: 1, fps: 0, layout: "horizontal", anchor: [0.5, 0.5], renderScale: 1, manifestKind: null };

function clampInt(n, lo, hi, def) { const v = Math.round(Number(n)); return Number.isFinite(v) && v >= lo && v <= hi ? v : def; }
function clampNum(n, lo, hi, def) { const v = Number(n); return Number.isFinite(v) && v >= lo && v <= hi ? v : def; }

// Parse a `key:`/`manifestKey:` directive out of Instructions (first wins). Returns the RAW (unsanitized) token.
function directive(s, names) {
  for (const name of names) {
    const m = s.match(new RegExp(`(?:^|[\\s,;])${name}\\s*[:=]\\s*([A-Za-z0-9._/\\-]+)`, "i"));
    if (m) return m[1];
  }
  return null;
}

// Sanitize any string → a legal manifest key: lowercase, keep [a-z0-9._-], collapse runs, strip leading/
// trailing dots/dashes, drop `..`. Empty result → null so the caller falls back.
export function sanitizeKey(raw) {
  let k = String(raw || "").toLowerCase().replace(/[\s/\\]+/g, ".").replace(KEY_CHAR_RE, "").replace(/\.{2,}/g, ".").replace(/^[.\-_]+|[.\-_]+$/g, "");
  if (!k || k === "." || k.includes("..")) return null;
  return k.slice(0, 64);
}

// ── parseInstructions(instructions) → {size,frames,fps,layout,anchor,renderScale,manifestKind} ────────
export function parseInstructions(instructions = "") {
  const s = String(instructions || "");
  const out = { ...DEFAULTS, size: [...DEFAULTS.size], anchor: [...DEFAULTS.anchor] };

  // size: "24x24" / "24 x 24" / "24×24" / "size: 32,32" / "32*32"
  let m = s.match(/(?:size\s*[:=]\s*)?(\d{1,4})\s*[x×*,]\s*(\d{1,4})/i);
  if (m) { out.size = [clampInt(m[1], 1, 4096, 32), clampInt(m[2], 1, 4096, 32)]; }

  // frames: "6 frames" / "frames: 6" / "6-frame"
  m = s.match(/(?:frames?\s*[:=]\s*(\d{1,3})|(\d{1,3})\s*[-\s]?frames?)/i);
  if (m) { out.frames = clampInt(m[1] ?? m[2], 1, 512, 1); }

  // fps: "8fps" / "8 fps" / "fps: 8"
  m = s.match(/(?:fps\s*[:=]\s*(\d{1,3})|(\d{1,3})\s*fps)/i);
  if (m) { out.fps = clampInt(m[1] ?? m[2], 0, 240, 0); }

  // layout: horizontal | vertical | grid (default horizontal)
  if (/\bvertical\b/i.test(s)) out.layout = "vertical";
  else if (/\bgrid\b/i.test(s)) out.layout = "grid";
  else if (/\bhorizontal\b/i.test(s)) out.layout = "horizontal";

  // anchor: "anchor 0.5,1" / "anchor: 0.5 1"
  m = s.match(/anchor\s*[:=]?\s*(-?\d*\.?\d+)\s*[,\s]\s*(-?\d*\.?\d+)/i);
  if (m) { out.anchor = [clampNum(m[1], -2, 2, 0.5), clampNum(m[2], -2, 2, 0.5)]; }

  // renderScale: "scale 2" / "renderScale: 1.5" / "2x scale"
  m = s.match(/(?:render)?scale\s*[:=]?\s*(\d*\.?\d+)|(\d*\.?\d+)\s*x\s*scale/i);
  if (m) { out.renderScale = clampNum(m[1] ?? m[2], 0.05, 16, 1); }

  // explicit manifest kind directive: "kind: enemy"
  const kd = directive(s, ["kind"]);
  if (kd && MANIFEST_KINDS.has(kd.toLowerCase())) out.manifestKind = kd.toLowerCase();

  return out;
}

// Infer the manifest AssetEntry kind from the key prefix ("player.hero"→player, "enemy.grunt"→enemy), then
// the parsed `kind:` directive, else "fx". The manifest `kind` only drives the placeholder color/category —
// positioning (size/anchor) is what actually matters — so a wrong guess is cosmetic, never gameplay-shifting.
export function inferManifestKind(key, parsed) {
  if (parsed && parsed.manifestKind) return parsed.manifestKind;
  const prefix = String(key || "").split(".")[0].toLowerCase();
  if (MANIFEST_KINDS.has(prefix)) return prefix;
  return "fx";
}

// ── deriveManifestKey(asset) → a deterministic manifest key for an uploaded image/audio ───────────────
// Precedence (§15e/§15a — deterministic, not the model):
//   1) an explicit `key:`/`manifestKey:` directive in Instructions (operator-chosen), sanitized;
//   2) the upload filename base (sans extension), sanitized — the common case ("hero_walk.png" → "hero_walk");
//   3) fallback `<kind>.<idTail>` (always legal, unique per asset) so a key is NEVER null.
export function deriveManifestKey({ filename = null, instructions = "", kind = "image", id = "" } = {}) {
  const fromInstr = sanitizeKey(directive(String(instructions || ""), ["manifestkey", "key"]));
  if (fromInstr) return fromInstr;
  const base = String(filename || "").replace(/\.[^.]+$/, "");
  const fromName = sanitizeKey(base);
  if (fromName) return fromName;
  const tail = String(id || "").split("-").pop() || "x";
  return sanitizeKey(`${kind}.${tail}`) || `fx.${tail}`;
}

// ── manifestEntryFor(asset, existing) → a game manifest AssetEntry (merged over any existing entry) ──────
// Used by derive `--bind` to materialize/refresh a manifest slot from an uploaded asset's Instructions. The
// SOURCE path is always `source/<key>.png` (fix 14 filename auto-bind). status/atlasHash are left to derive.
export function manifestEntryFor(key, asset = {}, existing = null) {
  const parsed = parseInstructions(asset.instructions || "");
  const kind = inferManifestKind(key, parsed);
  const base = existing && typeof existing === "object" ? existing : {};
  return {
    kind: base.kind || kind,
    status: base.status || "placeholder",       // derive flips → "real"
    source: `source/${key}.png`,
    size: parsed.size,
    renderScale: parsed.renderScale,
    anchor: parsed.anchor,
    frames: parsed.frames,
    fps: parsed.fps,
    atlasHash: base.atlasHash || "",
  };
}
