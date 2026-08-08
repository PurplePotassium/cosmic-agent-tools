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

// DefaultCodexModel is used when a Codex workflow turn has no model set.
const DefaultCodexModel = "gpt-5.6-terra"

// Codex drives the CLI installed with ChatGPT/Codex desktop. `exec --json`
// emits typed JSONL and `exec resume` continues the runtime-minted thread, so
// it satisfies Hal's one-process-per-conversational-turn contract without
// pretending Codex accepts caller-minted session ids.
type Codex struct {
	mu     sync.Mutex
	exe    string
	caps   Capabilities
	probed bool
}

func NewCodex() *Codex { return &Codex{} }

func (c *Codex) Name() string { return "codex" }

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
		Interactive: true,
	}
	hctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(hctx, exe, "exec", "--help")
	cmd.WaitDelay = 2 * time.Second
	help, herr := cmd.CombinedOutput()
	if herr == nil {
		text := string(help)
		caps.Effort = strings.Contains(text, "--config")
		// JSONL plus resume are both required for workflow turns. Older Codex
		// builds remain discoverable in Settings but cannot be selected safely.
		if !strings.Contains(text, "--json") || !strings.Contains(text, "resume") {
			caps.Interactive = false
		}
	} else {
		caps.Interactive = false
	}
	c.caps = caps
	c.probed = true
	return caps, nil
}

func (c *Codex) Plan(spec InvokeSpec) (ExecPlan, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.probed {
		return ExecPlan{}, fmt.Errorf("driver: codex not probed")
	}
	if spec.Interactive && !c.caps.Interactive {
		return ExecPlan{}, fmt.Errorf("driver: installed codex does not support JSONL resume turns")
	}
	model := spec.Model
	if model == "" {
		model = DefaultCodexModel
	}
	args := []string{"exec"}
	if spec.ResumeSessionID != "" {
		args = append(args, "resume")
	}
	args = append(args, "--model", model)
	if spec.Effort != "" && c.caps.Effort {
		args = append(args, "--config", fmt.Sprintf("model_reasoning_effort=%q", spec.Effort))
	}
	if spec.SkipPermissions {
		args = append(args, "--dangerously-bypass-approvals-and-sandbox")
	} else if spec.ResumeSessionID == "" {
		// A stage's first turn establishes workspace-write for its session.
		// Read-only stages still need their artifact folder; Hal's post-turn
		// tree check remains the authoritative path-scope enforcement.
		args = append(args, "--sandbox", "workspace-write")
	} else {
		// `exec resume` has no --sandbox flag, but accepts the equivalent
		// documented config override.
		args = append(args, "--config", `sandbox_mode="workspace-write"`)
	}
	if spec.Interactive {
		args = append(args, "--json")
	}
	args = append(args, spec.ExtraArgs...)
	if spec.ResumeSessionID != "" {
		args = append(args, spec.ResumeSessionID)
	}
	// Explicit '-' makes stdin delivery unambiguous for both exec forms.
	args = append(args, "-")
	return ExecPlan{
		Exe: c.exe, Args: args, StdinPrompt: true, Mode: c.caps.SpawnMode(),
	}, nil
}

func findCodex() (string, error) {
	if v := os.Getenv("HAL_CODEX_BIN"); v != "" {
		abs, err := filepath.Abs(v)
		if err != nil {
			return "", fmt.Errorf("driver: HAL_CODEX_BIN %q: %w", v, err)
		}
		if info, err := os.Stat(abs); err != nil || info.IsDir() {
			return "", fmt.Errorf("driver: HAL_CODEX_BIN %q is not an executable file", v)
		}
		return abs, nil
	}
	exe, err := exec.LookPath("codex")
	if err != nil {
		return "", fmt.Errorf("driver: codex not found on PATH (install ChatGPT/Codex, or set HAL_CODEX_BIN)")
	}
	return exe, nil
}
