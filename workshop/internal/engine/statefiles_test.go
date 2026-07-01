package engine

import (
	"path/filepath"
	"testing"

	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/store"
)

// TestReconcilePreservesConcurrentEdits is the load-bearing correctness test: the
// pass-end reconcile must apply the AGENT's file edits (drain the top item, append a
// follow-up) WITHOUT clobbering a UI edit that landed in the DB mid-pass.
func TestReconcilePreservesConcurrentEdits(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	pid := "p1"
	if err := st.CreateProject(&store.Project{
		ID: pid, Name: "p", RepoPath: filepath.Join(dir, "repo"), StateDir: dir,
	}); err != nil {
		t.Fatal(err)
	}

	// Seed two backlog items and one existing completion.
	b1, _ := st.AddBacklog(pid, store.BacklogItem{ID: "b1", Title: "first"}, false)
	_, _ = st.AddBacklog(pid, store.BacklogItem{ID: "b2", Title: "second"}, false)
	_ = st.AddCompletion(pid, store.Completion{ID: "c0", Title: "already done"})

	// Pass start: materialize files + capture the snapshot the agent was handed.
	snap, err := materializeState(dir, st, pid)
	if err != nil {
		t.Fatal(err)
	}
	if !snap.backlog["b1"] || !snap.backlog["b2"] {
		t.Fatalf("snapshot should contain b1,b2: %v", snap.backlog)
	}

	// The AGENT rewrites backlog.json: drains b1 (its task) and appends follow-up b3.
	agentBacklog := []store.BacklogItem{
		{ID: "b2", Title: "second"},
		{ID: "b3", Title: "agent follow-up", Created: b1.Created},
	}
	if err := writeJSONFile(filepath.Join(dir, backlogFile), agentBacklog); err != nil {
		t.Fatal(err)
	}
	// The AGENT appends a completion.
	agentCompletions := []store.Completion{
		{ID: "c0", Title: "already done"},
		{ID: "c1", Title: "did the first thing", Result: "verified"},
	}
	if err := writeJSONFile(filepath.Join(dir, completionsFile), agentCompletions); err != nil {
		t.Fatal(err)
	}
	// The AGENT writes its self-report.
	if err := writeJSONFile(filepath.Join(dir, progressFile), store.Progress{
		Phase: "done", Task: "first", Result: "verified", Updated: "2026-06-30T00:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}

	// Meanwhile the UI added b4 to the DB DURING the pass (never in the materialized file).
	_, _ = st.AddBacklog(pid, store.BacklogItem{ID: "b4", Title: "ui added mid-pass"}, false)

	// Pass end: reconcile.
	prog := reconcileState(dir, st, pid, snap)

	// Backlog must be exactly {b2, b3, b4}: b1 drained, b3 added, b4 PRESERVED.
	got, _ := st.ListBacklog(pid)
	gotIDs := map[string]bool{}
	for _, it := range got {
		gotIDs[it.ID] = true
	}
	want := []string{"b2", "b3", "b4"}
	if len(got) != len(want) {
		t.Fatalf("backlog = %v ids, want %v", ids(got), want)
	}
	for _, id := range want {
		if !gotIDs[id] {
			t.Fatalf("backlog missing %s; got %v", id, ids(got))
		}
	}
	if gotIDs["b1"] {
		t.Fatalf("b1 should have been drained; got %v", ids(got))
	}

	// Completions must include the agent's new c1 (and keep c0).
	compl, _ := st.ListCompletions(pid, 100)
	var hasC1 bool
	for _, c := range compl {
		if c.ID == "c1" {
			hasC1 = true
		}
	}
	if !hasC1 {
		t.Fatalf("completion c1 not persisted; got %d records", len(compl))
	}

	// Progress must be the agent's final self-report.
	if prog.Phase != "done" {
		t.Fatalf("progress phase = %q, want done", prog.Phase)
	}
	stored, _ := st.GetProgress(pid)
	if stored.Phase != "done" || stored.Result != "verified" {
		t.Fatalf("stored progress = %+v", stored)
	}
}

func ids(items []store.BacklogItem) []string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.ID
	}
	return out
}
