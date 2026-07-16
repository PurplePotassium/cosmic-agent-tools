package export

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Two agy-shaped steps: the prompt, then a response with thinking.
const agyTranscript = `{"step_index":0,"source":"USER_EXPLICIT","type":"USER_INPUT","status":"DONE","created_at":"2026-07-16T08:16:32Z","content":"<USER_REQUEST>draw a hero</USER_REQUEST>"}
{"step_index":3,"source":"MODEL","type":"PLANNER_RESPONSE","status":"DONE","created_at":"2026-07-16T08:16:35Z","content":"Saved the asset.","thinking":"**Planning**\n\nOne image, PNG."}
`

// Claude Code session lines: user prompt, assistant thinking+text+tool call,
// tool result, and ancillary lines the renderer tallies instead of rendering.
const claudeTranscript = `{"type":"summary","summary":"Fix the loader crash"}
{"type":"user","message":{"role":"user","content":"fix the crash in the loader"},"timestamp":"2026-07-16T09:00:00Z"}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"thinking","thinking":"The crash is a nil map write."},{"type":"text","text":"Fixing the loader."},{"type":"tool_use","id":"tu1","name":"Bash","input":{"command":"go test ./..."}}]},"timestamp":"2026-07-16T09:00:05Z"}
{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tu1","content":[{"type":"text","text":"ok  \tloader\t0.1s"}]}]}}
{"type":"file-history-snapshot","messageId":"x"}
{"type":"file-history-snapshot","messageId":"y"}
not json at all
`

func TestPassCopiesEvidenceAndRenders(t *testing.T) {
	logDir := t.TempDir()
	exportDir := filepath.Join(t.TempDir(), "audit", "main")
	write(t, logDir, "iter-000042.log", "=== ws(main) iter 42 ===\n")
	write(t, logDir, "iter-000042.log.op.log", "operational\n")
	write(t, logDir, "iter-000042.transcript.jsonl", agyTranscript)
	write(t, logDir, "iter-000042.screen.png", "png-bytes")
	write(t, logDir, "iter-000041.log", "previous pass — must not be exported\n")

	if err := Pass(exportDir, true, filepath.Join(logDir, "iter-000042.log")); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{
		"iter-000042.log", "iter-000042.log.op.log",
		"iter-000042.transcript.jsonl", "iter-000042.screen.png",
		"iter-000042.transcript.md",
	} {
		if _, err := os.Stat(filepath.Join(exportDir, name)); err != nil {
			t.Errorf("%s not exported: %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(exportDir, "iter-000041.log")); !os.IsNotExist(err) {
		t.Errorf("neighbor pass leaked into the export (err=%v)", err)
	}

	md, err := os.ReadFile(filepath.Join(exportDir, "iter-000042.transcript.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"draw a hero", "Saved the asset.", "> **Planning**"} {
		if !strings.Contains(string(md), want) {
			t.Errorf("rendered transcript missing %q:\n%s", want, md)
		}
	}
}

func TestPassWithoutHumanReadableSkipsMarkdown(t *testing.T) {
	logDir := t.TempDir()
	exportDir := t.TempDir()
	write(t, logDir, "iter-000001.log", "log\n")
	write(t, logDir, "iter-000001.transcript.jsonl", agyTranscript)

	if err := Pass(exportDir, false, filepath.Join(logDir, "iter-000001.log")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(exportDir, "iter-000001.transcript.jsonl")); err != nil {
		t.Fatalf("transcript copy missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(exportDir, "iter-000001.transcript.md")); !os.IsNotExist(err) {
		t.Fatalf("markdown rendered with human_readable off (err=%v)", err)
	}
}

func TestRenderTranscriptClaude(t *testing.T) {
	got := RenderTranscript("iter-000007", []byte(claudeTranscript))
	for _, want := range []string{
		"# Transcript — iter-000007",
		"> Session summary: Fix the loader crash",
		"fix the crash in the loader",
		"> The crash is a nil map write.",
		"Fixing the loader.",
		"**Tool call: Bash**",
		`"command": "go test ./..."`,
		"**Tool result:**",
		"ok  \tloader\t0.1s",
		"2 ancillary line(s) not rendered (file-history-snapshot×2)",
		"not json at all", // unparseable lines are kept raw, never dropped
	} {
		if !strings.Contains(got, want) {
			t.Errorf("rendered output missing %q:\n%s", want, got)
		}
	}
}

func TestRenderCropsToolPayloadsOnly(t *testing.T) {
	long := strings.Repeat("x", maxToolChars+500)
	line := `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t","content":"` + long + `"}]}}` + "\n" +
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"thinking","thinking":"` + long + `"}]}}`
	got := RenderTranscript("t", []byte(line))
	if !strings.Contains(got, "… (truncated") {
		t.Fatal("oversized tool result not truncated")
	}
	if strings.Count(got, "… (truncated") != 1 {
		t.Fatal("thinking must never be truncated — it is the audit target")
	}
}

// A transcript whose content embeds a triple-backtick fence must not break
// the surrounding markdown fence.
func TestRenderFenceEscapesBackticks(t *testing.T) {
	line := `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t","content":"a\n` + "```" + `\nb"}]}}`
	got := RenderTranscript("t", []byte(line))
	if !strings.Contains(got, "````") {
		t.Fatalf("fence not widened past embedded backticks:\n%s", got)
	}
}
