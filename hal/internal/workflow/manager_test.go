package workflow

import (
	"context"
	"errors"
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
	"github.com/PurplePotassium/cosmic-agent-tools/hal/internal/prompt"
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
	done   chan struct{} // closed when the run loop returns
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
	done := make(chan struct{})
	go func() { defer close(done); _ = m.Run(ctx) }()
	waitFor(t, "manager running", func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.runCtx != nil
	})
	return &fixture{m: m, st: st, runner: runner, repo: repo, cancel: cancel, done: done}
}

// halt models the dashboard's "stop turns" button: cancel the engine context
// and wait for the run loop to unwind (app.EngineControl.Halt).
func (f *fixture) halt(t *testing.T) {
	t.Helper()
	f.cancel()
	select {
	case <-f.done:
	case <-time.After(30 * time.Second):
		t.Fatal("timed out waiting for the run loop to stop")
	}
}

// relaunch brings the run loop back up against the SAME Manager, exactly as
// EngineControl does after a halt.
func (f *fixture) relaunch(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	f.cancel, f.done = cancel, make(chan struct{})
	go func() { defer close(f.done); _ = f.m.Run(ctx) }()
	waitFor(t, "manager running again", func() bool {
		f.m.mu.Lock()
		defer f.m.mu.Unlock()
		return f.m.runCtx != nil && f.m.runCtx.Err() == nil
	})
}

