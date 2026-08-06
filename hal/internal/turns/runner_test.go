package turns

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PurplePotassium/cosmic-agent-tools/hal/internal/driver"
	"github.com/PurplePotassium/cosmic-agent-tools/hal/internal/proc"
)

// The test binary doubles as the agent process: "replay" streams a golden
// NDJSON capture and exits; "hang" emits an init + one delta, then lingers —
// the shape of a turn that must be killed (interrupt/timeout paths).
func TestMain(m *testing.M) {
	switch os.Getenv("HAL_TURNS_ROLE") {
	case "replay":
		data, err := os.ReadFile(os.Getenv("HAL_TURNS_GOLDEN"))
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Stdout.Write(data)
		os.Exit(0)
	case "hang":
		fmt.Println(`{"type":"system","subtype":"init","session_id":"99999999-0000-0000-0000-000000000000"}`)
		fmt.Println(`{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"thinking..."}},"session_id":"99999999-0000-0000-0000-000000000000"}`)
		time.Sleep(60 * time.Second)
		os.Exit(0)
	case "oversized":
		fmt.Println(strings.Repeat("x", 2*maxOutputLine))
		fmt.Println(`{"type":"system","subtype":"init","session_id":"88888888-0000-0000-0000-000000000000"}`)
		fmt.Println(`{"type":"result","subtype":"success","is_error":false,"result":"finished after oversized output","session_id":"88888888-0000-0000-0000-000000000000","total_cost_usd":0.01,"num_turns":1}`)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// stubDriver plans the test binary as the agent, with the role and golden
// path delivered via TurnSpec.ExtraEnv.
type stubDriver struct{}

func (stubDriver) Name() string { return "stub" }
func (stubDriver) Probe(context.Context) (driver.Capabilities, error) {
	return driver.Capabilities{
		PromptVia: driver.PromptStdin, Capture: driver.CaptureStreaming, Interactive: true,
	}, nil
}
func (stubDriver) Plan(spec driver.InvokeSpec) (driver.ExecPlan, error) {
	if !spec.Interactive {
		return driver.ExecPlan{}, fmt.Errorf("stub: expected an interactive spec")
	}
	return driver.ExecPlan{Exe: os.Args[0], StdinPrompt: true, Mode: proc.Piped}, nil
}

func golden(name string) string {
	// Absolute: the replay child runs in the turn's WorkDir, not the
	// package dir.
	abs, err := filepath.Abs(filepath.Join("..", "driver", "testdata", "stream", name))
	if err != nil {
		panic(err)
	}
	return abs
}

func replayEnv(name string) []string {
	return []string{"HAL_TURNS_ROLE=replay", "HAL_TURNS_GOLDEN=" + golden(name)}
}

// collectSink gathers events thread-safely and closes signal on the first
// text delta (the "the turn is visibly streaming" moment).
type collectSink struct {
	mu         sync.Mutex
	events     []driver.StreamEvent
	once       sync.Once
	firstDelta chan struct{}
}

func newCollectSink() *collectSink { return &collectSink{firstDelta: make(chan struct{})} }

func (c *collectSink) sink(ev driver.StreamEvent) {
	c.mu.Lock()
	c.events = append(c.events, ev)
	c.mu.Unlock()
	if ev.Kind == driver.StreamTextDelta {
		c.once.Do(func() { close(c.firstDelta) })
	}
}

func (c *collectSink) count(kind driver.StreamEventKind) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, e := range c.events {
		if e.Kind == kind {
			n++
		}
	}
	return n
}

func TestRunnerDone(t *testing.T) {
	sink := newCollectSink()
	logPath := filepath.Join(t.TempDir(), "turn-000001.log")
	res, err := ProcRunner{}.Run(context.Background(), stubDriver{}, TurnSpec{
		Prompt:   "hello",
		WorkDir:  t.TempDir(),
		LogPath:  logPath,
		ExtraEnv: replayEnv("success.ndjson"),
	}, sink.sink)
	if err != nil {
		t.Fatal(err)
	}
	if res.State != TurnDone {
		t.Fatalf("state: %s (%+v)", res.State, res)
	}
	if res.SessionID != "11111111-2222-3333-4444-555555555555" {
		t.Fatalf("session id not captured: %q", res.SessionID)
	}
	if !strings.Contains(res.FinalText, "question file is written") {
		t.Fatalf("final text: %q", res.FinalText)
	}
	if res.Usage == nil || res.Usage.IsError || res.Usage.TotalCostUSD == 0 {
		t.Fatalf("usage: %+v", res.Usage)
	}
	if sink.count(driver.StreamTextDelta) != 2 || sink.count(driver.StreamToolUse) != 1 {
		t.Fatalf("sink events: %+v", sink.events)
	}
	// The raw NDJSON is preserved verbatim in the turn log.
	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(logData), `"type":"result"`) {
		t.Fatal("turn log missing raw stream")
	}
}

