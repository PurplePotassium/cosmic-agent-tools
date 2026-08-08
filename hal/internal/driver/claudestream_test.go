package driver

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// parseGolden feeds every line of a testdata capture through ParseStreamLine
// and returns the flattened events. It must never panic — that IS the test
// for the hostile files.
func parseGolden(t *testing.T, name string) []StreamEvent {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", "stream", name))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var events []StreamEvent
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		events = append(events, ParseStreamLine(sc.Bytes())...)
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return events
}

func kinds(events []StreamEvent, kind StreamEventKind) []StreamEvent {
	var out []StreamEvent
	for _, e := range events {
		if e.Kind == kind {
			out = append(out, e)
		}
	}
	return out
}

func TestParseStreamSuccessTurn(t *testing.T) {
	events := parseGolden(t, "success.ndjson")

	inits := kinds(events, StreamInit)
	if len(inits) != 1 || inits[0].SessionID != "11111111-2222-3333-4444-555555555555" {
		t.Fatalf("init: %+v", inits)
	}

	deltas := kinds(events, StreamTextDelta)
	if len(deltas) != 2 {
		t.Fatalf("text deltas: %+v", deltas)
	}
	if joined := deltas[0].Text + deltas[1].Text; joined != "OK, writing the file now." {
		t.Fatalf("delta text: %q", joined)
	}
	if len(kinds(events, StreamThinkingDelta)) != 1 {
		t.Fatal("thinking delta missing")
	}

	asst := kinds(events, StreamAssistant)
	if len(asst) != 1 || asst[0].Text != "OK, writing the file now." {
		t.Fatalf("assistant: %+v", asst)
	}

	tools := kinds(events, StreamToolUse)
	if len(tools) != 1 || tools[0].ToolName != "Write" || !strings.Contains(tools[0].Text, "01-question.md") {
		t.Fatalf("tool use: %+v", tools)
	}
	if results := kinds(events, StreamToolResult); len(results) != 1 || !strings.Contains(results[0].Text, "File created") {
		t.Fatalf("tool result: %+v", results)
	}

	finals := kinds(events, StreamResult)
	if len(finals) != 1 {
		t.Fatalf("result: %+v", finals)
	}
	u := finals[0].Result
	if u.IsError || u.Subtype != "success" || u.NumTurns != 4 || u.TotalCostUSD == 0 {
		t.Fatalf("usage: %+v", u)
	}
	if !strings.Contains(u.ResultText, "question file is written") {
		t.Fatalf("result text: %q", u.ResultText)
	}
}

// A KillTree'd turn ends mid-stream: deltas but NO result event, and a
// truncated final JSON line that must parse to nothing.
func TestParseStreamKilledTurn(t *testing.T) {
	events := parseGolden(t, "killed.ndjson")
	if len(kinds(events, StreamResult)) != 0 {
		t.Fatal("killed capture must have no result event")
	}
	if len(kinds(events, StreamInit)) != 1 {
		t.Fatal("init missing")
	}
	if len(kinds(events, StreamTextDelta)) == 0 {
		t.Fatal("deltas missing")
	}
}

// Shim noise, unknown event types, malformed JSON, blank lines: all noise.
// The only extractable event in the garbage capture is the error result.
func TestParseStreamGarbage(t *testing.T) {
	events := parseGolden(t, "garbage.ndjson")
	if len(events) != 1 {
		t.Fatalf("garbage must yield exactly the result event, got %+v", events)
	}
	u := events[0].Result
	if events[0].Kind != StreamResult || u == nil || !u.IsError || u.Subtype != "error_during_execution" {
		t.Fatalf("error result: %+v", events[0])
	}
}

func TestParseCodexJSONLTurn(t *testing.T) {
	lines := []string{
		`{"type":"thread.started","thread_id":"aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"}`,
		`{"type":"item.started","item":{"id":"item_0","type":"command_execution","command":"rg -n TODO"}}`,
		`{"type":"item.completed","item":{"id":"item_0","type":"command_execution","command":"rg -n TODO","aggregated_output":"clean\\n","exit_code":0,"status":"completed"}}`,
		`{"type":"item.completed","item":{"id":"item_1","type":"agent_message","text":"Research saved."}}`,
		`{"type":"turn.completed","usage":{"input_tokens":10,"output_tokens":4}}`,
	}
	var events []StreamEvent
	for _, line := range lines {
		events = append(events, ParseStreamLine([]byte(line))...)
	}
	if got := kinds(events, StreamInit); len(got) != 1 || got[0].SessionID != "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee" {
		t.Fatalf("init: %+v", got)
	}
	if got := kinds(events, StreamToolUse); len(got) != 1 || !strings.Contains(got[0].Text, "rg -n TODO") {
		t.Fatalf("tool: %+v", got)
	}
	if got := kinds(events, StreamAssistant); len(got) != 1 || got[0].Text != "Research saved." {
		t.Fatalf("assistant: %+v", got)
	}
	if got := kinds(events, StreamResult); len(got) != 1 || got[0].Result == nil || got[0].Result.IsError {
		t.Fatalf("result: %+v", got)
	}
}
