package driver

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/gw1108/cosmic-agent-tools/workshop/internal/proc"
)

func fakeClaudeBin(t *testing.T) string {
	t.Helper()
	// Any existing file works: Plan never executes it; Probe's --help exec
	// fails fast and degrades to Effort=false.
	path := filepath.Join(t.TempDir(), "claude-stub")
	if err := os.WriteFile(path, []byte("stub"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestClaudePlanArgs(t *testing.T) {
	t.Setenv("WORKSHOP_CLAUDE_BIN", fakeClaudeBin(t))
	c := NewClaude()
	caps, err := c.Probe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if caps.PromptVia != PromptStdin || caps.Capture != CaptureStreaming || !caps.AuthProbe {
		t.Fatalf("caps: %+v", caps)
	}

	plan, err := c.Plan(InvokeSpec{Model: "claude-opus-4-8", SkipPermissions: true, ExtraArgs: []string{"--verbose"}})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"-p", "--model", "claude-opus-4-8", "--dangerously-skip-permissions", "--verbose"}
	if !slices.Equal(plan.Args, want) {
		t.Fatalf("args: %v, want %v", plan.Args, want)
	}
	if !plan.StdinPrompt || plan.Mode != proc.Piped {
		t.Fatalf("plan: %+v", plan)
	}

	// Default model applies when unset.
	plan, _ = c.Plan(InvokeSpec{})
	if !slices.Contains(plan.Args, DefaultClaudeModel) {
		t.Fatalf("default model missing: %v", plan.Args)
	}

	// Effort not probed as supported -> flag never emitted.
	plan, _ = c.Plan(InvokeSpec{Effort: "high"})
	if slices.Contains(plan.Args, "--effort") {
		t.Fatalf("effort flag emitted without capability: %v", plan.Args)
	}
}

func TestClaudeEffortWhenSupported(t *testing.T) {
	t.Setenv("WORKSHOP_CLAUDE_BIN", fakeClaudeBin(t))
	c := NewClaude()
	if _, err := c.Probe(context.Background()); err != nil {
		t.Fatal(err)
	}
	c.caps.Effort = true // simulate a probe that saw --effort in help

	plan, err := c.Plan(InvokeSpec{Effort: "xhigh"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(plan.Args, "--effort") || !slices.Contains(plan.Args, "xhigh") {
		t.Fatalf("effort flag missing: %v", plan.Args)
	}
}

func TestParseEffortSupport(t *testing.T) {
	if parseEffortSupport("usage: claude [options]\n  --model <id>\n") {
		t.Fatal("false positive")
	}
	if !parseEffortSupport("  --effort <level>   reasoning effort\n") {
		t.Fatal("false negative")
	}
}

func TestFakeDriver(t *testing.T) {
	t.Setenv("WORKSHOP_FAKE_BIN", "")
	f := NewFake()
	if _, err := f.Probe(context.Background()); err == nil {
		t.Fatal("probe should fail without WORKSHOP_FAKE_BIN")
	}
	t.Setenv("WORKSHOP_FAKE_BIN", os.Args[0])
	caps, err := f.Probe(context.Background())
	if err != nil || caps.Capture != CaptureStreaming {
		t.Fatalf("caps: %+v, %v", caps, err)
	}
	plan, err := f.Plan(InvokeSpec{ExtraArgs: []string{"--extra"}})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Exe != os.Args[0] || !slices.Equal(plan.Args, []string{"_fake-agent", "--extra"}) {
		t.Fatalf("plan: %+v", plan)
	}
}

func TestRegistry(t *testing.T) {
	if d, err := New("claude"); err != nil || d.Name() != "claude" {
		t.Fatalf("claude: %v", err)
	}
	if d, err := New("fake"); err != nil || d.Name() != "fake" {
		t.Fatalf("fake: %v", err)
	}
	if _, err := New("nonsense"); err == nil {
		t.Fatal("unknown driver should error")
	}
}
