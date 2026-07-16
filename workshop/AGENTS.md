# AGENTS.md — driver behavior facts (read before touching driver wiring)

This file records **externally imposed** behaviors of the agent CLIs Workshop
spawns. None of these are Workshop design choices; "fixing" the code that
respects them re-breaks hard-won lessons. The implementation lives in
`internal/driver/` — every difference below is a declared `Capability`, never
an if-chain on an agent's name elsewhere.

## claude (Claude Code)

- Prompt over **stdin**; invocation `claude -p --model <id>
  [--effort <level>] [--dangerously-skip-permissions]`.
- Output is **capturable and streamable** — the engine pipes stdout+stderr
  line-by-line to the pass log, the dashboard, and the auth scan.
- **Effort flag support is probed, never assumed**: the driver greps
  `claude --help` for `--effort` at startup. If absent, configured efforts
  are ignored with a `driver.effort_ignored` event.
- Auth failures are detectable from output (`AuthProbe=true`): the engine
  scans failed passes for auth keywords and halts the pipeline immediately —
  an expired login never self-heals and must not burn passes.
- Binary discovery: `WORKSHOP_CLAUDE_BIN` env override → PATH.

## agy (Antigravity CLI / Gemini)

The blind driver. Upstream facts, all confirmed the hard way in the
PowerShell era:

- **stdout is silently dropped when it is a pipe/redirect (non-TTY)**, and
  redirected spawns can hang. Therefore agy is **NEVER piped**: it gets its
  own hidden console (Windows `CREATE_NEW_CONSOLE` + hidden window, zero
  stdio redirection). ConPTY tricks do not help.
- Consequently a pass is **blind**: the iter log holds only a header. This
  is EXPECTED, not a bug, and cannot be "fixed" headless.
- The agent's `progress.json` self-report is the **only window** into a
  pass. The pass contract forces a start-write the moment work begins; the
  dashboard shows the report + its freshness instead of a fake log pane.
- Prompt goes as a **`-p` argument**, never stdin.
- `--log-file` captures agy's **operational log only** — not the model's
  response text.
- `--print-timeout 30m` (upstream default 5m cuts real increments short).
  Pipeline wedge timeouts must exceed it.
- **Auth failures are invisible** (generic nonzero exit). The engine's
  heuristic: N consecutive failures with **no progress start-write** raises
  an `auth.suspected` alert telling the operator to run `agy` interactively
  once. The circuit breaker remains the backstop.
- **A wrong `--model` label exits 1 BEFORE the prompt is sent** (verified
  2026-07), still with no stdout — but the `--log-file` operational log then
  contains `invalid --model` **plus the full "Available models:" list**.
  `driver.(*Agy).ListModels` exploits this: probing with a deliberately
  bogus label enumerates the valid labels headless and quota-free (`agy
  models` itself needs a real TTY and hangs on pipes). Labels are display
  strings and must match EXACTLY (case-insensitive): `Gemini 3.1 Pro
  (High)` works, `gemini 3 pro` is rejected.
- **Conversation resume is native**: `--conversation <id>` (also `-c` /
  `--continue` for most-recent). The id of the conversation a `-p` run just
  used is recoverable headless from
  `~/.gemini/antigravity-cli/cache/last_conversations.json` — a
  `{"<abs workdir>": "<uuid>"}` map agy rewrites WHOLE on every run. That
  whole-file rewrite is why art passes hold the engine's exclusive agy lock:
  any concurrently running agy instance races the record the art-gen-trans
  flow depends on. (`WORKSHOP_AGY_STATE_DIR` overrides the state dir root.)
- Binary discovery: `WORKSHOP_AGY_BIN` → PATH → known install dirs
  (`%LOCALAPPDATA%\agy\bin\agy.exe`, `~/.local/bin/agy`, ...) — installers
  update the registry PATH, which already-open shells don't see.

## fake (tests and smoke runs)

`WORKSHOP_FAKE_BIN` points at any binary that re-enters
`internal/fakeagent.Main()` (the workshop binary itself and the engine test
binaries both do, via the hidden `_fake-agent` argv). Scenario-driven via
`WORKSHOP_FAKE_SCENARIO`: happy / blocked / reverted / silent / crash / auth
/ sleep / resolve. `WORKSHOP_FAKE_BLIND=1` simulates an agy-shaped blind
driver. This is how every engine path is tested without real agents.

## Hard rules the engine enforces (don't relax them)

- Every JSON state file the agent touches is **BOM-free UTF-8**; readers
  strip a BOM defensively; writes are atomic (temp+rename).
- One commit per dirty pass, subject `ws(<pipeline>) iter <N> [<agent>]`,
  with `Workshop-Task:` / `Workshop-Pass:` trailers.
- The agent never runs `git commit`, never edits `backlog.json` /
  `completions.json` (engine-owned projections; a misbehaving agent's edits
  are diff-ingested harmlessly, never trusted).
- Agent processes are spawned in their own process group and killed as a
  **tree** — on Windows via a kill-on-close Job Object (reaches grandchildren
  whose intermediate parent exited, and dies with the Workshop on a crash;
  `taskkill /T /F` is the fallback), on Unix via negative-pgid SIGKILL. A
  wedged pass must not leak builds or test servers.
- Model/effort switching happens per pass via routing (pin > live pipeline
  override (dashboard ⚙, re-read from the store every pass) > `[types.*]` >
  pipeline bundle); a bundle that switches agents never inherits the other
  agent's model or effort.
