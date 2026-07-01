# Workshop — how the agent drivers actually work (READ before touching agent/model wiring)

Scope: the **Workshop** (single-agent loop) only. The engine is Go now: `internal/engine`
runs the loop, `internal/agent` resolves each driver, `internal/server` exposes the API/UI.
Workshop-only behavior (the live agent/model switch re-read each pass + per-pass
`agent.Classify` auto-routing) lives in the engine. This is NOT the fleet (`../ralph`).

This file exists because "the agents behave weirdly headless" keeps recurring. Most of it is
NOT a bug — it's how the two backends behave when there's no terminal. Know this before you
"fix" anything.

## The two drivers

| | **claude** (Claude Code) | **agy** (Antigravity CLI / Gemini) |
|---|---|---|
| invoke | prompt over **stdin**, `claude -p --model <id> --dangerously-skip-permissions` | prompt as **arg**, `agy -p <prompt> --dangerously-skip-permissions --print-timeout 30m [--model <id>]` |
| output capture | **streamed live + captured** (merged stdout+stderr) to the iter log + SSE | **uncapturable headless — see below** |
| commit | the loop commits each pass itself (`ralph iter N [agent]`) | same — the loop commits it |
| reliability headless | **solid** — the dependable choice | **works but BLIND** (no captured output) |

In `internal/agent`, claude resolves to `Mode=stdin, Capturable=true`; agy to
`Mode=arg, Capturable=false`. The engine (`internal/engine/engine.go`) branches on this:
`spawnClaude` pipes + captures line-by-line; `spawnAgy` does NOT pipe.

## ⚠️ agy's killer gotcha: print output is uncapturable under non-TTY (upstream, unfixed)

`agy -p` / `agy models` / any agy print silently **drops stdout when stdout is a pipe,
redirect, or subprocess** (non-TTY), and **hangs if you redirect its streams**. Confirmed:
`agy models` returns EMPTY when captured by a script. Upstream bug, open, no fix. ConPTY/winpty
don't help.

Consequences for the Workshop:
- `spawnAgy` deliberately does **not** pipe agy — it lets agy inherit the server's console
  (`os.Stdin/Stdout/Stderr`) and points `--log-file` at the project's `logs/` dir. That
  `--log-file` is agy's **operational** log, **not** the model response text. The iter log for
  an agy pass is empty except the header + a tail of that operational log. **Expected, not a
  crash.** Because agy needs a real console, launch `workshop` from a terminal when driving agy.
- **Auth-failure detection is blind for agy.** The auth-keyword scan (`authRe` in the engine)
  reads captured output, which is empty for agy. An agy auth failure shows up only as a
  non-zero exit → counted as a generic transient fail (5-in-a-row trips the circuit-breaker).
  If agy "does nothing" for several passes, **suspect auth** and run `agy` interactively once.
- Liveness comes from the **process/DB state + git dirty tree**, never the log.
- **The agent self-report is the real window into an agy pass.** `PROMPT.md` requires every
  pass to overwrite `progress.json` at pass START (`phase=working` + plan) and END. File writes
  work even when agy stdout doesn't. The engine reconciles `progress.json` into the DB and
  pushes it over SSE (`progress` event); the UI flags a STALE report from a previous pass. If
  you drive agy, this is how the operator sees it; if you ARE a pass, never skip the start write.

To SEE agy output for debugging, run agy **in a real interactive terminal**, or read its
conversation DB (`<user>\.gemini\antigravity-cli\conversations\<id>.db`, sqlite).

## agy model id — what string to pass

`agy --model` accepts **`gemini-3.5-flash`** and resolves/serves it as canonical
**`gemini-3-flash`** (Gemini 3 Flash — there is no separate served "3.5"). So the configured id
**already works**; it is not the bug. To use a **different** agy model, get the exact id from
`agy models` **in a real interactive terminal** (empty when captured — same non-TTY bug). Do NOT
guess an id: a bad `--model` fails **silently and blind** headless. The id lives in
`AgentOptions` (`internal/server/options.go`) and in each project's `agent`/`model` columns.

## Where agent/model selection lives

- Each **project row** carries `agent` (`claude|agy|auto`) + `model` (`<id>|auto`). The UI edits
  it via `POST /api/projects/{id}/agent` (validated against `AgentOptions`). The engine re-reads
  the project at the **top of each pass**, so a switch lands on the **NEXT** pass, never the
  in-flight one. No control file, no BOM concerns — it's a DB column now.
- `auto` → `agent.Classify(topBacklogTitle, detail)`: light/presentation → agy;
  heavy/structural → claude/opus; else → claude/sonnet. Run every pass on the top item.

## No restart needed for an agent/model switch

Unlike the old PowerShell loop, the engine and server share one process + one DB. Changing the
selection (or the goal/prompt/backlog) takes effect on the **next pass** with no restart. Only a
change to the **Go code** needs a rebuild + relaunch.

## Hard rules

- The engine materializes `backlog.json` / `completions.json` from the DB before each pass and
  reconciles them back (diff-based) after — keep those files valid JSON, no BOM (Go writes none).
- The loop commits each pass as `ralph iter N [agent]` — keep that subject prefix; the commit
  feed + history highlight it.
- Everything Workshop writes stays in the **per-project state dir OUTSIDE the repo tree**. Never
  write Workshop state into the target repo — the per-pass `git add -A` must only ever commit the
  agent's real code changes.
