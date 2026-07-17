// Package domain holds the pure data types shared across Workshop.
// It performs no I/O and imports nothing outside the standard library.
package domain

import (
	"strings"
	"time"
)

// MainBacklog is the Task.Backlog value for the shared main backlog.
// Any other value names the pipeline whose exclusive backlog owns the task.
const MainBacklog = ""

// TaskStatus is the lifecycle state of a Task.
type TaskStatus string

const (
	TaskOpen      TaskStatus = "open"
	TaskClaimed   TaskStatus = "claimed"
	TaskDone      TaskStatus = "done"
	TaskFailed    TaskStatus = "failed"
	TaskStuck     TaskStatus = "stuck" // repeatedly blocked/reverted; needs operator attention
	TaskCancelled TaskStatus = "cancelled"
)

// TaskOrigin records who created a task.
type TaskOrigin string

const (
	OriginOperator TaskOrigin = "operator"
	OriginAgent    TaskOrigin = "agent"
	OriginSystem   TaskOrigin = "system" // merge-conflict, gate-red, ...
)

// Bundle is one validated agent/model/effort selection.
type Bundle struct {
	Agent  string `json:"agent,omitempty" toml:"agent,omitempty"`
	Model  string `json:"model,omitempty" toml:"model,omitempty"`
	Effort string `json:"effort,omitempty" toml:"effort,omitempty"`
}

// IsZero reports whether no field of the bundle is set.
func (b Bundle) IsZero() bool { return b.Agent == "" && b.Model == "" && b.Effort == "" }

// Overlay returns b with any empty field filled from base.
func (b Bundle) Overlay(base Bundle) Bundle {
	if b.Agent == "" {
		b.Agent = base.Agent
	}
	if b.Model == "" {
		b.Model = base.Model
	}
	if b.Effort == "" {
		b.Effort = base.Effort
	}
	return b
}

// Efforts is the ordered set of recognized effort levels.
var Efforts = []string{"low", "medium", "high", "xhigh", "max"}

// ValidEffort reports whether e is empty (unset) or a recognized effort level.
func ValidEffort(e string) bool {
	if e == "" {
		return true
	}
	for _, v := range Efforts {
		if e == v {
			return true
		}
	}
	return false
}

// ClaudeModels are the curated model-id prefixes for the claude driver: the
// four Claude families (version suffixes change often; family names don't).
var ClaudeModels = []string{"claude-sonnet", "claude-fable", "claude-opus", "claude-haiku"}

// AgyModels are the curated model-id prefixes for the agy driver. agy
// (Antigravity CLI) is wired up here purely for Gemini routing — a wrong
// `--model` id fails the pass blind (see AGENTS.md), so this only ever
// produces a warning, never blocks.
var AgyModels = []string{"gemini"}

// Art-generation task types. Both run on the claude driver pinned to a
// frontier model (see ArtClaudeModels): the claude pass ORCHESTRATES — it
// invokes the Antigravity CLI (agy, the Gemini image model) to do the actual
// image generation and verifies what agy wrote. They are the only task types
// whose passes produce an image asset instead of editing code. art-gen-trans
// additionally rescreens the asset in the SAME agy conversation and
// chroma-keys the background away, producing a transparent PNG.
const (
	ArtGenType      = "art-gen"
	ArtGenTransType = "art-gen-trans"
)

// IsArtType reports whether t names one of the art-generation task types.
func IsArtType(t string) bool { return t == ArtGenType || t == ArtGenTransType }

// ArtClaudeModels is the ordered preference list of claude model-id PREFIXES
// allowed to orchestrate an art pass: only the frontier families — fable (the
// Mythos-class tier above opus) first, then opus. Directing an image model
// and judging its output is judgment-heavy work, so weaker families are
// re-routed at pass time. Prefixes, not full ids, so a newer (better) version
// of either family qualifies without a code change.
var ArtClaudeModels = []string{"claude-fable", "claude-opus"}

