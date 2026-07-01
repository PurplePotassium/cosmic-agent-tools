package agent

import "testing"

func TestClassify(t *testing.T) {
	cases := []struct {
		name      string
		title     string
		detail    string
		wantAgent string
		wantModel string
	}{
		{"light art", "Add particle juice to explosions", "screenshake + palette polish", "agy", "gemini-3.5-flash"},
		{"light audio", "Wire up SFX", "hook sound effects", "agy", "gemini-3.5-flash"},
		{"heavy refactor", "Refactor the save system", "restructure serialization", "claude", "claude-opus-4-8"},
		{"heavy architecture", "Rework pathfinding architecture", "state machine overhaul", "claude", "claude-opus-4-8"},
		{"default systems", "Add a new enemy type", "spawns from the left", "claude", "claude-sonnet-4-6"},
		{"empty -> default", "", "", "claude", "claude-sonnet-4-6"},
		// heavy takes precedence over light when both present (heavy checked first)
		{"heavy beats light", "Refactor the art pipeline", "migrate sprite loading", "claude", "claude-opus-4-8"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Classify(c.title, c.detail)
			if got.Agent != c.wantAgent || got.Model != c.wantModel {
				t.Fatalf("Classify(%q,%q) = %s/%s, want %s/%s",
					c.title, c.detail, got.Agent, got.Model, c.wantAgent, c.wantModel)
			}
		})
	}
}

func TestResolveDriver(t *testing.T) {
	// claude: stdin mode, capturable, has -p + --model + skip flag
	d := Resolve("claude", "claude-opus-4-8", true, nil)
	if d.Mode != ModeStdin || !d.Capturable {
		t.Fatalf("claude should be stdin+capturable, got mode=%s capturable=%v", d.Mode, d.Capturable)
	}
	if !containsSeq(d.Args, "-p") || !containsSeq(d.Args, "--model") || !containsSeq(d.Args, "--dangerously-skip-permissions") {
		t.Fatalf("claude args missing expected flags: %v", d.Args)
	}

	// unknown agent falls back to a gated claude (never an empty exe)
	fb := Resolve("auto", "", true, nil)
	if fb.Agent != "claude" || fb.Mode != ModeStdin {
		t.Fatalf("unknown agent should fall back to claude/stdin, got %s/%s", fb.Agent, fb.Mode)
	}

	// agy: arg mode, NOT capturable, has the print-timeout guard, no -p yet (engine appends it)
	a := Resolve("agy", "gemini-3.5-flash", true, nil)
	if a.Mode != ModeArg || a.Capturable {
		t.Fatalf("agy should be arg+non-capturable, got mode=%s capturable=%v", a.Mode, a.Capturable)
	}
	if !containsSeq(a.Args, "--print-timeout") {
		t.Fatalf("agy args missing --print-timeout guard: %v", a.Args)
	}
	if containsSeq(a.Args, "-p") {
		t.Fatalf("agy Args must NOT include -p (engine appends it with the prompt): %v", a.Args)
	}
}

func containsSeq(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}
