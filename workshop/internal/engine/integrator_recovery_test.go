package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/store"
)

// Conflict tasks must pin the PRE-ROUND trunk, never mid-round trunk: by the
// time a later lane conflicts, trunk already carries earlier lanes' unvetted
// merges, which the gate may reset away — a pin on those would resurrect
// bisected-out commits through the resolution, or dangle entirely.
func TestConflictTaskPinsPreRoundTrunk(t *testing.T) {
	r := newQueueRig(t, []string{"first", "conflicted"}, "", true)
	ctx := context.Background()

	// Trunk edits README; the "conflicted" lane edits it too; "first" is a
	// clean merge that advances trunk before the conflict is seen.
	if err := os.WriteFile(filepath.Join(r.repo, "README.md"), []byte("trunk side\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r.git(r.repo, "commit", "-q", "-am", "trunk edit")
	r.laneCommit(0, "first.txt", "x", "first work")
	r.laneCommit(1, "README.md", "lane side\n", "conflicting work")

	preRound := r.git(r.repo, "rev-parse", "HEAD")
	if _, err := r.ig.RunRound(ctx); err != nil {
		t.Fatal(err)
	}

	li, err := r.st.GetIntegration(ctx, "conflicted")
	if err != nil || li.ConflictTaskID == "" {
		t.Fatalf("no conflict task: %+v err=%v", li, err)
	}
	task, err := r.st.GetTask(ctx, li.ConflictTaskID)
	if err != nil {
		t.Fatal(err)
	}
	if got := task.Meta[MetaTrunkTip]; got != preRound {
		t.Fatalf("conflict task pinned %s, want pre-round trunk %s", got, preRound)
	}
}

// Every terminal path of a resolution lane must retire the parked state.
// Before the fix, a resolution branch re-conflicting with a moved trunk left
// ConflictTaskID pointing at the done task: the lane could never park, the
// retry budget reset each time, and conflict tasks were minted forever.
func TestResolutionReconflictIsBoundedByBudget(t *testing.T) {
	r := newQueueRig(t, []string{"solo"}, "", true)
	ctx := context.Background()

	countConflictTasks := func() int {
		t.Helper()
		tasks, err := r.st.ListTasks(ctx, store.TaskFilter{})
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for _, task := range tasks {
			if task.Type == "merge-conflict" {
				n++
			}
		}
		return n
	}
	trunkEdit := func(content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(r.repo, "README.md"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		r.git(r.repo, "commit", "-q", "-am", "trunk edit")
	}

	trunkEdit("trunk side 1\n")
	r.laneCommit(0, "README.md", "lane side\n", "lane edit")

	// Cycle 1: conflict -> task 1 -> resolved.
	if _, err := r.ig.RunRound(ctx); err != nil {
		t.Fatal(err)
	}
	if got := countConflictTasks(); got != 1 {
		t.Fatalf("tasks after first conflict: %d", got)
	}
	worker := r.conflictWorker(t, "resolve")
	if res, err := worker.RunPass(ctx); err != nil || res != PassRan {
		t.Fatalf("resolution 1: res=%v err=%v", res, err)
	}

	// Trunk moves before the resolution lands: it re-conflicts. Attempt 2
	// (the budget) mints exactly one more task.
	trunkEdit("trunk side 2\n")
	if _, err := r.ig.RunRound(ctx); err != nil {
		t.Fatal(err)
	}
	if got := countConflictTasks(); got != 2 {
		t.Fatalf("tasks after re-conflict: %d, want 2", got)
	}
	if res, err := worker.RunPass(ctx); err != nil || res != PassRan {
		t.Fatalf("resolution 2: res=%v err=%v", res, err)
	}

	// Trunk moves again: the budget (2) is spent, so the lane must PARK —
	// no third task, resolution branch retired, ConflictTaskID cleared.
	trunkEdit("trunk side 3\n")
	if _, err := r.ig.RunRound(ctx); err != nil {
		t.Fatal(err)
	}
	li, _ := r.st.GetIntegration(ctx, "solo")
	if li.ConflictTaskID != "" {
		t.Fatalf("lane not parked after budget spent: %+v", li)
	}
	if got := countConflictTasks(); got != 2 {
		t.Fatalf("budget overrun: %d tasks, want 2", got)
	}

	// Parked stays parked: further rounds mint nothing.
	for i := 0; i < 3; i++ {
		if _, err := r.ig.RunRound(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if got := countConflictTasks(); got != 2 {
		t.Fatalf("parked lane minted tasks: %d, want 2", got)
	}

	// The lane tip advancing opens a FRESH cycle with a fresh budget.
	r.laneCommit(0, "more.txt", "y", "lane advances")
	if _, err := r.ig.RunRound(ctx); err != nil {
		t.Fatal(err)
	}
	if got := countConflictTasks(); got != 3 {
		t.Fatalf("advanced tip should mint a fresh task: %d, want 3", got)
	}
}

// A round that died between its merges and the gate verdict leaves unvetted
// merge commits on trunk. The next round must strip them (they were never
// gated) and forget the lanes' seen-tips so the work re-lands THROUGH the
// gate instead of being silently blessed.
func TestReconcileStripsUnvettedCrashMerges(t *testing.T) {
	r := newQueueRig(t, []string{"one", "two"}, "", false)
	ctx := context.Background()

	// Round 1 establishes a real green baseline.
	r.laneCommit(0, "one.txt", "a", "one work")
	r.laneCommit(1, "two.txt", "b", "two work")
	if landed, err := r.ig.RunRound(ctx); err != nil || landed != 2 {
		t.Fatalf("baseline round: landed=%d err=%v", landed, err)
	}
	green := r.git(r.repo, "rev-parse", GreenRef)

	// Simulate the crash: new lane work is merged onto trunk by hand (as a
	// dying phase-1 loop would leave it), never gated, green ref stale —
	// and lane "one" was already marked landed by the dying round.
	r.laneCommit(0, "one2.txt", "aa", "one more work")
	r.laneCommit(1, "two2.txt", "bb", "two more work")
	oneTip := r.git(r.lanes[0].Dir, "rev-parse", "HEAD")
	r.git(r.repo, "merge", "--no-ff", "-m", "Merge branch 'workshop/one'", "workshop/one")
	r.git(r.repo, "merge", "--no-ff", "-m", "Merge branch 'workshop/two'", "workshop/two")
	li, _ := r.st.GetIntegration(ctx, "one")
	li.LastSeenTip = oneTip
	if err := r.st.PutIntegration(ctx, li); err != nil {
		t.Fatal(err)
	}

	// The next round strips the unvetted merges, then re-lands both lanes
	// through the gate like any normal round.
	landed, err := r.ig.RunRound(ctx)
	if err != nil || landed != 2 {
		t.Fatalf("recovery round: landed=%d err=%v", landed, err)
	}
	files := r.trunkFiles()
	if !files["one2.txt"] || !files["two2.txt"] {
		t.Fatalf("re-landed files missing: %v", files)
	}
	// Each lane's merge appears exactly once — the crash merges are gone.
	log := r.git(r.repo, "log", "--format=%s", green+"..main")
	for _, branch := range []string{"workshop/one", "workshop/two"} {
		if got := strings.Count(log, "Merge branch '"+branch+"'"); got != 1 {
			t.Fatalf("%s merged %d times since green:\n%s", branch, got, log)
		}
	}
	// And the gate blessed the result: green follows trunk again.
	if r.git(r.repo, "rev-parse", GreenRef) != r.git(r.repo, "rev-parse", "HEAD") {
		t.Fatal("green ref not advanced over the re-landed work")
	}
}

// Intent/human commits below crash merges must survive reconciliation.
func TestReconcileStopsAtForeignCommits(t *testing.T) {
	r := newQueueRig(t, []string{"one"}, "", false)
	ctx := context.Background()
	r.laneCommit(0, "one.txt", "a", "one work")
	if landed, err := r.ig.RunRound(ctx); err != nil || landed != 1 {
		t.Fatalf("baseline: landed=%d err=%v", landed, err)
	}

	// A human commit on trunk, then a crash merge on top of it.
	if err := os.WriteFile(filepath.Join(r.repo, "human.txt"), []byte("h"), 0o644); err != nil {
		t.Fatal(err)
	}
	r.git(r.repo, "add", "-A")
	r.git(r.repo, "commit", "-q", "-m", "human work on trunk")
	r.laneCommit(0, "one2.txt", "aa", "more lane work")
	r.git(r.repo, "merge", "--no-ff", "-m", "Merge branch 'workshop/one'", "workshop/one")

	landed, err := r.ig.RunRound(ctx)
	if err != nil || landed != 1 {
		t.Fatalf("recovery round: landed=%d err=%v", landed, err)
	}
	files := r.trunkFiles()
	if !files["human.txt"] || !files["one2.txt"] {
		t.Fatalf("trunk files after reconcile: %v", files)
	}
}

// Store errors during integrator bookkeeping must skip the write, not panic
// the integrator goroutine mid-merge.
func TestIntegratorBookkeepingSurvivesStoreErrors(t *testing.T) {
	r := newQueueRig(t, []string{"solo"}, "", true)
	ctx := context.Background()
	if err := r.st.Close(); err != nil {
		t.Fatal(err)
	}

	lane := pendingLane{pipeline: "solo", branch: "workshop/solo", tip: "x", laneTip: "x"}
	// Each of these dereferenced a nil row before the fix.
	r.ig.markSeen(ctx, lane)
	r.ig.land(ctx, lane)
	r.ig.handleConflict(ctx, lane, []string{"README.md"}, "deadbeef")

	if li := r.ig.integrationState(ctx, "solo"); li != nil {
		t.Fatalf("expected nil state on a closed store, got %+v", li)
	}
}
