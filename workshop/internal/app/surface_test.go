package app

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/config"
	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/domain"
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

// TestDeleteTaskRemovesReferencedAttachments: an attachment's only owner is
// the markdown line SaveAttachment's caller writes into the task's detail —
// once the task is gone that file must go with it, or it leaks under
// <StateDir>/attachments forever.
func TestDeleteTaskRemovesReferencedAttachments(t *testing.T) {
	a := newTestApp(t, initRepo(t))
	ctx := context.Background()

	path, err := a.SaveAttachment("shot.png", "data:image/png;base64,aGk=")
	if err != nil {
		t.Fatal(err)
	}
	unrelated, err := a.SaveAttachment("other.png", "data:image/png;base64,aGk=")
	if err != nil {
		t.Fatal(err)
	}

	task, err := a.Store.AddTask(ctx, &domain.Task{
		Title:  "with attachment",
		Detail: fmt.Sprintf("see this:\n![shot.png](%s)\nand also stray text (%s) that isn't a real ref", path, unrelated),
	}, false)
	if err != nil {
		t.Fatal(err)
	}

	if err := a.DeleteTask(ctx, task.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("referenced attachment %s still exists (err=%v)", path, err)
	}
	if _, err := os.Stat(unrelated); err != nil {
		t.Fatalf("unrelated attachment %s was removed: %v", unrelated, err)
	}
}

// TestSetPipelineBundleValidation checks the guards SetPipelineBundle applies
// before it ever touches the store: an unknown pipeline name, an unrecognized
// agent (driver.New failure), and an unrecognized effort must all be
// rejected, and rejected calls must leave no override behind.
func TestSetPipelineBundleValidation(t *testing.T) {
	a := newTestApp(t, initRepo(t))
	ctx := context.Background()

	if err := a.SetPipelineBundle(ctx, "no-such-pipeline", domain.Bundle{Effort: "high"}); err == nil {
		t.Fatal("SetPipelineBundle with unknown pipeline name: want error, got nil")
	}
	if err := a.SetPipelineBundle(ctx, config.DefaultPipelineName, domain.Bundle{Agent: "bogus-agent"}); err == nil {
		t.Fatal("SetPipelineBundle with unrecognized agent: want error, got nil")
	}
	if err := a.SetPipelineBundle(ctx, config.DefaultPipelineName, domain.Bundle{Effort: "bogus-effort"}); err == nil {
		t.Fatal("SetPipelineBundle with unrecognized effort: want error, got nil")
	}

	b, err := a.Store.PipelineBundle(ctx, config.DefaultPipelineName)
	if err != nil {
		t.Fatal(err)
	}
	if !b.IsZero() {
		t.Fatalf("rejected SetPipelineBundle calls left an override behind: %+v", b)
	}
}

// TestSetPipelineBundleSetAndClear pins the live-override contract: a valid
// bundle reaches the store and publishes a "pipeline.bundle" event, and
// setting the zero bundle clears it again (the dashboard's "reset to
// configured" action).
func TestSetPipelineBundleSetAndClear(t *testing.T) {
	a := newTestApp(t, initRepo(t))
	ctx := context.Background()
	events, cancel := a.Bus.Subscribe()
	defer cancel()

	want := domain.Bundle{Agent: "fake", Model: "some-model", Effort: "high"}
	if err := a.SetPipelineBundle(ctx, config.DefaultPipelineName, want); err != nil {
		t.Fatal(err)
	}
	got, err := a.Store.PipelineBundle(ctx, config.DefaultPipelineName)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("stored bundle = %+v, want %+v", got, want)
	}
	select {
	case ev := <-events:
		if ev.Type != "pipeline.bundle" || ev.Pipeline != config.DefaultPipelineName {
			t.Fatalf("unexpected event: %+v", ev)
		}
		if cleared, _ := ev.Payload["cleared"].(bool); cleared {
			t.Fatalf("event marked cleared=true for a non-zero bundle: %+v", ev.Payload)
		}
	default:
		t.Fatal("SetPipelineBundle did not publish a pipeline.bundle event")
	}

	if err := a.SetPipelineBundle(ctx, config.DefaultPipelineName, domain.Bundle{}); err != nil {
		t.Fatal(err)
	}
	got, err = a.Store.PipelineBundle(ctx, config.DefaultPipelineName)
	if err != nil {
		t.Fatal(err)
	}
	if !got.IsZero() {
		t.Fatalf("clearing bundle: stored bundle = %+v, want zero", got)
	}
	select {
	case ev := <-events:
		if cleared, _ := ev.Payload["cleared"].(bool); !cleared {
			t.Fatalf("clearing event did not mark cleared=true: %+v", ev.Payload)
		}
	default:
		t.Fatal("clearing SetPipelineBundle did not publish a pipeline.bundle event")
	}
}

