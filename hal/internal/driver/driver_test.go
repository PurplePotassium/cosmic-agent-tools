package driver

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/PurplePotassium/cosmic-agent-tools/hal/internal/proc"
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
	t.Setenv("HAL_CLAUDE_BIN", fakeClaudeBin(t))
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
	t.Setenv("HAL_CLAUDE_BIN", fakeClaudeBin(t))
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

func TestClaudeInteractivePlan(t *testing.T) {
	t.Setenv("HAL_CLAUDE_BIN", fakeClaudeBin(t))
	cfgDir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", cfgDir)
	c := NewClaude()
	caps, err := c.Probe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !caps.Interactive {
		t.Fatal("claude must declare the Interactive capability")
	}

	// Fresh turn: minted session id, stream-json flags, scoped tools.
	const id = "0eca2f27-e7b7-4a96-89df-4631bee2db0e"
	plan, err := c.Plan(InvokeSpec{
		Interactive:     true,
		Model:           "claude-sonnet-5",
		SessionID:       id,
		WorkDir:         `C:\GameDev\my.repo`,
		PermissionMode:  "plan",
		AllowedTools:    []string{"Read", "Grep", "Write"},
		DisallowedTools: []string{"AskUserQuestion"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"-p", "--model", "claude-sonnet-5",
		"--output-format", "stream-json", "--include-partial-messages", "--verbose",
		"--permission-mode", "plan",
		"--allowedTools", "Read,Grep,Write",
		"--disallowedTools", "AskUserQuestion",
		"--session-id", id,
	}
	if !slices.Equal(plan.Args, want) {
		t.Fatalf("args: %v, want %v", plan.Args, want)
	}
	if plan.TranscriptPath == "" {
		t.Fatal("fresh interactive turn must report a transcript path")
	}

	// Resumed turn: --resume wins, no --session-id, transcript keyed by the
	// resumed id (ids do not rotate on v2.1.220).
	const resume = "5b119aaf-c4c6-4e2f-9d6f-1a2b3c4d5e6f"
	plan, err = c.Plan(InvokeSpec{
		Interactive: true, SessionID: id, ResumeSessionID: resume, WorkDir: `C:\GameDev\my.repo`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(plan.Args, "--session-id") {
		t.Fatalf("resume must suppress --session-id: %v", plan.Args)
	}
	i := slices.Index(plan.Args, "--resume")
	if i < 0 || plan.Args[i+1] != resume {
		t.Fatalf("resume flag missing: %v", plan.Args)
	}
	if !strings.Contains(plan.TranscriptPath, resume) {
		t.Fatalf("transcript must key off the resumed id: %q", plan.TranscriptPath)
	}

	// SkipPermissions suppresses the scoped-permission flags entirely.
	plan, err = c.Plan(InvokeSpec{
		Interactive: true, SkipPermissions: true,
		PermissionMode: "plan", AllowedTools: []string{"Read"},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, flag := range []string{"--permission-mode", "--allowedTools"} {
		if slices.Contains(plan.Args, flag) {
			t.Fatalf("%s must not combine with --dangerously-skip-permissions: %v", flag, plan.Args)
		}
	}
	if !slices.Contains(plan.Args, "--dangerously-skip-permissions") {
		t.Fatalf("skip flag missing: %v", plan.Args)
	}

	// Non-interactive specs must not leak turn flags (the art/inquiry path).
	plan, err = c.Plan(InvokeSpec{Model: "claude-sonnet-5"})
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(plan.Args, "--output-format") {
		t.Fatalf("one-shot plan leaked stream flags: %v", plan.Args)
	}
}

func TestNonInteractiveDriversRefuseTurns(t *testing.T) {
	a := &Agy{exe: `C:\bin\agy.exe`, probed: true}
	if _, err := a.Plan(InvokeSpec{Interactive: true, Prompt: "x"}); err == nil {
		t.Fatal("agy must refuse interactive specs")
	}
}

func TestClaudeEffortWhenSupported(t *testing.T) {
	t.Setenv("HAL_CLAUDE_BIN", fakeClaudeBin(t))
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

// TestFindAgyEnvOverrideGuard pins the same security guard findClaude has: a
// relative HAL_AGY_BIN must be absolutized against the launch cwd (never
// left to resolve against cmd.Dir — the agent's worktree, where a previous
// pass could have committed a malicious binary), and the target must exist as
// a file.
func TestFindAgyEnvOverrideGuard(t *testing.T) {
	dir := t.TempDir()
	stub := filepath.Join(dir, "agy-stub")
	if err := os.WriteFile(stub, []byte("stub"), 0o755); err != nil {
		t.Fatal(err)
	}

	t.Setenv("HAL_AGY_BIN", stub)
	got, err := findAgy()
	if err != nil || got != stub {
		t.Fatalf("absolute override: got %q, %v; want %q", got, err, stub)
	}

	// A relative override resolves against the launch cwd and comes back
	// absolute, so it can never re-resolve inside a worktree later.
	t.Chdir(dir)
	t.Setenv("HAL_AGY_BIN", "agy-stub")
	got, err = findAgy()
	if err != nil {
		t.Fatalf("relative override: %v", err)
	}
	if !filepath.IsAbs(got) || got != stub {
		t.Fatalf("relative override not absolutized: got %q, want %q", got, stub)
	}

	t.Setenv("HAL_AGY_BIN", filepath.Join(dir, "does-not-exist"))
	if _, err := findAgy(); err == nil {
		t.Fatal("missing override binary must be refused")
	}
	t.Setenv("HAL_AGY_BIN", dir)
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
