package store

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/PurplePotassium/cosmic-agent-tools/hal/internal/domain"
)

func TestEncodeURIPath(t *testing.T) {
	// Reserved URI chars must be percent-encoded so they don't truncate the
	// filename, but the '/' separators stay literal.
	got := encodeURIPath("/repos/x#y%z/hal.db")
	if want := "/repos/x%23y%25z/hal.db"; got != want {
		t.Fatalf("encodeURIPath = %q, want %q", got, want)
	}
	if runtime.GOOS == "windows" {
		// Backslash separators and the drive colon stay literal too — but
		// only on Windows; on Unix '\' is an ordinary filename character
		// and filepath.ToSlash leaves it alone.
		got := encodeURIPath(`C:\repos\x#y%z\hal.db`)
		if want := "C:/repos/x%23y%25z/hal.db"; got != want {
			t.Fatalf("encodeURIPath = %q, want %q", got, want)
		}
	}
}

// A store path under a directory whose name holds URI-reserved characters
// legal on Windows and Unix (#, %) must open and write to that exact file —
// an unescaped '#' would start a URI fragment and truncate the DB filename.
func TestOpenPathWithReservedChars(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "x#y%z")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(dir, "test.db")
	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("open under %q: %v", dir, err)
	}
	defer s.Close()
	if _, err := s.AddTask(context.Background(), &domain.Task{Title: "t"}, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dbPath); err != nil {
		t.Fatalf("db did not land at %q: %v", dbPath, err)
	}
}

func openTest(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func addTask(t *testing.T, s *Store, backlog, typ, title string, top bool) *domain.Task {
	t.Helper()
	task, err := s.AddTask(context.Background(), &domain.Task{Backlog: backlog, Type: typ, Title: title}, top)
	if err != nil {
		t.Fatal(err)
	}
	return task
}

func TestAddListOrder(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	a := addTask(t, s, "", "code", "first", false)
	b := addTask(t, s, "", "code", "second", false)
	c := addTask(t, s, "", "art", "urgent", true) // top

	main := domain.MainBacklog
	got, err := s.ListTasks(ctx, TaskFilter{Backlog: &main})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].ID != c.ID || got[1].ID != a.ID || got[2].ID != b.ID {
		t.Fatalf("order wrong: %v", titles(got))
	}
}

