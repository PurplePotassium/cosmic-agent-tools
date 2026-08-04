package domain

import "time"

// This file holds the interactive-workflow entities: a Workflow moves
// through six fixed human-gated stages (refine → research → design → plan →
// implement → validate), each a live conversation whose approved markdown
// artifact is the handoff to the next stage.

// WorkflowStage is one of the six fixed stages.
type WorkflowStage string

const (
	StageRefine    WorkflowStage = "refine"
	StageResearch  WorkflowStage = "research"
	StageDesign    WorkflowStage = "design"
	StagePlan      WorkflowStage = "plan"
	StageImplement WorkflowStage = "implement"
	StageValidate  WorkflowStage = "validate"
)

// StageOrder is the fixed stage sequence.
var StageOrder = []WorkflowStage{
	StageRefine, StageResearch, StageDesign, StagePlan, StageImplement, StageValidate,
}

// ValidWorkflowStage reports whether s names a stage.
func ValidWorkflowStage(s string) bool {
	for _, st := range StageOrder {
		if string(st) == s {
			return true
		}
	}
	return false
}

// NextStage returns the stage after s (ok=false at validate).
func NextStage(s WorkflowStage) (WorkflowStage, bool) {
	for i, st := range StageOrder {
		if st == s && i+1 < len(StageOrder) {
			return StageOrder[i+1], true
		}
	}
	return "", false
}

// StageIndex returns s's position in StageOrder (0-based, -1 if unknown).
func StageIndex(s WorkflowStage) int {
	for i, st := range StageOrder {
		if st == s {
			return i
		}
	}
	return -1
}

// WorkflowStatus is the workflow-level state the dashboard renders.
type WorkflowStatus string

const (
	// WorkflowTurnRunning: an agent turn process is in flight.
	WorkflowTurnRunning WorkflowStatus = "turn-running"
	// WorkflowAwaitingUser: the agent's last turn ended with questions (or
	// without a ready signal) — the human's move.
	WorkflowAwaitingUser WorkflowStatus = "awaiting-user"
	// WorkflowAwaitingApproval: the stage artifact is ready for review.
	WorkflowAwaitingApproval WorkflowStatus = "awaiting-approval"
	// WorkflowBlocked: the agent hit the implement mismatch hard-stop
	// (plan/reality divergence) and needs direction.
	WorkflowBlocked WorkflowStatus = "blocked"
	// WorkflowError: a turn failed (auth, timeout, crash). Any user message
	// retries via resume.
	WorkflowError WorkflowStatus = "error"
	// WorkflowCompleted: validate approved.
	WorkflowCompleted WorkflowStatus = "completed"
	// WorkflowAbandoned: operator gave up; artifacts left on disk.
	WorkflowAbandoned WorkflowStatus = "abandoned"
)

// Terminal reports whether no further transitions are possible.
func (s WorkflowStatus) Terminal() bool {
	return s == WorkflowCompleted || s == WorkflowAbandoned
}

// AwaitingHuman reports whether the workflow is idle on the human's move —
// the dashboard's attention badge and chime states.
func (s WorkflowStatus) AwaitingHuman() bool {
	switch s {
	case WorkflowAwaitingUser, WorkflowAwaitingApproval, WorkflowBlocked, WorkflowError:
		return true
	}
	return false
}

// Per-stage record states.
const (
	StagePending  = "pending"  // not reached yet
	StageActive   = "active"   // the workflow's current stage
	StageApproved = "approved" // artifact approved; handoff done
	StageSkipped  = "skipped"  // user skipped; stub artifact written
)

// Workflow is one human-gated unit of work.
type Workflow struct {
	ID     string
	Title  string
	Brief  string // the user's initial ask, verbatim
	Stage  WorkflowStage
	Status WorkflowStatus
	Error  string // last failure detail (Status == error)
	// BaseSHA is trunk's tip when implement started — the diff base for
	// validate and the dashboard.
	BaseSHA string
	// SkipValidate: intake left the validate box unchecked. Approving
	// implement stub-skips validate and completes the workflow — no
	// validate turn, no verify gate.
	SkipValidate bool
	// Bundle is the per-workflow model/effort override ("" fields = stage
	// defaults). Agent is always the interactive driver.
	Bundle  Bundle
	Created time.Time
	Updated time.Time
}

// WorkflowStageState is one stage's per-workflow record.
type WorkflowStageState struct {
	WorkflowID string
	Stage      WorkflowStage
	Status     string // pending | active | approved | skipped
	// SessionID is the LATEST captured agent session id — the resume key.
	SessionID string
	// Artifact is the stage's artifact path, repo-relative.
	Artifact     string
	DecisionNote string
	Started      time.Time
	Ended        time.Time
	Decided      time.Time
}

// Turn states (wf_turns.state).
const (
	TurnStateRunning     = "running"
	TurnStateDone        = "done"
	TurnStateFailed      = "failed"
	TurnStateTimeout     = "timeout"
	TurnStateInterrupted = "interrupted"
)

// WorkflowTurn is one agent process run within a stage conversation.
type WorkflowTurn struct {
	ID          int64
	WorkflowID  string
	Stage       WorkflowStage
	N           int // per-workflow counter, 1-based
	SessionID   string
	ResumedFrom string
	State       string
	ExitCode    int
	Error       string
	LogPath     string
	CostUSD     float64
	NumTurns    int
	Started     time.Time
	Ended       time.Time
}

// Message roles and kinds (wf_messages).
const (
	RoleUser      = "user"
	RoleAssistant = "assistant"
	RoleSystem    = "system"

	MsgChat         = "chat"         // ordinary conversation
	MsgStageOpen    = "stage-open"   // engine-authored stage kickoff notice
	MsgInterjection = "interjection" // user message that killed a turn
	MsgApproval     = "approval"     // stage approved (content = note)
	MsgRejection    = "rejection"    // request-changes feedback
	MsgGate         = "gate"         // verify-command output
	MsgNotice       = "notice"       // engine notices (restart, violations)
)

// WorkflowMessage is one chat-history row.
type WorkflowMessage struct {
	ID         int64
	WorkflowID string
	Stage      WorkflowStage
	TurnID     int64
	Role       string
	Kind       string
	Content    string
	Created    time.Time
}

// WorkflowStatusFile is the agent's per-turn self-report, overwritten as the
// last action of every turn at <StateDir>/workflows/<id>/status.json — the
// successor of the pass contract's progress.json.
type WorkflowStatusFile struct {
	// Phase: "asking" (reply ends in questions for the user), "working"
	// (mid-flight commentary), "ready" (artifact written and final), or
	// "blocked" (implement's plan/reality mismatch hard-stop).
	Phase    string `json:"phase"`
	Artifact string `json:"artifact,omitempty"` // repo-relative, when ready
	Note     string `json:"note,omitempty"`     // one line: question/blocker/summary
	Updated  string `json:"updated,omitempty"`  // ISO-8601 UTC
}

// Status-file phases.
const (
	PhaseAsking  = "asking"
	PhaseWorking = "working"
	PhaseReady   = "ready"
	PhaseBlocked = "blocked"
)
