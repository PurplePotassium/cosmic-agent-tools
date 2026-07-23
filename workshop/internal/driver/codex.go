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

// DefaultCodexModel is used when a Codex pipeline has no model configured.
// Terra is the balanced default; Sol and Luna remain selectable through
// routing, task pins, and the dashboard.
const DefaultCodexModel = "gpt-5.6-terra"

// Codex drives the OpenAI Codex CLI in its non-interactive exec mode. Prompts
// arrive on stdin and output stays pipeable, so it uses Workshop's ordinary
// streaming and auth-detection paths.
type Codex struct {
	mu     sync.Mutex
	exe    string
	caps   Capabilities
	probed bool
}

// NewCodex returns the Codex driver (unprobed).
func NewCodex() *Codex { return &Codex{} }

func (c *Codex) Name() string { return "codex" }

// Probe locates Codex and checks whether its generic config override switch is
// available before promising Workshop's effort knob. Current Codex releases
// accept model_reasoning_effort through that switch.
func (c *Codex) Probe(ctx context.Context) (Capabilities, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.probed {
		return c.caps, nil
	}
	exe, err := findCodex()
	if err != nil {
		return Capabilities{}, err
	}
	c.exe = exe

	caps := Capabilities{
		PromptVia:   PromptStdin,
		Capture:     CaptureStreaming,
		ModelSelect: true,
		AuthProbe:   true,
	}
	hctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(hctx, exe, "exec", "--help")
	// As with the other JavaScript-distributed CLIs, a timed-out shim may leave
	// a child process holding the pipe. Bound Wait as well as the context.
	cmd.WaitDelay = 2 * time.Second
	help, herr := cmd.CombinedOutput()
	if herr == nil {
		caps.Effort = parseCodexConfigSupport(string(help))
	}
	c.caps = caps
	c.probed = true
	return caps, nil
}

// parseCodexConfigSupport detects Codex's -c/--config override flag. Workshop
// uses it only for model_reasoning_effort; older CLIs simply omit that knob.
func parseCodexConfigSupport(help string) bool {
	return strings.Contains(help, "--config")
}

// Plan builds a non-interactive `codex exec` invocation. Codex has no
// caller-minted session-transcript capability, so SessionID is deliberately
// not forwarded.
func (c *Codex) Plan(spec InvokeSpec) (ExecPlan, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.probed {
		return ExecPlan{}, fmt.Errorf("driver: codex not probed")
	}
	model := spec.Model
	if model == "" {
		model = DefaultCodexModel
	}
	args := []string{"exec", "--model", model}
	if spec.Effort != "" && c.caps.Effort {
		args = append(args, "--config", fmt.Sprintf("model_reasoning_effort=%q", spec.Effort))
	}
	if spec.SkipPermissions {
		// This is Codex's documented unattended mode. It intentionally maps
		// Workshop's existing [safety].skip_permissions opt-in, whose default
		// is true for all autonomous drivers.
		args = append(args, "--dangerously-bypass-approvals-and-sandbox")
	} else {
		// Keep non-interactive passes limited to the repository workspace when
		// the operator has explicitly disabled the unattended bypass.
		args = append(args, "--sandbox", "workspace-write")
	}
	args = append(args, spec.ExtraArgs...)
	return ExecPlan{
		Exe: c.exe, Args: args, StdinPrompt: true, Mode: c.caps.SpawnMode(),
	}, nil
}

func findCodex() (string, error) {
	if v := os.Getenv("WORKSHOP_CODEX_BIN"); v != "" {
		// Match the other drivers' guard: never leave a relative binary path
		// for exec.Cmd to resolve later from an agent-controlled worktree.
		abs, err := filepath.Abs(v)
		if err != nil {
			return "", fmt.Errorf("driver: WORKSHOP_CODEX_BIN %q: %w", v, err)
		}
		if info, err := os.Stat(abs); err != nil || info.IsDir() {
			return "", fmt.Errorf("driver: WORKSHOP_CODEX_BIN %q is not an executable file", v)
		}
		return abs, nil
	}
	exe, err := exec.LookPath("codex")
	if err != nil {
		return "", fmt.Errorf("driver: codex not found on PATH (install Codex CLI, or set WORKSHOP_CODEX_BIN)")
	}
	return exe, nil
}
