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

	"github.com/gw1108/cosmic-agent-tools/workshop/internal/bus"
	"github.com/gw1108/cosmic-agent-tools/workshop/internal/domain"
	"github.com/gw1108/cosmic-agent-tools/workshop/internal/driver"
	"github.com/gw1108/cosmic-agent-tools/workshop/internal/fakeagent"
	"github.com/gw1108/cosmic-agent-tools/workshop/internal/gitx"
	"github.com/gw1108/cosmic-agent-tools/workshop/internal/statedir"
	"github.com/gw1108/cosmic-agent-tools/workshop/internal/store"
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
	drv, err := driver.New("fake")
	if err != nil {
		t.Fatal(err)
	}
	w := NewWorker(cfg, drv, st, bus.New(st))
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
