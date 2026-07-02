package store

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/gw1108/cosmic-agent-tools/workshop/internal/domain"
)

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

func TestClaimTwoTierPrecedence(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	mainTask := addTask(t, s, "", "code", "main-task", false)
	ownTask := addTask(t, s, "code", "art", "own-task", false) // wrong type on purpose

	// Own backlog wins even though its task type isn't in the filter.
	got, err := s.Claim(ctx, "code", true, []string{"code", "tests"}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.ID != ownTask.ID {
		t.Fatalf("claimed %+v, want own-task", got)
	}
	if got.Status != domain.TaskClaimed || got.ClaimedBy != "code" {
		t.Fatalf("claim fields wrong: %+v", got)
	}

	// Next claim falls through to main.
	got, err = s.Claim(ctx, "code", true, []string{"code", "tests"}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.ID != mainTask.ID {
		t.Fatalf("claimed %+v, want main-task", got)
	}

	// Nothing left.
	got, err = s.Claim(ctx, "code", true, nil, 3)
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatalf("claimed %+v, want nil", got)
	}
}

func TestClaimHonorsTypeFilterAndDrainMain(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	addTask(t, s, "", "art", "art-task", false)

	// Type filter excludes it.
	got, err := s.Claim(ctx, "code", true, []string{"code"}, 1)
	if err != nil || got != nil {
		t.Fatalf("got %+v, %v; want nil, nil", got, err)
	}
	// drainMain=false never touches main.
	got, err = s.Claim(ctx, "art", false, nil, 1)
	if err != nil || got != nil {
		t.Fatalf("got %+v, %v; want nil, nil", got, err)
	}
	// Matching pipeline claims it.
	got, err = s.Claim(ctx, "art", true, []string{"art", "audio"}, 1)
	if err != nil || got == nil {
		t.Fatalf("got %+v, %v; want the art task", got, err)
	}
}

func TestClaimRespectsNotBefore(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	task := addTask(t, s, "", "", "backoff-task", false)
	if _, err := s.Claim(ctx, "main", true, nil, 1); err != nil {
		t.Fatal(err)
	}
	// Fail it with a long backoff: released but not claimable yet.
	if _, err := s.FailTask(ctx, task.ID, time.Hour, 3); err != nil {
		t.Fatal(err)
	}
	got, err := s.Claim(ctx, "main", true, nil, 2)
	if err != nil || got != nil {
		t.Fatalf("claimed backoff task early: %+v, %v", got, err)
	}
}

func TestFailTaskStuckAfterMaxAttempts(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	task := addTask(t, s, "", "", "flaky", false)
	for i := 0; i < 3; i++ {
		got, err := s.FailTask(ctx, task.ID, 0, 3)
		if err != nil {
			t.Fatal(err)
		}
		wantStatus := domain.TaskOpen
		if i == 2 {
			wantStatus = domain.TaskStuck
		}
		if got.Status != wantStatus || got.Attempts != i+1 {
			t.Fatalf("attempt %d: %+v", i+1, got)
		}
	}
}

// TestClaimRaced launches many goroutines racing to claim; every task must be
// claimed exactly once.
func TestClaimRaced(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	const nTasks, nWorkers = 20, 16
	for i := 0; i < nTasks; i++ {
		addTask(t, s, "", "", "task", false)
	}
	var mu sync.Mutex
	claimed := map[string]string{}
	var wg sync.WaitGroup
	for w := 0; w < nWorkers; w++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for {
				task, err := s.Claim(ctx, "main", true, nil, int64(worker))
				if err != nil {
					t.Error(err)
					return
				}
				if task == nil {
					return
				}
				mu.Lock()
				if prev, dup := claimed[task.ID]; dup {
					t.Errorf("task %s claimed twice (by %s and worker %d)", task.ID, prev, worker)
				}
				claimed[task.ID] = "worker"
				mu.Unlock()
			}
		}(w)
	}
	wg.Wait()
	if len(claimed) != nTasks {
		t.Fatalf("claimed %d of %d tasks", len(claimed), nTasks)
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

func TestIntegrationRoundtrip(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	li, err := s.GetIntegration(ctx, "code")
	if err != nil || li.Pipeline != "code" || li.Blocked {
		t.Fatalf("zero value: %+v, %v", li, err)
	}
	li.LastSeenTip = "deadbeef"
	li.Blocked = true
	li.BlockedBy = "gate"
	li.ProvenCulprit = true
	if err := s.PutIntegration(ctx, li); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetIntegration(ctx, "code")
	if err != nil {
		t.Fatal(err)
	}
	if *got != *li {
		t.Fatalf("roundtrip: %+v vs %+v", got, li)
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
