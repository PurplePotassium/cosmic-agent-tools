package engine

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// filesWithConflictMarkers is the engine's trunk-safety guard: after a
// resolution pass it scans the originally-conflicted files and refuses to land
// the merge if any leftover <<<<<<< / >>>>>>> marker survives. These cases pin
// the exact scan contract runConflictPass depends on.
func TestFilesWithConflictMarkers(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// A properly resolved file: both sides merged, no markers.
	write("resolved.go", "package p\n\nfunc F() int { return 3 }\n")
	// An abandoned resolution still carrying git's markers.
	write("botched.go", "package p\n\n<<<<<<< HEAD\nvar A = 1\n=======\nvar A = 2\n>>>>>>> trunk\n")
	// A file the agent never touched but which legitimately contains an
	// equals-run (e.g. a markdown rule / separator). The guard keys off the
	// distinctive <<<<<<< and >>>>>>> lines, NOT the bare ======= separator,
	// so this must stay clean.
	write("doc.md", "Title\n=======\n\nbody\n")
	// A marker-laden file that was NOT among the conflicted set — it must be
	// ignored, because the guard only scans the files it was handed.
	write("unrelated.go", "<<<<<<< HEAD\njunk\n>>>>>>> trunk\n")

	conflicted := []string{"resolved.go", "botched.go", "doc.md", "gone.go"}
	bad := filesWithConflictMarkers(dir, conflicted)

	if want := []string{"botched.go"}; !slices.Equal(bad, want) {
		t.Fatalf("filesWithConflictMarkers = %v, want %v", bad, want)
	}
}

// A resolution that removed every marker (or had no markers to begin with)
// reports clean, so the pass may proceed to gate + commit.
func TestFilesWithConflictMarkersAllClean(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"a.go", "b.go"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("package p\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if bad := filesWithConflictMarkers(dir, []string{"a.go", "b.go"}); len(bad) != 0 {
		t.Fatalf("clean files flagged as bad: %v", bad)
	}
}

// tailLines keeps only the last n lines (after trimming surrounding
// whitespace) — it bounds the gate.red event payload the dashboard renders.
func TestTailLines(t *testing.T) {
	// Fewer lines than the cap: everything survives, trimmed.
	if got := tailLines("\nalpha\nbeta\n\n", 5); got != "alpha\nbeta" {
		t.Fatalf("tailLines(short) = %q, want %q", got, "alpha\nbeta")
	}
	// More lines than the cap: only the last n remain.
	if got := tailLines("l1\nl2\nl3\nl4\nl5", 2); got != "l4\nl5" {
		t.Fatalf("tailLines(long) = %q, want %q", got, "l4\nl5")
	}
	// Exactly the cap: unchanged.
	if got := tailLines("one\ntwo\nthree", 3); got != "one\ntwo\nthree" {
		t.Fatalf("tailLines(exact) = %q, want %q", got, "one\ntwo\nthree")
	}
}
