// Package workflow implements the interactive human-gated workflow engine:
// a pure state machine (this file) and the Manager that drives one live
// conversation per workflow through the fixed stages — five for a task
// workflow, the single validate stage for a validation run.
package workflow

import (
	"fmt"

	"github.com/PurplePotassium/cosmic-agent-tools/hal/internal/domain"
)

// SettleTurn maps a finished turn onto the workflow status. Inputs: how the
// turn process ended, the agent's status-file phase ("" = file missing or
// stale — the safe default hands control to the user), and whether the
// stage's artifact actually exists on disk (the belt-and-suspenders check
// behind a "ready" claim).
func SettleTurn(turnState, phase string, artifactExists bool) domain.WorkflowStatus {
	switch turnState {
	case domain.TurnStateInterrupted:
		// An interject/halt: the queued user message (or the operator's next
		// one) resumes the session.
		return domain.WorkflowAwaitingUser
	case domain.TurnStateTimeout:
		return domain.WorkflowError
	case domain.TurnStateFailed:
		return domain.WorkflowError
	case domain.TurnStateDone:
		switch phase {
		case domain.PhaseReady:
			if artifactExists {
				return domain.WorkflowAwaitingApproval
			}
			// Claimed ready without the artifact — the manager sends one
			// corrective turn; until then the human holds the ball.
			return domain.WorkflowAwaitingUser
		case domain.PhaseBlocked:
			return domain.WorkflowBlocked
		default:
			// asking, working, or no report at all.
			return domain.WorkflowAwaitingUser
		}
	default:
		return domain.WorkflowError
	}
}

// Operator actions validated by CanAct.
const (
	ActMessage   = "message"   // send a chat turn
	ActInterrupt = "interrupt" // kill the in-flight turn
	ActApprove   = "approve"   // approve the current stage's artifact
	ActReject    = "reject"    // request changes (feedback turn)
	ActSkip      = "skip"      // skip the current stage (stub artifact)
	ActAbandon   = "abandon"   // give up the workflow
)

// CanAct validates an operator action against the workflow's live state.
// artifactExists is consulted only for approve: per the design, the approve
// gate is artifact existence — a forgotten ready signal must never wedge a
// workflow. It returns a user-presentable error.
func CanAct(wf domain.Workflow, action string, artifactExists bool) error {
	if wf.Status.Terminal() {
		return fmt.Errorf("workflow %s is %s", wf.ID, wf.Status)
	}
	running := wf.Status == domain.WorkflowTurnRunning
	switch action {
	case ActMessage:
		if running {
			return fmt.Errorf("a turn is in flight — interrupt it or wait")
		}
		return nil
	case ActInterrupt:
		if !running {
			return fmt.Errorf("no turn is in flight")
		}
		return nil
	case ActApprove:
		if running {
			return fmt.Errorf("a turn is in flight — interrupt it or wait")
		}
		if !artifactExists {
			return fmt.Errorf("the %s artifact does not exist yet", wf.Stage)
		}
		return nil
	case ActReject:
		if running {
			return fmt.Errorf("a turn is in flight — interrupt it or wait")
		}
		return nil
	case ActSkip:
		if running {
			return fmt.Errorf("a turn is in flight — interrupt it or wait")
		}
		return nil
	case ActAbandon:
		return nil // always allowed on a live workflow; kills any turn
	default:
		return fmt.Errorf("unknown action %q", action)
	}
}

// Advance returns the workflow's state after approving/skipping the current
// stage: the next task-ladder stage, or completed after implement (task
// workflows) / validate (validation runs, whose only stage it is).
func Advance(kind string, current domain.WorkflowStage) (next domain.WorkflowStage, completed bool) {
	if kind == domain.KindValidation {
		return current, true
	}
	if n, ok := domain.NextTaskStage(current); ok {
		return n, false
	}
	return current, true
}
