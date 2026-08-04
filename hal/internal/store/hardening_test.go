package store

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/PurplePotassium/cosmic-agent-tools/hal/internal/domain"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "hal.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

// The stored schema_version must track the binary that last migrated the
// schema, or the refuse-newer-hal guard compares against the version
// that first CREATED the database and can never fire.
func TestSchemaVersionAdvancesOnMigrate(t *testing.T) {
	st := openTestStore(t)
	// Regress the stored version to simulate a database created by an
	// older hal, then re-run migrate (what Open does on upgrade).
	if _, err := st.db.Exec(`UPDATE kv SET v = '0' WHERE k = 'schema_version'`); err != nil {
		t.Fatal(err)
	}
	if err := st.migrate(); err != nil {
		t.Fatal(err)
	}
	var v string
	if err := st.db.QueryRow(`SELECT v FROM kv WHERE k = 'schema_version'`).Scan(&v); err != nil {
		t.Fatal(err)
	}
	if want := "1"; v != want {
		t.Fatalf("schema_version after migrate = %q, want %q (DO NOTHING regression)", v, want)
	}
}

// Position is computed inside the INSERT: concurrent adds (engine + CLI +
// dashboard are all writers) must never claim the same slot.
func TestConcurrentAddTaskPositionsAreDistinct(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	const n = 16
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(top bool) {
			defer wg.Done()
			if _, err := st.AddTask(ctx, &domain.Task{Title: "t"}, top); err != nil {
				t.Errorf("add: %v", err)
			}
		}(i%2 == 0)
	}
	wg.Wait()

	tasks, err := st.ListTasks(ctx, TaskFilter{})
	if err != nil || len(tasks) != n {
		t.Fatalf("tasks=%d err=%v", len(tasks), err)
	}
	seen := map[float64]bool{}
	for _, task := range tasks {
		if seen[task.Position] {
			t.Fatalf("duplicate position %v", task.Position)
		}
		seen[task.Position] = true
	}
}

// MoveTask computes its new edge position inside the UPDATE; concurrent moves
// into one backlog (racing adds) must never collide on a position — the old
// SELECT-MAX-then-UPDATE could read the same stale edge from two goroutines.
func TestConcurrentMoveTaskPositionsAreDistinct(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	const n = 12
	ids := make([]string, n)
	for i := 0; i < n; i++ {
		task, err := st.AddTask(ctx, &domain.Task{Backlog: "code", Title: "seed"}, false)
		if err != nil {
			t.Fatal(err)
		}
		ids[i] = task.ID
	}

	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(2)
		go func(id string) {
			defer wg.Done()
			if _, err := st.MoveTask(ctx, id, domain.MainBacklog); err != nil {
				t.Errorf("move: %v", err)
			}
		}(ids[i])
		go func() {
			defer wg.Done()
			if _, err := st.AddTask(ctx, &domain.Task{Title: "add"}, false); err != nil {
				t.Errorf("add: %v", err)
			}
		}()
	}
	wg.Wait()

	main := domain.MainBacklog
	tasks, err := st.ListTasks(ctx, TaskFilter{Backlog: &main})
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 2*n {
		t.Fatalf("shared backlog has %d tasks, want %d", len(tasks), 2*n)
	}
	seen := map[float64]bool{}
	for _, task := range tasks {
		if seen[task.Position] {
			t.Fatalf("duplicate position %v after concurrent moves", task.Position)
		}
		seen[task.Position] = true
	}
}

// A crash leaves pass rows open (ended = 0). CleanupOrphanPasses must close
// them as setup-failures — and only them, exactly once, so a second engine
// start doesn't rewrite history.
func TestCleanupOrphanPassesClosesCrashLeftovers(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	p, err := st.StartPass(ctx, "main")
	if err != nil {
		t.Fatal(err)
	}
	n, err := st.CleanupOrphanPasses(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("cleaned %d passes, want 1", n)
	}
	got, err := st.GetPass(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != domain.PassFailed {
		t.Fatalf("orphan pass state = %q, want %q", got.State, domain.PassFailed)
	}
	if got.Ended.IsZero() {
		t.Fatal("orphan pass still has no end time after cleanup")
	}
	// Idempotent: everything is closed now, a second sweep touches nothing.
	n, err = st.CleanupOrphanPasses(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("second cleanup touched %d passes, want 0", n)
	}
}

// A corrupted hal.db (random bytes where SQLite expects its header) must
// fail Open with a clean error — never a panic — so the CLI exits 2 with a
// message instead of a stack trace.
func TestOpenCorruptedDBFailsCleanly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hal.db")
	garbage := bytes.Repeat([]byte{0xDE, 0xAD, 0xBE, 0xEF}, 1024)
	if err := os.WriteFile(path, garbage, 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path)
	if err == nil {
		s.Close()
		t.Fatal("Open succeeded on a garbage file; want a clean error")
	}
}

// Completing a task twice (retried finalization) must record exactly one
// completion row.
func TestCompleteTaskIsIdempotent(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	task, err := st.AddTask(ctx, &domain.Task{Title: "one and done"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CompleteTask(ctx, task.ID, "main", "did it"); err != nil {
		t.Fatal(err)
	}
	if err := st.CompleteTask(ctx, task.ID, "main", "did it again"); err != ErrNotFound {
		t.Fatalf("second complete: err=%v, want ErrNotFound", err)
	}
	comps, err := st.ListCompletions(ctx, 10)
	if err != nil || len(comps) != 1 {
		t.Fatalf("completions=%d err=%v, want exactly 1", len(comps), err)
	}
}
