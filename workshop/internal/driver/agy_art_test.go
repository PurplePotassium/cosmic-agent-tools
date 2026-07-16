package driver

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A trimmed capture of a real agy operational log after an invalid --model
// (2026-07): the "Available models:" dump is the headless source of truth.
const sampleOpLog = `I0716 01:17:16.319701 30960 model_resolver.go:62] Resolving model Gemini Bogus 9000 (Ultra)
W0716 01:17:16.319701 30960 model_config_manager.go:56] Failed to resolve model flag Gemini Bogus 9000 (Ultra): model Gemini Bogus 9000 (Ultra) is not recognized as a known model or custom model in settings
E0716 01:17:16.802651 30960 printmode.go:192] Print mode: invalid --model "Gemini Bogus 9000 (Ultra)": model Gemini Bogus 9000 (Ultra) is not recognized as a known model or custom model in settings
Available models:
  Gemini 3.5 Flash (Medium)
  Gemini 3.5 Flash (High)
  Gemini 3.5 Flash (Low)
  Gemini 3.1 Pro (Low)
  Gemini 3.1 Pro (High)
  Claude Sonnet 4.6 (Thinking)
  Claude Opus 4.6 (Thinking)
  GPT-OSS 120B (Medium)
I0716 01:17:16.802651 30960 input_loop.go:523] Auth done received, triggering experiment refresh
`

func TestParseAgyAvailableModels(t *testing.T) {
	models := ParseAgyAvailableModels(sampleOpLog)
	if len(models) != 8 {
		t.Fatalf("parsed %d models (%v); want 8", len(models), models)
	}
	if models[0] != "Gemini 3.5 Flash (Medium)" || models[7] != "GPT-OSS 120B (Medium)" {
		t.Fatalf("unexpected list boundaries: %v", models)
	}
	if !AgyHasModel(models, "Gemini 3.1 Pro (High)") || !AgyHasModel(models, "gemini 3.5 flash (high)") {
		t.Fatal("expected labels missing (match must be case-insensitive)")
	}
	if AgyHasModel(models, "gemini 3 pro") {
		t.Fatal(`"gemini 3 pro" is NOT a valid label and must not match`)
	}
	if got := ParseAgyAvailableModels("no list in here"); len(got) != 0 {
		t.Fatalf("parsed %v from a log without the dump; want none", got)
	}
}

func TestAgyLastConversation(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("WORKSHOP_AGY_STATE_DIR", stateDir)

	// Missing file: no conversation, no error.
	if id, err := AgyLastConversation(`C:\Some\Dir`); err != nil || id != "" {
		t.Fatalf("missing file: id=%q err=%v; want empty, nil", id, err)
	}

	if err := os.MkdirAll(filepath.Join(stateDir, "cache"), 0o755); err != nil {
		t.Fatal(err)
	}
	workDir := t.TempDir()
	// agy records the cwd string it was spawned with — casing may differ
	// from the caller's, so the lookup must match case-insensitively.
	recorded := strings.ToUpper(workDir)
	raw := `{"` + escapeJSON(recorded) + `": "5ac58547-42c1-4859-a180-8d0283fc14cf"}`
	if err := os.WriteFile(filepath.Join(stateDir, "cache", "last_conversations.json"), []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	id, err := AgyLastConversation(workDir)
	if err != nil || id != "5ac58547-42c1-4859-a180-8d0283fc14cf" {
		t.Fatalf("id=%q err=%v; want the recorded conversation (case-insensitive path match)", id, err)
	}
	if id, _ := AgyLastConversation(filepath.Join(workDir, "elsewhere")); id != "" {
		t.Fatalf("unrelated dir resolved to %q; want empty", id)
	}
}

func escapeJSON(s string) string {
	out := ""
	for _, r := range s {
		if r == '\\' || r == '"' {
			out += "\\"
		}
		out += string(r)
	}
	return out
}

func TestAgyPlanConversationFlag(t *testing.T) {
	a := &Agy{exe: "agy", probed: true, caps: Capabilities{
		PromptVia: PromptArg, Capture: CaptureNone, NeedsConsole: true, ModelSelect: true,
	}}
	plan, err := a.Plan(InvokeSpec{Prompt: "p", Model: "Gemini 3.1 Pro (High)", ConversationID: "abc-123"})
	if err != nil {
		t.Fatal(err)
	}
	joined := ""
	for i, arg := range plan.Args {
		if arg == "--conversation" {
			if i+1 >= len(plan.Args) || plan.Args[i+1] != "abc-123" {
				t.Fatalf("--conversation not followed by the id: %v", plan.Args)
			}
			joined = "found"
		}
	}
	if joined == "" {
		t.Fatalf("--conversation missing from %v", plan.Args)
	}
	plan, err = a.Plan(InvokeSpec{Prompt: "p"})
	if err != nil {
		t.Fatal(err)
	}
	for _, arg := range plan.Args {
		if arg == "--conversation" {
			t.Fatalf("--conversation present without a ConversationID: %v", plan.Args)
		}
	}
}