// ArtClaudeDefault is the claude model art passes run when routing/pinning
// did not already pick an allowed frontier model.
const ArtClaudeDefault = "claude-fable-5"

// AllowedArtClaudeModel reports whether model belongs to one of the frontier
// families allowed to orchestrate art passes (case-insensitive prefix match).
func AllowedArtClaudeModel(model string) bool {
	lower := strings.ToLower(model)
	for _, fam := range ArtClaudeModels {
		if strings.HasPrefix(lower, fam) {
			return true
		}
	}
	return false
}

// ArtAgyModels is the ordered preference list of agy model labels allowed to
// generate art-pass images — the label the claude orchestrator hands to agy's
// --model flag. These are agy's EXACT labels (verified against `agy models` /
// the invalid-model probe — see AGENTS.md): shorthand like "gemini 3 pro" is
// rejected by agy with exit 1. Earlier entries are preferred; launch
// verification picks the first one agy actually offers.
var ArtAgyModels = []string{"Gemini 3.1 Pro (High)", "Gemini 3.5 Flash (High)"}

// AllowedArtModel reports whether model is one of the labels art passes may
// run (case-insensitive; agy labels are display strings).
func AllowedArtModel(model string) bool {
	for _, m := range ArtAgyModels {
		if strings.EqualFold(m, model) {
			return true
		}
	}
	return false
}

// ModelFamilies returns the curated model-id prefixes recognized for agent,
// or nil if agent names no built-in driver (nothing curated to check against).
func ModelFamilies(agent string) []string {
	switch agent {
	case "claude":
		return ClaudeModels
	case "agy":
		return AgyModels
	default:
		return nil
	}
}

// KnownModel reports whether model is empty, agent has no curated family list,
// or model matches one of agent's curated prefixes. The prefix match is
// case-insensitive: agy model ids are display labels ("Gemini 3.1 Pro (High)")
// while the curated families are lowercase.
func KnownModel(agent, model string) bool {
	if model == "" {
		return true
	}
	families := ModelFamilies(agent)
	if families == nil {
		return true
	}
	lower := strings.ToLower(model)
	for _, fam := range families {
		if strings.HasPrefix(lower, fam) {
			return true
		}
	}
	return false
}

// Task is one unit of backlog work. Tasks live either on the shared main
// backlog (Backlog == MainBacklog) or on exactly one pipeline's exclusive
// backlog (Backlog == pipeline name).
type Task struct {
	ID       string     `json:"id"`
	Backlog  string     `json:"backlog,omitempty"`
	Type     string     `json:"type,omitempty"` // "" = unclassified
	Title    string     `json:"title"`
	Detail   string     `json:"detail,omitempty"`
	Files    []string   `json:"files,omitempty"` // optional scope hint
	Pin      Bundle     `json:"pin,omitzero"`    // operator pin — beats the routing table
	Position float64    `json:"-"`               // per-backlog ordering; lower = sooner
	Status   TaskStatus `json:"status,omitempty"`
	Origin   TaskOrigin `json:"origin,omitempty"`

	ClaimedBy string            `json:"claimedBy,omitempty"` // pipeline name
	ClaimPass int64             `json:"-"`
	Attempts  int               `json:"attempts,omitempty"`
	NotBefore time.Time         `json:"-"` // retry backoff gate
	Meta      map[string]string `json:"meta,omitempty"`

	Created time.Time `json:"created"`
	Updated time.Time `json:"-"`
}

// NormalizeType canonicalizes a task type: lowercased, trimmed, and
// restricted to [a-z0-9_-]; anything else returns "" (= auto-classify).
// Types are compared case-sensitively in the SQL claim filter and the
// routing map, AND used as a prompt-fragment path segment
// (prompts/types/<type>.md) — so a freeform value from an agent proposal
// must never pass through verbatim: "Art" would be unclaimable by a
// types=["art"] pipeline, and "../x" would read files outside prompts/.
func NormalizeType(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	for _, r := range s {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' && r != '_' {
			return ""
		}
	}
	return s
}