func TestCompleteMoveReorderDelete(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	a := addTask(t, s, "", "", "a", false)
	b := addTask(t, s, "", "", "b", false)
	c := addTask(t, s, "", "", "c", false)

	if err := s.CompleteTask(ctx, a.ID, "main", "did it"); err != nil {
		t.Fatal(err)
	}
	comps, err := s.ListCompletions(ctx, 10)
	if err != nil || len(comps) != 1 || comps[0].Title != "a" || comps[0].Pipeline != "main" {
		t.Fatalf("completions: %v, %v", comps, err)
	}

	if _, err := s.MoveTask(ctx, b.ID, "art"); err != nil {
		t.Fatal(err)
	}
	art := "art"
	artTasks, _ := s.ListTasks(ctx, TaskFilter{Backlog: &art})
	if len(artTasks) != 1 || artTasks[0].ID != b.ID {
		t.Fatalf("move failed: %v", titles(artTasks))
	}

	d := addTask(t, s, "", "", "d", false)
	if err := s.ReorderBacklog(ctx, "", []string{d.ID, c.ID}); err != nil {
		t.Fatal(err)
	}
	main := domain.MainBacklog
	open, _ := s.ListTasks(ctx, TaskFilter{Backlog: &main, Statuses: []domain.TaskStatus{domain.TaskOpen}})
	if len(open) != 2 || open[0].ID != d.ID || open[1].ID != c.ID {
		t.Fatalf("reorder failed: %v", titles(open))
	}

	if err := s.DeleteTask(ctx, c.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetTask(ctx, c.ID); err != ErrNotFound {
		t.Fatalf("delete: %v", err)
	}
}

func TestPassLifecycleAndCounter(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	p1, err := s.StartPass(ctx, "code")
	if err != nil {
		t.Fatal(err)
	}
	p2, err := s.StartPass(ctx, "code")
	if err != nil {
		t.Fatal(err)
	}
	if p1.N != 1 || p2.N != 2 {
		t.Fatalf("iteration counter: %d then %d", p1.N, p2.N)
	}
	sha := "abc1234"
	state := domain.PassDone
	outcome := domain.OutcomeDone
	ended := time.Now().UTC()
	if err := s.UpdatePass(ctx, p1.ID, PassPatch{CommitSHA: &sha, State: &state, Outcome: &outcome, Ended: &ended}); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetPass(ctx, p1.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.CommitSHA != sha || got.State != domain.PassDone || got.Ended.IsZero() {
		t.Fatalf("pass: %+v", got)
	}
	recent, _ := s.RecentPasses(ctx, "code", 10)
	if len(recent) != 2 || recent[0].ID != p2.ID {
		t.Fatalf("recent: %v", recent)
	}
}

func TestEventsAppendAndReplay(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		if _, err := s.AppendEvent(ctx, &domain.Event{Type: "test", Payload: map[string]any{"i": i}}); err != nil {
			t.Fatal(err)
		}
	}
	evs, err := s.EventsSince(ctx, 2, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 3 || evs[0].Seq != 3 {
		t.Fatalf("replay: %d events, first seq %d", len(evs), evs[0].Seq)
	}
}

func titles(ts []*domain.Task) []string {
	out := make([]string, len(ts))
	for i, t := range ts {
		out[i] = t.Title
	}
	return out
}

func TestPassSessionID(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	pass, err := s.StartPass(ctx, "main")
	if err != nil {
		t.Fatal(err)
	}
	sid := "0eca2f27-e7b7-4a96-89df-4631bee2db0e"
	if err := s.UpdatePass(ctx, pass.ID, PassPatch{SessionID: &sid}); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetPass(ctx, pass.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.SessionID != sid {
		t.Fatalf("session id: %q, want %q", got.SessionID, sid)
	}
}

// TestMigrateAddsSkipValidateColumn proves a database written before the
// intake validate checkbox opens cleanly, and that its workflows keep the
// full ladder (skip_validate defaults to 0 = validate runs).
func TestMigrateAddsSkipValidateColumn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`DROP TABLE workflows`,
		`CREATE TABLE workflows (
			id TEXT PRIMARY KEY, title TEXT NOT NULL, brief TEXT NOT NULL DEFAULT '',
			stage TEXT NOT NULL, status TEXT NOT NULL, error TEXT NOT NULL DEFAULT '',
			base_sha TEXT NOT NULL DEFAULT '', bundle_agent TEXT NOT NULL DEFAULT '',
			bundle_model TEXT NOT NULL DEFAULT '', bundle_effort TEXT NOT NULL DEFAULT '',
			created INTEGER NOT NULL, updated INTEGER NOT NULL
		)`,
		`INSERT INTO workflows (id, title, stage, status, created, updated)
			VALUES ('old-wf', 'legacy', 'plan', 'awaiting-user', 1, 1)`,
	} {
		if _, err := s.db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	s.Close()

	s2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen after downgrade: %v", err)
	}
	defer s2.Close()
	wf, err := s2.GetWorkflow(context.Background(), "old-wf")
	if err != nil {
		t.Fatalf("scan migrated workflow: %v", err)
	}
	if wf.SkipValidate {
		t.Fatal("a pre-checkbox workflow must still run validate")
	}
}

// TestMigrateAddsSessionColumn proves an existing database created before the
// session_id column opens cleanly and gains the column.
func TestMigrateAddsSessionColumn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	// Rewind the passes table to its pre-session_id shape.
	for _, stmt := range []string{
		`DROP TABLE passes`,
		`CREATE TABLE passes (
			id INTEGER PRIMARY KEY AUTOINCREMENT, pipeline TEXT NOT NULL, n INTEGER NOT NULL,
			task_id TEXT NOT NULL DEFAULT '', spice TEXT NOT NULL DEFAULT '', state TEXT NOT NULL,
			started INTEGER NOT NULL, ended INTEGER NOT NULL DEFAULT 0, exit_code INTEGER,
			commit_sha TEXT NOT NULL DEFAULT '', outcome TEXT NOT NULL DEFAULT '',
			failure TEXT NOT NULL DEFAULT '', log_path TEXT NOT NULL DEFAULT ''
		)`,
		`INSERT INTO passes (pipeline, n, state, started) VALUES ('main', 1, 'done', 1)`,
	} {
		if _, err := s.db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	s.Close()

	s2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen after downgrade: %v", err)
	}
	defer s2.Close()
	passes, err := s2.RecentPasses(context.Background(), "", 10)
	if err != nil {
		t.Fatalf("scan migrated passes: %v", err)
	}
	if len(passes) != 1 || passes[0].SessionID != "" {
		t.Fatalf("migrated pass wrong: %+v", passes)
	}
}
