package app

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/config"
	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/domain"
	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/store"
)

// A giant pass log must come back capped to its tail (bounded memory), on a
// clean line boundary, with a banner — the dashboard renders the tail anyway.
func TestPassLogTailCaps(t *testing.T) {
	a := newTestApp(t, initRepo(t))
	ctx := context.Background()

	pass, err := a.Store.StartPass(ctx, config.DefaultPipelineName)
	if err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(t.TempDir(), "pass.log")

	var buf bytes.Buffer
	buf.WriteString("FIRST-LINE-should-be-truncated\n")
	for buf.Len() < maxPassLogTail+(1<<20) {
		buf.WriteString("filler line to grow the log past the cap boundary\n")
	}
	buf.WriteString("LAST-LINE-must-survive\n")
	if err := os.WriteFile(logPath, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := a.Store.UpdatePass(ctx, pass.ID, store.PassPatch{LogPath: &logPath}); err != nil {
		t.Fatal(err)
	}

	got, err := a.PassLog(ctx, pass.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) > maxPassLogTail+512 {
		t.Fatalf("returned %d bytes, want <= cap+banner (~%d)", len(got), maxPassLogTail)
	}
	if !utf8.ValidString(got) {
		t.Fatal("returned tail is not valid UTF-8")
	}
	if !strings.HasPrefix(got, "… log truncated") {
		t.Fatalf("missing truncation banner; got prefix %q", got[:min(40, len(got))])
	}
	if strings.Contains(got, "FIRST-LINE") {
		t.Fatal("head of the log leaked into the tail")
	}
	if !strings.Contains(got, "LAST-LINE-must-survive") {
		t.Fatal("tail is missing the final line")
	}
}

// A giant log with no newline anywhere in its tail (one minified line) can
// put the seek point mid-rune; the tail must still come back as valid UTF-8.
// The log is 3 MiB of "→" (3 bytes each): the 1 MiB seek offset ≡ 1 (mod 3),
// so it always lands on a continuation byte.
func TestPassLogTailNoNewlineStaysValidUTF8(t *testing.T) {
	a := newTestApp(t, initRepo(t))
	ctx := context.Background()

	pass, err := a.Store.StartPass(ctx, config.DefaultPipelineName)
	if err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(t.TempDir(), "oneline.log")
	body := strings.Repeat("→", 1<<20) // 3 MiB, no newline
	if err := os.WriteFile(logPath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := a.Store.UpdatePass(ctx, pass.ID, store.PassPatch{LogPath: &logPath}); err != nil {
		t.Fatal(err)
	}

	got, err := a.PassLog(ctx, pass.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !utf8.ValidString(got) {
		t.Fatal("tail of a newline-free log is not valid UTF-8 (split rune survived)")
	}
	if !strings.Contains(got, "2.0 MB of 3.0 MB") {
		t.Fatalf("banner should report fractional sizes; got prefix %q", got[:min(60, len(got))])
	}
}

// A small log (under the cap) comes back whole, with no banner.
func TestPassLogUnderCapReturnsWhole(t *testing.T) {
	a := newTestApp(t, initRepo(t))
	ctx := context.Background()

	pass, err := a.Store.StartPass(ctx, config.DefaultPipelineName)
	if err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(t.TempDir(), "small.log")
	body := "only a few lines\nof output here\n"
	if err := os.WriteFile(logPath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := a.Store.UpdatePass(ctx, pass.ID, store.PassPatch{LogPath: &logPath}); err != nil {
		t.Fatal(err)
	}
	got, err := a.PassLog(ctx, pass.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got != body {
		t.Fatalf("small log = %q, want it returned whole %q", got, body)
	}
}

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

// TestUpdateTaskRemovesDroppedAttachments: editing a task's detail (e.g. via
// the dashboard's PATCH endpoint) to drop or replace an image reference must
// unlink the now-unreferenced attachment file the same way DeleteTask does,
// or it leaks under <StateDir>/attachments forever. A reference that survives
// the edit must be left alone.
func TestUpdateTaskRemovesDroppedAttachments(t *testing.T) {
	a := newTestApp(t, initRepo(t))
	ctx := context.Background()

	dropped, err := a.SaveAttachment("shot.png", "data:image/png;base64,aGk=")
	if err != nil {
		t.Fatal(err)
	}
	kept, err := a.SaveAttachment("kept.png", "data:image/png;base64,aGk=")
	if err != nil {
		t.Fatal(err)
	}

	task, err := a.Store.AddTask(ctx, &domain.Task{
		Title:  "with attachments",
		Detail: fmt.Sprintf("![shot.png](%s)\n![kept.png](%s)", dropped, kept),
	}, false)
	if err != nil {
		t.Fatal(err)
	}

	newDetail := fmt.Sprintf("![kept.png](%s)", kept)
	if _, err := a.UpdateTask(ctx, task.ID, store.TaskPatch{Detail: &newDetail}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dropped); !os.IsNotExist(err) {
		t.Fatalf("dropped attachment %s still exists (err=%v)", dropped, err)
	}
	if _, err := os.Stat(kept); err != nil {
		t.Fatalf("kept attachment %s was removed: %v", kept, err)
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

// drainHasEvent consumes every event currently buffered on ch and reports
// whether any carried the given type. Publish is synchronous onto a buffered
// channel, so by the time the call under test returns its events are already
// readable — no sleep needed.
func drainHasEvent(ch <-chan domain.Event, typ string) bool {
	found := false
	for {
		select {
		case ev := <-ch:
			if ev.Type == typ {
				found = true
			}
		default:
			return found
		}
	}
}

// TestSetPipelineBundleWarnsUnknownModel pins the operator-feedback contract
// for the live model dial: an override whose model isn't a known (or
// extra_models) id for its effective agent publishes a non-blocking
// driver.model_unknown warning — the same check config load applies, since a
// wrong id fails silently for blind drivers (AGENTS.md) — yet the override
// still lands (warn, never block). A curated model stays silent.
func TestSetPipelineBundleWarnsUnknownModel(t *testing.T) {
	a := newTestApp(t, initRepo(t))
	ctx := context.Background()
	events, cancel := a.Bus.Subscribe()
	defer cancel()

	// claude agent + a non-claude model id: off-family, so it must warn but
	// still store the override.
	bad := domain.Bundle{Agent: "claude", Model: "gpt-4o-mini"}
	if err := a.SetPipelineBundle(ctx, config.DefaultPipelineName, bad); err != nil {
		t.Fatalf("an off-family model must warn, not block: %v", err)
	}
	got, err := a.Store.PipelineBundle(ctx, config.DefaultPipelineName)
	if err != nil {
		t.Fatal(err)
	}
	if got != bad {
		t.Fatalf("override did not land despite the warning: %+v", got)
	}
	if !drainHasEvent(events, "driver.model_unknown") {
		t.Fatal("an off-family model override did not publish driver.model_unknown")
	}

	// A curated claude model id must NOT warn.
	ok := domain.Bundle{Agent: "claude", Model: "claude-opus-4-8"}
	if err := a.SetPipelineBundle(ctx, config.DefaultPipelineName, ok); err != nil {
		t.Fatal(err)
	}
	if drainHasEvent(events, "driver.model_unknown") {
		t.Fatal("a curated claude model must not warn")
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
