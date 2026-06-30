#!/usr/bin/env node
/*
 * Workshop UI — a tiny, zero-dependency web server for the single-agent Workshop loop.
 *
 * Serves index.html and a handful of /api/workshop/* endpoints that read/write the loop's on-disk
 * state (GOAL.md, backlog.json, completions.json, agent.json, progress.json) and shell out to the
 * PowerShell scripts (workshop-status.ps1 for liveness, start/stop-workshop.ps1 for control). All
 * state lives on disk in the workshop dir — the server keeps nothing important in RAM, so a restart
 * loses nothing. No framework, no node_modules: `node ui/server.js`.
 */
'use strict';
const http = require('http');
const fs = require('fs');
const path = require('path');
const { exec, execFileSync, spawn } = require('child_process');

const WS_DIR  = path.dirname(__dirname);            // the workshop/ dir (parent of ui/)
const UI_DIR  = __dirname;
const CONFIG_PS   = path.join(WS_DIR, 'workshop.config.ps1');
const GOAL        = path.join(WS_DIR, 'GOAL.md');
const BACKLOG     = path.join(WS_DIR, 'backlog.json');
const COMPLETIONS = path.join(WS_DIR, 'completions.json');
const AGENT       = path.join(WS_DIR, 'agent.json');
const STATUS_PS   = path.join(WS_DIR, 'workshop-status.ps1');
const START_PS    = path.join(WS_DIR, 'start-workshop.ps1');
const STOP_PS     = path.join(WS_DIR, 'stop-workshop.ps1');

// The only agent/model combos the Workshop offers. Keyed by a UI id; the loop reads {agent,model}.
// AGY MODEL ID: `gemini-3.5-flash` is an ACCEPTED --model input that agy resolves/serves as canonical
// `gemini-3-flash` (Gemini 3 Flash; there is no distinct served "3.5"). Keep this exact string — it is
// proven to run. To offer a DIFFERENT agy model, get its exact id from `agy models` IN A REAL
// INTERACTIVE TERMINAL (agy's stdout is uncapturable when piped — it returns empty in a script). Do not
// guess an id; an unverified one fails silently + blind. See ../AGENTS.md before changing this.
const WS_AGENT_OPTIONS = {
  'auto':          { agent: 'auto',   model: 'auto',               label: 'Auto — pick per task' },
  'agy-flash':     { agent: 'agy',    model: 'gemini-3.5-flash',   label: 'Agy — Gemini 3 Flash (headless: blind)' },
  'claude-opus':   { agent: 'claude', model: 'claude-opus-4-8',    label: 'Claude Code — Opus 4.8' },
  'claude-sonnet': { agent: 'claude', model: 'claude-sonnet-4-6',  label: 'Claude Code — Sonnet 4.6' },
};
function wsAgentIdFor(sel) {
  return Object.keys(WS_AGENT_OPTIONS).find(k =>
    WS_AGENT_OPTIONS[k].agent === sel.agent && WS_AGENT_OPTIONS[k].model === sel.model) || null;
}

// --- read the PowerShell config once at startup -----------------------------------------------------
// Dot-source workshop.config.ps1 and emit $WorkshopConfig as JSON, so the UI shares the SAME knobs as
// the loop scripts (one source of truth). Falls back to defaults if powershell/config is unavailable.
function loadConfig() {
  const fallback = { Root: '', UiPort: 4455, PreviewUrl: '', PreviewPath: '', WedgeMinutes: 20 };
  try {
    const out = execFileSync('powershell', ['-NoProfile', '-ExecutionPolicy', 'Bypass', '-Command',
      `. '${CONFIG_PS}'; $WorkshopConfig | ConvertTo-Json -Compress`], { encoding: 'utf-8' });
    return Object.assign(fallback, JSON.parse(out.replace(/^﻿/, '')));
  } catch (e) {
    console.error('config read failed, using defaults:', e.message);
    return fallback;
  }
}
const CFG  = loadConfig();
const PORT = Number(CFG.UiPort) || 4455;

// --- tiny helpers -----------------------------------------------------------------------------------
function readJson(p) { return JSON.parse(fs.readFileSync(p, 'utf-8').replace(/^﻿/, '')); }
function readArr(p)  { try { return readJson(p); } catch { return []; } }
function writeJson(p, v) { fs.writeFileSync(p, JSON.stringify(v, null, 2), 'utf-8'); }   // node = no BOM
function send(res, code, obj) {
  const body = JSON.stringify(obj);
  res.writeHead(code, { 'Content-Type': 'application/json; charset=utf-8' });
  res.end(body);
}
function readBody(req) {
  return new Promise((resolve) => {
    let b = '';
    req.on('data', c => { b += c; if (b.length > 1e6) req.destroy(); });
    req.on('end', () => { try { resolve(b ? JSON.parse(b) : {}); } catch { resolve({}); } });
  });
}
const MIME = { '.html': 'text/html', '.js': 'text/javascript', '.css': 'text/css',
  '.json': 'application/json', '.png': 'image/png', '.svg': 'image/svg+xml', '.ico': 'image/x-icon' };
function serveFile(res, file) {
  fs.readFile(file, (err, buf) => {
    if (err) { res.writeHead(404); res.end('not found'); return; }
    res.writeHead(200, { 'Content-Type': MIME[path.extname(file).toLowerCase()] || 'application/octet-stream' });
    res.end(buf);
  });
}

