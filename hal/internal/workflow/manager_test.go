package workflow

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PurplePotassium/cosmic-agent-tools/hal/internal/bus"
	"github.com/PurplePotassium/cosmic-agent-tools/hal/internal/domain"
	"github.com/PurplePotassium/cosmic-agent-tools/hal/internal/driver"
	"github.com/PurplePotassium/cosmic-agent-tools/hal/internal/statedir"
	"github.com/PurplePotassium/cosmic-agent-tools/hal/internal/store"
	"github.com/PurplePotassium/cosmic-agent-tools/hal/internal/turns"
)

// scriptedTurn is one pre-programmed agent turn.
type scriptedTurn struct {
	state      turns.TurnState
	finalText  string
	status     *domain.WorkflowStatusFile // written to the status file (env) before returning
	artifact   string                     // content written to the artifact path (env)
	outOfScope string                     // repo-relative path written OUTSIDE the artifact dir
	exitCode   int
	tail       []string
	block      bool // wait for ctx cancellation, then return interrupted
}

// scriptedRunner replays turns in order — the in-process stand-in for the
// claude CLI, exercising the exact env/status-file contract real turns use.
type scriptedRunner struct {
	mu    sync.Mutex
	steps []scriptedTurn
	specs []turns.TurnSpec
}

func (r *scriptedRunner) Run(ctx context.Context, _ driver.Driver, spec turns.TurnSpec, sink turns.Sink) (turns.TurnResult, error) {
	r.mu.Lock()
	r.specs = append(r.specs, spec)
	if len(r.steps) == 0 {
		r.mu.Unlock()
		return turns.TurnResult{State: turns.TurnFailed, ExitCode: 1, FinalText: "script exhausted"}, nil
	}
	step := r.steps[0]
	r.steps = r.steps[1:]
	r.mu.Unlock()

	env := map[string]string{}
	for _, kv := range spec.ExtraEnv {
		if i := strings.Index(kv, "="); i > 0 {
			env[kv[:i]] = kv[i+1:]
		}
	}
	sessionID := spec.Resume
	if sessionID == "" {
		sessionID = spec.SessionID
	}
	if step.block {
		<-ctx.Done()
		return turns.TurnResult{State: turns.TurnInterrupted, SessionID: sessionID, ExitCode: -1}, nil
	}
	if step.artifact != "" {
		path := env["HAL_WORKFLOW_ARTIFACT"]
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return turns.TurnResult{State: turns.TurnFailed, ExitCode: 1}, err
		}
		if err := os.WriteFile(path, []byte(step.artifact), 0o644); err != nil {
			return turns.TurnResult{State: turns.TurnFailed, ExitCode: 1}, err
		}
	}
	if step.outOfScope != "" {
		path := filepath.Join(spec.WorkDir, filepath.FromSlash(step.outOfScope))
		_ = os.MkdirAll(filepath.Dir(path), 0o755)
		_ = os.WriteFile(path, []byte("rogue"), 0o644)
	}
	if step.status != nil {
		_ = statedir.WriteJSON(env["HAL_WORKFLOW_STATUS_FILE"], step.status)
	}
	if sink != nil {
		sink(driver.StreamEvent{Kind: driver.StreamTextDelta, Text: step.finalText})
	}
	usage := &driver.TurnUsage{ResultText: step.finalText, NumTurns: 1, TotalCostUSD: 0.01}
	if step.state == turns.TurnFailed {
		usage = nil
	}
	return turns.TurnResult{
		State: step.state, SessionID: sessionID, FinalText: step.finalText,
		Usage: usage, ExitCode: step.exitCode, Tail: step.tail,
	}, nil
}

// interactiveStub satisfies driver.Driver for the manager (never spawned).
type interactiveStub struct{}

func (interactiveStub) Name() string { return "stub" }
func (interactiveStub) Probe(context.Context) (driver.Capabilities, error) {
	return driver.Capabilities{Interactive: true, PromptVia: driver.PromptStdin, Capture: driver.CaptureStreaming}, nil
}
func (interactiveStub) Plan(driver.InvokeSpec) (driver.ExecPlan, error) {
	return driver.ExecPlan{}, fmt.Errorf("stub: never planned")
}

