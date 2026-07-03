// Cosmo Canyon — headless game screenshot for feel/visual verification (§3c/§3d, spike B).
//
// A headless `claude -p` tick has NO preview MCP (spike B), so feel-verify uses a puppeteer
// screenshot of the vite dev server → Claude Reads the PNG + visual-critique. puppeteer's own
// screenshot works on a WebGL/PixiJS canvas (the Claude Preview MCP hangs on such pages — that
// gotcha is MCP-specific). WebGL2 in headless runs on SwiftShader. Ported from FC fc-snapshot.mjs.
//
// Usage: node snapshot.mjs [--out <png>] [--url http://localhost:8780/] [--run <seed>] [--ticks N]
//   --run <seed>: drive window.__cc into live gameplay before the shot (else captures the menu).
import { existsSync, mkdirSync } from "node:fs";
import { dirname } from "node:path";
import { createRequire } from "node:module";

// puppeteer-core lives in game/node_modules (the game owns it); this script lives in
// orchestrator/, so an ESM bare import can't reach it. Resolve via require rooted at game/.
const require = createRequire("C:/Vibes/cosmo-canyon/game/package.json");
const puppeteer = require("puppeteer-core");

const args = process.argv.slice(2);
const arg = (n, d) => { const i = args.indexOf(`--${n}`); return i >= 0 && i + 1 < args.length ? args[i + 1] : d; };
const URL = arg("url", "http://localhost:8780/");
const OUT = arg("out", "C:\\Vibes\\cosmo-canyon\\snapshots\\latest.png");
const RUN_SEED = arg("run", null);
const RUN_TICKS = Number(arg("ticks", "240"));
const BROWSERS = [
  "C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe",
  "C:\\Program Files (x86)\\Google\\Chrome\\Application\\chrome.exe",
  "C:\\Program Files (x86)\\Microsoft\\Edge\\Application\\msedge.exe",
  "C:\\Program Files\\Microsoft\\Edge\\Application\\msedge.exe",
];

async function main() {
  const exe = BROWSERS.find(existsSync);
  if (!exe) { console.error("snapshot: no Chrome/Edge found"); process.exit(2); }
  mkdirSync(dirname(OUT), { recursive: true });
  const browser = await puppeteer.launch({
    executablePath: exe,
    headless: true,
    args: ["--no-sandbox", "--disable-dev-shm-usage", "--use-gl=angle", "--use-angle=swiftshader", "--enable-unsafe-swiftshader"],
  });
  try {
    const page = await browser.newPage();
    await page.setViewport({ width: 900, height: 620, deviceScaleFactor: 1 });
    await page.goto(URL, { waitUntil: "domcontentloaded", timeout: 15000 });
    await page.waitForSelector("canvas", { timeout: 8000 });
    await new Promise((r) => setTimeout(r, 1500)); // let boot + a few frames run
    if (RUN_SEED != null) {
      // drive the headless sim into live gameplay so the shot shows the world, not the menu
      await page.evaluate((seed, n) => {
        const cc = (window).__cc; if (!cc) return;
        cc.beginRun(seed >>> 0);
        for (let i = 0; i < n; i++) { cc.aiPick(); cc.step(1 / 60); }
      }, Number(RUN_SEED), RUN_TICKS);
      await new Promise((r) => setTimeout(r, 300));
    }
    try { await page.evaluate(() => (window).__render && (window).__render()); } catch { /* harness may not expose it */ }
    await new Promise((r) => setTimeout(r, 200));
    await page.screenshot({ path: OUT });
    console.log("snapshot ->", OUT);
  } finally {
    await browser.close();
  }
}
main().catch((e) => { console.error("snapshot failed:", e.message); process.exit(1); });
