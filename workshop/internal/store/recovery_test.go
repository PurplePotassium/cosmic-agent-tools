package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/domain"
)

// A crash between claim and settle used to strand the task forever: startup
// recovery closed the orphan PASS row but nothing released the claim, and
// Claim only considers open tasks (while proposal dedupe swallows re-adds of
// the same title). ReleaseOrphanClaims is the counterpart cleanup.
func TestReleaseOrphanClaimsRecoversCrashedClaim(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "tasks.db")

	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddTask(ctx, &domain.Task{Backlog: domain.MainBacklog, Title: "add plasma rifle"}, false); err != nil {
		t.Fatal(err)
	}
	claimed, err := st.Claim(ctx, "code", true, nil, 0)
	if err != nil || claimed == nil {
		t.Fatalf("first claim: task=%v err=%v", claimed, err)
	}
	// Crash: close without settling.
	st.Close()

	st2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	if _, err := st2.CleanupOrphanPasses(ctx); err != nil {
		t.Fatal(err)
	}
	n, err := st2.ReleaseOrphanClaims(ctx)
	if err != nil || n != 1 {
		t.Fatalf("ReleaseOrphanClaims: n=%d err=%v", n, err)
	}
	again, err := st2.Claim(ctx, "code", true, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if again == nil {
		t.Fatal("task must be claimable again after crash recovery")
	}
	if again.Attempts != 0 {
		t.Fatalf("orphan release must be penalty-free, got attempts=%d", again.Attempts)
	}
}

// FailTask must not resurrect a task that was finalized concurrently (the
// operator marking it done from the dashboard while a pass had it claimed):
// without the status guard, the pass's later timeout flipped the done task
// back to open and it was re-executed.
func TestFailTaskDoesNotResurrectDoneTask(t *testing.T) {
	ctx := context.Background()
	st, err := Open(filepath.Join(t.TempDir(), "tasks.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	task, err := st.AddTask(ctx, &domain.Task{Backlog: domain.MainBacklog, Title: "fix save crash"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Claim(ctx, "code", true, nil, 0); err != nil {
		t.Fatal(err)
	}
	if err := st.CompleteTask(ctx, task.ID, "operator", "done by hand"); err != nil {
		t.Fatal(err)
	}

	if _, err := st.FailTask(ctx, task.ID, time.Minute, 3); err != ErrNotFound {
		t.Fatalf("FailTask on a done task: err=%v, want ErrNotFound", err)
	}
	got, err := st.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != domain.TaskDone || got.Attempts != 0 {
		t.Fatalf("done task mutated by FailTask: %+v", got)
	}
}

// The claim runs before StartPass mints the pass id, so claim_pass is
// back-filled; it must land on the claimed row (and only while claimed).
func TestSetTaskClaimPass(t *testing.T) {
	ctx := context.Background()
	st, err := Open(filepath.Join(t.TempDir(), "tasks.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	task, err := st.AddTask(ctx, &domain.Task{Backlog: domain.MainBacklog, Title: "record provenance"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Claim(ctx, "code", true, nil, 0); err != nil {
		t.Fatal(err)
	}
	if err := st.SetTaskClaimPass(ctx, task.ID, 42); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ClaimPass != 42 {
		t.Fatalf("claim_pass = %d, want 42", got.ClaimPass)
	}
}
