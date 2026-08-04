package app

import (
	"context"
	"fmt"

	"github.com/PurplePotassium/cosmic-agent-tools/hal/internal/domain"
)

// StartAutoInquiry subscribes to the event bus and, when a workflow raises the
// operator-facing red banner — a failed turn (workflow.error: crash, timeout,
// auth) — automatically fires the self-evaluator to diagnose WHY, so a pinned
// answer lands beside the banner in the dashboard instead of the operator
// having to notice it and type the question themselves.
//
// It subscribes synchronously (events published after this returns are not
// missed) and watches in a goroutine until ctx is cancelled or the returned
// stop func is called. Wire it in for the life of the engine.
func (a *App) StartAutoInquiry(ctx context.Context) (stop func()) {
	events, cancel := a.Bus.Subscribe()
	go func() {
		defer cancel()
		for {
			select {
			case <-ctx.Done():
				return
			case ev, ok := <-events:
				if !ok {
					return
				}
				// Dispatch off the drain loop: ask can run for minutes (it
				// execs an agent), and blocking here would overflow the
				// subscriber's bounded bus buffer on turn-log traffic and
				// silently drop later critical events. Concurrency is still
				// bounded — ask itself returns ErrInquiryBusy while one runs.
				if ev.Type == "workflow.error" {
					go a.autoInquire(ctx, workflowErrorQuestion(ev))
				}
			}
		}
	}()
	return cancel
}

// autoInquire fires one best-effort self-evaluator run for a red-banner event.
// It is best-effort: a busy evaluator (ErrInquiryBusy, e.g. the operator is
// already asking something) or a missing/unprobeable agent just means no
// auto-diagnosis this time — the banner is surfaced on its own regardless.
func (a *App) autoInquire(ctx context.Context, question string) {
	if ctx.Err() != nil {
		return // shutting down — don't start a new diagnosis
	}
	_, _ = a.ask(ctx, question, domain.Bundle{}, true)
}

// workflowErrorQuestion diagnoses a failed workflow turn. The workflow id
// rides in the event's Pipeline field; the failure detail in payload.reason.
func workflowErrorQuestion(ev domain.Event) string {
	reason := "its last agent turn failed"
	if s, _ := ev.Payload["reason"].(string); s != "" {
		reason = fmt.Sprintf("its last agent turn failed with: %s", s)
	}
	return fmt.Sprintf(
		"Workflow %q just entered the error state — %s. From the recorded evidence (turn logs, archived "+
			"transcripts, and git history), determine whether this is a dead/expired agent session, a rejected or "+
			"misspelled model id, a timeout, or a genuine failure in the work itself, and state the concrete step "+
			"the operator should take to get the workflow moving again.",
		ev.Pipeline, reason)
}
