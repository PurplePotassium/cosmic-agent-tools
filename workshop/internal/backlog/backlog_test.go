package backlog

import (
	"context"
	"path/filepath"
	"sync"
	"testing"

	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/domain"
	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/store"
)

func newTestService(t *testing.T) *Service {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "workshop.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return New(st)
}

// Two workers finishing passes at once can propose the same title. The ingest
// transaction must admit it exactly once: a snapshot-then-insert without the
// transaction let both slip past a stale dedupe snapshot and duplicate the task.
func TestConcurrentIngestDedupesSameTitle(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	proposals := []domain.Proposal{{Title: "Add a retry budget"}}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, _, err := svc.Ingest(ctx, "worker", proposals, nil, 10); err != nil {
				t.Errorf("ingest: %v", err)
			}
		}()
	}
	wg.Wait()

	tasks, err := svc.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := normTitle("Add a retry budget")
	n := 0
	for _, task := range tasks {
		if normTitle(task.Title) == want {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("title admitted %d times, want exactly 1", n)
	}
}

// A single ingest still dedupes within its own batch and against existing
// tasks, and honors maxAccept — reporting what the cap cost as dropped.
// Dups (of existing tasks or within the batch) are NOT dropped ideas: they
// are already queued, so only genuinely lost proposals come back.
func TestIngestDedupeAndCap(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	// Pre-existing task the ingest must not duplicate.
	if _, _, err := svc.Ingest(ctx, "seed", []domain.Proposal{{Title: "already here"}}, nil, 10); err != nil {
		t.Fatal(err)
	}

	added, dropped, err := svc.Ingest(ctx, "worker", []domain.Proposal{
		{Title: "already here"},        // dup of existing -> skipped, not dropped
		{Title: "brand new one"},       // accepted
		{Title: "  brand new one  "},   // normalized dup within batch -> skipped, not dropped
		{Title: "second new idea"},     // over the cap -> dropped
		{Title: "second new idea"},     // dup of a DROPPED title -> reported once, not twice
		{Title: "third new idea"},      // over the cap -> dropped
	}, nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(added) != 1 || added[0].Title != "brand new one" {
		t.Fatalf("added = %v, want exactly [brand new one]", titles(added))
	}
	if len(dropped) != 2 || dropped[0].Title != "second new idea" || dropped[1].Title != "third new idea" {
		t.Fatalf("dropped = %+v, want the two capped-out unique titles", dropped)
	}
}

func titles(ts []*domain.Task) []string {
	out := make([]string, len(ts))
	for i, t := range ts {
		out[i] = t.Title
	}
	return out
}
