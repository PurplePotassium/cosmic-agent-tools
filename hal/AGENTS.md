# AGENTS.md — driver behavior facts (read before touching driver wiring)

This file records **externally imposed** behaviors of the agent CLIs Hal
spawns. None of these are Hal design choices; "fixing" the code that
respects them re-breaks hard-won lessons. The implementation lives in
`internal/driver/` — every difference below is a declared `Capability`, never
an if-chain on an agent's name elsewhere.

## claude (Claude Code)

- Prompt over **stdin**; invocation `claude -p --model <id>
  [--effort <level>] [--dangerously-skip-permissions]`.
- **Interactive workflow turns** add `--output-format stream-json
  --include-partial-messages --verbose` plus `--session-id <minted-uuid>`
  (first turn of a stage) or `--resume <id>` (every later turn): one OS
  process per conversational turn, typed NDJSON events on stdout ending in a
  per-turn `result`. The full probed contract (event vocabulary, kill-mid-turn
  → resume, permission behavior in print mode) lives in
  [`docs/interactive-driver.md`](docs/interactive-driver.md); sanitized real
  captures are the parser's golden files under
  `internal/driver/testdata/stream/`.
- **Path-scoped `Write(<glob>)` permission rules do NOT match in print mode**
  (probed v2.1.220): read-only stages allow bare `Write` and the engine's
  post-turn tree check is the real write-scope enforcement. Don't "fix" this
  by re-scoping the rules.
- Output is **capturable and streamable** — the engine pipes stdout+stderr
  line-by-line to the turn log, the dashboard, and the auth scan.
- **Effort flag support is probed, never assumed**: the driver greps
  `claude --help` for `--effort` at startup. If absent, configured efforts
  are ignored with a `driver.effort_ignored` event.
- Auth failures are detectable from output (`AuthProbe=true`): the engine
  scans failed turns for auth keywords and marks the failure `auth:` — an
  expired login never self-heals and must not burn retries.
- Binary discovery: `HAL_CLAUDE_BIN` env override → PATH.

## codex (OpenAI Codex CLI)

- Prompt over **stdin**; invocation `codex exec --model <id>` with
  `-c model_reasoning_effort="<level>"` when the installed CLI advertises
  `--config` support.
- Workflow turns use `codex exec --json` and recover Codex's runtime-minted
  thread id from `thread.started`; later turns use
  `codex exec resume <id> --json`. The JSONL is capturable and translated to
  the same typed turn vocabulary as Claude. Codex does not emit token deltas,
  so assistant text appears when its `agent_message` item settles.
- Codex has no caller-minted session-transcript flag in `exec` mode, so the
  driver does not claim the `Sessions` capability. Stage rows record which
  runtime minted a resume id; changing agents mid-stage starts a fresh
  session instead of passing an alien id to the new CLI.
- `[safety] skip_permissions = true` maps to Codex's documented
  `--dangerously-bypass-approvals-and-sandbox`; when disabled, Hal uses
  `--sandbox workspace-write` instead.
- Binary discovery: `HAL_CODEX_BIN` env override → PATH.

## agy (Antigravity CLI / Gemini)

The blind driver. Upstream facts, all confirmed the hard way in the
PowerShell era:

- **stdout is silently dropped when it is a pipe/redirect (non-TTY)**, and
  redirected spawns can hang. Therefore agy is **NEVER piped**: it gets its
  own hidden console (Windows `CREATE_NEW_CONSOLE` + hidden window, zero
  stdio redirection). ConPTY tricks do not help. This is also why the art
  jobs' claude orchestrator must invoke agy through `hal agy-run`
  (a consoled passthrough, `driver.AgyExec`) — a bare `agy` from an agent's
  shell tool is piped and therefore unsafe.
- Consequently a direct agy run is **blind**: its log holds only a header.
  This is EXPECTED, not a bug, and cannot be "fixed" headless. (Art jobs
  stay observable because the piped claude orchestrator narrates them.)
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
- **A full transcript exists despite the blindness** (verified 2026-07-16):
  agy unconditionally writes
  `<state>/brain/<conversation-id>/.system_generated/logs/transcript.jsonl`
  — JSONL steps holding the USER_INPUT prompt exactly as the model saw it,
  the MODEL response `content` AND its `thinking` reasoning summary, and
  SYSTEM checkpoint/reminder steps. No flag involved (`--log-file` stays
  operational-only). The id is runtime-minted, so the engine recovers it
  post-run from `last_conversations.json` (Capability
  `ConversationTranscript`; `driver.AgyTranscriptPath`) and archives the
  file beside the pass log like a claude `--session-id` transcript.
