package driver

import (
	"encoding/json"
	"fmt"
	"strings"
)

// This file parses the interactive drivers' JSONL output (one event per line)
// into the small typed vocabulary the turn runner and dashboard need.
// The contract (probed v2.1.220, see docs/interactive-driver.md and
// testdata/stream/): the CLI emits documented types (system/init,
// stream_event, assistant, user, result) interleaved with undocumented
// chatter (system/status, system/thinking_tokens, rate_limit_event, ...) and
// — because the engine merges stderr into stdout — occasional non-JSON shim
// noise. Parsing is therefore FORGIVING BY CONSTRUCTION: anything
// unrecognized yields no events and must never fail a turn. Raw lines are
// preserved verbatim in the turn log regardless.

// StreamEventKind classifies one parsed stream event.
type StreamEventKind int

const (
	// StreamInit: session start — carries the authoritative session id.
	StreamInit StreamEventKind = iota
	// StreamTextDelta: a live assistant-text fragment (dashboard streaming).
	StreamTextDelta
	// StreamThinkingDelta: a live reasoning fragment (surfaced, not stored).
	StreamThinkingDelta
	// StreamAssistant: a complete assistant message's text (deltas already
	// streamed it; this is the settled form).
	StreamAssistant
	// StreamToolUse: the assistant invoked a tool — one-line summary.
	StreamToolUse
	// StreamToolResult: a tool finished — truncated result summary.
	StreamToolResult
	// StreamResult: the per-turn terminal event.
	StreamResult
)

// TurnUsage is the per-turn terminal report from the result event.
type TurnUsage struct {
	IsError           bool
	Subtype           string // "success" | "error_max_turns" | "error_during_execution" | ...
	ResultText        string // the assistant's final text
	NumTurns          int
	DurationMS        int64
	TotalCostUSD      float64
	PermissionDenials int
}

// StreamEvent is one parsed event.
type StreamEvent struct {
	Kind      StreamEventKind
	SessionID string     // set when the line carried one (init and result always do)
	Text      string     // delta fragment / assistant text / tool summary
	ToolName  string     // StreamToolUse only
	Result    *TurnUsage // StreamResult only
}

// ParseStreamLine parses one raw output line into zero or more events.
// Unknown types/subtypes, malformed JSON, and non-JSON noise all return nil —
// never an error. A single assistant line can yield several events (its text
// plus one per tool_use block).
func ParseStreamLine(line []byte) []StreamEvent {
	trimmed := strings.TrimSpace(string(line))
	if trimmed == "" || trimmed[0] != '{' {
		return nil
	}
	var raw struct {
		Type      string          `json:"type"`
		Subtype   string          `json:"subtype"`
		SessionID string          `json:"session_id"`
		ThreadID  string          `json:"thread_id"`
		Event     json.RawMessage `json:"event"`
		Message   json.RawMessage `json:"message"`
		Item      json.RawMessage `json:"item"`
		Usage     json.RawMessage `json:"usage"`
		Error     json.RawMessage `json:"error"`

		// result-event fields
		IsError           bool              `json:"is_error"`
		Result            string            `json:"result"`
		NumTurns          int               `json:"num_turns"`
		DurationMS        int64             `json:"duration_ms"`
		TotalCostUSD      float64           `json:"total_cost_usd"`
		PermissionDenials []json.RawMessage `json:"permission_denials"`
	}
	if err := json.Unmarshal([]byte(trimmed), &raw); err != nil {
		return nil
	}
	switch raw.Type {
	case "system":
		if raw.Subtype == "init" {
			return []StreamEvent{{Kind: StreamInit, SessionID: raw.SessionID}}
		}
		return nil
	case "stream_event":
		return parseAPIEvent(raw.Event, raw.SessionID)
	case "assistant":
		return parseAssistant(raw.Message, raw.SessionID)
	case "user":
		return parseToolResults(raw.Message, raw.SessionID)
	case "result":
		return []StreamEvent{{
			Kind:      StreamResult,
			SessionID: raw.SessionID,
			Text:      raw.Result,
			Result: &TurnUsage{
				IsError:           raw.IsError,
				Subtype:           raw.Subtype,
				ResultText:        raw.Result,
				NumTurns:          raw.NumTurns,
				DurationMS:        raw.DurationMS,
				TotalCostUSD:      raw.TotalCostUSD,
				PermissionDenials: len(raw.PermissionDenials),
			},
		}}
	case "thread.started":
		return []StreamEvent{{Kind: StreamInit, SessionID: raw.ThreadID}}
	case "item.started", "item.updated", "item.completed":
		return parseCodexItem(raw.Type, raw.Item)
	case "turn.completed":
		return []StreamEvent{{
			Kind:   StreamResult,
			Result: &TurnUsage{Subtype: "success", NumTurns: 1},
		}}
	case "turn.failed", "error":
		return []StreamEvent{{
			Kind:   StreamResult,
			Result: &TurnUsage{IsError: true, Subtype: raw.Type, ResultText: codexErrorText(raw.Error)},
		}}
	default:
		return nil
	}
}

