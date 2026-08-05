package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/PurplePotassium/cosmic-agent-tools/hal/internal/domain"
)

func newTestWorkflow(t *testing.T, s *Store, title string) domain.Workflow {
	t.Helper()
	wf := domain.Workflow{
		ID:          NewWorkflowID(title, time.Now()),
		Title:       title,
		Brief:       "the ask",
		Stage:       domain.StageRefine,
		Status:      domain.WorkflowAwaitingUser,
		AutoApprove: true,
		Created:     time.Now(),
	}
	if err := s.CreateWorkflow(context.Background(), wf); err != nil {
		t.Fatal(err)
	}
	return wf
}

func TestNewWorkflowID(t *testing.T) {
	now := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
	id := NewWorkflowID("Fix the Boss HP bar!", now)
	if !strings.HasPrefix(id, "2026-08-03-fix-the-boss-hp-bar-") {
		t.Fatalf("id: %q", id)
	}
	if id == NewWorkflowID("Fix the Boss HP bar!", now) {
		t.Fatal("ids must not collide")
	}
	if got := NewWorkflowID("!!!", now); !strings.HasPrefix(got, "2026-08-03-workflow-") {
		t.Fatalf("empty slug fallback: %q", got)
	}
	long := NewWorkflowID(strings.Repeat("very long title ", 20), now)
	if len(long) > 64 {
		t.Fatalf("slug not capped: %q (%d)", long, len(long))
	}
}

func TestWorkflowCRUDAndStageLadder(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	wf := newTestWorkflow(t, s, "test workflow")

	got, err := s.GetWorkflow(ctx, wf.ID)
	if err != nil || got.Title != "test workflow" || got.Stage != domain.StageRefine || !got.AutoApprove {
		t.Fatalf("get: %+v, %v", got, err)
	}

	// All six stage rows seeded, refine active, rest pending.
	stages, err := s.WorkflowStages(ctx, wf.ID)
	if err != nil || len(stages) != len(domain.StageOrder) {
		t.Fatalf("stages: %d, %v", len(stages), err)
	}
	for i, st := range stages {
		if st.Stage != domain.StageOrder[i] {
			t.Fatalf("stage order: %+v", stages)
		}
		want := domain.StagePending
		if i == 0 {
			want = domain.StageActive
		}
		if st.Status != want {
			t.Fatalf("stage %s status %q, want %q", st.Stage, st.Status, want)
		}
	}

	// Approve refine, advance to research.
	if err := s.SetStageArtifact(ctx, wf.ID, domain.StageRefine, ".hal/workflows/x/01-question.md"); err != nil {
		t.Fatal(err)
	}
	if err := s.DecideStage(ctx, wf.ID, domain.StageRefine, domain.StageApproved, "lgtm"); err != nil {
		t.Fatal(err)
	}
	// A second decision on a settled stage is refused.
	if err := s.DecideStage(ctx, wf.ID, domain.StageRefine, domain.StageApproved, ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("double decide: %v", err)
	}
	if err := s.AdvanceWorkflowStage(ctx, wf.ID, domain.StageResearch); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetWorkflow(ctx, wf.ID)
	if got.Stage != domain.StageResearch {
		t.Fatalf("stage: %s", got.Stage)
	}
	stages, _ = s.WorkflowStages(ctx, wf.ID)
	if stages[0].Status != domain.StageApproved || stages[0].DecisionNote != "lgtm" || stages[0].Decided.IsZero() {
		t.Fatalf("refine after approval: %+v", stages[0])
	}
	if stages[1].Status != domain.StageActive || stages[1].Started.IsZero() {
		t.Fatalf("research after advance: %+v", stages[1])
	}

	// Listing filters terminal workflows by default.
	done := newTestWorkflow(t, s, "finished one")
	if err := s.SetWorkflowStatus(ctx, done.ID, domain.WorkflowCompleted, ""); err != nil {
		t.Fatal(err)
	}
	active, err := s.ListWorkflows(ctx, false)
	if err != nil || len(active) != 1 || active[0].ID != wf.ID {
		t.Fatalf("active list: %+v, %v", active, err)
	}
	all, err := s.ListWorkflows(ctx, true)
	if err != nil || len(all) != 2 {
		t.Fatalf("all list: %+v, %v", all, err)
	}

	if _, err := s.GetWorkflow(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing workflow: %v", err)
	}
	if err := s.SetWorkflowStatus(ctx, "nope", domain.WorkflowError, "x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("update missing: %v", err)
	}
}