- **Conversation resume is native**: `--conversation <id>` (also `-c` /
  `--continue` for most-recent). The id of the conversation a `-p` run just
  used is recoverable headless from
  `~/.gemini/antigravity-cli/cache/last_conversations.json` — a
  `{"<abs workdir>": "<uuid>"}` map agy rewrites WHOLE on every run. That
  whole-file rewrite is why art passes hold the engine's exclusive agy lock:
  any concurrently running agy instance races the record the art-gen-trans
  flow depends on. (`HAL_AGY_STATE_DIR` overrides the state dir root.)
- Binary discovery: `HAL_AGY_BIN` → PATH → known install dirs
  (`%LOCALAPPDATA%\agy\bin\agy.exe`, `~/.local/bin/agy`, ...) — installers
  update the registry PATH, which already-open shells don't see.

## fake (tests and smoke runs)

`HAL_FAKE_BIN` points at any binary that re-enters
`internal/fakeagent.Main()` (the hal binary itself and the engine test
binaries both do, via the hidden `_fake-agent` argv). Scenario-driven via
`HAL_FAKE_SCENARIO`: happy / blocked / reverted / silent / crash / auth
/ sleep / resolve / **interactive**. `HAL_FAKE_BLIND=1` simulates an
agy-shaped blind driver. This is how every engine path is tested without
real agents.

The **interactive** behavior scripts workflow turns: the scenario's `turns`
array is consumed one entry per process run (a counter file in
`HAL_FAKE_TURNS_DIR`, falling back to the scenario's directory; past
the end the last entry repeats). Each turn emits real stream-json NDJSON
(init, text deltas, the settled assistant message, a `result`), then writes
the artifact (`HAL_WORKFLOW_ARTIFACT`) and — last, per the contract —
the status file (`HAL_WORKFLOW_STATUS_FILE`), then sleeps `sleepMs`
(interject bait) and exits 0. The app-level seam `HAL_WORKFLOW_AGENT=
fake` reroutes the workflow Manager's driver from claude to the fake — how
the e2e suite drives full workflows through the real binary.

## Hard rules the engine enforces (don't relax them)

- Every JSON state file the agent touches is **BOM-free UTF-8**; readers
  strip a BOM defensively; writes are atomic (temp+rename).
- The agent never runs `git commit` — the ENGINE commits: stage approvals
  commit the workflow's artifact folder (subject `ws(flow <short-id>)
  <stage> approved: <title>`, `Hal-Workflow:` / `Hal-Stage:`
  trailers), and implement turns commit the whole tree after each turn so a
  killed turn never strands work.
- Read-only stages are enforced twice: a per-turn tool allow/disallow list,
  AND the post-turn tree check that reverts any change outside the
  workflow's artifact folder (only paths that were clean before the turn —
  operator edits are sacred). The tree check is the real enforcement (see
  the claude print-mode Write note above).
- The agent's status file (`HAL_WORKFLOW_STATUS_FILE`) is removed
  before every turn — a stale `ready` must never settle a later turn; a
  `ready` claim without the artifact on disk gets exactly one corrective
  nudge turn.
- Agent processes are spawned in their own process group and killed as a
  **tree** — on Windows via a kill-on-close Job Object (reaches grandchildren
  whose intermediate parent exited, and dies with the Hal on a crash;
  `taskkill /T /F` is the fallback), on Unix via negative-pgid SIGKILL. A
  wedged or interjected turn must not leak builds or test servers.
- Model/effort resolution per turn: per-workflow dashboard ⚙ override >
  `[workflow.stages.<stage>]` > defaults. Claude is the default interactive
  driver; when the installed Codex CLI passes the JSONL/resume probe, the
  dashboard can route a stage to Sol, Terra, or Luna. `HAL_WORKFLOW_AGENT`
  remains a whole-engine test-only seam.
