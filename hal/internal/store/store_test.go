package store

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

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

// TestMigrateAddsValidationColumns proves a database written before the
// validation-run era opens cleanly and its workflows scan with the grafted
// kind/validated/validated_by columns at their defaults (a task workflow,
// not yet validated).
func TestMigrateAddsValidationColumns(t *testing.T) {
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
	if wf.Kind != domain.KindTask {
		t.Fatalf("a legacy workflow must scan as a task workflow, got kind %q", wf.Kind)
	}
	if !wf.Validated.IsZero() || wf.ValidatedBy != "" {
		t.Fatal("a legacy workflow must scan as not yet validated")
	}
	if !wf.AutoApprove {
		t.Fatal("a legacy workflow must inherit auto-approval's default-on behavior")
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
