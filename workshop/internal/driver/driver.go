// Package driver abstracts the coding-agent CLIs Workshop can spawn. Every
// behavioral difference between agents — how the prompt is delivered, whether
// output is capturable, whether a reasoning-effort knob exists — is a
// declared Capability, never an if-chain on the agent's name elsewhere in
// the codebase.
package driver

import (
	"context"
	"fmt"

	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/proc"
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
	// Sessions: the runtime accepts a caller-chosen session id and writes a
	// recoverable full transcript (tool calls, reasoning) keyed by it. When
	// set, Plan honors InvokeSpec.SessionID and reports ExecPlan.TranscriptPath.
	Sessions bool
	// ConversationTranscript: the runtime keeps a full transcript (prompt as
	// sent, reasoning, response) keyed by its own RUNTIME-MINTED conversation
	// id, so — unlike Sessions — the location is only discoverable AFTER a
	// run (AgyLastConversation + AgyTranscriptPath). The engine archives it
	// post-run when ExecPlan.TranscriptPath is empty.
	ConversationTranscript bool
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
	SessionID       string // caller-minted session id; ignored if !Capabilities.Sessions
	WorkDir         string // the cwd the process will run in (some transcript paths derive from it)
	// ConversationID resumes a previous conversation of the agent's own
	// runtime (agy `--conversation`). Unlike SessionID it is runtime-minted:
	// the caller discovers it AFTER a prior pass (driver.AgyLastConversation)
	// and passes it back to continue that session with full context.
	ConversationID string
}

// ExecPlan is the concrete process invocation derived from an InvokeSpec.
type ExecPlan struct {
	Exe         string
	Args        []string
	StdinPrompt bool // pipe InvokeSpec.Prompt to stdin (else it is in Args)
	Mode        proc.SpawnMode
	// TranscriptPath is where the runtime will leave this run's full session
	// transcript ("" = the runtime keeps none we can locate). Read it AFTER
	// the process exits; archive it promptly — runtimes prune their own logs.
	TranscriptPath string
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
