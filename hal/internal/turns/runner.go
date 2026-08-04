package turns

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/PurplePotassium/cosmic-agent-tools/hal/internal/driver"
	"github.com/PurplePotassium/cosmic-agent-tools/hal/internal/statedir"
)

// TurnSpec is one conversational turn: a single agent process that receives
// one message and streams events until its per-turn result.
type TurnSpec struct {
	Prompt string // the stage-opening prompt or the user's message

	Model  string
	Effort string

	// SessionID mints a fresh session (first turn of a stage); Resume
	// continues a prior one with full context. Resume wins when both are set.
	SessionID string
	Resume    string

	SkipPermissions bool
	PermissionMode  string
	AllowedTools    []string
	DisallowedTools []string
	ExtraArgs       []string

	WorkDir  string
	LogPath  string        // raw NDJSON turn log ("" = no log file)
	Timeout  time.Duration // per-turn ceiling; NEVER covers waiting on the user
	ExtraEnv []string
}

// TurnState classifies how a turn ended.
type TurnState string

const (
	// TurnDone: result event arrived, is_error false.
	TurnDone TurnState = "done"
	// TurnFailed: is_error result, non-zero exit, or the process died
	// without a result event.
	TurnFailed TurnState = "failed"
	// TurnTimeout: the TurnSpec.Timeout elapsed mid-turn.
	TurnTimeout TurnState = "timeout"
	// TurnInterrupted: the parent context was canceled (operator interject
	// or engine halt) — resume-able, never counted as a failure.
	TurnInterrupted TurnState = "interrupted"
)

// TurnResult is the settled outcome of one turn.
type TurnResult struct {
	State TurnState
	// SessionID is the authoritative id for resuming: captured from the
	// stream's init/result events, falling back to the requested id.
	SessionID string
	// FinalText is the assistant's settled reply: the result event's text,
	// else the accumulated deltas (killed/failed turns).
	FinalText string
	Usage     *driver.TurnUsage // nil when no result event arrived
	ExitCode  int
	Tail      []string // last raw lines — auth-failure probing on failures
}

// Sink observes parsed stream events live (bus bridging, delta streaming).
// Called from the drain goroutine; must not block.
type Sink func(driver.StreamEvent)

// Runner executes turns. The interface seam lets manager tests substitute a
// scripted in-process runner (no OS processes at all).
type Runner interface {
	Run(ctx context.Context, drv driver.Driver, spec TurnSpec, sink Sink) (TurnResult, error)
}

// ProcRunner is the production Runner: one OS process per turn via Exec.
type ProcRunner struct{}

// Run executes one turn to completion. The returned error covers plan/spawn
// failures only; agent-level failures land in TurnResult.State.
func (ProcRunner) Run(ctx context.Context, drv driver.Driver, spec TurnSpec, sink Sink) (TurnResult, error) {
	caps, err := drv.Probe(ctx)
	if err != nil {
		return TurnResult{State: TurnFailed, ExitCode: -1}, err
	}
	if !caps.Interactive {
		return TurnResult{State: TurnFailed, ExitCode: -1},
			fmt.Errorf("turns: driver %q cannot run interactive turns", drv.Name())
	}
	plan, err := drv.Plan(driver.InvokeSpec{
		Prompt:          spec.Prompt,
		Model:           spec.Model,
		Effort:          spec.Effort,
		Interactive:     true,
		SessionID:       spec.SessionID,
		ResumeSessionID: spec.Resume,
		SkipPermissions: spec.SkipPermissions,
		PermissionMode:  spec.PermissionMode,
		AllowedTools:    spec.AllowedTools,
		DisallowedTools: spec.DisallowedTools,
		ExtraArgs:       spec.ExtraArgs,
		WorkDir:         spec.WorkDir,
	})
	if err != nil {
		return TurnResult{State: TurnFailed, ExitCode: -1}, err
	}

	var logFile *os.File
	if spec.LogPath != "" {
		if err := os.MkdirAll(filepath.Dir(spec.LogPath), 0o755); err != nil {
			return TurnResult{State: TurnFailed, ExitCode: -1}, err
		}
		logFile, err = os.Create(spec.LogPath)
		if err != nil {
			return TurnResult{State: TurnFailed, ExitCode: -1}, err
		}
		defer logFile.Close()
	}

	// Accumulated under mu: the drain goroutine can outlive Exec by the
	// drain grace period when a grandchild wedges the pipe.
	var mu sync.Mutex
	var sessionID string
	var deltas strings.Builder
	var usage *driver.TurnUsage
	var finalText string

	res, err := Exec(ctx, ExecSpec{
		Plan:     plan,
		Prompt:   spec.Prompt,
		Dir:      spec.WorkDir,
		ExtraEnv: spec.ExtraEnv,
		Timeout:  spec.Timeout,
		LogFile:  logFile,
		OnLine: func(line string) {
			for _, ev := range driver.ParseStreamLine([]byte(line)) {
				mu.Lock()
				if ev.SessionID != "" {
					sessionID = ev.SessionID
				}
				switch ev.Kind {
				case driver.StreamTextDelta:
					deltas.WriteString(ev.Text)
				case driver.StreamResult:
					u := *ev.Result
					usage = &u
					finalText = u.ResultText
				}
				mu.Unlock()
				if sink != nil {
					sink(ev)
				}
			}
		},
	})

	mu.Lock()
	out := TurnResult{
		SessionID: sessionID,
		FinalText: finalText,
		Usage:     usage,
		ExitCode:  res.ExitCode,
		Tail:      res.Tail,
	}
	if out.FinalText == "" {
		out.FinalText = deltas.String()
	}
	mu.Unlock()
	if out.SessionID == "" {
		if spec.Resume != "" {
			out.SessionID = spec.Resume
		} else {
			out.SessionID = spec.SessionID
		}
	}

	switch {
	case err != nil:
		out.State = TurnFailed
	case ctx.Err() != nil:
		// Parent canceled: operator interject or engine halt. Trumps
		// everything — the session resumes later.
		out.State = TurnInterrupted
	case res.TimedOut:
		out.State = TurnTimeout
	case out.Usage != nil && !out.Usage.IsError && res.ExitCode == 0:
		out.State = TurnDone
	default:
		// is_error result, non-zero exit, or death without a result event.
		out.State = TurnFailed
	}

	archiveTranscript(drv, plan, spec, out.SessionID)
	return out, err
}

// archiveTranscript copies the runtime's session transcript next to the turn
// log. Runtimes prune their own stores — this copy is ours. Best-effort: a
// killed turn may not have flushed one yet, and the resumed session's file
// keeps growing, so later turns re-archive the superset.
func archiveTranscript(drv driver.Driver, plan driver.ExecPlan, spec TurnSpec, capturedID string) {
	if spec.LogPath == "" {
		return
	}
	src := plan.TranscriptPath
	// A rotated session id (future CLI versions may fork on --resume) moves
	// the transcript; re-derive from the captured id.
	if drv.Name() == "claude" && capturedID != "" && spec.WorkDir != "" {
		if derived := driver.ClaudeTranscriptPath(spec.WorkDir, capturedID); derived != "" {
			src = derived
		}
	}
	if src == "" {
		return
	}
	_ = statedir.CopyFile(src, TranscriptArchivePath(spec.LogPath))
}

// TranscriptArchivePath derives the transcript archive location from a turn
// log path (turn-000003.log -> turn-000003.transcript.jsonl).
func TranscriptArchivePath(logPath string) string {
	return strings.TrimSuffix(logPath, ".log") + ".transcript.jsonl"
}
