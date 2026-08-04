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

	"github.com/PurplePotassium/cosmic-agent-tools/hal/internal/domain"
	"github.com/PurplePotassium/cosmic-agent-tools/hal/internal/store"
)

// A giant pass log must come back capped to its tail (bounded memory), on a
// clean line boundary, with a banner — the dashboard renders the tail anyway.
func TestPassLogTailCaps(t *testing.T) {
	a := newTestApp(t, initRepo(t))
	ctx := context.Background()

	pass, err := a.Store.StartPass(ctx, "art")
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

	pass, err := a.Store.StartPass(ctx, "art")
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

	pass, err := a.Store.StartPass(ctx, "art")
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

