// Package driver abstracts the coding-agent CLIs Workshop can spawn. Every
// behavioral difference between agents — how the prompt is delivered, whether
// output is capturable, whether a reasoning-effort knob exists — is a
// declared Capability, never an if-chain on the agent's name elsewhere in
// the codebase.
package driver

import (
	"context"
	"fmt"

	"github.com/gw1108/cosmic-agent-tools/workshop/internal/proc"
)

// PromptVia says how the prompt reaches the agent process.
type PromptVia int

const (
	PromptStdin PromptVia = iota // piped to stdin
	PromptArg                    // embedded in the argv
)

// CaptureMode says whether the agent's output can be captured.
type CaptureMode int

const (
	// CaptureStreaming: stdout/stderr are pipeable; the engine streams
	// them to the log and event bus.
	CaptureStreaming CaptureMode = iota
	// CaptureNone: the agent silently drops output on a non-TTY handle
	// (or hangs) — it must own a real console and the engine is blind;
	// the agent's progress.json self-report is the only window.
	CaptureNone
)

// Capabilities declares what an agent CLI can do. Probed once per run.
type Capabilities struct {
	PromptVia    PromptVia
	Capture      CaptureMode
	NeedsConsole bool // must own a real (hidden) console — proc.Consoled
	Effort       bool // supports a reasoning-effort flag
	ModelSelect  bool // supports --model
	AuthProbe    bool // auth failures are detectable from captured output
}

// SpawnMode maps capabilities onto the proc spawn mode.
func (c Capabilities) SpawnMode() proc.SpawnMode {
	if c.NeedsConsole || c.Capture == CaptureNone {
		return proc.Consoled
	}
	return proc.Piped
}

// InvokeSpec is one pass's invocation request.
type InvokeSpec struct {
	Prompt          string
	Model           string // "" = driver default
	Effort          string // "" = agent default; ignored (with a warning event) if !Capabilities.Effort
	SkipPermissions bool
	ExtraArgs       []string
	OpLogPath       string // where the driver's own operational log goes ("" = driver default)
}

// ExecPlan is the concrete process invocation derived from an InvokeSpec.
type ExecPlan struct {
	Exe         string
	Args        []string
	StdinPrompt bool // pipe InvokeSpec.Prompt to stdin (else it is in Args)
	Mode        proc.SpawnMode
}

// Driver is one agent CLI backend.
type Driver interface {
	Name() string
	// Probe discovers the binary and its capabilities. Cached by callers
	// per run; implementations may cache internally too.
	Probe(ctx context.Context) (Capabilities, error)
	// Plan builds the process invocation for one pass. Call Probe first.
	Plan(spec InvokeSpec) (ExecPlan, error)
}

// New returns the driver registered under name.
func New(name string) (Driver, error) {
	switch name {
	case "claude", "":
		return NewClaude(), nil
	case "agy":
		return NewAgy(), nil
	case "fake":
		return NewFake(), nil
	default:
		return nil, fmt.Errorf("driver: unknown agent %q (available: claude, agy, fake)", name)
	}
}