// --- request router ---------------------------------------------------------------------------------
const server = http.createServer(async (req, res) => {
  const url = new URL(req.url, `http://localhost:${PORT}`);
  const p = url.pathname;

  try {
    // ---- API ----
    if (p === '/api/workshop/config' && req.method === 'GET') {
      let preview = null;
      if (CFG.PreviewUrl)       preview = { url: CFG.PreviewUrl };
      else if (CFG.PreviewPath) preview = { url: '/preview/' };
      return send(res, 200, { uiPort: PORT, root: CFG.Root || '', preview, options: WS_AGENT_OPTIONS });
    }

    if (p === '/api/workshop/status' && req.method === 'GET') {
      return exec(`powershell -NoProfile -ExecutionPolicy Bypass -File "${STATUS_PS}"`,
        { maxBuffer: 4 * 1024 * 1024 }, (err, stdout) => {
          if (err) return send(res, 500, { error: err.message });
          try { send(res, 200, JSON.parse(stdout.replace(/^﻿/, ''))); }
          catch (e) { send(res, 500, { error: 'bad status JSON', raw: stdout, parseError: e.message }); }
        });
    }

    if (p === '/api/workshop/goal') {
      if (req.method === 'GET') {
        return send(res, 200, { goal: fs.existsSync(GOAL) ? fs.readFileSync(GOAL, 'utf-8') : '' });
      }
      if (req.method === 'POST') {
        const body = await readBody(req);
        if (body.goal == null) return send(res, 400, { error: 'goal is required' });
        fs.writeFileSync(GOAL, String(body.goal), 'utf-8');
        return send(res, 200, { ok: true });
      }
    }

    if (p === '/api/workshop/backlog') {
      if (req.method === 'GET') return send(res, 200, readArr(BACKLOG));
      if (req.method === 'POST') {
        const body = await readBody(req);
        const title = (body.title || '').trim();
        if (!title) return send(res, 400, { error: 'title is required' });
        const backlog = readArr(BACKLOG);
        const item = { id: `ws-${Date.now()}`, title, detail: (body.detail || '').trim(),
          created: new Date().toISOString() };
        if (body.top) backlog.unshift(item); else backlog.push(item);
        writeJson(BACKLOG, backlog);
        return send(res, 200, { ok: true, item });
      }
    }

    if (p === '/api/workshop/backlog/delete' && req.method === 'POST') {
      const body = await readBody(req);
      if (!body.id) return send(res, 400, { error: 'id is required' });
      writeJson(BACKLOG, readArr(BACKLOG).filter(b => b.id !== body.id));
      return send(res, 200, { ok: true });
    }

    if (p === '/api/workshop/completions' && req.method === 'GET') {
      const arr = readArr(COMPLETIONS);
      arr.sort((a, b) => String(b.completed || '').localeCompare(String(a.completed || '')));
      return send(res, 200, arr.slice(0, 100));
    }

    if (p === '/api/workshop/agent') {
      if (req.method === 'GET') {
        let sel = { agent: 'claude', model: 'claude-sonnet-4-6' };
        try { if (fs.existsSync(AGENT)) sel = readJson(AGENT); } catch {}
        return send(res, 200, { ...sel, id: wsAgentIdFor(sel), options: WS_AGENT_OPTIONS });
      }
      if (req.method === 'POST') {
        const body = await readBody(req);
        const opt = WS_AGENT_OPTIONS[body.id];
        if (!opt) return send(res, 400, { error: `Unknown agent option: ${body.id}` });
        // BOM-free: the loop's PS5.1 readers + Node alike choke on a UTF-8 BOM. Node writes none.
        fs.writeFileSync(AGENT, JSON.stringify({ agent: opt.agent, model: opt.model }, null, 2), 'utf-8');
        return send(res, 200, { ok: true, id: body.id, agent: opt.agent, model: opt.model });
      }
    }

    // Loop control — spawn the (self-detaching) start/stop scripts and return their short output.
    if ((p === '/api/workshop/start' || p === '/api/workshop/stop') && req.method === 'POST') {
      const script = p.endsWith('start') ? START_PS : STOP_PS;
      const child = spawn('powershell', ['-NoProfile', '-ExecutionPolicy', 'Bypass', '-File', script],
        { windowsHide: true });
      let out = '';
      child.stdout.on('data', d => { out += d; });
      child.stderr.on('data', d => { out += d; });
      child.on('close', code => send(res, 200, { ok: code === 0, code, output: out.trim() }));
      child.on('error', e => send(res, 500, { error: e.message }));
      return;
    }

    // ---- static: optional project preview mounted from Root/PreviewPath ----
    if (p.startsWith('/preview/') && CFG.PreviewPath && CFG.Root) {
      const rel = decodeURIComponent(p.slice('/preview/'.length)) || 'index.html';
      const base = path.resolve(CFG.Root, CFG.PreviewPath);
      const file = path.resolve(base, rel);
      if (!file.startsWith(base)) { res.writeHead(403); return res.end('forbidden'); }   // no traversal
      return serveFile(res, fs.existsSync(file) && fs.statSync(file).isFile() ? file : path.join(base, 'index.html'));
    }

    // ---- static UI ----
    if (p === '/' || p === '/index.html') return serveFile(res, path.join(UI_DIR, 'index.html'));
    const staticFile = path.resolve(UI_DIR, '.' + p);
    if (staticFile.startsWith(UI_DIR) && fs.existsSync(staticFile) && fs.statSync(staticFile).isFile()) {
      return serveFile(res, staticFile);
    }

    res.writeHead(404); res.end('not found');
  } catch (e) {
    send(res, 500, { error: e.message });
  }
});

server.listen(PORT, () => {
  console.log(`Workshop UI → http://localhost:${PORT}`);
  console.log(`  workshop dir : ${WS_DIR}`);
  console.log(`  target repo  : ${CFG.Root || '(set Root in workshop.config.ps1)'}`);
});
