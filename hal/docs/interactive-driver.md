# Interactive claude driver — spike findings

Probed 2026-08-03 against Claude Code v2.1.220 on Windows 11 (the v2.1.214
stdin fix is in). These findings underpin the turn-per-process interactive
driver (see the workflow rework plan). Re-run the probes after CLI upgrades:
`HAL_PROBE=1 go test ./internal/driver -run Interactive`.

## The turn model

One OS process per conversational turn:

```
claude -p --output-format stream-json --include-partial-messages --verbose
       [--resume <session-id> | --session-id <minted-uuid>]
       --model <m> [--effort <e>] [permission flags]
```

with the user/stage message piped to stdin as plain text. The process exits
after the turn's `result` event. Verified:

1. **Minted session ids are honored** (`--session-id <uuid>`): `init` and
   `result` events echo the minted id; the transcript lands at the
   deterministic `claudeTranscriptPath` location.
2. **`--resume <id>` does NOT rotate the session id** on v2.1.220 — `init`
   and `result` report the *same* id, the same JSONL keeps growing, and full
   context is preserved across processes. The runner still treats the id
   captured from `init`/`result` as authoritative (cheap insurance against a
   future version forking on resume).
3. **Kill-mid-turn → resume works.** A `taskkill /T /F` (KillTree) during
   streaming leaves an intact transcript (the turn's user message is flushed
   at turn start); a follow-up `--resume` has full context including the
   killed turn's prompt. This is the interject mechanism: kill the turn
   process, respawn with `--resume` plus the user's message.

## Stream-json output events (observed)

NDJSON, one event per line. Beyond the documented set there are undocumented
types — **the parser must treat unknown types/subtypes as noise, never as an
error**:

| type | notes |
|---|---|
| `system/status` | permission-mode + lifecycle chatter, several per turn |
| `system/init` | session_id, cwd, model, tools, claude_code_version |
| `system/thinking_tokens` | token-count estimates (undocumented) |
| `rate_limit_event` | quota window info (undocumented) |
| `stream_event` | wraps raw API events: `message_start`, `content_block_start`, `content_block_delta` (`delta.type`: `text_delta` / `thinking_delta`), `content_block_stop`, `message_delta`, `message_stop` |
| `assistant` | complete assistant message; `message.content[]` blocks: `thinking`, `text`, `tool_use` |
| `user` | tool_result carrier (one per tool call), `parent_tool_use_id` for subagents |
| `result` | per-turn terminal: `is_error`, `subtype` (`success`/`error_*`), `result` (final text), `session_id`, `num_turns`, `total_cost_usd`, `usage`, `permission_denials[]` |

A killed turn's capture simply ends mid-stream with **no `result` event** —
that absence (plus context cancellation) is how the runner distinguishes
`interrupted`/`failed` from `done`.

Sanitized real captures live in `internal/driver/testdata/stream/` and are
the parser's golden files.

## Permissions (print mode)

- Unlisted tools are **auto-denied without hanging**; denials are recorded in
  `result.permission_denials[]` and the turn completes normally.
- **Path-scoped `Write(<glob>)` rules do NOT match in `-p` mode** on v2.1.220
  — tested with absolute (`C:/...`) and cwd-relative globs; both denied even
  for in-scope paths. Bare `Write` works.
- Consequence: early workflow stages allow **bare `Write`** and rely on the
  engine's post-turn tree check (revert any change outside the workflow's
  artifact dir) as the actual enforcement. The tree check is mandatory, not
  belt-and-braces.

## Rejected alternative: persistent bidirectional stdin

`--input-format stream-json` (one long-lived process, messages written to
stdin) exists but its control protocol (interrupt, permission responses) is
undocumented and SDK-internal. The per-turn model gets identical context
continuity via `--resume` with none of that risk, and reuses the engine's
battle-tested spawn/KillTree/log plumbing. Revisit only if per-turn process
startup latency ever becomes the bottleneck.
