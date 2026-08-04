package statedir

import (
	"os"
	"path/filepath"

	"github.com/PurplePotassium/cosmic-agent-tools/hal/internal/domain"
)

// Agent-facing file names inside a pipeline state dir.
const (
	TaskFile     = "task.json"
	ProgressFile = "progress.json"
)

// PipelineDir returns the per-pipeline agent-facing state dir.
func PipelineDir(projectStateDir, pipeline string) string {
	return filepath.Join(projectStateDir, "state", pipeline)
}

// SharedLabel is how the main backlog is named in CLI/API surfaces.
const SharedLabel = "shared"

// Materialize (re)writes the engine-owned files for one run and clears the
// agent-owned self-report. Call before the agent spawns. task is what the
// agent is asked to do (art jobs pass a synthetic task).
func Materialize(dir string, task *domain.Task) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := WriteJSON(filepath.Join(dir, TaskFile), task); err != nil {
		return err
	}
	// Seed an empty progress report; the agent overwrites it. A run that
	// never writes progress is detectable by the zero phase.
	return WriteJSON(filepath.Join(dir, ProgressFile), domain.Progress{})
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
