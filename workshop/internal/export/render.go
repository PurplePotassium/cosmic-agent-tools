package export

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// maxToolChars caps tool inputs/results in the rendered markdown. Prompts,
// thinking, and response text are never cropped — they are what the operator
// audits; tool payloads are secondary and live in full in the .jsonl.
const maxToolChars = 2000

// RenderTranscript renders an archived agent-runtime transcript (JSONL) as
// markdown for human auditing. Two line shapes are sniffed per line: Claude
// Code session lines ({"type","message",...}) and agy brain-transcript steps
// ({"step_index","source",...}). Unrecognized ancillary lines are counted in
// a footer, and unparseable lines are included raw — the renderer never
// fails, it degrades.
func RenderTranscript(title string, jsonl []byte) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Transcript — %s\n", title)
	skipped := map[string]int{}
	n := 0
	for _, line := range bytes.Split(jsonl, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		n++
		var m map[string]any
		if err := json.Unmarshal(line, &m); err != nil {
			fmt.Fprintf(&b, "\n## [%d] unparsed line\n\n%s\n", n, fence(crop(string(line))))
			continue
		}
		if _, agy := m["step_index"]; agy {
			renderAgyStep(&b, m)
		} else {
			renderClaudeLine(&b, n, m, skipped)
		}
	}
	if len(skipped) > 0 {
		kinds := make([]string, 0, len(skipped))
		total := 0
		for k, c := range skipped {
			kinds = append(kinds, fmt.Sprintf("%s×%d", k, c))
			total += c
		}
		sort.Strings(kinds)
		fmt.Fprintf(&b, "\n---\n\n_%d ancillary line(s) not rendered (%s) — full fidelity in the .transcript.jsonl._\n",
			total, strings.Join(kinds, ", "))
	}
	return b.String()
}

// renderAgyStep renders one agy brain-transcript step: USER_INPUT carries the
// prompt exactly as the model saw it, PLANNER_RESPONSE the response `content`
// plus the `thinking` reasoning summary, SYSTEM steps checkpoints/reminders.
func renderAgyStep(b *strings.Builder, m map[string]any) {
	ts := ""
	if v := str(m["created_at"]); v != "" {
		ts = " (" + v + ")"
	}
	fmt.Fprintf(b, "\n## Step %v — %s %s%s\n", m["step_index"], str(m["source"]), str(m["type"]), ts)
	if th := str(m["thinking"]); th != "" {
		b.WriteString("\n**Thinking:**\n\n" + quote(th) + "\n")
	}
	if c := str(m["content"]); c != "" {
		b.WriteString("\n" + c + "\n")
	}
}

// renderClaudeLine renders one Claude Code session line. Ancillary line types
// (progress, file-history snapshots, …) are tallied into skipped.
func renderClaudeLine(b *strings.Builder, n int, m map[string]any, skipped map[string]int) {
	kind := str(m["type"])
	switch kind {
	case "summary":
		fmt.Fprintf(b, "\n> Session summary: %s\n", str(m["summary"]))
	case "system":
		if c := str(m["content"]); c != "" {
			fmt.Fprintf(b, "\n> system: %s\n", c)
		} else {
			skipped[kind]++
		}
	case "user", "assistant":
		msg, _ := m["message"].(map[string]any)
		ts := ""
		if v := str(m["timestamp"]); v != "" {
			ts = " (" + v + ")"
		}
		fmt.Fprintf(b, "\n## [%d] %s%s\n", n, kind, ts)
		switch content := msg["content"].(type) {
		case string:
			b.WriteString("\n" + content + "\n")
		case []any:
			for _, raw := range content {
				block, _ := raw.(map[string]any)
				renderClaudeBlock(b, block)
			}
		}
	default:
		if kind == "" {
			kind = "(untyped)"
		}
		skipped[kind]++
	}
}

func renderClaudeBlock(b *strings.Builder, block map[string]any) {
	switch str(block["type"]) {
	case "text":
		b.WriteString("\n" + str(block["text"]) + "\n")
	case "thinking":
		b.WriteString("\n**Thinking:**\n\n" + quote(str(block["thinking"])) + "\n")
	case "tool_use":
		input := ""
		if raw, err := json.MarshalIndent(block["input"], "", "  "); err == nil {
			input = string(raw)
		}
		fmt.Fprintf(b, "\n**Tool call: %s**\n\n%s\n", str(block["name"]), fence(crop(input)))
	case "tool_result":
		label := "**Tool result:**"
		if isErr, _ := block["is_error"].(bool); isErr {
			label = "**Tool result (error):**"
		}
		fmt.Fprintf(b, "\n%s\n\n%s\n", label, fence(crop(toolResultText(block["content"]))))
	case "image":
		b.WriteString("\n_(image attachment)_\n")
	default:
		fmt.Fprintf(b, "\n_(%s block)_\n", str(block["type"]))
	}
}

// toolResultText flattens a tool_result content payload (a plain string, or a
// list of text blocks) into one string.
func toolResultText(v any) string {
	switch content := v.(type) {
	case string:
		return content
	case []any:
		var parts []string
		for _, raw := range content {
			block, _ := raw.(map[string]any)
			switch str(block["type"]) {
			case "text":
				parts = append(parts, str(block["text"]))
			case "image":
				parts = append(parts, "(image)")
			}
		}
		return strings.Join(parts, "\n")
	}
	return ""
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

// quote renders text as a markdown blockquote.
func quote(text string) string {
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	for i, l := range lines {
		lines[i] = "> " + l
	}
	return strings.Join(lines, "\n")
}

// fence wraps text in a code fence long enough that embedded backtick runs
// cannot terminate it early.
func fence(text string) string {
	run, longest := 0, 0
	for _, r := range text {
		if r == '`' {
			run++
		} else {
			run = 0
		}
		if run > longest {
			longest = run
		}
	}
	d := strings.Repeat("`", max(3, longest+1))
	return d + "\n" + strings.TrimRight(text, "\n") + "\n" + d
}

// crop bounds tool payloads at maxToolChars, pointing at the raw JSONL for
// the remainder.
func crop(s string) string {
	if len(s) <= maxToolChars {
		return s
	}
	// Back off to a rune boundary so the cut never splits a UTF-8 sequence.
	cut := maxToolChars
	for cut > 0 && s[cut]&0xC0 == 0x80 {
		cut--
	}
	return s[:cut] + "\n… (truncated — full content in the .transcript.jsonl)"
}