// A process that dies without a result event (the KillTree'd-turn capture)
// fails the turn but preserves the captured session id and the streamed text.
func TestRunnerDiedWithoutResult(t *testing.T) {
	res, err := ProcRunner{}.Run(context.Background(), stubDriver{}, TurnSpec{
		Prompt:   "count",
		WorkDir:  t.TempDir(),
		ExtraEnv: replayEnv("killed.ndjson"),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.State != TurnFailed {
		t.Fatalf("state: %s", res.State)
	}
	if res.SessionID != "22222222-3333-4444-5555-666666666666" {
		t.Fatalf("session id: %q", res.SessionID)
	}
	if res.Usage != nil {
		t.Fatal("no result event means no usage")
	}
	if res.FinalText != "1\n2\n3\n" {
		t.Fatalf("final text should be the accumulated deltas: %q", res.FinalText)
	}
}

// An is_error result settles as failed with the error subtype preserved.
func TestRunnerErrorResult(t *testing.T) {
	res, err := ProcRunner{}.Run(context.Background(), stubDriver{}, TurnSpec{
		Prompt:   "x",
		WorkDir:  t.TempDir(),
		ExtraEnv: replayEnv("garbage.ndjson"),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.State != TurnFailed {
		t.Fatalf("state: %s", res.State)
	}
	if res.Usage == nil || res.Usage.Subtype != "error_during_execution" {
		t.Fatalf("usage: %+v", res.Usage)
	}
}

// Parent-context cancellation mid-stream is an interject: the turn settles
// as interrupted (never failed) with the session id intact for the resume.
func TestRunnerInterrupted(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sink := newCollectSink()
	go func() {
		select {
		case <-sink.firstDelta:
			cancel()
		case <-time.After(30 * time.Second):
		}
	}()
	res, err := ProcRunner{}.Run(ctx, stubDriver{}, TurnSpec{
		Prompt:   "count forever",
		WorkDir:  t.TempDir(),
		ExtraEnv: []string{"HAL_TURNS_ROLE=hang"},
	}, sink.sink)
	if err != nil {
		t.Fatal(err)
	}
	if res.State != TurnInterrupted {
		t.Fatalf("state: %s (%+v)", res.State, res)
	}
	if res.SessionID != "99999999-0000-0000-0000-000000000000" {
		t.Fatalf("session id: %q", res.SessionID)
	}
}

// The per-turn timeout kills a wedged turn and reports TurnTimeout.
func TestRunnerTimeout(t *testing.T) {
	start := time.Now()
	res, err := ProcRunner{}.Run(context.Background(), stubDriver{}, TurnSpec{
		Prompt:   "hang",
		WorkDir:  t.TempDir(),
		Timeout:  500 * time.Millisecond,
		ExtraEnv: []string{"HAL_TURNS_ROLE=hang"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.State != TurnTimeout {
		t.Fatalf("state: %s", res.State)
	}
	if elapsed := time.Since(start); elapsed > 25*time.Second {
		t.Fatalf("timeout took %v — KillTree/WaitDelay regression", elapsed)
	}
}

// An oversized tool-output line is discarded without stopping the pipe drain:
// the child must stay unblocked and its later result event must still settle
// the turn successfully.
func TestRunnerContinuesAfterOversizedLine(t *testing.T) {
	res, err := ProcRunner{}.Run(context.Background(), stubDriver{}, TurnSpec{
		Prompt:   "emit a large tool result",
		WorkDir:  t.TempDir(),
		Timeout:  3 * time.Second,
		ExtraEnv: []string{"HAL_TURNS_ROLE=oversized"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.State != TurnDone {
		t.Fatalf("state: %s (%+v)", res.State, res)
	}
	if res.FinalText != "finished after oversized output" {
		t.Fatalf("final text: %q", res.FinalText)
	}
	if len(res.Tail) != 3 || !strings.Contains(res.Tail[0], "exceeded 1 MiB") {
		t.Fatalf("tail should contain one truncation marker followed by init/result: %q", res.Tail)
	}
}

// Non-interactive drivers are refused before any process spawns.
func TestRunnerRefusesNonInteractiveDriver(t *testing.T) {
	t.Setenv("HAL_FAKE_BLIND", "1")
	t.Setenv("HAL_FAKE_BIN", os.Args[0])
	_, err := ProcRunner{}.Run(context.Background(), driver.NewFake(), TurnSpec{Prompt: "x"}, nil)
	if err == nil {
		t.Fatal("blind driver must be refused")
	}
}
