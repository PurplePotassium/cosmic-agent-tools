package turns

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/PurplePotassium/cosmic-agent-tools/hal/internal/driver"
)

// TestInteractiveProbe exercises the REAL claude CLI (costs tokens; needs
// auth). Run after CLI upgrades to re-verify the probed contract in
// docs/interactive-driver.md:
//
//	HAL_PROBE=1 go test ./internal/turns -run TestInteractiveProbe -v
//
// It proves, in order: (1) a fresh minted-session turn parses and settles
// done; (2) --resume continues with full context in a new process and
// reports whether the session id rotated; (3) an interrupted (KillTree'd)
// turn resumes with context intact.
func TestInteractiveProbe(t *testing.T) {
	if testing.Short() || !probeEnabled(t) {
		t.Skip("set HAL_PROBE=1 to run the real-CLI probe")
	}
	drv := driver.NewClaude()
	if _, err := drv.Probe(context.Background()); err != nil {
		t.Skipf("claude not available: %v", err)
	}
	workDir := t.TempDir()
	const model = "claude-haiku-4-5-20251001"

	// 1. Fresh turn.
	minted := uuid.NewString()
	res, err := ProcRunner{}.Run(context.Background(), drv, TurnSpec{
		Prompt:    "Remember the word banana. Reply with just: OK",
		Model:     model,
		SessionID: minted,
		WorkDir:   workDir,
		Timeout:   3 * time.Minute,
	}, nil)
	if err != nil || res.State != TurnDone {
		t.Fatalf("fresh turn: %+v, %v", res, err)
	}
	if res.SessionID != minted {
		t.Logf("NOTE: fresh turn rotated the minted id: %s -> %s", minted, res.SessionID)
	}

	// 2. Resume in a new process: context must survive.
	res2, err := ProcRunner{}.Run(context.Background(), drv, TurnSpec{
		Prompt:  "What word did I ask you to remember? Reply with just that word.",
		Model:   model,
		Resume:  res.SessionID,
		WorkDir: workDir,
		Timeout: 3 * time.Minute,
	}, nil)
	if err != nil || res2.State != TurnDone {
		t.Fatalf("resume turn: %+v, %v", res2, err)
	}
	if !strings.Contains(strings.ToLower(res2.FinalText), "banana") {
		t.Fatalf("resume lost context: %q", res2.FinalText)
	}
	if res2.SessionID != res.SessionID {
		t.Logf("NOTE: --resume rotated the session id: %s -> %s (update docs/interactive-driver.md)", res.SessionID, res2.SessionID)
	}

	// 3. Interject: kill a long turn mid-stream, then resume.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var once sync.Once
	res3, err := ProcRunner{}.Run(ctx, drv, TurnSpec{
		Prompt:  "Count slowly from 1 to 500. Write each number on its own line. Do not stop early.",
		Model:   model,
		Resume:  res2.SessionID,
		WorkDir: workDir,
		Timeout: 5 * time.Minute,
	}, func(ev driver.StreamEvent) {
		if ev.Kind == driver.StreamTextDelta {
			once.Do(cancel)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if res3.State != TurnInterrupted {
		t.Fatalf("killed turn should settle interrupted: %+v", res3)
	}
	res4, err := ProcRunner{}.Run(context.Background(), drv, TurnSpec{
		Prompt:  "Stop counting. What was the word from the very beginning of our conversation? Reply with just that word.",
		Model:   model,
		Resume:  res3.SessionID,
		WorkDir: workDir,
		Timeout: 3 * time.Minute,
	}, nil)
	if err != nil || res4.State != TurnDone {
		t.Fatalf("post-kill resume: %+v, %v", res4, err)
	}
	if !strings.Contains(strings.ToLower(res4.FinalText), "banana") {
		t.Fatalf("post-kill resume lost context: %q", res4.FinalText)
	}
}

func probeEnabled(t *testing.T) bool {
	t.Helper()
	return os.Getenv("HAL_PROBE") == "1"
}
