package statedir

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/gw1108/cosmic-agent-tools/workshop/internal/domain"
)

// Agent-facing file names inside a pipeline state dir.
const (
	TaskFile        = "task.json"
	BacklogFile     = "backlog.json"
	CompletionsFile = "completions.json"
	ProgressFile    = "progress.json"
	ProposalsFile   = "proposals.json"
)

// PipelineDir returns the per-pipeline agent-facing state dir.
func PipelineDir(projectStateDir, pipeline string) string {
	return filepath.Join(projectStateDir, "state", pipeline)
}

// TaskView is the agent-facing projection of a task in backlog.json: enough
// to survey the whole task space (and dedupe proposals) without internal
// bookkeeping fields.
type TaskView struct {
	ID      string `json:"id"`
	Backlog string `json:"backlog"` // "shared" for the main backlog
	Type    string `json:"type,omitempty"`
	Title   string `json:"title"`
	Detail  string `json:"detail,omitempty"`
	Status  string `json:"status"`
	Claimed string `json:"claimedBy,omitempty"`
}

// SharedLabel is how the main backlog is named in agent-facing files.
const SharedLabel = "shared"

// NewTaskView projects a task for the snapshot.
func NewTaskView(t *domain.Task) TaskView {
	backlog := t.Backlog
	if backlog == domain.MainBacklog {
		backlog = SharedLabel
	}
	return TaskView{
		ID: t.ID, Backlog: backlog, Type: t.Type, Title: t.Title,
		Detail: t.Detail, Status: string(t.Status), Claimed: t.ClaimedBy,
	}
}

// Materialize (re)writes the engine-owned files for one pass and clears the
// agent-owned ones. Call inside the claim step, before the agent spawns.
//   - task: the claimed task (nil for an invent pass — task.json then holds
//     an explanatory stub).
//   - snapshot: ALL open/claimed tasks across every backlog.
//   - completions: recent completions digest.
func Materialize(dir string, task *domain.Task, snapshot []*domain.Task, completions []*domain.Completion) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	var taskDoc any
	if task != nil {
		taskDoc = task
	} else {
		taskDoc = map[string]string{
			"note": "no eligible backlog task — INVENT one increment toward the GOAL (see prompt)",
		}
	}
	if err := WriteJSON(filepath.Join(dir, TaskFile), taskDoc); err != nil {
		return err
	}
	views := make([]TaskView, 0, len(snapshot))
	for _, t := range snapshot {
		views = append(views, NewTaskView(t))
	}
	if err := WriteJSON(filepath.Join(dir, BacklogFile), views); err != nil {
		return err
	}
	if completions == nil {
		completions = []*domain.Completion{}
	}
	if err := WriteJSON(filepath.Join(dir, CompletionsFile), completions); err != nil {
		return err
	}
	// Seed an empty progress report; the agent overwrites it. A pass that
	// never writes progress is detectable by the zero phase.
	if err := WriteJSON(filepath.Join(dir, ProgressFile), domain.Progress{}); err != nil {
		return err
	}
	// Stale proposals from a previous pass must not be re-ingested.
	if err := os.Remove(filepath.Join(dir, ProposalsFile)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// ReadProgress reads the agent's self-report; a missing/empty/corrupt file
// returns a zero Progress (never an error — the agent may be mid-write).
func ReadProgress(dir string) domain.Progress {
	var p domain.Progress
	if err := ReadJSON(filepath.Join(dir, ProgressFile), &p); err != nil {
		return domain.Progress{}
	}
	return p
}

// ReadProposals reads and validates the agent's follow-up suggestions.
// Missing file = no proposals. A corrupt file is skipped (never fails a
// pass); entries without a title are dropped.
func ReadProposals(dir string) []domain.Proposal {
	var raw []domain.Proposal
	if err := ReadJSON(filepath.Join(dir, ProposalsFile), &raw); err != nil {
		return nil
	}
	var out []domain.Proposal
	for _, p := range raw {
		if p.Title != "" {
			out = append(out, p)
		}
	}
	return out
}
