package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/app"
)

// migratedMarkerPath resolves the migrated.json marker for a test repo's
// state dir (state is keyed by the repo path app.Open resolves).
func migratedMarkerPath(t *testing.T, repo string) string {
	t.Helper()
	a, err := app.Open(context.Background(), repo)
	if err != nil {
		t.Fatalf("app.Open: %v", err)
	}
	defer a.Close()
	return filepath.Join(a.StateDir, "migrated.json")
}

// A backlog.json that EXISTS but doesn't parse is real legacy data we failed
// to import: migrate must exit nonzero and must NOT stamp migrated.json —
// stamping it would make the retry refuse to run without --force.
func TestMigrateMalformedBacklogFailsWithoutMarker(t *testing.T) {
	repo := initTestRepo(t)
	from := t.TempDir()
	if err := os.WriteFile(filepath.Join(from, "backlog.json"), []byte("{this is not json"), 0o644); err != nil {
		t.Fatal(err)
	}

	if code := cmdMigrate([]string{"--repo", repo, "--from", from}); code == 0 {
		t.Fatalf("migrate on malformed backlog.json exited 0, want nonzero")
	}
	if _, err := os.Stat(migratedMarkerPath(t, repo)); !os.IsNotExist(err) {
		t.Fatalf("migrated.json must not be written on failure; stat err = %v", err)
	}

	// The failure must not poison a later, fixed run: a valid backlog now
	// imports without --force.
	if err := os.WriteFile(filepath.Join(from, "backlog.json"),
		[]byte(`[{"id":"1","title":"legacy task"}]`), 0o644); err != nil {
		t.Fatal(err)
	}
	if code := cmdMigrate([]string{"--repo", repo, "--from", from}); code != 0 {
		t.Fatalf("re-run after fixing the legacy file exited %d, want 0", code)
	}
	tasks := listAllTasks(t, repo)
	if len(tasks) != 1 || tasks[0].Title != "legacy task" {
		t.Fatalf("fixed backlog not imported; got %+v", tasks)
	}
}

// Genuinely absent or empty legacy files are a skip, not an error: migrate
// succeeds and stamps the marker.
func TestMigrateMissingAndEmptyFilesSkip(t *testing.T) {
	repo := initTestRepo(t)
	from := t.TempDir() // no backlog.json at all
	// completions.json exists but is empty — "not written yet", also a skip.
	if err := os.WriteFile(filepath.Join(from, "completions.json"), []byte("  \n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if code := cmdMigrate([]string{"--repo", repo, "--from", from}); code != 0 {
		t.Fatalf("migrate with missing/empty legacy files exited %d, want 0", code)
	}
	if _, err := os.Stat(migratedMarkerPath(t, repo)); err != nil {
		t.Fatalf("successful migrate must write migrated.json: %v", err)
	}
}
