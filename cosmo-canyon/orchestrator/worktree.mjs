// Cosmo Canyon — per-agent WORKTREE lifecycle (§15c-2 / 15.43). EXPLICIT-PATH-ONLY, no bare prune, ever.
//
// A parallel agent gets an isolated `C:/Vibes-cc-wt/<assetId>` checkout off `cosmo-canyon` `--detach` at the
// claim's baseSha. The per-agent tick ANCHOR (baseSha/beadId/worktree) lives in the CLAIM (claim.mjs), NOT a
// singleton .tick.json (15.26) — this module only makes/destroys the tree and hands its path back.
//
// 15.43 (CRITICAL, FC/fleet retired): NEVER `git worktree prune` anywhere — a bare prune would GC a sibling
// worktree whose dir momentarily looks absent. Remove ONLY by EXPLICIT path, and refuse any path that is not
// a direct child of the configured worktree root (default `C:/Vibes-cc-wt`). On a `remove` failure (dir
// vanished / not a worktree) delete ONLY that assetId's `.git/worktrees/<name>` admin dir — never a prune.
//
// Before any destructive op INSIDE a worktree, assert `rev-parse --show-toplevel == that worktree path`
// (blast-radius guard, mirrors bookkeep's toplevel assert) so a wrong-cwd never nukes the main tree or a
// sibling. Toggle stays OFF: no live caller (phase 8 arms dispatch). Contract + unit test on a throwaway repo.
import { spawnSync } from "node:child_process";
import { existsSync, rmSync, symlinkSync, lstatSync } from "node:fs";
import { resolve, basename } from "node:path";
import { readConfig } from "./config.mjs";

// §SPLIT (2026-07-03) — worktrees are now checkouts of the GAME repo (cosmo-canyon/game, its own .git). "REPO" here
// is that game repo. CC_GAME pins it directly (harness); else it's cosmo-canyon/game under CC_REPO (default C:/Vibes).
const REPO = process.env.CC_GAME || `${process.env.CC_REPO || "C:/Vibes"}/cosmo-canyon/game`;
const norm = (p) => String(p || "").replace(/\\/g, "/").replace(/\/+$/, "");
// §15g phase 8 — the game's node_modules is GITIGNORED, so a fresh worktree checkout has none → `npm run gate`
// (tsc + tsx) cannot run in the worktree. Junction the MAIN tree's node_modules in so a parallel worker can gate
// IN ITS worktree. CRITICAL blast-radius (empirically confirmed): `git worktree remove --force` FOLLOWS a
// node_modules junction and DELETES the shared node_modules → the junction MUST be dropped (non-following) BEFORE
// any worktree removal. remove() does that at the single choke point (reconcileParallel + merge both call it).
const GAME_NM_REL = "node_modules"; // §SPLIT — the worktree IS the game repo, so node_modules sits at its root
function gameNodeModules(wtPath) { return `${norm(wtPath)}/${GAME_NM_REL}`; }
function mainNodeModules() { return `${REPO}/${GAME_NM_REL}`; }
// drop a node_modules JUNCTION (reparse point) without following it into the shared target. rmSync recursive:false
// on a junction unlinks the link only (verified: main node_modules untouched). A REAL dir (non-junction) is left
// alone — never recursively deleted here (that would be the blast-radius bug we are guarding against).
function dropNodeModulesJunction(wtPath) {
  const nm = gameNodeModules(wtPath);
  let st; try { st = lstatSync(nm); } catch { return false; }
  if (!st.isSymbolicLink()) return false; // a real node_modules (shouldn't happen) is NOT touched
  try { rmSync(nm, { recursive: false, force: true }); return true; } catch { return false; }
}

// git via argv-array (shell:false) — worktree paths never touch a shell (no metachar injection).
function git(cwd, ...args) { return spawnSync("git", ["-C", cwd, ...args], { encoding: "utf8", maxBuffer: 32 * 1024 * 1024 }); }

const WT_ID_RE = /^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$/;
function validateWtId(id) {
  if (typeof id !== "string" || !WT_ID_RE.test(id) || id.includes("..")) throw new Error(`bad worktree id: ${JSON.stringify(id)}`);
  return id;
}
function effectiveRoot(opts = {}) {
  const root = norm(opts.root || readConfig().concurrency.worktreeRoot || "C:/Vibes-cc-wt");
  if (!/^[A-Za-z]:\/\S/.test(root)) throw new Error(`unsafe worktree root (must be drive-anchored abs path): ${root}`);
  return root;
}

// The load-bearing 15.43 guard: `path` MUST be a direct child of `root` (root/<child>, no deeper nesting, no
// escape). Refuses anything else — this is what makes "explicit-path-only removal" enforceable.
export function assertExplicitWorktreePath(path, opts = {}) {
  const root = effectiveRoot(opts);
  const p = norm(resolve(norm(path)));
  const r = norm(resolve(root));
  if (!p.startsWith(r + "/")) throw new Error(`refusing worktree op: '${p}' not under root '${r}' (15.43)`);
  if (norm(resolve(r, basename(p))) !== p) throw new Error(`refusing worktree op: '${p}' is not a direct child of '${r}' (15.43)`);
  return p;
}

