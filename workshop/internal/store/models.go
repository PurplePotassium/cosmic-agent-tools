package store

import "time"

// Project is the first-class unit of Workshop: one coding agent working one repo
// working directory toward a goal. Multiple projects run concurrently (each in its
// own working tree) — that is the whole multi-agent/multi-goal model, with NO
// worktrees, lanes, or trunk-merge. Each project's state (GOAL.md/PROMPT.md/logs +
// the materialized backlog/completions/progress) lives in StateDir, OUTSIDE RepoPath,
// so the per-pass `git add -A` never sweeps Workshop state into the user's repo.
type Project struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	RepoPath string    `json:"repoPath"`
	StateDir string    `json:"stateDir"`
	Branch   string    `json:"branch"`
	Agent    string    `json:"agent"` // claude | agy | auto
	Model    string    `json:"model"` // concrete model id, or "auto"
	Status   string    `json:"status"`
	Preview  string    `json:"preview"` // optional preview URL shown in the UI
	Created  time.Time `json:"created"`
	Updated  time.Time `json:"updated"`
}

// Project statuses.
const (
	StatusStopped = "stopped"
	StatusRunning = "running"
	StatusError   = "error"
)

// BacklogItem is one operator-curated task. The backlog is drained TOP-first
// (lowest Position). The JSON tags match the on-disk backlog.json shape the agent
// reads/writes each pass; Position is a DB-only ordering column.
type BacklogItem struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Detail   string `json:"detail"`
	Created  string `json:"created"`
	Position int    `json:"-"`
}

// Completion is a finished-pass record. JSON tags match completions.json.
type Completion struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Result    string `json:"result"`
	Completed string `json:"completed"`
}

// Progress is the agent's self-report ("offboard"), written at pass start and end.
// It is the ONLY window into an `agy` pass (whose stdout is uncapturable headless).
// JSON tags match progress.json.
type Progress struct {
	Phase   string `json:"phase"`
	Task    string `json:"task"`
	Plan    string `json:"plan"`
	Note    string `json:"note"`
	Result  string `json:"result"`
	Updated string `json:"updated"`
}

// Run is one Start→Stop session of a project's loop.
type Run struct {
	ID         int64      `json:"id"`
	ProjectID  string     `json:"projectId"`
	Started    time.Time  `json:"started"`
	Ended      *time.Time `json:"ended,omitempty"`
	Iterations int        `json:"iterations"`
	Status     string     `json:"status"`
}

// Iteration records one pass: which agent/model ran it, the anti-circling spice,
// the log-file path, the resulting commit + files, and how it ended.
type Iteration struct {
	ID           int64      `json:"id"`
	ProjectID    string     `json:"projectId"`
	RunID        int64      `json:"runId"`
	Num          int        `json:"num"`
	Agent        string     `json:"agent"`
	Model        string     `json:"model"`
	Spice        string     `json:"spice"`
	LogPath      string     `json:"logPath"`
	CommitSHA    string     `json:"commitSha"`
	FilesChanged string     `json:"filesChanged"`
	Status       string     `json:"status"` // ok | failed | auth-fail | skipped
	Started      time.Time  `json:"started"`
	Ended        *time.Time `json:"ended,omitempty"`
}
