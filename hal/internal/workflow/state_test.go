package workflow

import (
	"testing"

	"github.com/PurplePotassium/cosmic-agent-tools/hal/internal/domain"
)

func TestSettleTurn(t *testing.T) {
	cases := []struct {
		turnState, phase string
		artifact         bool
		want             domain.WorkflowStatus
	}{
		// The happy ladder.
		{domain.TurnStateDone, domain.PhaseAsking, false, domain.WorkflowAwaitingUser},
		{domain.TurnStateDone, domain.PhaseWorking, false, domain.WorkflowAwaitingUser},
		{domain.TurnStateDone, domain.PhaseReady, true, domain.WorkflowAwaitingApproval},
		// Ready claimed but no artifact: safe default, never approval.
		{domain.TurnStateDone, domain.PhaseReady, false, domain.WorkflowAwaitingUser},
		// The implement mismatch hard-stop.
		{domain.TurnStateDone, domain.PhaseBlocked, false, domain.WorkflowBlocked},
		// No status report at all: hand control to the user.
		{domain.TurnStateDone, "", true, domain.WorkflowAwaitingUser},
		// Process endings.
		{domain.TurnStateInterrupted, "", false, domain.WorkflowAwaitingUser},
		{domain.TurnStateTimeout, "", false, domain.WorkflowError},
		{domain.TurnStateFailed, "", false, domain.WorkflowError},
		// A stale "ready" from a previous turn must not mask a failure.
		{domain.TurnStateFailed, domain.PhaseReady, true, domain.WorkflowError},
		{"garbage", "", false, domain.WorkflowError},
	}
	for _, c := range cases {
		if got := SettleTurn(c.turnState, c.phase, c.artifact); got != c.want {
			t.Errorf("SettleTurn(%q, %q, %v) = %s, want %s", c.turnState, c.phase, c.artifact, got, c.want)
		}
	}
}

func TestCanAct(t *testing.T) {
	live := func(status domain.WorkflowStatus) domain.Workflow {
		return domain.Workflow{ID: "wf", Stage: domain.StageDesign, Status: status}
	}
	// Terminal workflows accept nothing.
	for _, status := range []domain.WorkflowStatus{domain.WorkflowCompleted, domain.WorkflowAbandoned} {
		for _, act := range []string{ActMessage, ActInterrupt, ActApprove, ActReject, ActSkip, ActAbandon} {
			if err := CanAct(live(status), act, true); err == nil {
				t.Errorf("%s on %s workflow must be refused", act, status)
			}
		}
	}
	// A running turn blocks everything except interrupt and abandon.
	running := live(domain.WorkflowTurnRunning)
	for _, act := range []string{ActMessage, ActApprove, ActReject, ActSkip} {
		if err := CanAct(running, act, true); err == nil {
			t.Errorf("%s must be refused while a turn runs", act)
		}
	}
	if err := CanAct(running, ActInterrupt, false); err != nil {
		t.Errorf("interrupt while running: %v", err)
	}
	if err := CanAct(running, ActAbandon, false); err != nil {
		t.Errorf("abandon while running: %v", err)
	}
	// Idle: message/reject/skip fine; interrupt refused; approve gated on
	// the artifact existing — regardless of a forgotten ready signal.
	idle := live(domain.WorkflowAwaitingUser)
	for _, act := range []string{ActMessage, ActReject, ActSkip} {
		if err := CanAct(idle, act, false); err != nil {
			t.Errorf("%s while idle: %v", act, err)
		}
	}
	if err := CanAct(idle, ActInterrupt, false); err == nil {
		t.Error("interrupt with no turn in flight must be refused")
	}
	if err := CanAct(idle, ActApprove, false); err == nil {
		t.Error("approve without an artifact must be refused")
	}
	if err := CanAct(idle, ActApprove, true); err != nil {
		t.Errorf("approve with artifact: %v", err)
	}
	// Error state still accepts a retry message.
	if err := CanAct(live(domain.WorkflowError), ActMessage, false); err != nil {
		t.Errorf("message on errored workflow: %v", err)
	}
}

func TestAdvance(t *testing.T) {
	next, completed := Advance(domain.StageRefine)
	if completed || next != domain.StageResearch {
		t.Fatalf("refine -> %s, %v", next, completed)
	}
	next, completed = Advance(domain.StageImplement)
	if completed || next != domain.StageValidate {
		t.Fatalf("implement -> %s, %v", next, completed)
	}
	if _, completed = Advance(domain.StageValidate); !completed {
		t.Fatal("validate approval must complete the workflow")
	}
}