// rev-parse --show-toplevel INSIDE the worktree must equal the worktree path (else the cwd is wrong → abort
// before any destructive op). Returns the normalized toplevel; throws on mismatch.
export function assertWorktreeToplevel(path) {
  const p = norm(resolve(norm(path)));
  const top = norm(git(p, "rev-parse", "--show-toplevel").stdout || "").trim();
  if (top !== p) throw new Error(`refusing destructive worktree op: toplevel '${top}' != '${p}'`);
  return p;
}

export function worktreePath(assetId, opts = {}) { return `${effectiveRoot(opts)}/${validateWtId(assetId)}`; }

// list linked worktrees (read-only; porcelain parse). NEVER used for removal decisions beyond info.
export function list(opts = {}) {
  const repo = norm(opts.repo || REPO);
  const out = git(repo, "worktree", "list", "--porcelain").stdout || "";
  const wts = []; let cur = null;
  for (const line of out.split("\n")) {
    if (line.startsWith("worktree ")) { cur = { path: norm(line.slice(9)), detached: false, head: null }; wts.push(cur); }
    else if (cur && line.startsWith("HEAD ")) cur.head = line.slice(5).trim();
    else if (cur && line === "detached") cur.detached = true;
  }
  return wts;
}

// create C:/Vibes-cc-wt/<assetId> off `branch` --detach at baseSha. Explicit-path-guarded. Returns {ok,path}.
export function create(assetId, baseSha, opts = {}) {
  const repo = norm(opts.repo || REPO);
  const path = assertExplicitWorktreePath(worktreePath(assetId, opts), opts);
  if (!baseSha) throw new Error("worktree.create: baseSha required (anchor)");
  const r = git(repo, "worktree", "add", "--detach", path, baseSha);
  if (r.status !== 0) return { ok: false, path, error: ((r.stderr || "") + (r.stdout || "")).slice(0, 240) };
  return { ok: true, path };
}

// §15g phase 8 — junction the MAIN tree's game node_modules into the worktree so a parallel worker can run
// `npm run gate` (tsc+tsx) in isolation. Explicit-path-guarded (the worktree must be under the configured root).
// Idempotent (skip if already a link). Returns {ok, linked, path|error}. The junction is dropped by remove()
// BEFORE the destructive worktree removal (never followed → shared node_modules is safe).
export function linkNodeModules(wtPath, opts = {}) {
  const p = assertExplicitWorktreePath(wtPath, opts);
  const nm = gameNodeModules(p);
  const target = mainNodeModules();
  // no main node_modules to link (a dep-free game, or a fresh clone before `npm install`) → skip the junction;
  // a missing dep will surface as a gate failure, not as a dispatch rollback (more robust than hard-failing here).
  if (!existsSync(target)) return { ok: true, linked: false, note: "no main node_modules to link" };
  try { const st = lstatSync(nm); if (st.isSymbolicLink() || st.isDirectory()) return { ok: true, linked: false, path: nm, already: true }; } catch { /* absent → create */ }
  try { symlinkSync(target, nm, "junction"); return { ok: true, linked: true, path: nm }; }
  catch (e) { return { ok: false, error: String((e && e.message) || e).slice(0, 160) }; }
}

// remove ONLY by explicit path (15.43). Refuse non-root paths. Assert toplevel before the destructive remove.
// On remove failure, delete ONLY this id's .git/worktrees/<name> admin dir — NEVER `git worktree prune`.
export function remove(path, opts = {}) {
  const repo = norm(opts.repo || REPO);
  const p = assertExplicitWorktreePath(path, opts); // refuses any path not a direct child of the root
  // §15g phase 8 CRITICAL — drop the node_modules junction BEFORE any destructive remove. `git worktree remove
  // --force` (and a recursive rmSync fallback) FOLLOW a junction and DELETE the shared main node_modules
  // (empirically confirmed: 7733→7487 files). dropNodeModulesJunction unlinks the link non-recursively (safe).
  const jDropped = dropNodeModulesJunction(p);
  if (existsSync(p)) {
    // only assert toplevel if it still looks like a git worktree (a half-gone dir has no toplevel)
    const top = norm(git(p, "rev-parse", "--show-toplevel").stdout || "").trim();
    if (top === p) {
      const r = git(repo, "worktree", "remove", "--force", p);
      if (r.status === 0) return { ok: true, removed: p, junctionDropped: jDropped };
    }
  }
  // fallback: the worktree dir is gone / not a worktree → drop ONLY this id's admin dir (never a bare prune)
  const admin = `${repo}/.git/worktrees/${basename(p)}`;
  let adminRemoved = false;
  if (existsSync(admin)) { try { rmSync(admin, { recursive: true, force: true }); adminRemoved = true; } catch {} }
  // and remove the (possibly empty/leftover) worktree dir itself, explicit path only
  if (existsSync(p)) { try { rmSync(p, { recursive: true, force: true }); } catch {} }
  return { ok: true, removed: p, viaAdminDir: adminRemoved, junctionDropped: jDropped };
}
