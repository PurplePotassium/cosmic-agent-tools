package driver

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/proc"
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

func TestClaudeSessionPlan(t *testing.T) {
	t.Setenv("WORKSHOP_CLAUDE_BIN", fakeClaudeBin(t))
	cfgDir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", cfgDir)
	c := NewClaude()
	caps, err := c.Probe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !caps.Sessions {
		t.Fatal("claude must declare the Sessions capability")
	}

	const id = "0eca2f27-e7b7-4a96-89df-4631bee2db0e"
	plan, err := c.Plan(InvokeSpec{SessionID: id, WorkDir: `C:\GameDev\my.repo`})
	if err != nil {
		t.Fatal(err)
	}
	i := slices.Index(plan.Args, "--session-id")
	if i < 0 || i+1 >= len(plan.Args) || plan.Args[i+1] != id {
		t.Fatalf("session flag missing: %v", plan.Args)
	}
	want := filepath.Join(cfgDir, "projects", "C--GameDev-my-repo", id+".jsonl")
	if plan.TranscriptPath != want {
		t.Fatalf("transcript path: %q, want %q", plan.TranscriptPath, want)
	}

	// No session id -> no flag, no transcript path.
	plan, _ = c.Plan(InvokeSpec{WorkDir: `C:\GameDev\my.repo`})
	if slices.Contains(plan.Args, "--session-id") || plan.TranscriptPath != "" {
		t.Fatalf("sessionless plan leaked session artifacts: %+v", plan)
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

// TestFindAgyEnvOverrideGuard pins the same security guard findClaude has: a
// relative WORKSHOP_AGY_BIN must be absolutized against the launch cwd (never
// left to resolve against cmd.Dir — the agent's worktree, where a previous
// pass could have committed a malicious binary), and the target must exist as
// a file.
func TestFindAgyEnvOverrideGuard(t *testing.T) {
	dir := t.TempDir()
	stub := filepath.Join(dir, "agy-stub")
	if err := os.WriteFile(stub, []byte("stub"), 0o755); err != nil {
		t.Fatal(err)
	}

	t.Setenv("WORKSHOP_AGY_BIN", stub)
	got, err := findAgy()
	if err != nil || got != stub {
		t.Fatalf("absolute override: got %q, %v; want %q", got, err, stub)
	}

	// A relative override resolves against the launch cwd and comes back
	// absolute, so it can never re-resolve inside a worktree later.
	t.Chdir(dir)
	t.Setenv("WORKSHOP_AGY_BIN", "agy-stub")
	got, err = findAgy()
	if err != nil {
		t.Fatalf("relative override: %v", err)
	}
	if !filepath.IsAbs(got) || got != stub {
		t.Fatalf("relative override not absolutized: got %q, want %q", got, stub)
	}

	t.Setenv("WORKSHOP_AGY_BIN", filepath.Join(dir, "does-not-exist"))
	if _, err := findAgy(); err == nil {
		t.Fatal("missing override binary must be refused")
	}
	t.Setenv("WORKSHOP_AGY_BIN", dir)
	if _, err := findAgy(); err == nil {
		t.Fatal("directory override must be refused")
	}
}

func TestAgyPlanRefusesOversizedPrompt(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("command-line cap is a Windows constraint")
	}
	a := &Agy{exe: `C:\bin\agy.exe`, probed: true}
	if _, err := a.Plan(InvokeSpec{Prompt: strings.Repeat("x", 40000)}); err == nil {
		t.Fatal("a 40k-char prompt must refuse to plan on windows")
	}
	if _, err := a.Plan(InvokeSpec{Prompt: "small prompt"}); err != nil {
		t.Fatalf("small prompt must plan: %v", err)
	}
}
