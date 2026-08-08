package domain

import "time"

// This file holds the interactive-workflow entities: a task Workflow moves
// through five fixed human-gated stages (refine → research → design → plan →
// implement), each a live conversation whose approved markdown artifact is
// the handoff to the next stage. Validation is no longer a per-workflow
// stage: a validation RUN is a special workflow (Kind == KindValidation)
// whose single live stage is validate — it sweeps every implemented-but-
// unvalidated workflow at once, runs the verify command and the agent-play
// smoke test, and is empowered to fix obvious issues it finds.

// WorkflowStage is one of the fixed stages.
type WorkflowStage string

const (
	StageRefine    WorkflowStage = "refine"
	StageResearch  WorkflowStage = "research"
	StageDesign    WorkflowStage = "design"
	StagePlan      WorkflowStage = "plan"
	StageImplement WorkflowStage = "implement"
	StageValidate  WorkflowStage = "validate"
)

// StageOrder is the fixed stage sequence. Task workflows climb it only as
// far as implement; the validate rung belongs to validation runs.
var StageOrder = []WorkflowStage{
	StageRefine, StageResearch, StageDesign, StagePlan, StageImplement, StageValidate,
}

// TaskStageOrder is the ladder a task workflow climbs — everything but
// validate, which runs as a cross-workflow validation run instead.
var TaskStageOrder = StageOrder[:len(StageOrder)-1]

// Workflow kinds.
const (
	// KindTask ("" for historical rows): a normal unit of work.
	KindTask = ""
	// KindValidation: a validation run — one validate-stage conversation
	// sweeping every implemented-but-unvalidated task workflow.
	KindValidation = "validation"
)

// ValidWorkflowStage reports whether s names a stage.
func ValidWorkflowStage(s string) bool {
	for _, st := range StageOrder {
		if string(st) == s {
			return true
		}
	}
	return false
}

// NextTaskStage returns the task-ladder stage after s (ok=false at
// implement, the task ladder's last rung).
func NextTaskStage(s WorkflowStage) (WorkflowStage, bool) {
	for i, st := range TaskStageOrder {
		if st == s && i+1 < len(TaskStageOrder) {
			return TaskStageOrder[i+1], true
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
	// WorkflowCompleted: the ladder finished — implement approved/skipped
	// for a task workflow, validate approved/skipped for a validation run.
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
	// validation runs and the dashboard.
	BaseSHA string
	// Kind: "" = task workflow, KindValidation = a validation run.
	Kind string
	// AutoApprove advances a ready stage through the normal approval path.
	// Asking/blocked/error states and failed validation gates always stop.
	AutoApprove bool
	// Validated: when a validation run covered this (task) workflow's
	// implementation; zero until then. ValidatedBy is that run's workflow id.
	Validated   time.Time
	ValidatedBy string
	// Bundle is the per-workflow agent/model/effort override ("" fields =
	// stage defaults).
	Bundle Bundle
	// StageBundles overrides model/effort for individual stages, beating
	// Bundle for that stage (which in turn beats the config's per-stage
	// defaults). Nil/absent entries mean "no override for that stage".
	StageBundles map[WorkflowStage]Bundle
	Created      time.Time
	Updated      time.Time
}

// WorkflowStageState is one stage's per-workflow record.
type WorkflowStageState struct {
	WorkflowID string
	Stage      WorkflowStage
	Status     string // pending | active | approved | skipped
	// SessionID is the LATEST captured agent session id — the resume key.
	SessionID    string
	SessionAgent string // driver that minted SessionID; empty historical rows mean claude
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