type fixture struct {
	m      *Manager
	st     *store.Store
	runner *scriptedRunner
	repo   string
	cancel context.CancelFunc
}

func newFixture(t *testing.T, steps []scriptedTurn, opts ...func(*Config)) *fixture {
	t.Helper()
	repo := t.TempDir()
	gitInit(t, repo)
	st, err := store.Open(filepath.Join(t.TempDir(), "hal.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	runner := &scriptedRunner{steps: steps}
	cfg := Config{
		RepoDir:  repo,
		StateDir: t.TempDir(),
		LogDir:   t.TempDir(),
		Verify:   "", // vacuously green gate
	}
	for _, opt := range opts {
		opt(&cfg)
	}
	m := New(st, bus.New(st), interactiveStub{}, runner, cfg)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = m.Run(ctx) }()
	waitFor(t, "manager running", func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.runCtx != nil
	})
	return &fixture{m: m, st: st, runner: runner, repo: repo, cancel: cancel}
}

func gitInit(t *testing.T, dir string) {
	t.Helper()
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"config", "user.name", "t"}, {"config", "user.email", "t@t"},
		{"commit", "-q", "--allow-empty", "-m", "init"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	// Generous: the whole suite runs in parallel with git-heavy packages
	// and CI machines are slow — a tight bound here only buys flakes.
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func (f *fixture) waitStatus(t *testing.T, id string, want domain.WorkflowStatus) domain.Workflow {
	t.Helper()
	var wf domain.Workflow
	waitFor(t, fmt.Sprintf("status %s", want), func() bool {
		var err error
		wf, err = f.st.GetWorkflow(context.Background(), id)
		return err == nil && wf.Status == want
	})
	return wf
}

// waitStage waits for a (stage, status) pair — the reliable signal after an
// approval, when the status alone may not change.
func (f *fixture) waitStage(t *testing.T, id string, stage domain.WorkflowStage, status domain.WorkflowStatus) {
	t.Helper()
	waitFor(t, fmt.Sprintf("stage %s / %s", stage, status), func() bool {
		wf, err := f.st.GetWorkflow(context.Background(), id)
		return err == nil && wf.Stage == stage && wf.Status == status
	})
}

// waitTurnCount waits until n runner turns have been dispatched.
func (f *fixture) waitTurnCount(t *testing.T, n int) {
	t.Helper()
	waitFor(t, fmt.Sprintf("%d turns", n), func() bool {
		f.runner.mu.Lock()
		defer f.runner.mu.Unlock()
		return len(f.runner.specs) >= n
	})
}

// spec reads a dispatched TurnSpec under the runner lock.
func (f *fixture) spec(t *testing.T, i int) turns.TurnSpec {
	t.Helper()
	f.runner.mu.Lock()
	defer f.runner.mu.Unlock()
	if i >= len(f.runner.specs) {
		t.Fatalf("turn %d not dispatched (have %d)", i, len(f.runner.specs))
	}
	return f.runner.specs[i]
}

func ready(artifact string) *domain.WorkflowStatusFile {
	return &domain.WorkflowStatusFile{Phase: domain.PhaseReady, Artifact: artifact, Note: "done"}
}

func asking(note string) *domain.WorkflowStatusFile {
	return &domain.WorkflowStatusFile{Phase: domain.PhaseAsking, Note: note}
}

// The full six-stage happy path: refine asks then readies; every later stage
// readies straight away; six approvals land the workflow completed with the
// artifacts committed.
func TestSixStageHappyPath(t *testing.T) {
	art := "---\nstub: true\n---\ncontent"
	f := newFixture(t, []scriptedTurn{
		{state: turns.TurnDone, finalText: "What exactly should the boss do?", status: asking("q1")},
		{state: turns.TurnDone, finalText: "Question saved.", status: ready("01"), artifact: art},
		{state: turns.TurnDone, finalText: "Research saved.", status: ready("02"), artifact: art},
		{state: turns.TurnDone, finalText: "Design saved.", status: ready("03"), artifact: art},
		{state: turns.TurnDone, finalText: "Plan saved.", status: ready("04"), artifact: art},
		{state: turns.TurnDone, finalText: "Implemented.", status: ready("05"), artifact: art},
		{state: turns.TurnDone, finalText: "Validated: PASS.", status: ready("06"), artifact: art},
	})
	ctx := context.Background()
	wf, err := f.m.Create(ctx, CreateReq{Title: "boss fight", Brief: "make the boss harder"})
	if err != nil {
		t.Fatal(err)
	}
	// Refine asks a question first.
	f.waitStatus(t, wf.ID, domain.WorkflowAwaitingUser)
	if err := f.m.Message(ctx, wf.ID, "it should enrage at 50% HP", false); err != nil {
		t.Fatal(err)
	}
	f.waitStatus(t, wf.ID, domain.WorkflowAwaitingApproval)

	// Approve every stage in order.
	for i, stage := range domain.StageOrder {
		got, err := f.st.GetWorkflow(ctx, wf.ID)
		if err != nil || got.Stage != stage {
			t.Fatalf("expected stage %s, got %+v (%v)", stage, got, err)
		}
		if err := f.m.Approve(ctx, wf.ID, stage, "lgtm"); err != nil {
			t.Fatalf("approve %s: %v", stage, err)
		}
		if stage == domain.StageValidate {
			break
		}
		f.waitStage(t, wf.ID, domain.StageOrder[i+1], domain.WorkflowAwaitingApproval)
	}
	final := f.waitStatus(t, wf.ID, domain.WorkflowCompleted)
	if final.BaseSHA == "" {
		t.Error("base sha must be recorded at implement start")
	}

	// Stage rows all approved; artifacts recorded.
	stages, _ := f.st.WorkflowStages(ctx, wf.ID)
	for _, st := range stages {
		if st.Status != domain.StageApproved {
			t.Errorf("stage %s: %s", st.Stage, st.Status)
		}
		if st.Artifact == "" {
			t.Errorf("stage %s missing artifact", st.Stage)
		}
	}

	// Approval commits carry the workflow trailers.
	log := gitLog(t, f.repo)
	if !strings.Contains(log, "refine approved") || !strings.Contains(log, "Hal-Workflow: "+wf.ID) {
		t.Fatalf("approval commits missing:\n%s", log)
	}
	// The resumed turns rode the captured session.
	if f.spec(t, 1).Resume == "" {
		t.Error("second refine turn must resume the session")
	}
	if f.spec(t, 2).Resume != "" {
		t.Error("research's first turn must be a fresh session (per-stage sessions)")
	}
}

func gitLog(t *testing.T, repo string) string {
	t.Helper()
	cmd := exec.Command("git", "log", "--format=%s%n%(trailers)")
	cmd.Dir = repo
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

// Interject: a blocking turn is killed; the queued message resumes.
func TestInterject(t *testing.T) {
	f := newFixture(t, []scriptedTurn{
		{block: true},
		{state: turns.TurnDone, finalText: "adjusted per your note", status: asking("ok")},
	})
	ctx := context.Background()
	wf, err := f.m.Create(ctx, CreateReq{Title: "x", Brief: "y"})
	if err != nil {
		t.Fatal(err)
	}
	f.waitStatus(t, wf.ID, domain.WorkflowTurnRunning)
	if err := f.m.Message(ctx, wf.ID, "stop — different direction", true); err != nil {
		t.Fatal(err)
	}
	f.waitStatus(t, wf.ID, domain.WorkflowAwaitingUser)
	turnsList, _ := f.st.ListTurns(ctx, wf.ID)
	if len(turnsList) != 2 || turnsList[0].State != domain.TurnStateInterrupted {
		t.Fatalf("turns: %+v", turnsList)
	}
	msgs, _ := f.st.ListMessages(ctx, wf.ID, 0, 0)
	foundInterjection := false
	for _, msg := range msgs {
		if msg.Kind == domain.MsgInterjection {
			foundInterjection = true
		}
	}
	if !foundInterjection {
		t.Fatal("interjection message missing")
	}
}

// Reject sends the feedback as a turn of the same stage.
func TestRejectFeedbackTurn(t *testing.T) {
	art := "a"
	f := newFixture(t, []scriptedTurn{
		{state: turns.TurnDone, status: ready("01"), artifact: art},
		{state: turns.TurnDone, finalText: "revised", status: ready("01"), artifact: art + "v2"},
	})
	ctx := context.Background()
	wf, _ := f.m.Create(ctx, CreateReq{Title: "x", Brief: "y"})
	f.waitStatus(t, wf.ID, domain.WorkflowAwaitingApproval)
	if err := f.m.Reject(ctx, wf.ID, domain.StageRefine, "too vague, name the subsystem"); err != nil {
		t.Fatal(err)
	}
	f.waitTurnCount(t, 2)
	f.waitStatus(t, wf.ID, domain.WorkflowAwaitingApproval)
	if got := f.spec(t, 1).Prompt; !strings.Contains(got, "too vague") || !strings.Contains(got, "<user-feedback>") {
		t.Fatalf("feedback prompt: %q", got)
	}
	if f.spec(t, 1).Resume == "" {
		t.Fatal("feedback turn must resume the stage session")
	}
}

// Skip writes the stub artifact and advances.
func TestSkipStage(t *testing.T) {
	f := newFixture(t, []scriptedTurn{
		{state: turns.TurnDone, status: ready("01"), artifact: "q"},
		{state: turns.TurnDone, status: asking("research opening")},
	})
	ctx := context.Background()
	wf, _ := f.m.Create(ctx, CreateReq{Title: "x", Brief: "y"})
	f.waitStatus(t, wf.ID, domain.WorkflowAwaitingApproval)
	if err := f.m.Approve(ctx, wf.ID, domain.StageRefine, ""); err != nil {
		t.Fatal(err)
	}
	f.waitStatus(t, wf.ID, domain.WorkflowAwaitingUser) // research opened
	if err := f.m.Skip(ctx, wf.ID, domain.StageResearch); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "design stage", func() bool {
		got, _ := f.st.GetWorkflow(ctx, wf.ID)
		return got.Stage == domain.StageDesign
	})
	stub, err := os.ReadFile(filepath.Join(f.repo, ".hal", "workflows", wf.ID, "02-research.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(stub), "status: skipped") {
		t.Fatalf("stub: %s", stub)
	}
}

// Start-at-stage intake stubs every earlier stage.
func TestCreateStartAtPlan(t *testing.T) {
	f := newFixture(t, []scriptedTurn{
		{state: turns.TurnDone, status: asking("plan opening")},
	})
	ctx := context.Background()
	wf, err := f.m.Create(ctx, CreateReq{Title: "quick fix", Brief: "just fix the typo", StartStage: domain.StagePlan})
	if err != nil {
		t.Fatal(err)
	}
	f.waitStatus(t, wf.ID, domain.WorkflowAwaitingUser)
	stages, _ := f.st.WorkflowStages(ctx, wf.ID)
	for _, st := range stages[:3] {
		if st.Status != domain.StageSkipped {
			t.Errorf("stage %s: %s", st.Stage, st.Status)
		}
	}
	if stages[3].Status != domain.StageActive {
		t.Errorf("plan stage: %+v", stages[3])
	}
	// The refine stub carries the raw ask verbatim.
	stub, _ := os.ReadFile(filepath.Join(f.repo, ".hal", "workflows", wf.ID, "01-question.md"))
	if !strings.Contains(string(stub), "just fix the typo") {
		t.Fatalf("refine stub must embed the brief: %s", stub)
	}
}

// Validation turned off at intake: approving implement stub-skips validate
// and completes the workflow — no validate turn, no verify gate.
func TestSkipValidateEndsAtImplement(t *testing.T) {
	f := newFixture(t, []scriptedTurn{
		{state: turns.TurnDone, status: ready("05"), artifact: "changelog"},
	})
	ctx := context.Background()
	wf, err := f.m.Create(ctx, CreateReq{
		Title: "x", Brief: "y", StartStage: domain.StageImplement, SkipValidate: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !wf.SkipValidate {
		t.Fatal("SkipValidate must survive intake")
	}
	f.waitStatus(t, wf.ID, domain.WorkflowAwaitingApproval)
	if err := f.m.Approve(ctx, wf.ID, domain.StageImplement, ""); err != nil {
		t.Fatal(err)
	}
	f.waitStatus(t, wf.ID, domain.WorkflowCompleted)

	stages, _ := f.st.WorkflowStages(ctx, wf.ID)
	if got := stages[5].Status; got != domain.StageSkipped {
		t.Errorf("validate stage: %s, want skipped", got)
	}
	stub, err := os.ReadFile(filepath.Join(f.repo, ".hal", "workflows", wf.ID, "06-validation.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(stub), "status: skipped") {
		t.Errorf("stub: %s", stub)
	}
	// The whole point: no validate turn was ever dispatched.
	f.runner.mu.Lock()
	dispatched := len(f.runner.specs)
	f.runner.mu.Unlock()
	if dispatched != 1 {
		t.Errorf("turns dispatched = %d, want 1 (implement only)", dispatched)
	}
}

// Starting AT validate is a request to validate: the unchecked box is
// ignored rather than completing the workflow before its first turn.
func TestSkipValidateIgnoredWhenStartingThere(t *testing.T) {
	f := newFixture(t, []scriptedTurn{
		{state: turns.TurnDone, status: asking("validate opening")},
	})
	ctx := context.Background()
	wf, err := f.m.Create(ctx, CreateReq{
		Title: "x", Brief: "y", StartStage: domain.StageValidate, SkipValidate: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if wf.SkipValidate {
		t.Fatal("a workflow starting at validate must not skip it")
	}
	f.waitStage(t, wf.ID, domain.StageValidate, domain.WorkflowAwaitingUser)
}

// The implement mismatch protocol lands the workflow in blocked.
func TestBlockedMismatch(t *testing.T) {
	f := newFixture(t, []scriptedTurn{
		{state: turns.TurnDone, finalText: "Issue in Phase 2: Expected X Found Y. How should I proceed?",
			status: &domain.WorkflowStatusFile{Phase: domain.PhaseBlocked, Note: "plan/reality mismatch"}},
	})
	ctx := context.Background()
	wf, err := f.m.Create(ctx, CreateReq{Title: "x", Brief: "y", StartStage: domain.StageImplement})
	if err != nil {
		t.Fatal(err)
	}
	f.waitStatus(t, wf.ID, domain.WorkflowBlocked)
}

// A read-only stage writing outside the artifact dir gets reverted.
func TestTreeCheckRevertsOutOfScope(t *testing.T) {
	f := newFixture(t, []scriptedTurn{
		{state: turns.TurnDone, status: ready("01"), artifact: "q", outOfScope: "src/rogue.go"},
	})
	ctx := context.Background()
	wf, _ := f.m.Create(ctx, CreateReq{Title: "x", Brief: "y"})
	f.waitStatus(t, wf.ID, domain.WorkflowAwaitingApproval)
	if _, err := os.Stat(filepath.Join(f.repo, "src", "rogue.go")); !os.IsNotExist(err) {
		t.Fatal("out-of-scope write must be reverted")
	}
	if _, err := os.Stat(filepath.Join(f.repo, ".hal", "workflows", wf.ID, "01-question.md")); err != nil {
		t.Fatal("in-scope artifact must survive the tree check")
	}
	// Pre-existing operator edits are sacred: seed one and run another turn.
	msgs, _ := f.st.ListMessages(ctx, wf.ID, 0, 0)
	violation := false
	for _, msg := range msgs {
		if strings.Contains(msg.Content, "rogue.go") {
			violation = true
		}
	}
	if !violation {
		t.Fatal("violation notice missing")
	}
}

// Operator edits that predate the turn are never reverted.
func TestTreeCheckSparesOperatorEdits(t *testing.T) {
	f := newFixture(t, []scriptedTurn{
		{state: turns.TurnDone, status: asking("hm")},
	})
	ctx := context.Background()
	operatorFile := filepath.Join(f.repo, "notes.md")
	if err := os.WriteFile(operatorFile, []byte("operator scribbles"), 0o644); err != nil {
		t.Fatal(err)
	}
	wf, _ := f.m.Create(ctx, CreateReq{Title: "x", Brief: "y"})
	f.waitStatus(t, wf.ID, domain.WorkflowAwaitingUser)
	if data, err := os.ReadFile(operatorFile); err != nil || string(data) != "operator scribbles" {
		t.Fatalf("operator edit clobbered: %s, %v", data, err)
	}
}

// ready-without-artifact triggers exactly one corrective nudge turn.
func TestCorrectiveNudge(t *testing.T) {
	f := newFixture(t, []scriptedTurn{
		{state: turns.TurnDone, finalText: "saved!", status: ready("01")}, // lies: no artifact
		{state: turns.TurnDone, finalText: "really saved", status: ready("01"), artifact: "q"},
	})
	ctx := context.Background()
	wf, _ := f.m.Create(ctx, CreateReq{Title: "x", Brief: "y"})
	f.waitStatus(t, wf.ID, domain.WorkflowAwaitingApproval)
	f.waitTurnCount(t, 2)
	if !strings.Contains(f.spec(t, 1).Prompt, "does not exist") {
		t.Fatalf("nudge prompt: %q", f.spec(t, 1).Prompt)
	}
}

// Auth-shaped failures surface as error with the auth: prefix.
func TestAuthFailure(t *testing.T) {
	f := newFixture(t, []scriptedTurn{
		{state: turns.TurnFailed, exitCode: 1, tail: []string{"Error: not logged in. please run /login"}},
	})
	ctx := context.Background()
	wf, _ := f.m.Create(ctx, CreateReq{Title: "x", Brief: "y"})
	got := f.waitStatus(t, wf.ID, domain.WorkflowError)
	if !strings.HasPrefix(got.Error, "auth: ") {
		t.Fatalf("error detail: %q", got.Error)
	}
	// A retry message clears the error and runs a fresh turn (script is
	// exhausted, so it fails again — but the attempt itself must be made).
	if err := f.m.Message(ctx, wf.ID, "logged back in, retry", false); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "retry turn", func() bool {
		f.runner.mu.Lock()
		defer f.runner.mu.Unlock()
		return len(f.runner.specs) >= 2
	})
}

// Abandon from a running turn kills it and terminates the session.
func TestAbandon(t *testing.T) {
	f := newFixture(t, []scriptedTurn{{block: true}})
	ctx := context.Background()
	wf, _ := f.m.Create(ctx, CreateReq{Title: "x", Brief: "y"})
	f.waitStatus(t, wf.ID, domain.WorkflowTurnRunning)
	if err := f.m.Abandon(ctx, wf.ID); err != nil {
		t.Fatal(err)
	}
	f.waitStatus(t, wf.ID, domain.WorkflowAbandoned)
	if err := f.m.Message(ctx, wf.ID, "hello?", false); err == nil {
		t.Fatal("terminal workflow must refuse messages")
	}
}

// The validate gate blocks approval when the verify command is red.
//
//nolint:paralleltest
func TestValidateGateRed(t *testing.T) {
	f := newFixture(t, []scriptedTurn{
		{state: turns.TurnDone, finalText: "report written", status: ready("06"), artifact: "report"},
	}, func(cfg *Config) { cfg.Verify = "exit 1" })
	ctx := context.Background()
	wf, err := f.m.Create(ctx, CreateReq{Title: "x", Brief: "y", StartStage: domain.StageValidate})
	if err != nil {
		t.Fatal(err)
	}
	f.waitTurnCount(t, 1)
	// The gate demotes ready → awaiting-user and leaves a RED message.
	waitFor(t, "red gate message", func() bool {
		msgs, _ := f.st.ListMessages(ctx, wf.ID, 0, 0)
		for _, msg := range msgs {
			if msg.Kind == domain.MsgGate && strings.Contains(msg.Content, "RED") {
				return true
			}
		}
		return false
	})
	got := f.waitStatus(t, wf.ID, domain.WorkflowAwaitingUser)
	if got.Status != domain.WorkflowAwaitingUser {
		t.Fatalf("status: %s", got.Status)
	}
}
