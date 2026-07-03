package driver

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// DefaultClaudeModel is used when no model is configured anywhere. Passes are
// small and gated, so the fast frontier model is the right default; route
// heavy task types to a stronger model via the [types.*] table.
const DefaultClaudeModel = "claude-sonnet-5"

// Claude drives the Claude Code CLI: prompt over stdin, `-p` print mode,
// fully capturable output.
type Claude struct {
	mu     sync.Mutex
	exe    string
	caps   Capabilities
	probed bool
}

// NewClaude returns the claude driver (unprobed).
func NewClaude() *Claude { return &Claude{} }

func (c *Claude) Name() string { return "claude" }

// Probe locates the binary and detects the effort flag from --help output.
func (c *Claude) Probe(ctx context.Context) (Capabilities, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.probed {
		return c.caps, nil
	}
	exe, err := findClaude()
	if err != nil {
		return Capabilities{}, err
	}
	c.exe = exe

	caps := Capabilities{
		PromptVia:   PromptStdin,
		Capture:     CaptureStreaming,
		ModelSelect: true,
		AuthProbe:   true,
		Sessions:    true,
	}
	hctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	help, herr := exec.CommandContext(hctx, exe, "--help").CombinedOutput()
	if herr == nil {
		caps.Effort = parseEffortSupport(string(help))
	}
	// A failed --help leaves Effort=false (conservative) but doesn't block:
	// the binary existing is what matters.
	c.caps = caps
	c.probed = true
	return caps, nil
}

// parseEffortSupport detects a reasoning-effort flag in help text.
func parseEffortSupport(help string) bool {
	return strings.Contains(help, "--effort")
}

// Plan builds the invocation. Effort is applied only when probed as
// supported; the caller decides whether to surface a warning otherwise.
func (c *Claude) Plan(spec InvokeSpec) (ExecPlan, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.probed {
		return ExecPlan{}, fmt.Errorf("driver: claude not probed")
	}
	model := spec.Model
	if model == "" {
		model = DefaultClaudeModel
	}
	args := []string{"-p", "--model", model}
	if spec.Effort != "" && c.caps.Effort {
		args = append(args, "--effort", spec.Effort)
	}
	if spec.SkipPermissions {
		args = append(args, "--dangerously-skip-permissions")
	}
	transcript := ""
	if spec.SessionID != "" {
		args = append(args, "--session-id", spec.SessionID)
		if spec.WorkDir != "" {
			transcript = claudeTranscriptPath(spec.WorkDir, spec.SessionID)
		}
	}
	args = append(args, spec.ExtraArgs...)
	return ExecPlan{
		Exe: c.exe, Args: args, StdinPrompt: true, Mode: c.caps.SpawnMode(),
		TranscriptPath: transcript,
	}, nil
}

// claudeTranscriptPath is where the Claude Code CLI stores the full session
// transcript (JSONL: prompt, every tool call + result, thinking blocks) for a
// run with --session-id in workDir: <config dir>/projects/<slug>/<id>.jsonl,
// where the slug is the absolute workDir with every non-alphanumeric byte
// replaced by '-'. Returns "" when no home/config dir is resolvable.
func claudeTranscriptPath(workDir, sessionID string) string {
	cfg := os.Getenv("CLAUDE_CONFIG_DIR")
	if cfg == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		cfg = filepath.Join(home, ".claude")
	}
	slug := []byte(workDir)
	for i, c := range slug {
		if !('a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9') {
			slug[i] = '-'
		}
	}
	return filepath.Join(cfg, "projects", string(slug), sessionID+".jsonl")
}

func findClaude() (string, error) {
	if v := os.Getenv("WORKSHOP_CLAUDE_BIN"); v != "" {
		return v, nil
	}
	exe, err := exec.LookPath("claude")
	if err != nil {
		return "", fmt.Errorf("driver: claude not found on PATH (install Claude Code, or set WORKSHOP_CLAUDE_BIN)")
	}
	return exe, nil
}
