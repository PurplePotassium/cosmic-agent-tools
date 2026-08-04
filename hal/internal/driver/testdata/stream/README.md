# stream-json golden files

Sanitized captures from real `claude -p --output-format stream-json
--include-partial-messages --verbose` runs against Claude Code v2.1.220
(2026-08-03 spike; see `docs/interactive-driver.md`). Session ids and paths
are anonymized; event shapes are verbatim.

- `success.ndjson` — a full turn: status/init, thinking + text deltas,
  assistant message with a tool_use, tool_result carrier, undocumented
  chatter (`thinking_tokens`, `rate_limit_event`), terminal `result`.
- `killed.ndjson` — a KillTree'd turn: stream ends mid-delta with a
  truncated final line and **no `result` event**.
- `garbage.ndjson` — resilience: npm shim noise, unknown event types and
  subtypes, malformed JSON, blank line, then a **synthesized** `is_error`
  result (`error_during_execution` — shape per docs, not captured live).

The parser must consume all three without ever returning a fatal error:
unknown/malformed lines are `StreamOther`.