// parseCodexItem maps Codex CLI `exec --json` item events. Agent messages are
// settled (Codex does not currently emit token deltas here); command and MCP
// items become the same compact tool feed used for Claude turns.
func parseCodexItem(eventType string, item json.RawMessage) []StreamEvent {
	if len(item) == 0 {
		return nil
	}
	var it struct {
		Type             string `json:"type"`
		Text             string `json:"text"`
		Command          string `json:"command"`
		AggregatedOutput string `json:"aggregated_output"`
		ExitCode         *int   `json:"exit_code"`
		Server           string `json:"server"`
		Tool             string `json:"tool"`
		Status           string `json:"status"`
	}
	if json.Unmarshal(item, &it) != nil {
		return nil
	}
	switch it.Type {
	case "agent_message":
		if eventType == "item.completed" && strings.TrimSpace(it.Text) != "" {
			return []StreamEvent{{Kind: StreamAssistant, Text: it.Text}}
		}
	case "reasoning":
		if (eventType == "item.updated" || eventType == "item.completed") && it.Text != "" {
			return []StreamEvent{{Kind: StreamThinkingDelta, Text: it.Text}}
		}
	case "command_execution":
		if eventType == "item.started" {
			return []StreamEvent{{Kind: StreamToolUse, ToolName: "command", Text: "command " + clip(strings.ReplaceAll(it.Command, "\n", " "), 120)}}
		}
		if eventType == "item.completed" {
			return []StreamEvent{{Kind: StreamToolResult, Text: clip(it.AggregatedOutput, 200)}}
		}
	case "mcp_tool_call":
		name := strings.Trim(strings.Join([]string{it.Server, it.Tool}, "."), ".")
		if eventType == "item.started" {
			return []StreamEvent{{Kind: StreamToolUse, ToolName: name, Text: name}}
		}
		if eventType == "item.completed" {
			return []StreamEvent{{Kind: StreamToolResult, Text: clip(it.Status, 200)}}
		}
	}
	return nil
}

func codexErrorText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var obj struct {
		Message string `json:"message"`
	}
	_ = json.Unmarshal(raw, &obj)
	return obj.Message
}

// parseAPIEvent handles the stream_event wrapper around raw API events; only
// content_block_delta text/thinking fragments matter for live streaming.
func parseAPIEvent(event json.RawMessage, sessionID string) []StreamEvent {
	if len(event) == 0 {
		return nil
	}
	var ev struct {
		Type  string `json:"type"`
		Delta struct {
			Type     string `json:"type"`
			Text     string `json:"text"`
			Thinking string `json:"thinking"`
		} `json:"delta"`
	}
	if err := json.Unmarshal(event, &ev); err != nil || ev.Type != "content_block_delta" {
		return nil
	}
	switch ev.Delta.Type {
	case "text_delta":
		return []StreamEvent{{Kind: StreamTextDelta, SessionID: sessionID, Text: ev.Delta.Text}}
	case "thinking_delta":
		return []StreamEvent{{Kind: StreamThinkingDelta, SessionID: sessionID, Text: ev.Delta.Thinking}}
	default:
		return nil
	}
}

// parseAssistant flattens a complete assistant message into its settled text
// plus one StreamToolUse per tool_use block.
func parseAssistant(message json.RawMessage, sessionID string) []StreamEvent {
	if len(message) == 0 {
		return nil
	}
	var msg struct {
		Content []struct {
			Type  string         `json:"type"`
			Text  string         `json:"text"`
			Name  string         `json:"name"`
			Input map[string]any `json:"input"`
		} `json:"content"`
	}
	if err := json.Unmarshal(message, &msg); err != nil {
		return nil
	}
	var events []StreamEvent
	var text strings.Builder
	for _, block := range msg.Content {
		switch block.Type {
		case "text":
			if text.Len() > 0 {
				text.WriteString("\n")
			}
			text.WriteString(block.Text)
		case "tool_use":
			events = append(events, StreamEvent{
				Kind:      StreamToolUse,
				SessionID: sessionID,
				ToolName:  block.Name,
				Text:      summarizeToolInput(block.Name, block.Input),
			})
		}
	}
	if text.Len() > 0 {
		// Text first: it was streamed before the tool calls fired.
		events = append([]StreamEvent{{Kind: StreamAssistant, SessionID: sessionID, Text: text.String()}}, events...)
	}
	return events
}

// parseToolResults surfaces tool_result carriers ("user" events) as truncated
// one-liners for the dashboard's collapsed tool feed.
func parseToolResults(message json.RawMessage, sessionID string) []StreamEvent {
	if len(message) == 0 {
		return nil
	}
	var msg struct {
		Content []struct {
			Type    string          `json:"type"`
			Content json.RawMessage `json:"content"` // string or block list
		} `json:"content"`
	}
	if err := json.Unmarshal(message, &msg); err != nil {
		return nil
	}
	var events []StreamEvent
	for _, block := range msg.Content {
		if block.Type != "tool_result" {
			continue
		}
		var s string
		_ = json.Unmarshal(block.Content, &s) // non-string content stays ""
		events = append(events, StreamEvent{
			Kind: StreamToolResult, SessionID: sessionID, Text: clip(s, 200),
		})
	}
	return events
}

// summarizeToolInput renders a tool call as one short human line: the tool's
// most identifying argument, clipped.
func summarizeToolInput(name string, input map[string]any) string {
	for _, key := range []string{"file_path", "command", "pattern", "path", "url", "description", "prompt"} {
		if v, ok := input[key].(string); ok && v != "" {
			return fmt.Sprintf("%s %s", name, clip(strings.ReplaceAll(v, "\n", " "), 120))
		}
	}
	return name
}

func clip(s string, max int) string {
	if len(s) <= max {
		return s
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}
