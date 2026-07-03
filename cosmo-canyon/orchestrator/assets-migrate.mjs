// Cosmo Canyon — one-shot migration of the real placeholder manifest keys → placeholderOnly assets
// (§15g phase 1 / §15.31). For each `status:"placeholder"` key in game/assets/manifest.json, mint ONE
// placeholderOnly image asset (kind:image, manifestKey:<key>, state:not_ready, no artifact bytes since it
// is a positioning slot with no source art) with an inferred `files[]` = the manifest SRC path for that key
// (SRC only, NEVER an accept/ path per §15c-2/15.7). Idempotent: keyed by manifestKey — a re-run creates
// nothing (skips any key already backed by an asset). Non-placeholder keys are NOT migrated.
//
// ROOT via env CC_CONTROL (default real control/); manifest via env CC_MANIFEST (default real manifest).
import { createAsset, rebuildIndex } from "./assets.mjs";
import { readFileSync, readdirSync } from "node:fs";

const CC = "C:/Vibes/cosmo-canyon";
function controlRoot() { return process.env.CC_CONTROL || `${CC}/control`; }
function manifestPath() { return process.env.CC_MANIFEST || `${CC}/game/assets/manifest.json`; }
function readJsonSafe(p, d = null) { try { return JSON.parse(readFileSync(p, "utf8")); } catch { return d; } }

// manifestKeys already backed by an asset (authority scan, not the derived index) → idempotency guard.
function existingManifestKeys() {
  const keys = new Set();
  let names = [];
  try { names = readdirSync(`${controlRoot()}/assets`); } catch { names = []; }
  for (const id of names) {
    const m = readJsonSafe(`${controlRoot()}/assets/${id}/meta.json`, null);
    if (m && m.manifestKey) keys.add(m.manifestKey);
  }
  return keys;
}

function main() {
  const manifest = readJsonSafe(manifestPath(), null);
  if (!manifest || typeof manifest !== "object") { console.error(`no manifest at ${manifestPath()}`); process.exit(2); }
  const have = existingManifestKeys();
  const created = [];
  const skipped = [];
  for (const [key, entry] of Object.entries(manifest)) {
    if (!entry || entry.status !== "placeholder") { skipped.push({ key, why: "not-placeholder" }); continue; }
    if (have.has(key)) { skipped.push({ key, why: "exists" }); continue; }
    // inferred SRC ownership: entry.source is game/assets-relative ("source/<key>.png") → game-relative
    // "assets/source/<key>.png". SRC only; the accept/ grader is authored separately (PROTECTED, 15.7).
    const src = entry.source ? `assets/${entry.source}` : `assets/source/${key}.png`;
    const meta = createAsset({
      kind: "image",
      filename: `${key}.png`,
      bytes: null,               // placeholder slot — no source art
      instructions: "",
      manifestKey: key,
      files: [src],
      placeholderOnly: true,
      state: "not_ready",
    });
    created.push({ key, id: meta.id, files: meta.files });
  }
  const index = rebuildIndex();
  console.log(JSON.stringify({ created, skipped, counts: index.counts }, null, 2));
}

main();
