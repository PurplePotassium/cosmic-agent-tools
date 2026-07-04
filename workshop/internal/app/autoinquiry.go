package app

import (
	"context"
	"fmt"

	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/domain"
)

// StartAutoInquiry subscribes to the event bus and, when a pipeline trips its
// circuit breaker (halts after N consecutive failed passes), automatically
// fires the self-evaluator to diagnose WHY — so a pinned answer lands beside
// the red halt in the dashboard instead of the operator having to notice the
// banner and type the question themselves.
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
				if ev.Type == "breaker.tripped" {
					a.autoInquire(ctx, ev)
				}
			}
		}
	}()
	return cancel
}

// autoInquire fires one self-evaluator run diagnosing a breaker halt. It is
// best-effort: a busy evaluator (ErrInquiryBusy, e.g. the operator is already
// asking something) or a missing/unprobeable agent just means no
// auto-diagnosis this time — the halt is surfaced on its own regardless.
func (a *App) autoInquire(ctx context.Context, ev domain.Event) {
	count := "several"
	if n := payloadInt(ev.Payload["consecutiveFails"]); n > 0 {
		count = fmt.Sprintf("%d", n)
	}
	question := fmt.Sprintf(
		"Pipeline %q just halted: its circuit breaker tripped after %s consecutive failed passes. "+
			"From the pass evidence (outcomes, failures, logs, and archived transcripts), diagnose the single "+
			"most likely root cause of those failures and state the concrete change the operator should make to "+
			"get the pipeline green again.",
		ev.Pipeline, count)
	_, _ = a.ask(ctx, question, domain.Bundle{}, true)
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
