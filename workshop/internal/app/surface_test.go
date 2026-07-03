package app

import (
	"context"
	"fmt"
	"os"
	"testing"

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
