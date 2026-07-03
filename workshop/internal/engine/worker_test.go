package engine

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/bus"
	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/domain"
	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/fakeagent"
	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/gitx"
	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/statedir"
	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/store"
)

// The test binary doubles as the fake agent.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "_fake-agent" {
		os.Exit(fakeagent.Main())
	}
	os.Exit(m.Run())
}

type testRig struct {
	t      *testing.T
	repo   string
	state  string
	st     *store.Store
	worker *Worker
	cfg    WorkerConfig
}

func newRig(t *testing.T, scenario fakeagent.Scenario, tweak func(*WorkerConfig)) *testRig {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	repo := t.TempDir()
	git := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q", "-b", "main")
	git("config", "user.name", "Test")
	git("config", "user.email", "test@example.com")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", "-A")
	git("commit", "-q", "-m", "initial")

	stateRoot := t.TempDir()
	goalPath := filepath.Join(stateRoot, "GOAL.md")
	if err := os.WriteFile(goalPath, []byte("Test goal.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	scenarioPath := filepath.Join(stateRoot, "scenario.json")
	if err := statedir.WriteJSON(scenarioPath, scenario); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WORKSHOP_FAKE_BIN", os.Args[0])
	t.Setenv("WORKSHOP_FAKE_SCENARIO", scenarioPath)

	st, err := store.Open(filepath.Join(stateRoot, "workshop.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	cfg := WorkerConfig{
		Pipeline: domain.Pipeline{
			Name:        "main",
			Bundle:      domain.Bundle{Agent: "fake"},
			DrainMain:   true,
			Enabled:     true,
			PassTimeout: 2 * time.Minute,
		},
		RepoDir:      repo,
		StateDir:     statedir.PipelineDir(stateRoot, "main"),
		LogDir:       filepath.Join(stateRoot, "logs", "main"),
		GoalPath:     goalPath,
		SpiceEnabled: false,
		IdlePoll:     50 * time.Millisecond,
	}
	if tweak != nil {
		tweak(&cfg)
	}
	w := NewWorker(cfg, st, bus.New(st))
	return &testRig{t: t, repo: repo, state: stateRoot, st: st, worker: w, cfg: cfg}
}

func (r *testRig) addTask(title string) *domain.Task {
	r.t.Helper()
	task, err := r.st.AddTask(context.Background(), &domain.Task{Title: title}, false)
	if err != nil {
		r.t.Fatal(err)
	}
	return task
}

func (r *testRig) commits() []gitx.Commit {
	r.t.Helper()
	commits, err := gitx.RecentCommits(context.Background(), r.repo, 20)
	if err != nil {
		r.t.Fatal(err)
	}
	return commits
}

// The Phase 2 milestone: a bounded run drains two tasks into two commits.
func TestBoundedRunCommits(t *testing.T) {
	r := newRig(t, fakeagent.Scenario{Behavior: "happy"}, nil)
	ctx := context.Background()
	r.addTask("first task")
	r.addTask("second task")

	if err := r.worker.Loop(ctx, 2, false); err != nil {
		t.Fatal(err)
	}

	commits := r.commits()
	if len(commits) != 3 { // initial + 2 passes
		t.Fatalf("commits: %v", commits)
	}
	if commits[0].Subject != "ws(main) iter 2 [fake]" || commits[1].Subject != "ws(main) iter 1 [fake]" {
		t.Fatalf("subjects: %q, %q", commits[0].Subject, commits[1].Subject)
	}

	open, _ := r.st.ListTasks(ctx, store.TaskFilter{Statuses: []domain.TaskStatus{domain.TaskOpen, domain.TaskClaimed}})
	if len(open) != 0 {
		t.Fatalf("backlog not drained: %d left", len(open))
	}
	comps, _ := r.st.ListCompletions(ctx, 10)
	if len(comps) != 2 {
		t.Fatalf("completions: %d", len(comps))
	}
	passes, _ := r.st.RecentPasses(ctx, "main", 10)
	if len(passes) != 2 || passes[0].CommitSHA == "" || passes[0].Outcome != domain.OutcomeDone {
		t.Fatalf("passes: %+v", passes)
	}
	// The engine-owned snapshot got written for the pass.
	var views []statedir.TaskView
	if err := statedir.ReadJSON(filepath.Join(r.cfg.StateDir, statedir.BacklogFile), &views); err != nil {
		t.Fatal(err)
	}
}

func TestProposalsIngestedWithDedupe(t *testing.T) {
	r := newRig(t, fakeagent.Scenario{Behavior: "happy", Proposals: []domain.Proposal{
		{Title: "existing task"},                          // dup of an open task
		{Title: "shiny new idea", Type: "code"},           // accepted
		{Title: "for a ghost", Backlog: "nonexistent"},    // unknown pipeline -> shared
		{Title: "one too many", Detail: "over the limit"}, // beyond maxAccept=2
	}}, nil)
	ctx := context.Background()
	r.addTask("work me")
	r.addTask("existing task")

	if err := r.worker.Loop(ctx, 1, false); err != nil {
		t.Fatal(err)
	}

	open, _ := r.st.ListTasks(ctx, store.TaskFilter{Statuses: []domain.TaskStatus{domain.TaskOpen}})
	titles := map[string]string{}
	for _, task := range open {
		titles[task.Title] = task.Backlog
	}
	if _, dup := titles["existing task"]; !dup {
		t.Fatal("pre-existing open task went missing")
	}
	if backlog, ok := titles["shiny new idea"]; !ok || backlog != domain.MainBacklog {
		t.Fatalf("new proposal: %v", titles)
	}
	if backlog, ok := titles["for a ghost"]; !ok || backlog != domain.MainBacklog {
		t.Fatalf("unknown-pipeline proposal should land in shared: %v", titles)
	}
	if _, over := titles["one too many"]; over {
		t.Fatal("maxAccept=2 not enforced")
	}
	// 2 open originally - 1 done + 2 accepted proposals = 3
	if len(open) != 3 {
		t.Fatalf("open tasks: %d (%v)", len(open), titles)
	}
}

func TestBlockedTaskRetriesWithBackoff(t *testing.T) {
	r := newRig(t, fakeagent.Scenario{Behavior: "blocked"}, nil)
	ctx := context.Background()
	task := r.addTask("hard problem")

	res, err := r.worker.RunPass(ctx)
	if err != nil || res != PassRan {
		t.Fatalf("res=%v err=%v", res, err)
	}
	got, err := r.st.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != domain.TaskOpen || got.Attempts != 1 || !got.NotBefore.After(time.Now()) {
		t.Fatalf("task after block: %+v", got)
	}
	// Backoff means the very next pass has nothing eligible.
	res, err = r.worker.RunPass(ctx)
	if err != nil || res != PassIdle {
		t.Fatalf("expected idle during backoff, got %v err=%v", res, err)
	}
	passes, _ := r.st.RecentPasses(ctx, "main", 10)
	if len(passes) != 1 || passes[0].Outcome != domain.OutcomeBlocked {
		t.Fatalf("passes: %+v", passes)
	}
}

func TestCrashTripsBreaker(t *testing.T) {
	r := newRig(t, fakeagent.Scenario{Behavior: "crash"}, func(cfg *WorkerConfig) {
		cfg.Pipeline.Invent = true // keep passes coming despite no tasks
		cfg.BreakerLimit = 2
	})
	ctx := context.Background()

	err := r.worker.Loop(ctx, 10, false)
	if err != ErrHalted {
		t.Fatalf("err=%v, want ErrHalted", err)
	}
	reason, _ := r.st.HaltedReason(ctx, "main")
	if reason != HaltBreaker {
		t.Fatalf("halt reason %q", reason)
	}
	passes, _ := r.st.RecentPasses(ctx, "main", 10)
	if len(passes) != 2 {
		t.Fatalf("expected exactly BreakerLimit passes, got %d", len(passes))
	}
	for _, p := range passes {
		if p.Failure != domain.FailExit {
			t.Fatalf("pass failure: %+v", p)
		}
	}
}

func TestAuthFailureHaltsAndReleasesTask(t *testing.T) {
	r := newRig(t, fakeagent.Scenario{Behavior: "auth"}, nil)
	ctx := context.Background()
	task := r.addTask("innocent task")

	err := r.worker.Loop(ctx, 5, false)
	if err != ErrHalted {
		t.Fatalf("err=%v, want ErrHalted", err)
	}
	reason, _ := r.st.HaltedReason(ctx, "main")
	if reason != HaltAuth {
		t.Fatalf("halt reason %q", reason)
	}
	got, _ := r.st.GetTask(ctx, task.ID)
	if got.Status != domain.TaskOpen || got.Attempts != 0 {
		t.Fatalf("task should be released without penalty: %+v", got)
	}
}

func TestWedgedPassIsKilled(t *testing.T) {
	r := newRig(t, fakeagent.Scenario{Behavior: "sleep", SleepMs: 60_000}, func(cfg *WorkerConfig) {
		cfg.Pipeline.PassTimeout = 2 * time.Second
	})
	ctx := context.Background()
	r.addTask("sleepy task")

	start := time.Now()
	res, err := r.worker.RunPass(ctx)
	if err != nil || res != PassRan {
		t.Fatalf("res=%v err=%v", res, err)
	}
	if elapsed := time.Since(start); elapsed > 30*time.Second {
		t.Fatalf("wedge kill took %v", elapsed)
	}
	passes, _ := r.st.RecentPasses(ctx, "main", 5)
	if len(passes) != 1 || passes[0].Failure != domain.FailTimeout {
		t.Fatalf("passes: %+v", passes)
	}
}

func TestSilentAgentCountsAsFailedAttempt(t *testing.T) {
	r := newRig(t, fakeagent.Scenario{Behavior: "silent"}, nil)
	ctx := context.Background()
	task := r.addTask("quiet task")

	res, err := r.worker.RunPass(ctx)
	if err != nil || res != PassRan {
		t.Fatalf("res=%v err=%v", res, err)
	}
	got, _ := r.st.GetTask(ctx, task.ID)
	if got.Status != domain.TaskOpen || got.Attempts != 1 {
		t.Fatalf("silent pass should count an attempt: %+v", got)
	}
	passes, _ := r.st.RecentPasses(ctx, "main", 5)
	if passes[0].Outcome != domain.OutcomeNoChange {
		t.Fatalf("outcome: %+v", passes[0])
	}
}

func TestInventPassRecordsCompletion(t *testing.T) {
	r := newRig(t, fakeagent.Scenario{Behavior: "happy"}, func(cfg *WorkerConfig) {
		cfg.Pipeline.Invent = true
	})
	ctx := context.Background()

	if err := r.worker.Loop(ctx, 1, false); err != nil {
		t.Fatal(err)
	}
	comps, _ := r.st.ListCompletions(ctx, 5)
	if len(comps) != 1 || !strings.Contains(comps[0].Title, "invented") {
		t.Fatalf("completions: %+v", comps)
	}
	if got := r.commits(); len(got) != 2 {
		t.Fatalf("commits: %v", got)
	}
}

// Routing: a per-task pin must reach the spawned pass (visible in the log
// header) and the resolved agent must stamp the commit subject.
func TestRoutingPinReachesPass(t *testing.T) {
	r := newRig(t, fakeagent.Scenario{Behavior: "happy"}, func(cfg *WorkerConfig) {
		cfg.Types = map[string]domain.Bundle{
			"code": {Agent: "fake", Model: "routed-code-model", Effort: "high"},
		}
		cfg.Vocabulary = map[string]bool{"code": true}
	})
	ctx := context.Background()
	// Untyped, but the classifier should type it "code" from the title.
	r.addTask("fix the crash in the loader")

	if err := r.worker.Loop(ctx, 1, false); err != nil {
		t.Fatal(err)
	}
	logDir := r.cfg.LogDir
	data, err := os.ReadFile(filepath.Join(logDir, "iter-000001.log"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "model  : routed-code-model") {
		t.Fatalf("routed model missing from log header:\n%s", data)
	}
	// The task was classified before claiming.
	comps, _ := r.st.ListCompletions(ctx, 5)
	if len(comps) != 1 {
		t.Fatalf("completions: %+v", comps)
	}
}

// A pinned bundle beats the routing table.
func TestPinnedTaskBeatsTypeRoute(t *testing.T) {
	r := newRig(t, fakeagent.Scenario{Behavior: "happy"}, func(cfg *WorkerConfig) {
		cfg.Types = map[string]domain.Bundle{
			"code": {Agent: "fake", Model: "table-model"},
		}
		cfg.Vocabulary = map[string]bool{"code": true}
	})
	ctx := context.Background()
	if _, err := r.st.AddTask(ctx, &domain.Task{
		Title: "fix the bug",
		Type:  "code",
		Pin:   domain.Bundle{Model: "pinned-model"},
	}, false); err != nil {
		t.Fatal(err)
	}
	if err := r.worker.Loop(ctx, 1, false); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(r.cfg.LogDir, "iter-000001.log"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "model  : pinned-model") {
		t.Fatalf("pin lost:\n%s", data)
	}
}

// Blind drivers: repeated failures with no self-report raise the
// suspected-auth alert (auth is otherwise invisible).
func TestBlindDriverSuspectAuth(t *testing.T) {
	r := newRig(t, fakeagent.Scenario{Behavior: "crash"}, func(cfg *WorkerConfig) {
		cfg.Pipeline.Invent = true
		cfg.BreakerLimit = 5
		cfg.SuspectAuthAfter = 2
	})
	t.Setenv("WORKSHOP_FAKE_BLIND", "1")
	ctx := context.Background()

	for i := 0; i < 2; i++ {
		if res, err := r.worker.RunPass(ctx); err != nil || res != PassRan {
			t.Fatalf("pass %d: res=%v err=%v", i+1, res, err)
		}
	}
	evs, err := r.st.EventsSince(ctx, 0, 200)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, ev := range evs {
		if ev.Type == "auth.suspected" {
			found = true
		}
		if ev.Type == "auth.halt" {
			t.Fatal("blind driver must never hard-halt on auth keywords")
		}
	}
	if !found {
		t.Fatal("auth.suspected event missing")
	}
}

func TestHaltedPipelineRefusesToRun(t *testing.T) {
	r := newRig(t, fakeagent.Scenario{Behavior: "happy"}, nil)
	ctx := context.Background()
	r.addTask("waiting task")
	if err := r.st.SetHalted(ctx, "main", HaltBreaker); err != nil {
		t.Fatal(err)
	}
	res, err := r.worker.RunPass(ctx)
	if err != nil || res != PassHalted {
		t.Fatalf("res=%v err=%v", res, err)
	}
	// Clearing the halt lets it run again.
	if err := r.st.SetHalted(ctx, "main", ""); err != nil {
		t.Fatal(err)
	}
	res, err = r.worker.RunPass(ctx)
	if err != nil || res != PassRan {
		t.Fatalf("after clear: res=%v err=%v", res, err)
	}
	_ = fmt.Sprint() // keep fmt import if assertions change
}

// TestAuthReAnchoring pins the word-boundary fix: substrings like "author"
// (git's "Author identity unknown") and "OAuth" in ordinary output must not
// read as auth failures, while real auth-shaped lines still must.
func TestAuthReAnchoring(t *testing.T) {
	trips := []string{
		"Error: unauthorized - credential expired, please sign-in again",
		"401 Unauthorized",
		"authentication failed for host",
		"Not authorized to access this resource",
		"please re-authenticate and retry",
		"no credential helper configured",
		"keyring locked",
		"auth error",
	}
	for _, line := range trips {
		if !isAuthFailure([]string{line}) {
			t.Errorf("should trip auth detection: %q", line)
		}
	}
	clean := []string{
		"Author identity unknown",
		"fatal: unable to auto-detect email address (got 'Author identity unknown')",
		"Co-Authored-By: Claude <noreply@anthropic.com>",
		"refactored the OAuthor helper module",
		"wrote docs for the authorship tracking feature",
		"all tests passed",
	}
	for _, line := range clean {
		if isAuthFailure([]string{line}) {
			t.Errorf("must NOT trip auth detection: %q", line)
		}
	}
}

// TestLiveBundleOverrideAppliesNextPass pins the A1 behavior: a store-backed
// override set mid-run switches the model on the very next pass, no restart.
func TestLiveBundleOverrideAppliesNextPass(t *testing.T) {
	r := newRig(t, fakeagent.Scenario{Behavior: "happy"}, nil)
	ctx := context.Background()

	r.addTask("first task")
	if res, err := r.worker.RunPass(ctx); err != nil || res != PassRan {
		t.Fatalf("pass 1: res=%v err=%v", res, err)
	}

	if err := r.st.SetPipelineBundle(ctx, "main", domain.Bundle{Model: "switched-model"}); err != nil {
		t.Fatal(err)
	}
	r.addTask("second task")
	if res, err := r.worker.RunPass(ctx); err != nil || res != PassRan {
		t.Fatalf("pass 2: res=%v err=%v", res, err)
	}

	events, err := r.st.EventsSince(ctx, 0, 200)
	if err != nil {
		t.Fatal(err)
	}
	var models []string
	for _, ev := range events {
		if ev.Type == "pass.started" {
			models = append(models, fmt.Sprint(ev.Payload["model"]))
		}
	}
	if len(models) != 2 || models[0] == "switched-model" || models[1] != "switched-model" {
		t.Fatalf("pass models = %v; want [<empty>, switched-model]", models)
	}

	// Clearing restores the configured bundle.
	if err := r.st.SetPipelineBundle(ctx, "main", domain.Bundle{}); err != nil {
		t.Fatal(err)
	}
	if b, err := r.st.PipelineBundle(ctx, "main"); err != nil || !b.IsZero() {
		t.Fatalf("override not cleared: %+v err=%v", b, err)
	}
}
