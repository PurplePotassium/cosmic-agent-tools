package app

import (
	"context"
	"fmt"

	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/domain"
)

// StartAutoInquiry subscribes to the event bus and, when a pipeline raises one
// of the operator-facing red banners — a tripped circuit breaker
// (breaker.tripped), an auth halt (auth.halt), or the auth-suspected heuristic
// (auth.suspected) — automatically fires the self-evaluator to diagnose WHY, so
// a pinned answer lands beside the banner in the dashboard instead of the
// operator having to notice it and type the question themselves.
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
				// subscriber's bounded bus buffer on pass.log traffic and
				// silently drop later critical events. Concurrency is still
				// bounded — ask itself returns ErrInquiryBusy while one runs —
				// and the goroutine exits promptly on shutdown because ask
				// respects ctx (probe, publish) before handing off to the
				// self-cancelling run.
				switch ev.Type {
				case "breaker.tripped":
					go a.autoInquire(ctx, breakerQuestion(ev))
				case "auth.halt", "auth.suspected":
					go a.autoInquire(ctx, authQuestion(ev))
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

// breakerQuestion diagnoses a circuit-breaker halt.
func breakerQuestion(ev domain.Event) string {
	count := "several"
	if n := payloadInt(ev.Payload["consecutiveFails"]); n > 0 {
		count = fmt.Sprintf("%d", n)
	}
	return fmt.Sprintf(
		"Pipeline %q just halted: its circuit breaker tripped after %s consecutive failed passes. "+
			"From the pass evidence (outcomes, failures, logs, and archived transcripts), diagnose the single "+
			"most likely root cause of those failures and state the concrete change the operator should make to "+
			"get the pipeline green again.",
		ev.Pipeline, count)
}

// authQuestion diagnoses an auth halt (auth.halt) or the auth-suspected
// heuristic (auth.suspected): both point at a dead session or a rejected model
// id, so the tailored question steers the evaluator at credentials and config
// rather than the code under test.
func authQuestion(ev domain.Event) string {
	agent := "the agent"
	if s, _ := ev.Payload["agent"].(string); s != "" {
		agent = fmt.Sprintf("%q", s)
	}
	return fmt.Sprintf(
		"Pipeline %q raised an auth alert for %s. From the pass evidence (outcomes, failures, logs, and archived "+
			"transcripts), determine whether this is a dead/expired agent session, a rejected or misspelled model "+
			"id, or a genuine code failure being mistaken for one, and state the concrete step the operator should "+
			"take (e.g. re-authenticate the agent interactively, correct the model id) to clear the alert.",
		ev.Pipeline, agent)
}

// payloadInt reads an integer event-payload field regardless of whether it
// arrived as an in-memory int or was round-tripped through JSON (float64).
func payloadInt(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	}
	return 0
}