// TestSetPipelineModeValidation mirrors TestSetPipelineBundleValidation for
// the mode override: an unknown pipeline and an unrecognized mode must both
// be rejected before the store is touched.
func TestSetPipelineModeValidation(t *testing.T) {
	a := newTestApp(t, initRepo(t))
	ctx := context.Background()

	if err := a.SetPipelineMode(ctx, "no-such-pipeline", "discover"); err == nil {
		t.Fatal("SetPipelineMode with unknown pipeline name: want error, got nil")
	}
	if err := a.SetPipelineMode(ctx, config.DefaultPipelineName, "bogus-mode"); err == nil {
		t.Fatal("SetPipelineMode with unrecognized mode: want error, got nil")
	}

	mode, err := a.Store.PipelineMode(ctx, config.DefaultPipelineName)
	if err != nil {
		t.Fatal(err)
	}
	if mode != "" {
		t.Fatalf("rejected SetPipelineMode calls left an override behind: %q", mode)
	}
}

// TestSetPipelineModeSetAndClear pins the live mode-override contract: a
// valid mode reaches the store and publishes a "pipeline.mode" event, and
// setting "" clears it again.
func TestSetPipelineModeSetAndClear(t *testing.T) {
	a := newTestApp(t, initRepo(t))
	ctx := context.Background()
	events, cancel := a.Bus.Subscribe()
	defer cancel()

	if err := a.SetPipelineMode(ctx, config.DefaultPipelineName, "discover"); err != nil {
		t.Fatal(err)
	}
	mode, err := a.Store.PipelineMode(ctx, config.DefaultPipelineName)
	if err != nil {
		t.Fatal(err)
	}
	if mode != "discover" {
		t.Fatalf("stored mode = %q, want %q", mode, "discover")
	}
	select {
	case ev := <-events:
		if ev.Type != "pipeline.mode" || ev.Pipeline != config.DefaultPipelineName {
			t.Fatalf("unexpected event: %+v", ev)
		}
		if cleared, _ := ev.Payload["cleared"].(bool); cleared {
			t.Fatalf("event marked cleared=true for a non-empty mode: %+v", ev.Payload)
		}
	default:
		t.Fatal("SetPipelineMode did not publish a pipeline.mode event")
	}

	if err := a.SetPipelineMode(ctx, config.DefaultPipelineName, ""); err != nil {
		t.Fatal(err)
	}
	mode, err = a.Store.PipelineMode(ctx, config.DefaultPipelineName)
	if err != nil {
		t.Fatal(err)
	}
	if mode != "" {
		t.Fatalf("clearing mode: stored mode = %q, want empty", mode)
	}
	select {
	case ev := <-events:
		if cleared, _ := ev.Payload["cleared"].(bool); !cleared {
			t.Fatalf("clearing event did not mark cleared=true: %+v", ev.Payload)
		}
	default:
		t.Fatal("clearing SetPipelineMode did not publish a pipeline.mode event")
	}
}
