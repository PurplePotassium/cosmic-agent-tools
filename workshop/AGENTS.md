# Workshop — how the agent drivers actually work (READ before touching agent/model wiring)

Scope: the **Workshop** (single-agent loop) only — this directory + `ui/server.js`'s `/api/workshop/*`
endpoints. The engine is `workshop.ps1`; Workshop-only behavior (the live `-AgentControlFile`
agent/model switch + per-pass `Resolve-AutoSelection` routing) lives in it. This is NOT the fleet.

This file exists because "the agents behave weirdly headless" keeps recurring. Most of it is NOT a bug —
it's how the two backends behave when there's no terminal. Know this before you "fix" anything.

## The two drivers

| | **claude** (Claude Code) | **agy** (Antigravity CLI / Gemini) |
|---|---|---|
| invoke | prompt over **stdin**, `claude -p --model <id> --dangerously-skip-permissions` | prompt as **arg**, `agy -p <prompt> --dangerously-skip-permissions --print-timeout 30m [--model <id>]` |
| output capture | **streamed live + captured** to the iter log | **uncapturable headless — see below** |
| commit | the loop commits each pass itself (`ralph iter N [agent]`) | same — the loop commits it |
| reliability headless | **solid** — the dependable choice | **works but BLIND** (no captured output) |

## ⚠️ agy's killer gotcha: print output is uncapturable under non-TTY (upstream, unfixed)

`agy -p` / `agy models` / any agy print silently **drops stdout when stdout is a pipe, redirect, or
subprocess** (non-TTY). Confirmed: `agy models` returns EMPTY when captured by a script/`exec`. Upstream
bug, open, no fix: <https://github.com/google-antigravity/antigravity-cli/issues/76>. ConPTY/winpty don't
help; `Start-Process` with redirected streams **hangs**.

Consequences for the Workshop (the loop runs **hidden + detached**, so no human watches a live window):
- The iter log for an agy pass is **empty except the header**. That is **EXPECTED, not a crash.** It
  cannot be filled headless — don't "fix" it.
- `workshop.ps1` deliberately does **not** pipe agy (piping drops output AND can hang); it lets agy own
  the hidden console and points `--log-file` at the logs dir. `--log-file` is agy's **operational** log,
  **not** the model response — it won't contain the answer text.
- **Auth-failure detection is blind for agy.** The auth-keyword scan reads captured output, which is
  empty. An agy auth failure shows up only as a non-zero exit → counted as a generic transient fail
  (5-in-a-row trips the breaker). If agy "does nothing" for several passes, **suspect auth** and run
  `agy` interactively once to re-auth.
- Liveness comes from the **process tree + CPU + git dirty tree** (`workshop-status.ps1`), never the log.
  "Empty log" ≠ "wedged". `wedged` = pass older than `WedgeMinutes` (config; default 20).
- **The agent self-report is the real window into an agy pass.** `PROMPT.md` requires every pass to
  overwrite `progress.json` (`{phase,task,plan,note,result,updated}`) at pass START (phase=working) and
  END — **file writes work even when agy stdout doesn't**. `workshop-status.ps1` surfaces it as
  `progress` + `progressAgeSec` (so the UI can flag a STALE report from a previous pass). If you drive
  agy, this is how the operator sees it; if you ARE a pass, never skip the start write.

To SEE agy output for debugging, run agy **in a real interactive terminal**, or read its conversation DB
(`<user>\.gemini\antigravity-cli\conversations\<id>.db`, sqlite; cwd→conversation map in
`..\cache\last_conversations.json`).

## agy model id — what string to pass

`agy --model` accepts **`gemini-3.5-flash`** and resolves/serves it as canonical **`gemini-3-flash`**
(Gemini 3 Flash — there is no separate served "3.5"). So the configured id **already works**; it is not
the bug. To use a **different** agy model, get the exact id from `agy models` **in a real interactive
terminal** (it returns empty when captured — same non-TTY bug). Do NOT guess an id: a bad `--model` fails
**silently and blind** headless, so the loop just stops producing changes with no visible error. Put the
exact id in `agent.json` (via the UI) or `workshop.config.ps1`.

## Where agent/model selection lives

- `agent.json` = `{ "agent": "claude|agy|auto", "model": "<id>|auto" }`. UI-editable; the loop re-reads
  it at the **top of each pass** (`-AgentControlFile`), so a switch lands on the **NEXT** pass, never the
  in-flight one. Must stay **BOM-free** (Node `JSON.parse` chokes on a BOM; PS5.1 `Set-Content -Encoding
  utf8` writes one — the scripts write raw via `UTF8Encoding($false)`, and the UI server via Node, which
  writes none).
- `auto` → `Resolve-AutoSelection` (`workshop.ps1`) classifies the top backlog item: light/presentation
  → agy; heavy/structural → claude/opus; else → claude/sonnet.
- UI: `GET|POST /api/workshop/agent` (POST validates `{id}` against the baked-in `WS_AGENT_OPTIONS` in
  `ui/server.js`).

## One change needs a loop Stop → Start to take effect (NOT a bug)

A running loop holds the **old `workshop.ps1` in memory**. **Any edit to `workshop.ps1`** (driver
resolution, model-switch logic, agy invocation) only applies to a freshly-started loop — Stop → Start.
The **agent/model selection** itself (`agent.json`) does NOT need a restart — it's re-read each pass. If
the operator reports "my change/switch did nothing," the first answer is almost always: **Stop, Start.**

## Hard rules

- Keep `agent.json` / `backlog.json` / `completions.json` / `progress.json` valid JSON, **no BOM**.
- The loop commits each pass as `ralph iter N [agent]` — `workshop-status.ps1` greps `^ralph iter` for
  `lastIter`, so keep that subject prefix if you change the commit message.
