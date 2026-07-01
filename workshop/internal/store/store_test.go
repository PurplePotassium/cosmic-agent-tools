package store

import (
	"path/filepath"
	"testing"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestProjectCRUD(t *testing.T) {
	st := newTestStore(t)
	p := &Project{ID: "x", Name: "x", RepoPath: "/repo/x", StateDir: "/state/x", Agent: "claude", Model: "m"}
	if err := st.CreateProject(p); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetProject("x")
	if err != nil || got.Name != "x" {
		t.Fatalf("GetProject = %+v, %v", got, err)
	}
	if _, err := st.GetProjectByRepoPath("/repo/x"); err != nil {
		t.Fatalf("GetProjectByRepoPath: %v", err)
	}
	if err := st.SetProjectAgent("x", "agy", "gemini-3.5-flash"); err != nil {
		t.Fatal(err)
	}
	got, _ = st.GetProject("x")
	if got.Agent != "agy" || got.Model != "gemini-3.5-flash" {
		t.Fatalf("SetProjectAgent didn't persist: %+v", got)
	}
	if _, err := st.GetProject("missing"); err != ErrNotFound {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
	if err := st.DeleteProject("x"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetProject("x"); err != ErrNotFound {
		t.Fatalf("deleted project should be gone, got %v", err)
	}
}

func TestBacklogOrdering(t *testing.T) {
	st := newTestStore(t)
	_ = st.CreateProject(&Project{ID: "p", Name: "p", RepoPath: "/r", StateDir: "/s"})

	_, _ = st.AddBacklog("p", BacklogItem{ID: "a", Title: "a"}, false) // bottom
	_, _ = st.AddBacklog("p", BacklogItem{ID: "b", Title: "b"}, false) // bottom
	_, _ = st.AddBacklog("p", BacklogItem{ID: "c", Title: "c"}, true)  // TOP

	got, _ := st.ListBacklog("p")
	order := make([]string, len(got))
	for i, it := range got {
		order[i] = it.ID
	}
	if len(order) != 3 || order[0] != "c" || order[1] != "a" || order[2] != "b" {
		t.Fatalf("order = %v, want [c a b]", order)
	}

	// Reorder to b,c,a.
	if err := st.ReorderBacklog("p", []string{"b", "c", "a"}); err != nil {
		t.Fatal(err)
	}
	got, _ = st.ListBacklog("p")
	if got[0].ID != "b" || got[1].ID != "c" || got[2].ID != "a" {
		t.Fatalf("reordered = %v, want [b c a]", ids2(got))
	}

	// Delete the middle.
	if err := st.DeleteBacklogItem("p", "c"); err != nil {
		t.Fatal(err)
	}
	got, _ = st.ListBacklog("p")
	if len(got) != 2 || got[0].ID != "b" || got[1].ID != "a" {
		t.Fatalf("after delete = %v, want [b a]", ids2(got))
	}
}

func TestProgressUpsert(t *testing.T) {
	st := newTestStore(t)
	_ = st.CreateProject(&Project{ID: "p", Name: "p", RepoPath: "/r", StateDir: "/s"})

	// missing → zero value, no error
	if p, err := st.GetProgress("p"); err != nil || p.Phase != "" {
		t.Fatalf("empty progress = %+v, %v", p, err)
	}
	_ = st.SetProgress("p", Progress{Phase: "working", Task: "t1"})
	_ = st.SetProgress("p", Progress{Phase: "done", Task: "t1", Result: "ok"}) // upsert, not duplicate
	got, _ := st.GetProgress("p")
	if got.Phase != "done" || got.Result != "ok" {
		t.Fatalf("progress = %+v, want done/ok", got)
	}
}

func TestHistory(t *testing.T) {
	st := newTestStore(t)
	_ = st.CreateProject(&Project{ID: "p", Name: "p", RepoPath: "/r", StateDir: "/s"})

	runID, err := st.StartRun("p")
	if err != nil {
		t.Fatal(err)
	}
	itID, err := st.AddIteration(Iteration{ProjectID: "p", RunID: runID, Num: 1, Agent: "claude", Model: "m", Status: "running"})
	if err != nil {
		t.Fatal(err)
	}
	// running iteration is visible
	if ri, err := st.RunningIteration("p"); err != nil || ri.Num != 1 {
		t.Fatalf("RunningIteration = %+v, %v", ri, err)
	}
	if err := st.FinishIteration(itID, "abc123", "1 file changed", "ok"); err != nil {
		t.Fatal(err)
	}
	last, err := st.LastPassIteration("p")
	if err != nil || last.CommitSHA != "abc123" {
		t.Fatalf("LastPassIteration = %+v, %v", last, err)
	}
	if err := st.EndRun(runID, 1, "done"); err != nil {
		t.Fatal(err)
	}
	its, _ := st.ListIterations("p", 10)
	if len(its) != 1 {
		t.Fatalf("want 1 iteration, got %d", len(its))
	}
}

func ids2(items []BacklogItem) []string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.ID
	}
	return out
}
