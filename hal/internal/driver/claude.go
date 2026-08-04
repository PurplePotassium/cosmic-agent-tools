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
		Interactive: true,
	}
	hctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(hctx, exe, "--help")
	// CombinedOutput returns on pipe EOF, not process exit. An npm shim
	// (claude.cmd -> node) killed by the timeout leaves its node child
	// holding the pipe write-end — without WaitDelay the probe hangs
	// forever, well past the "15s timeout".
	cmd.WaitDelay = 2 * time.Second
	help, herr := cmd.CombinedOutput()
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
	if spec.Interactive {
		// Turn mode: typed NDJSON events, partial-message deltas for live
		// streaming, --verbose (required for stream-json in print mode).
		args = append(args, "--output-format", "stream-json", "--include-partial-messages", "--verbose")
	}
	if spec.SkipPermissions {
		args = append(args, "--dangerously-skip-permissions")
	} else {
		if spec.PermissionMode != "" {
			args = append(args, "--permission-mode", spec.PermissionMode)
		}
		if len(spec.AllowedTools) > 0 {
			args = append(args, "--allowedTools", strings.Join(spec.AllowedTools, ","))
		}
		if len(spec.DisallowedTools) > 0 {
			args = append(args, "--disallowedTools", strings.Join(spec.DisallowedTools, ","))
		}
	}
	transcript := ""
	switch {
	case spec.ResumeSessionID != "":
		// Probed (v2.1.220): --resume does not rotate the session id, so the
		// transcript keeps growing at the resumed id's path. The turn runner
		// still re-derives from the captured id in case a future CLI forks.
		args = append(args, "--resume", spec.ResumeSessionID)
		if spec.WorkDir != "" {
			transcript = claudeTranscriptPath(spec.WorkDir, spec.ResumeSessionID)
		}
	case spec.SessionID != "":
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

// ClaudeTranscriptPath exposes the transcript location for a session id the
// caller only learned AFTER a run (captured from stream-json init/result
// events) — the turn runner uses it when the runtime rotated ids on resume.
func ClaudeTranscriptPath(workDir, sessionID string) string {
	return claudeTranscriptPath(workDir, sessionID)
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
	if v := os.Getenv("HAL_CLAUDE_BIN"); v != "" {
		// Absolutize: a relative override would resolve against cmd.Dir —
		// the agent's WORKTREE — so a previous pass committing a file at
		// that relative path would get executed as the agent binary.
		abs, err := filepath.Abs(v)
		if err != nil {
			return "", fmt.Errorf("driver: HAL_CLAUDE_BIN %q: %w", v, err)
		}
		if info, err := os.Stat(abs); err != nil || info.IsDir() {
			return "", fmt.Errorf("driver: HAL_CLAUDE_BIN %q is not an executable file", v)
		}
		return abs, nil
	}
	exe, err := exec.LookPath("claude")
	if err != nil {
		return "", fmt.Errorf("driver: claude not found on PATH (install Claude Code, or set HAL_CLAUDE_BIN)")
	}
	return exe, nil
}