// ExpandMetaKey marks an expansion meta-task ("for each X, add a task"):
// the pass's deliverable is proposals.json with one entry per enumerated
// item — exempt from the freeform proposal cap — and a pass that reports
// done with no proposals is failed, not completed.
const ExpandMetaKey = "expand"

// IsExpand reports whether t is an expansion meta-task. Nil-safe.
func (t *Task) IsExpand() bool { return t != nil && t.Meta[ExpandMetaKey] == "true" }

// Pipeline is the configuration-shaped description of one worker lane.
// Runtime state (iteration counter, breaker, halt reason) lives in the store
// and engine, not here.
type Pipeline struct {
	Name      string
	Bundle    Bundle   // fallback bundle for untyped / invented tasks
	TaskTypes []string // main-backlog claim filter; empty = all types
	DrainMain bool     // also claim from the main backlog
	ScopeHint string   // soft file-ownership guidance appended to the prompt tail
	Invent    bool     // idle + empty backlog => may invent one task toward GOAL
	// AcceptProposals: whether a finished pass's proposals.json follow-ups get
	// ingested into the backlog. Together with Invent this forms the pipeline's
	// mode: goal (both true, the default), discover (Invent false, this true —
	// only proposes follow-ups right after finishing an assigned task), or
	// drain (both false — the pipeline only ever shrinks the backlog).
	AcceptProposals bool
	Enabled         bool
	Worktree        *bool // per-pipeline override of the project worktree setting; nil = inherit

	// Personality: "" (none, the default) does nothing; "random" draws one
	// from the configured [personality] roster each pass; anything else
	// names one roster entry directly (validated at config load).
	Personality string

	PassTimeout time.Duration // wedge threshold; 0 = driver default
	ExtraArgs   []string      // raw args appended to every agent invocation

	// Computed at runtime by the engine:
	Branch string // branch the pipeline commits to
	Dir    string // working dir (worktree dir, or the repo dir in simple mode)
}

// Mode reports the resolved operating mode implied by Invent and
// AcceptProposals: "goal" (both true, the default), "discover" (Invent false,
// AcceptProposals true), or "drain" (both false).
func (p Pipeline) Mode() string {
	switch {
	case p.Invent:
		return "goal"
	case p.AcceptProposals:
		return "discover"
	default:
		return "drain"
	}
}

// Modes is the ordered set of recognized pipeline modes.
var Modes = []string{"goal", "discover", "drain"}

// ValidMode reports whether m is empty (no override) or a recognized mode.
func ValidMode(m string) bool {
	if m == "" {
		return true
	}
	for _, v := range Modes {
		if m == v {
			return true
		}
	}
	return false
}

// ModeFlags is Mode's inverse: it converts a mode name to the
// (Invent, AcceptProposals) flags it implies. Callers only call this for a
// non-empty, ValidMode-checked m; there is no sensible flag pair for "".
func ModeFlags(m string) (invent, acceptProposals bool) {
	switch m {
	case "goal":
		return true, true
	case "discover":
		return false, true
	default: // "drain"
		return false, false
	}
}

// HandlesType reports whether the pipeline claims tasks of type t from the
// main backlog. Own-backlog tasks are always claimable regardless of type.
func (p Pipeline) HandlesType(t string) bool {
	if len(p.TaskTypes) == 0 {
		return true
	}
	for _, v := range p.TaskTypes {
		if strings.EqualFold(v, t) {
			return true
		}
	}
	return false
}

// PassState is the engine state machine position of a pass.
type PassState string

const (
	PassClaiming   PassState = "claiming"
	PassPreparing  PassState = "preparing"
	PassRunning    PassState = "running"
	PassIngesting  PassState = "ingesting"
	PassCommitting PassState = "committing"
	PassDone       PassState = "done"
	PassFailed     PassState = "failed"
)

// FailKind classifies why a pass failed.
type FailKind string