func TestWorkflowTurnsAndMessages(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	wf := newTestWorkflow(t, s, "turns")

	// Turn N is per-workflow and monotonic.
	id1, err := s.StartTurn(ctx, domain.WorkflowTurn{
		WorkflowID: wf.ID, Stage: domain.StageRefine, SessionID: "sess-1",
		LogPath: "logs/turn-000001.log", Started: time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.FinishTurn(ctx, id1, domain.TurnStateDone, 0, "", "sess-1", 0.02, 3); err != nil {
		t.Fatal(err)
	}
	id2, err := s.StartTurn(ctx, domain.WorkflowTurn{
		WorkflowID: wf.ID, Stage: domain.StageRefine, ResumedFrom: "sess-1", Started: time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	turns, err := s.ListTurns(ctx, wf.ID)
	if err != nil || len(turns) != 2 {
		t.Fatalf("turns: %+v, %v", turns, err)
	}
	if turns[0].N != 1 || turns[1].N != 2 || turns[1].ResumedFrom != "sess-1" {
		t.Fatalf("turn numbering: %+v", turns)
	}
	if turns[0].State != domain.TurnStateDone || turns[0].CostUSD != 0.02 || turns[0].NumTurns != 3 {
		t.Fatalf("settled turn: %+v", turns[0])
	}
	if got, err := s.GetTurn(ctx, id2); err != nil || got.State != domain.TurnStateRunning {
		t.Fatalf("get turn: %+v, %v", got, err)
	}

	// Messages replay in insertion order, with after-id paging.
	for i, m := range []domain.WorkflowMessage{
		{Role: domain.RoleSystem, Kind: domain.MsgStageOpen, Content: "refine started"},
		{Role: domain.RoleUser, Kind: domain.MsgChat, Content: "make the boss harder"},
		{Role: domain.RoleAssistant, Kind: domain.MsgChat, Content: "how much harder?"},
	} {
		m.WorkflowID = wf.ID
		m.Stage = domain.StageRefine
		m.Created = time.Now()
		if _, err := s.AppendMessage(ctx, m); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	msgs, err := s.ListMessages(ctx, wf.ID, 0, 0)
	if err != nil || len(msgs) != 3 {
		t.Fatalf("messages: %+v, %v", msgs, err)
	}
	if msgs[1].Role != domain.RoleUser || msgs[2].Content != "how much harder?" {
		t.Fatalf("order: %+v", msgs)
	}
	tail, err := s.ListMessages(ctx, wf.ID, msgs[1].ID, 0)
	if err != nil || len(tail) != 1 || tail[0].ID != msgs[2].ID {
		t.Fatalf("paging: %+v, %v", tail, err)
	}
}

// A crashed engine leaves running turns; startup settles them interrupted
// and drops their workflows to awaiting-user for a clean resume.
func TestCleanupOrphanTurns(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	wf := newTestWorkflow(t, s, "crashed")
	if _, err := s.StartTurn(ctx, domain.WorkflowTurn{
		WorkflowID: wf.ID, Stage: domain.StageRefine, SessionID: "sess-9", Started: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetWorkflowStatus(ctx, wf.ID, domain.WorkflowTurnRunning, ""); err != nil {
		t.Fatal(err)
	}
	calm := newTestWorkflow(t, s, "calm") // no open turn; must be untouched

	ids, err := s.CleanupOrphanTurns(ctx)
	if err != nil || len(ids) != 1 || ids[0] != wf.ID {
		t.Fatalf("cleanup: %v, %v", ids, err)
	}
	turns, _ := s.ListTurns(ctx, wf.ID)
	if turns[0].State != domain.TurnStateInterrupted || turns[0].Ended.IsZero() {
		t.Fatalf("orphan turn: %+v", turns[0])
	}
	got, _ := s.GetWorkflow(ctx, wf.ID)
	if got.Status != domain.WorkflowAwaitingUser {
		t.Fatalf("workflow status: %s", got.Status)
	}
	if got, _ := s.GetWorkflow(ctx, calm.ID); got.Status != domain.WorkflowAwaitingUser {
		t.Fatalf("calm workflow disturbed: %+v", got)
	}

	// Idempotent when nothing is orphaned.
	if ids, err := s.CleanupOrphanTurns(ctx); err != nil || len(ids) != 0 {
		t.Fatalf("second cleanup: %v, %v", ids, err)
	}
}

// Pruning trims history of finished workflows only.
func TestPruneWorkflows(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	finished := newTestWorkflow(t, s, "old")
	active := newTestWorkflow(t, s, "current")

	for _, wf := range []domain.Workflow{finished, active} {
		for i := 0; i < 5; i++ {
			id, err := s.StartTurn(ctx, domain.WorkflowTurn{
				WorkflowID: wf.ID, Stage: domain.StageRefine, Started: time.Now(),
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := s.FinishTurn(ctx, id, domain.TurnStateDone, 0, "", "", 0, 1); err != nil {
				t.Fatal(err)
			}
			if _, err := s.AppendMessage(ctx, domain.WorkflowMessage{
				WorkflowID: wf.ID, Role: domain.RoleUser, Kind: domain.MsgChat,
				Content: "m", Created: time.Now(),
			}); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := s.SetWorkflowStatus(ctx, finished.ID, domain.WorkflowAbandoned, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.PruneWorkflows(ctx, 2, 2); err != nil {
		t.Fatal(err)
	}
	if turns, _ := s.ListTurns(ctx, finished.ID); len(turns) != 2 {
		t.Fatalf("finished turns kept: %d", len(turns))
	}
	if msgs, _ := s.ListMessages(ctx, finished.ID, 0, 0); len(msgs) != 2 {
		t.Fatalf("finished messages kept: %d", len(msgs))
	}
	if turns, _ := s.ListTurns(ctx, active.ID); len(turns) != 5 {
		t.Fatalf("active turns must be untouched: %d", len(turns))
	}
	if msgs, _ := s.ListMessages(ctx, active.ID, 0, 0); len(msgs) != 5 {
		t.Fatalf("active messages must be untouched: %d", len(msgs))
	}
}
