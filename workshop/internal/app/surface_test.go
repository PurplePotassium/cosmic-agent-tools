package app

import (
	"context"
	"testing"
)

// TestPauseAfterHaltsEveryEnabledPipeline: the dashboard's "pause-after"
// action is the bulk form of SetPipelineDesired(name, false) — it must park
// every enabled pipeline the same way the per-pipeline "stop" button does.
func TestPauseAfterHaltsEveryEnabledPipeline(t *testing.T) {
	a := newTestApp(t, initRepo(t))
	ctx := context.Background()

	pipelines := a.EnabledPipelines()
	if len(pipelines) == 0 {
		t.Fatal("test setup: no enabled pipelines")
	}

	if err := a.PauseAfter(ctx); err != nil {
		t.Fatal(err)
	}
	for _, p := range pipelines {
		reason, err := a.Store.HaltedReason(ctx, p.Name)
		if err != nil {
			t.Fatal(err)
		}
		if reason != "operator" {
			t.Fatalf("pipeline %s halted reason = %q, want %q", p.Name, reason, "operator")
		}
	}
}