const (
	FailNone    FailKind = ""
	FailAuth    FailKind = "auth"
	FailExit    FailKind = "exit"
	FailTimeout FailKind = "timeout"
	FailSetup   FailKind = "setup"
)

// PassOutcome is the agent-reported (or engine-derived) result of a pass.
type PassOutcome string

const (
	OutcomeDone     PassOutcome = "done"
	OutcomeBlocked  PassOutcome = "blocked"
	OutcomeReverted PassOutcome = "reverted"
	OutcomeNoChange PassOutcome = "no-change"
	OutcomeFailed   PassOutcome = "failed"
)

// Pass is one iteration of one pipeline.
type Pass struct {
	ID       int64
	Pipeline string
	N        int    // per-pipeline iteration counter (survives restarts)
	TaskID   string // claimed task; freeform (invented) passes get a synthetic task
	Spice    string // "persona:X" / "recode:noun/Stem" / "" — recorded for forensics

	// Personality is the resolved [personality] roster entry this pass ran
	// under ("" when none resolved), mirroring Spice — recorded so the
	// dashboard can show it without opening the raw pass log.
	Personality string

	// SessionID is the agent runtime's own session id for this pass (set only
	// for drivers with Capabilities.Sessions). It keys the agent's full
	// transcript — every tool call and reasoning block — for forensics.
	SessionID string

	State   PassState
	Started time.Time
	Ended   time.Time

	ExitCode  *int
	CommitSHA string
	Outcome   PassOutcome
	Failure   FailKind
	LogPath   string
}

// Progress is the agent's self-report, overwritten whole-file each pass.
// It is the only window into a pass run by a blind (non-capturable) driver.
type Progress struct {
	Phase  string `json:"phase"` // working | done | blocked | reverted
	Task   string `json:"task,omitempty"`
	Plan   string `json:"plan,omitempty"`
	Note   string `json:"note,omitempty"`
	Result string `json:"result,omitempty"`
	// Decisions is the agent's optional record of judgment calls: assumptions
	// made, alternatives rejected, deviations from the task or project docs.
	// Preserved in the pass log for later "why did this happen?" forensics.
	Decisions string `json:"decisions,omitempty"`
	Updated   string `json:"updated,omitempty"` // ISO-8601, agent-written
}

// Completion is the durable record of a finished task.
type Completion struct {
	ID        string    `json:"id"`
	TaskID    string    `json:"taskId,omitempty"`
	Pipeline  string    `json:"pipeline,omitempty"`
	Title     string    `json:"title"`
	Result    string    `json:"result,omitempty"`
	Completed time.Time `json:"completed"`
}

// Proposal is a follow-up task suggested by an agent in proposals.json.
type Proposal struct {
	Title   string   `json:"title"`
	Detail  string   `json:"detail,omitempty"`
	Type    string   `json:"type,omitempty"`
	Backlog string   `json:"backlog,omitempty"` // "" = main; or a pipeline name
	Files   []string `json:"files,omitempty"`
}

// LaneIntegration is the integrator's per-pipeline merge-queue state.
type LaneIntegration struct {
	Pipeline         string
	LastSeenTip      string // tip SHA already merged-or-dropped (skip-until-advanced key)
	Blocked          bool
	BlockedBy        string // "" | "gate" | "conflict" | "+"-joined kept-set names
	ProvenCulprit    bool   // failed the gate ALONE on trunk (not just in combination)
	ConflictTaskID   string // open merge-conflict task, if any
	ConflictAttempts int    // resolution attempts for the current tip
}

// Event is one bus event. Payload keys are event-type specific.
type Event struct {
	Seq      int64          `json:"seq"`
	TS       time.Time      `json:"ts"`
	Type     string         `json:"type"`
	Pipeline string         `json:"pipeline,omitempty"`
	Pass     int64          `json:"pass,omitempty"`
	Payload  map[string]any `json:"payload,omitempty"`
}
