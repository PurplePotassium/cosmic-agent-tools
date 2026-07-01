package engine

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/store"
)

// The agent-facing JSON files live in the project's state dir (outside the repo).
// The DB is the source of truth the UI edits; the engine MATERIALIZES these files
// from the DB before each pass and RECONCILES them back afterward. Diff-based
// reconcile (not overwrite) preserves BOTH the agent's file edits AND any UI edit
// that landed in the DB mid-pass — replacing today's JSON read-modify-write races.
const (
	backlogFile     = "backlog.json"
	completionsFile = "completions.json"
	progressFile    = "progress.json"
)

// writeJSONFile marshals v as pretty JSON with no BOM (Go writes none — the agent's
// and Node's JSON readers both choke on a UTF-8 BOM).
func writeJSONFile(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

func readJSONFile(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	// Tolerate a stray UTF-8 BOM if some other tool wrote one.
	if len(b) >= 3 && b[0] == 0xEF && b[1] == 0xBB && b[2] == 0xBF {
		b = b[3:]
	}
	return json.Unmarshal(b, v)
}

type stateSnapshot struct {
	backlog     map[string]bool
	completions map[string]bool
}

// materializeState writes backlog.json + completions.json from the DB and ensures
// progress.json exists, returning the id snapshots used to reconcile after the pass.
func materializeState(dir string, st *store.Store, projectID string) (stateSnapshot, error) {
	snap := stateSnapshot{backlog: map[string]bool{}, completions: map[string]bool{}}

	backlog, err := st.ListBacklog(projectID)
	if err != nil {
		return snap, err
	}
	if backlog == nil {
		backlog = []store.BacklogItem{}
	}
	for _, b := range backlog {
		snap.backlog[b.ID] = true
	}
	if err := writeJSONFile(filepath.Join(dir, backlogFile), backlog); err != nil {
		return snap, err
	}

	compl, err := st.ListCompletions(projectID, 1000)
	if err != nil {
		return snap, err
	}
	if compl == nil {
		compl = []store.Completion{}
	}
	for _, c := range compl {
		snap.completions[c.ID] = true
	}
	if err := writeJSONFile(filepath.Join(dir, completionsFile), compl); err != nil {
		return snap, err
	}

	// Seed an empty progress.json if none — the agent overwrites it wholesale.
	pf := filepath.Join(dir, progressFile)
	if _, err := os.Stat(pf); os.IsNotExist(err) {
		_ = writeJSONFile(pf, store.Progress{})
	}
	return snap, nil
}

// reconcileState reads the agent-written JSON files back into the DB using a diff so
// concurrent UI edits aren't clobbered. It returns the latest progress self-report
// (zero value if unreadable). A file that fails to parse is SKIPPED — never treated
// as "the agent emptied it" — so a mid-pass write-in-progress can't wipe the DB.
func reconcileState(dir string, st *store.Store, projectID string, snap stateSnapshot) store.Progress {
	// --- backlog: agent may drain the top item and append up to 2 follow-ups ---
	var backlog []store.BacklogItem
	if err := readJSONFile(filepath.Join(dir, backlogFile), &backlog); err == nil {
		fileIDs := map[string]bool{}
		for _, b := range backlog {
			fileIDs[b.ID] = true
		}
		// removed = in snapshot but no longer in file → the agent drained them.
		var removed []string
		for id := range snap.backlog {
			if !fileIDs[id] {
				removed = append(removed, id)
			}
		}
		_ = st.DeleteBacklogItems(projectID, removed)
		// added = in file but not in snapshot → the agent's new follow-ups.
		var added []store.BacklogItem
		for _, b := range backlog {
			if !snap.backlog[b.ID] {
				added = append(added, b)
			}
		}
		_ = st.AppendBacklogItems(projectID, added)
	}

	// --- completions: agent only appends; insert any new records ---
	var compl []store.Completion
	if err := readJSONFile(filepath.Join(dir, completionsFile), &compl); err == nil {
		for _, c := range compl {
			if !snap.completions[c.ID] {
				_ = st.AddCompletion(projectID, c)
			}
		}
	}

	// --- progress: agent overwrites wholesale; store the latest ---
	var prog store.Progress
	if err := readJSONFile(filepath.Join(dir, progressFile), &prog); err == nil {
		_ = st.SetProgress(projectID, prog)
		return prog
	}
	return store.Progress{}
}