func (f *fixture) sessionCount() int {
	f.m.mu.Lock()
	defer f.m.mu.Unlock()
	return len(f.m.sessions)
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

// With auto-approval enabled, every ready stage takes the ordinary approval
// path (including its commit) and immediately opens the next stage.
func TestAutoApproveReadyStages(t *testing.T) {
	art := "---\nstub: true\n---\ncontent"
	f := newFixture(t, []scriptedTurn{
		{state: turns.TurnDone, status: ready("01"), artifact: art},
		{state: turns.TurnDone, status: ready("02"), artifact: art},
		{state: turns.TurnDone, status: ready("03"), artifact: art},
		{state: turns.TurnDone, status: ready("04"), artifact: art},
		{state: turns.TurnDone, status: ready("05"), artifact: art},
	})
	ctx := context.Background()
	if err := f.m.SetAutoValidate(ctx, false); err != nil {
		t.Fatal(err)
	}
	wf, err := f.m.Create(ctx, CreateReq{
		Title: "automatic ladder", Brief: "ship it", AutoApprove: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	f.waitStatus(t, wf.ID, domain.WorkflowCompleted)
	f.waitTurnCount(t, len(domain.TaskStageOrder))

	stages, err := f.st.WorkflowStages(ctx, wf.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, st := range stages {
		if st.Stage == domain.StageValidate {
			continue
		}
		if st.Status != domain.StageApproved || !strings.Contains(st.DecisionNote, "Automatically approved") {
			t.Fatalf("stage %s was not automatically approved: %+v", st.Stage, st)
		}
	}
}

// Auto-approval never guesses through a clarification request. Once the
// operator answers, later ready stages may continue automatically.
func TestAutoApprovePausesForClarification(t *testing.T) {
	art := "---\nstub: true\n---\ncontent"
	f := newFixture(t, []scriptedTurn{
		{state: turns.TurnDone, status: asking("Which behavior?"), finalText: "Which behavior?"},
		{state: turns.TurnDone, status: ready("01"), artifact: art},
		{state: turns.TurnDone, status: ready("02"), artifact: art},
		{state: turns.TurnDone, status: ready("03"), artifact: art},
		{state: turns.TurnDone, status: ready("04"), artifact: art},
		{state: turns.TurnDone, status: ready("05"), artifact: art},
	})
	ctx := context.Background()
	if err := f.m.SetAutoValidate(ctx, false); err != nil {
		t.Fatal(err)
	}
	wf, err := f.m.Create(ctx, CreateReq{
		Title: "clarify first", Brief: "change it", AutoApprove: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "clarification turn to settle", func() bool {
		got, err := f.st.GetWorkflow(ctx, wf.ID)
		turnRows, turnErr := f.st.ListTurns(ctx, wf.ID)
		return err == nil && turnErr == nil && got.Status == domain.WorkflowAwaitingUser &&
			len(turnRows) == 1 && turnRows[0].State == domain.TurnStateDone
	})
	got, _ := f.st.GetWorkflow(ctx, wf.ID)
	if got.Stage != domain.StageRefine {
		t.Fatalf("clarification advanced to %s; want refine", got.Stage)
	}
	if err := f.m.Message(ctx, wf.ID, "Use the existing behavior.", false); err != nil {
		t.Fatal(err)
	}
	f.waitStatus(t, wf.ID, domain.WorkflowCompleted)
}

func TestAutoApproveCanBeDisabled(t *testing.T) {
	f := newFixture(t, []scriptedTurn{
		{state: turns.TurnDone, status: ready("01"), artifact: "ready"},
	})
	wf, err := f.m.Create(context.Background(), CreateReq{
		Title: "manual gate", Brief: "review me", AutoApprove: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	got := f.waitStatus(t, wf.ID, domain.WorkflowAwaitingApproval)
	if got.Stage != domain.StageRefine {
		t.Fatalf("manual workflow advanced to %s", got.Stage)
	}
}

// The full five-stage happy path: refine asks then readies; every later
// stage readies straight away; five approvals land the workflow completed
// (and queued for validation) with the artifacts committed.
func TestFiveStageHappyPath(t *testing.T) {
	art := "---\nstub: true\n---\ncontent"
	f := newFixture(t, []scriptedTurn{
		{state: turns.TurnDone, finalText: "What exactly should the boss do?", status: asking("q1")},
		{state: turns.TurnDone, finalText: "Question saved.", status: ready("01"), artifact: art},
		{state: turns.TurnDone, finalText: "Research saved.", status: ready("02"), artifact: art},
		{state: turns.TurnDone, finalText: "Design saved.", status: ready("03"), artifact: art},
		{state: turns.TurnDone, finalText: "Plan saved.", status: ready("04"), artifact: art},
		{state: turns.TurnDone, finalText: "Implemented.", status: ready("05"), artifact: art},
	})
	ctx := context.Background()
	// Auto-validate off: this test proves the ladder alone.
	if err := f.m.SetAutoValidate(ctx, false); err != nil {
		t.Fatal(err)
	}
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

	// Approve every task stage in order; implement's approval completes.
	for i, stage := range domain.TaskStageOrder {
		got, err := f.st.GetWorkflow(ctx, wf.ID)
		if err != nil || got.Stage != stage {
			t.Fatalf("expected stage %s, got %+v (%v)", stage, got, err)
		}
		if err := f.m.Approve(ctx, wf.ID, stage, "lgtm"); err != nil {
			t.Fatalf("approve %s: %v", stage, err)
		}
		if stage == domain.StageImplement {
			break
		}
		f.waitStage(t, wf.ID, domain.TaskStageOrder[i+1], domain.WorkflowAwaitingApproval)
	}
	final := f.waitStatus(t, wf.ID, domain.WorkflowCompleted)
	if final.BaseSHA == "" {
		t.Error("base sha must be recorded at implement start")
	}

	// Task stage rows all approved; artifacts recorded. The validate row
	// stays pending — validation happens in cross-workflow runs.
	stages, _ := f.st.WorkflowStages(ctx, wf.ID)
	for _, st := range stages {
		if st.Stage == domain.StageValidate {
			if st.Status != domain.StagePending {
				t.Errorf("validate row: %s, want pending (runs own it)", st.Status)
			}
			continue
		}
		if st.Status != domain.StageApproved {
			t.Errorf("stage %s: %s", st.Stage, st.Status)
		}
		if st.Artifact == "" {
			t.Errorf("stage %s missing artifact", st.Stage)
		}
	}
	// The finished implementation is queued for the next validation run.
	pending, err := f.st.ListValidationPending(ctx)
	if err != nil || len(pending) != 1 || pending[0].ID != wf.ID {
		t.Fatalf("validation pending = %+v (%v), want [%s]", pending, err, wf.ID)
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

// Approving implement completes a task workflow — no validate stage runs
// inside it; the workflow instead lands on the validation-pending queue.
func TestImplementApprovalCompletes(t *testing.T) {
	f := newFixture(t, []scriptedTurn{
		{state: turns.TurnDone, status: ready("05"), artifact: "changelog"},
	})
	ctx := context.Background()
	if err := f.m.SetAutoValidate(ctx, false); err != nil {
		t.Fatal(err)
	}
	wf, err := f.m.Create(ctx, CreateReq{Title: "x", Brief: "y", StartStage: domain.StageImplement})
	if err != nil {
		t.Fatal(err)
	}
	f.waitStatus(t, wf.ID, domain.WorkflowAwaitingApproval)
	if err := f.m.Approve(ctx, wf.ID, domain.StageImplement, ""); err != nil {
		t.Fatal(err)
	}
	f.waitStatus(t, wf.ID, domain.WorkflowCompleted)

	// No validate turn was dispatched inside the workflow.
	f.runner.mu.Lock()
	dispatched := len(f.runner.specs)
	f.runner.mu.Unlock()
	if dispatched != 1 {
		t.Errorf("turns dispatched = %d, want 1 (implement only)", dispatched)
	}
	pending, _ := f.st.ListValidationPending(ctx)
	if len(pending) != 1 || pending[0].ID != wf.ID {
		t.Fatalf("pending = %+v, want [%s]", pending, wf.ID)
	}
}

// Intake can no longer start a workflow at validate — validation runs own
// that stage.
func TestCreateAtValidateRefused(t *testing.T) {
	f := newFixture(t, nil)
	if _, err := f.m.Create(context.Background(), CreateReq{
		Title: "x", Brief: "y", StartStage: domain.StageValidate,
	}); err == nil {
		t.Fatal("creating a workflow at validate must be refused")
	}
}

// The full validation-run lifecycle: an implemented workflow completes, a
// run covers it, approval stamps it validated and archives its artifact
// folder (recycle-bin seam) with the deletion committed.
func TestValidationRunLifecycle(t *testing.T) {
	var archived []string
	f := newFixture(t, []scriptedTurn{
		{state: turns.TurnDone, status: ready("05"), artifact: "changelog"},
		{state: turns.TurnDone, finalText: "PASS", status: ready("06"), artifact: "report"},
	}, func(cfg *Config) {
		cfg.Archive = func(path string) error {
			archived = append(archived, path)
			return os.RemoveAll(path)
		}
	})
	ctx := context.Background()
	if err := f.m.SetAutoValidate(ctx, false); err != nil {
		t.Fatal(err)
	}
	wf, err := f.m.Create(ctx, CreateReq{Title: "x", Brief: "y", StartStage: domain.StageImplement})
	if err != nil {
		t.Fatal(err)
	}
	f.waitStatus(t, wf.ID, domain.WorkflowAwaitingApproval)
	if err := f.m.Approve(ctx, wf.ID, domain.StageImplement, ""); err != nil {
		t.Fatal(err)
	}
	f.waitStatus(t, wf.ID, domain.WorkflowCompleted)

	run, err := f.m.StartValidation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if run.Kind != domain.KindValidation || run.Stage != domain.StageValidate {
		t.Fatalf("run: %+v", run)
	}
	// A second trigger while the run lives is refused.
	if _, err := f.m.StartValidation(ctx); err == nil {
		t.Fatal("second concurrent validation run must be refused")
	}
	// The opening prompt carries the target's changelog.
	f.waitTurnCount(t, 2)
	if p := f.spec(t, 1).Prompt; !strings.Contains(p, wf.ID) || !strings.Contains(p, "05-implementation.md") {
		t.Fatalf("run prompt must list the pending changelog: %q", p)
	}
	f.waitStatus(t, run.ID, domain.WorkflowAwaitingApproval)
	if err := f.m.Approve(ctx, run.ID, domain.StageValidate, "ship it"); err != nil {
		t.Fatal(err)
	}
	f.waitStatus(t, run.ID, domain.WorkflowCompleted)

	// The target is stamped validated and its folder archived + committed.
	got, _ := f.st.GetWorkflow(ctx, wf.ID)
	if got.Validated.IsZero() || got.ValidatedBy != run.ID {
		t.Fatalf("target not stamped validated: %+v", got)
	}
	if len(archived) != 1 || !strings.Contains(archived[0], wf.ID) {
		t.Fatalf("archived = %v, want the target's artifact dir", archived)
	}
	if _, err := os.Stat(filepath.Join(f.repo, ".hal", "workflows", wf.ID)); !os.IsNotExist(err) {
		t.Fatal("target artifact dir must be gone after archiving")
	}
	if log := gitLog(t, f.repo); !strings.Contains(log, "archived 1 validated workflow") {
		t.Fatalf("archive commit missing:\n%s", log)
	}
	// Nothing pending anymore.
	if pending, _ := f.st.ListValidationPending(ctx); len(pending) != 0 {
		t.Fatalf("pending after validation = %+v", pending)
	}
}

// Auto-validate: implement approval on an otherwise-idle engine opens a
// validation run by itself (the toggle defaults to on).
func TestAutoValidateTrigger(t *testing.T) {
	f := newFixture(t, []scriptedTurn{
		{state: turns.TurnDone, status: ready("05"), artifact: "changelog"},
		{state: turns.TurnDone, status: asking("run opening")},
	})
	ctx := context.Background()
	wf, err := f.m.Create(ctx, CreateReq{Title: "x", Brief: "y", StartStage: domain.StageImplement})
	if err != nil {
		t.Fatal(err)
	}
	f.waitStatus(t, wf.ID, domain.WorkflowAwaitingApproval)
	if err := f.m.Approve(ctx, wf.ID, domain.StageImplement, ""); err != nil {
		t.Fatal(err)
	}
	f.waitStatus(t, wf.ID, domain.WorkflowCompleted)
	waitFor(t, "auto validation run", func() bool {
		_, err := f.st.ActiveValidationRun(ctx)
		return err == nil
	})
	f.waitTurnCount(t, 2)
}

// Auto-validate off: implement approval completes without opening a run.
func TestAutoValidateOff(t *testing.T) {
	f := newFixture(t, []scriptedTurn{
		{state: turns.TurnDone, status: ready("05"), artifact: "changelog"},
	})
	ctx := context.Background()
	if err := f.m.SetAutoValidate(ctx, false); err != nil {
		t.Fatal(err)
	}
	wf, _ := f.m.Create(ctx, CreateReq{Title: "x", Brief: "y", StartStage: domain.StageImplement})
	f.waitStatus(t, wf.ID, domain.WorkflowAwaitingApproval)
	if err := f.m.Approve(ctx, wf.ID, domain.StageImplement, ""); err != nil {
		t.Fatal(err)
	}
	f.waitStatus(t, wf.ID, domain.WorkflowCompleted)
	if _, err := f.st.ActiveValidationRun(ctx); err == nil {
		t.Fatal("no validation run must open when the toggle is off")
	}
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

// A halt must not strand the session: the loop forgets itself on the way out,
// so the relaunched engine builds a fresh one and the operator's next message
// actually runs a turn. Before this, enqueue reused the dead session and every
// later message queued into an inbox nobody was reading — the workflow looked
// alive in the dashboard and answered nothing, forever.
func TestHaltRelaunchResumesMessages(t *testing.T) {
	f := newFixture(t, []scriptedTurn{
		{state: turns.TurnDone, finalText: "a question", status: asking("q")},
		{state: turns.TurnDone, finalText: "answered", status: asking("q2")},
	})
	ctx := context.Background()
	wf, err := f.m.Create(ctx, CreateReq{Title: "x", Brief: "y"})
	if err != nil {
		t.Fatal(err)
	}
	f.waitTurnCount(t, 1)
	f.waitStatus(t, wf.ID, domain.WorkflowAwaitingUser)
	waitFor(t, "session registered", func() bool { return f.sessionCount() == 1 })

	f.halt(t)
	if n := f.sessionCount(); n != 0 {
		t.Fatalf("halt left %d session(s) behind — a relaunched engine would reuse a dead inbox", n)
	}

	f.relaunch(t)
	if err := f.m.Message(ctx, wf.ID, "carry on", false); err != nil {
		t.Fatal(err)
	}
	f.waitTurnCount(t, 2)
	if spec := f.spec(t, 1); spec.Prompt != "carry on" {
		t.Fatalf("resumed turn prompt = %q", spec.Prompt)
	}
}

// A message that lands while the engine is down bounces — and leaves nothing
// in the transcript, so the operator never sees a message no turn will answer.
func TestMessageRejectedWhileEngineStopped(t *testing.T) {
	f := newFixture(t, []scriptedTurn{
		{state: turns.TurnDone, finalText: "a question", status: asking("q")},
	})
	ctx := context.Background()
	wf, err := f.m.Create(ctx, CreateReq{Title: "x", Brief: "y"})
	if err != nil {
		t.Fatal(err)
	}
	f.waitTurnCount(t, 1)
	f.waitStatus(t, wf.ID, domain.WorkflowAwaitingUser)
	before, _ := f.st.ListMessages(ctx, wf.ID, 0, 0)

	f.halt(t)
	err = f.m.Message(ctx, wf.ID, "are you there?", false)
	if !errors.Is(err, errEngineStopping) {
		t.Fatalf("message during a halt: err = %v, want errEngineStopping", err)
	}
	after, _ := f.st.ListMessages(ctx, wf.ID, 0, 0)
	if len(after) != len(before) {
		t.Fatalf("undeliverable message was persisted anyway (%d → %d)", len(before), len(after))
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
	// Nothing pending: the run still executes as a health check.
	wf, err := f.m.StartValidation(ctx)
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

// TestStageFragmentOverride pins the per-repo stage-prompt lookup: the
// numbered filename `hal init` seeds wins, the bare stage name still works
// for hand-rolled overrides predating the seeding, and an absent file falls
// back to the built-in text.
func TestStageFragmentOverride(t *testing.T) {
	prompts := t.TempDir()
	m := New(nil, nil, nil, nil, Config{PromptsDir: prompts})
	stages := filepath.Join(prompts, "stages")
	if err := os.MkdirAll(stages, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(stages, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	if got := m.stageFragment(domain.StageImplement); got != "" {
		t.Fatalf("no override file should read empty (built-in fallback); got %q", got)
	}
	write("implement.md", "legacy body")
	if got := m.stageFragment(domain.StageImplement); got != "legacy body" {
		t.Fatalf("bare stage name: %q", got)
	}
	write("05-implement.md", "seeded body")
	if got := m.stageFragment(domain.StageImplement); got != "seeded body" {
		t.Fatalf("seeded numbered name must win: %q", got)
	}
	// An emptied seeded file is not an override — the built-in text stands.
	write("05-implement.md", "   \n")
	if got := m.stageFragment(domain.StageImplement); got != "legacy body" {
		t.Fatalf("empty seeded file: %q", got)
	}

	// Every stage's seeded filename must be a live lookup key, or `hal init`
	// would write files no turn ever reads.
	for _, st := range domain.StageOrder {
		asset, err := prompt.StageAsset(st)
		if err != nil {
			t.Fatalf("%s: %v", st, err)
		}
		write(asset, "override-"+string(st))
		if got := m.stageFragment(st); got != "override-"+string(st) {
			t.Fatalf("%s (%s): %q", st, asset, got)
		}
	}
}

// TestBundleForPerStagePrecedence pins the resolution order: config stage
// entry (filled from the config default) < workflow base bundle < workflow
// per-stage override.
func TestBundleForPerStagePrecedence(t *testing.T) {
	m := New(nil, nil, nil, nil, Config{
		StageBundles: map[domain.WorkflowStage]domain.Bundle{
			domain.StagePlan: {Model: "cfg-plan", Effort: "low"},
		},
		DefaultBundle: domain.Bundle{Model: "cfg-default", Effort: "medium"},
	})
	wf := domain.Workflow{
		Bundle: domain.Bundle{Agent: "codex", Model: "wf-base"},
		StageBundles: map[domain.WorkflowStage]domain.Bundle{
			domain.StageResearch: {Agent: "claude", Model: "wf-research"},
			domain.StagePlan:     {Effort: "max"},
		},
	}
	// research: the per-stage model wins; effort has no override anywhere on
	// the workflow, so the config default holds.
	if b := m.bundleFor(wf, domain.StageResearch); b.Agent != "claude" || b.Model != "wf-research" || b.Effort != "medium" {
		t.Fatalf("research: %+v", b)
	}
	// plan: the workflow base model beats the config stage entry; the
	// per-stage effort beats everything.
	if b := m.bundleFor(wf, domain.StagePlan); b.Agent != "codex" || b.Model != "wf-base" || b.Effort != "max" {
		t.Fatalf("plan: %+v", b)
	}
	// implement: untouched by per-stage overrides — base model, default effort.
	if b := m.bundleFor(wf, domain.StageImplement); b.Model != "wf-base" || b.Effort != "medium" {
		t.Fatalf("implement: %+v", b)
	}

	// Without a workflow base, a stage not overridden per-stage resolves to
	// its config entry.
	wf.Bundle = domain.Bundle{}
	if b := m.bundleFor(wf, domain.StagePlan); b.Model != "cfg-plan" || b.Effort != "max" {
		t.Fatalf("plan sans base: %+v", b)
	}
}
