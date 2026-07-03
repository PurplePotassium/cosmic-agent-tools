package store

import (
	"context"
	"path/filepath"
	"sync"
	"testing"

	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/domain"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "workshop.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

// The stored schema_version must track the binary that last migrated the
// schema, or the refuse-newer-workshop guard compares against the version
// that first CREATED the database and can never fire.
func TestSchemaVersionAdvancesOnMigrate(t *testing.T) {
	st := openTestStore(t)
	// Regress the stored version to simulate a database created by an
	// older workshop, then re-run migrate (what Open does on upgrade).
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
