package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/domain"
	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/engine"
	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/fakeagent"
	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/statedir"
	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/store"
)

// The test binary doubles as the fake agent (same trick as the engine tests).
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "_fake-agent" {
		os.Exit(fakeagent.Main())
	}
	os.Exit(m.Run())
}

func TestWriteInquiryEvidence(t *testing.T) {
	a := newTestApp(t, initRepo(t))
	ctx := context.Background()

	task, err := a.Store.AddTask(ctx, &domain.Task{Title: "make coins smaller"}, false)
	if err != nil {
		t.Fatal(err)
	}
	pass, err := a.Store.StartPass(ctx, "main")
	if err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(a.StateDir, "logs", "main", "iter-000001.log")
	sid := "11111111-2222-3333-4444-555555555555"
	sha := "abc1234"
	if err := a.Store.UpdatePass(ctx, pass.ID, store.PassPatch{
		TaskID: &task.ID, SessionID: &sid, LogPath: &logPath, CommitSHA: &sha,
	}); err != nil {
		t.Fatal(err)
	}
	// An archived transcript exists for this pass; evidence must point at it.
	transcript := engine.TranscriptArchivePath(logPath)
	if err := os.MkdirAll(filepath.Dir(transcript), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(transcript, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	path, err := a.writeInquiryEvidence(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Passes []inquiryPass `json:"passes"`
	}
	if err := statedir.ReadJSON(path, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Passes) != 1 {
		t.Fatalf("passes: %+v", doc.Passes)
	}
	got := doc.Passes[0]
	if got.Pass != pass.ID || got.TaskTitle != "make coins smaller" ||
		got.SessionID != sid || got.Transcript != transcript || got.CommitSHA != sha {
		t.Fatalf("evidence row wrong: %+v", got)
	}
}

// TestAskInquiryLifecycle drives one full inquiry against the scripted fake
// agent: validation, the one-at-a-time guard, a real (streamed, captured)
// answer, and the slot freeing up afterwards.
func TestAskInquiryLifecycle(t *testing.T) {
	a := newTestApp(t, initRepo(t))
	ctx := context.Background()

	if _, err := a.AskInquiry(ctx, "   "); err == nil {
		t.Fatal("empty question must be rejected")
	}

	// Route the inquiry to the fake driver, exactly as an operator would
	// route it to another model: a [types.inquiry] entry.
	a.Res().Config.Types["inquiry"] = domain.Bundle{Agent: "fake"}
	t.Setenv("WORKSHOP_FAKE_BIN", os.Args[0])
	// fakeagent refuses to run without the pass env; inquiry children
	// inherit the test process environment.
	t.Setenv("WORKSHOP_PASS_STATE_DIR", t.TempDir())
	t.Setenv("WORKSHOP_PASS_REPO_DIR", a.RepoDir)

	settle := func() *Inquiry {
		t.Helper()
		deadline := time.Now().Add(15 * time.Second)
		for {
			if list := a.Inquiries(); len(list) > 0 && list[0].State != "running" {
				return list[0]
			}
			if time.Now().After(deadline) {
				t.Fatal("inquiry never settled")
			}
			time.Sleep(25 * time.Millisecond)
		}
	}

	inq, err := a.AskInquiry(ctx, "why are the coin pickups so big?")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.AskInquiry(ctx, "second question"); !errors.Is(err, ErrInquiryBusy) {
		t.Fatalf("concurrent ask: err = %v, want ErrInquiryBusy", err)
	}
	done := settle()
	if done.ID != inq.ID || done.State != "done" || !strings.Contains(done.Answer, "fake pass complete") {
		t.Fatalf("inquiry settled wrong: %+v", done)
	}

	// The slot frees up for the next question.
	if _, err := a.AskInquiry(ctx, "and the duplicator levels?"); err != nil {
		t.Fatalf("ask after settle: %v", err)
	}
	settle()
}
