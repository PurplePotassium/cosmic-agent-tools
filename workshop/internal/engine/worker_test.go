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
			Name:            "main",
			Bundle:          domain.Bundle{Agent: "fake"},
			DrainMain:       true,
			AcceptProposals: true,
			Enabled:         true,
			PassTimeout:     2 * time.Minute,
		},
		RepoDir:             repo,
		StateDir:            statedir.PipelineDir(stateRoot, "main"),
		LogDir:              filepath.Join(stateRoot, "logs", "main"),
		GoalPath:            goalPath,
		SpiceEnabled:        false,
		PlanningPercent:     75,
		PlanningProposalMax: defaultPlanningProposalMax,
		FollowUpProposalMax: defaultFollowUpProposalMax,
		IdlePoll:            50 * time.Millisecond,
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
	if commits[0].Subject != "ws(main) iter 2 [fake]: second task" || commits[1].Subject != "ws(main) iter 1 [fake]: first task" {
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

// With [export] configured, every finished pass mirrors its evidence files
// into the export folder — including failed passes, which are the ones an
// audit cares about most.
func TestPassExportsEvidence(t *testing.T) {
	exportDir := filepath.Join(t.TempDir(), "audit", "main")
	r := newRig(t, fakeagent.Scenario{Behavior: "happy"}, func(cfg *WorkerConfig) {
		cfg.ExportDir = exportDir
	})
	ctx := context.Background()
	r.addTask("exported task")
	if err := r.worker.Loop(ctx, 1, false); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(exportDir, "iter-000001.log"))
	if err != nil {
		t.Fatalf("pass log not exported: %v", err)
	}
	if !strings.Contains(string(data), "=== ws(main) iter 1 ===") {
		t.Fatalf("exported log is not the pass log:\n%s", data)
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
	// The capped-out proposal must not vanish silently: a proposals.dropped
	// event names the lost idea so the operator can rescue it.
	evs, err := r.st.EventsSince(ctx, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	var dropped map[string]any
	for _, ev := range evs {
		if ev.Type == "proposals.dropped" {
			dropped = ev.Payload
		}
	}
	if dropped == nil {
		t.Fatal("no proposals.dropped event for the capped-out proposal")
	}
	if fmt.Sprint(dropped["count"]) != "1" || !strings.Contains(fmt.Sprint(dropped["titles"]), "one too many") {
		t.Fatalf("proposals.dropped payload: %v", dropped)
	}
}

// Idle planning passes are proposal-only work. They may enqueue up to five
// deduplicated follow-ups, while ordinary assigned-task follow-ups retain the
// stricter two-item cap covered above.
func TestInventPassUsesConfiguredPlanningProposalCap(t *testing.T) {
	const planningProposalMax = 3
	props := make([]domain.Proposal, planningProposalMax+1)
	for i := range props {
		props[i] = domain.Proposal{Title: fmt.Sprintf("planned task %d", i+1)}
	}
	r := newRig(t, fakeagent.Scenario{Behavior: "happy", NoEdit: true, Proposals: props}, func(cfg *WorkerConfig) {
		cfg.Pipeline.Invent = true
		cfg.PlanningPercent = 100
		cfg.PlanningProposalMax = planningProposalMax
	})
	ctx := context.Background()

	if err := r.worker.Loop(ctx, 1, false); err != nil {
		t.Fatal(err)
	}

	open, _ := r.st.ListTasks(ctx, store.TaskFilter{Statuses: []domain.TaskStatus{domain.TaskOpen}})
	if len(open) != planningProposalMax {
		t.Fatalf("accepted %d idle-pass proposals, want %d: %+v", len(open), planningProposalMax, open)
	}
	for _, task := range open {
		if task.Title == "planned task 4" {
			t.Fatalf("proposal over idle-pass cap was ingested: %+v", open)
		}
	}
	evs, err := r.st.EventsSince(ctx, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range evs {
		if ev.Type == "proposals.dropped" && fmt.Sprint(ev.Payload["cap"]) == fmt.Sprint(planningProposalMax) {
			return
		}
	}
	t.Fatal("no proposals.dropped event for idle-pass proposal over the configured cap")
}

func TestAssignedTaskUsesConfiguredFollowUpProposalCap(t *testing.T) {
	const followUpProposalMax = 1
	r := newRig(t, fakeagent.Scenario{Behavior: "happy", Proposals: []domain.Proposal{
		{Title: "first follow-up"}, {Title: "second follow-up"},
	}}, func(cfg *WorkerConfig) {
		cfg.FollowUpProposalMax = followUpProposalMax
	})
	ctx := context.Background()
	r.addTask("assigned task")

	if err := r.worker.Loop(ctx, 1, false); err != nil {
		t.Fatal(err)
	}
	open, _ := r.st.ListTasks(ctx, store.TaskFilter{Statuses: []domain.TaskStatus{domain.TaskOpen}})
	if len(open) != followUpProposalMax || open[0].Title != "first follow-up" {
		t.Fatalf("configured follow-up cap ignored: %+v", open)
	}
}

func TestPlanningPassNeedsAtLeastOneNewProposal(t *testing.T) {
	r := newRig(t, fakeagent.Scenario{Behavior: "happy", NoEdit: true}, func(cfg *WorkerConfig) {
		cfg.Pipeline.Invent = true
		cfg.PlanningPercent = 100
	})
	ctx := context.Background()

	if res, err := r.worker.RunPass(ctx); err != nil || res != PassRan {
		t.Fatalf("res=%v err=%v", res, err)
	}
	completions, _ := r.st.ListCompletions(ctx, 5)
	if len(completions) != 0 {
		t.Fatalf("empty planning pass must not complete: %+v", completions)
	}
	passes, _ := r.st.RecentPasses(ctx, "main", 1)
	if len(passes) != 1 || passes[0].Outcome != domain.OutcomeNoChange {
		t.Fatalf("empty planning pass outcome: %+v", passes)
	}
}

// An expand task's proposals are its deliverable: every enumerated item is
// ingested — the freeform cap does not apply — even on a proposal-refusing
// (drain-mode) pipeline, and the task completes without any repo edit.
func TestExpandTaskIngestsAllProposals(t *testing.T) {
	r := newRig(t, fakeagent.Scenario{Behavior: "happy", NoEdit: true, Proposals: []domain.Proposal{
		{Title: "update Whip"}, {Title: "update Garlic"}, {Title: "update Fire Wand"}, {Title: "update Runetracer"},
	}}, func(cfg *WorkerConfig) {
		cfg.Pipeline.AcceptProposals = false // drain must not block operator-ordered expansion
	})
	ctx := context.Background()
	task, err := r.st.AddTask(ctx, &domain.Task{
		Title: "for each weapon, add an update task",
		Meta:  map[string]string{domain.ExpandMetaKey: "true"},
	}, false)
	if err != nil {
		t.Fatal(err)
	}

	if err := r.worker.Loop(ctx, 1, false); err != nil {
		t.Fatal(err)
	}

	got, err := r.st.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != domain.TaskDone {
		t.Fatalf("expand task should complete: %+v", got)
	}
	open, _ := r.st.ListTasks(ctx, store.TaskFilter{Statuses: []domain.TaskStatus{domain.TaskOpen}})
	if len(open) != 4 {
		t.Fatalf("all 4 enumerated proposals should be ingested, got %d: %+v", len(open), open)
	}
	evs, _ := r.st.EventsSince(ctx, 0, 1000)
	for _, ev := range evs {
		if ev.Type == "proposals.dropped" {
			t.Fatalf("nothing may be dropped from an expansion: %v", ev.Payload)
		}
	}
}

// An expand enumeration larger than the per-pass cap must not complete the
// task: the overflow would be stranded forever (the ExpandBlock prompt even
// promises "never trim the list yourself"). The pass accepts a cap's worth,
// then the task is retried — accepted items dedupe, so each retry drains up
// to another cap's worth until nothing is dropped.
func TestExpandTaskOverCapRetriesInsteadOfCompleting(t *testing.T) {
	props := make([]domain.Proposal, expandProposalCap+3)
	for i := range props {
		props[i] = domain.Proposal{Title: fmt.Sprintf("update item %03d", i)}
	}
	r := newRig(t, fakeagent.Scenario{Behavior: "happy", NoEdit: true, Proposals: props}, nil)
	ctx := context.Background()
	task, err := r.st.AddTask(ctx, &domain.Task{
		Title: "for each item, add an update task",
		Meta:  map[string]string{domain.ExpandMetaKey: "true"},
	}, false)
	if err != nil {
		t.Fatal(err)
	}

	res, err := r.worker.RunPass(ctx)
	if err != nil || res != PassRan {
		t.Fatalf("res=%v err=%v", res, err)
	}
	got, _ := r.st.GetTask(ctx, task.ID)
	if got.Status != domain.TaskOpen || got.Attempts != 1 {
		t.Fatalf("over-cap expansion must retry the expand task, not complete it: %+v", got)
	}
	open, _ := r.st.ListTasks(ctx, store.TaskFilter{Statuses: []domain.TaskStatus{domain.TaskOpen}})
	accepted := 0
	for _, o := range open {
		if o.ID != task.ID {
			accepted++
		}
	}
	if accepted != expandProposalCap {
		t.Fatalf("accepted %d proposals, want the full cap %d", accepted, expandProposalCap)
	}
}

// An expand task that reports done without writing any proposals enqueued
// nothing: the engine fails and retries it instead of completing it.
func TestExpandTaskWithNoProposalsFails(t *testing.T) {
	r := newRig(t, fakeagent.Scenario{Behavior: "happy", NoEdit: true}, nil)
	ctx := context.Background()
	task, err := r.st.AddTask(ctx, &domain.Task{
		Title: "for each weapon, add an update task",
		Meta:  map[string]string{domain.ExpandMetaKey: "true"},
	}, false)
	if err != nil {
		t.Fatal(err)
	}

	res, err := r.worker.RunPass(ctx)
	if err != nil || res != PassRan {
		t.Fatalf("res=%v err=%v", res, err)
	}
	got, _ := r.st.GetTask(ctx, task.ID)
	if got.Status != domain.TaskOpen || got.Attempts != 1 {
		t.Fatalf("done-with-no-proposals must retry the expand task: %+v", got)
	}
	comps, _ := r.st.ListCompletions(ctx, 5)
	if len(comps) != 0 {
		t.Fatalf("no completion may be recorded: %+v", comps)
	}
	passes, _ := r.st.RecentPasses(ctx, "main", 5)
	if len(passes) != 1 || passes[0].Outcome != domain.OutcomeNoChange {
		t.Fatalf("passes: %+v", passes)
	}
}

// The wiring from Task.Meta to the prompt: an expand task's composed prompt
// must carry the expansion directive instead of the plain task block only.
func TestExpandTaskPromptCarriesDirective(t *testing.T) {
	r := newRig(t, fakeagent.Scenario{Behavior: "happy"}, nil)
	ctx := context.Background()
	task, err := r.st.AddTask(ctx, &domain.Task{
		Title: "for each weapon, add an update task",
		Meta:  map[string]string{domain.ExpandMetaKey: "true"},
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	full, _, _, _, err := r.worker.preparePass(ctx, task)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(full, "THIS IS AN EXPANSION TASK") {
		t.Fatal("expand directive missing from the composed prompt")
	}
}

// A syntactically corrupt proposals.json must not fail the pass — but it
// must not vanish silently either: the worker publishes a proposals.invalid
// event so the malformed agent contract is visible on the dashboard.
func TestCorruptProposalsEmitsEventAndPassSucceeds(t *testing.T) {
	r := newRig(t, fakeagent.Scenario{Behavior: "happy", RawProposals: `{"not": "an array"`}, nil)
	ctx := context.Background()
	r.addTask("work me")

	if err := r.worker.Loop(ctx, 1, false); err != nil {
		t.Fatal(err)
	}

	passes, _ := r.st.RecentPasses(ctx, "main", 5)
	if len(passes) != 1 || passes[0].Outcome != domain.OutcomeDone {
		t.Fatalf("pass must succeed despite corrupt proposals: %+v", passes)
	}
	evs, err := r.st.EventsSince(ctx, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, ev := range evs {
		if ev.Type == "proposals.invalid" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("no proposals.invalid event was published for a corrupt proposals.json")
	}
	// The garbage fabricated no tasks: only the drained original remains done.
	open, _ := r.st.ListTasks(ctx, store.TaskFilter{Statuses: []domain.TaskStatus{domain.TaskOpen, domain.TaskClaimed}})
	if len(open) != 0 {
		t.Fatalf("corrupt proposals fabricated tasks: %v", open)
	}
}

// Drain-mode pipelines (AcceptProposals = false) may only shrink the
// backlog: a finished pass's proposals.json follow-ups must be dropped.
func TestDrainModeSkipsProposalIngestion(t *testing.T) {
	r := newRig(t, fakeagent.Scenario{Behavior: "happy", Proposals: []domain.Proposal{
		{Title: "shiny new idea", Type: "code"},
	}}, func(cfg *WorkerConfig) {
		cfg.Pipeline.AcceptProposals = false
	})
	ctx := context.Background()
	r.addTask("work me")

	if err := r.worker.Loop(ctx, 1, false); err != nil {
		t.Fatal(err)
	}
	open, _ := r.st.ListTasks(ctx, store.TaskFilter{Statuses: []domain.TaskStatus{domain.TaskOpen}})
	for _, task := range open {
		if task.Title == "shiny new idea" {
			t.Fatalf("proposal ingested despite AcceptProposals=false: %+v", open)
		}
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

// TestLiveModeOverrideAppliesNextPass is the mode dial's counterpart to
// TestLiveBundleOverrideAppliesNextPass: a dashboard-set goal/discover/drain
// override must flip Invent and AcceptProposals on the very next pass, no
// restart, and clearing it must restore the configured mode.
func TestLiveModeOverrideAppliesNextPass(t *testing.T) {
	r := newRig(t, fakeagent.Scenario{Behavior: "happy", Proposals: []domain.Proposal{
		{Title: "shiny idea", Type: "code"},
	}}, func(cfg *WorkerConfig) {
		cfg.Pipeline.Invent = false
		cfg.Pipeline.AcceptProposals = false // configured "drain"
	})
	ctx := context.Background()

	// Empty backlog + configured drain => idle, no agent spawned.
	if res, err := r.worker.RunPass(ctx); err != nil || res != PassIdle {
		t.Fatalf("pass 1 (configured drain): res=%v err=%v", res, err)
	}

	if err := r.st.SetPipelineMode(ctx, "main", "goal"); err != nil {
		t.Fatal(err)
	}
	if res, err := r.worker.RunPass(ctx); err != nil || res != PassRan {
		t.Fatalf("pass 2 (live goal override): res=%v err=%v", res, err)
	}
	comps, _ := r.st.ListCompletions(ctx, 5)
	if len(comps) != 1 || !strings.Contains(comps[0].Title, "invented") {
		t.Fatalf("goal override should invent: completions=%+v", comps)
	}
	open, _ := r.st.ListTasks(ctx, store.TaskFilter{Statuses: []domain.TaskStatus{domain.TaskOpen}})
	var proposed *domain.Task
	for _, task := range open {
		if task.Title == "shiny idea" {
			proposed = task
		}
	}
	if proposed == nil {
		t.Fatalf("goal override should also accept proposals: %+v", open)
	}
	// Remove the ingested proposal so pass 3's backlog is genuinely empty —
	// otherwise the drain fallback would still claim it and run.
	if err := r.st.DeleteTask(ctx, proposed.ID); err != nil {
		t.Fatal(err)
	}

	// Clearing restores the configured (drain) mode.
	if err := r.st.SetPipelineMode(ctx, "main", ""); err != nil {
		t.Fatal(err)
	}
	if mode, err := r.st.PipelineMode(ctx, "main"); err != nil || mode != "" {
		t.Fatalf("override not cleared: %q err=%v", mode, err)
	}
	if res, err := r.worker.RunPass(ctx); err != nil || res != PassIdle {
		t.Fatalf("pass 3 (cleared, back to configured drain): res=%v err=%v", res, err)
	}
}

// resolvePersonality is otherwise a pure function of WorkerConfig — exercise
// its modes directly rather than through a full pass. It does need a real
// store (nil panics on the live-override lookup), so each case gets its own
// pipeline name to keep the shared store's overrides from bleeding across
// cases.
func TestResolvePersonality(t *testing.T) {
	roster := []string{"Edgar Allan Poe", "Bill Gates"}
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "workshop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	none := NewWorker(WorkerConfig{Pipeline: domain.Pipeline{Name: "none", Personality: ""}, PersonalityEnabled: true, Personalities: roster}, st, nil)
	if got := none.resolvePersonality(ctx); got != "" {
		t.Fatalf("empty selector should be a no-op, got %q", got)
	}

	fixed := NewWorker(WorkerConfig{Pipeline: domain.Pipeline{Name: "fixed", Personality: "Bill Gates"}, PersonalityEnabled: true, Personalities: roster}, st, nil)
	if got := fixed.resolvePersonality(ctx); got != "Bill Gates" {
		t.Fatalf("fixed selector = %q, want the pinned name", got)
	}

	disabled := NewWorker(WorkerConfig{Pipeline: domain.Pipeline{Name: "disabled", Personality: "Bill Gates"}, PersonalityEnabled: false, Personalities: roster}, st, nil)
	if got := disabled.resolvePersonality(ctx); got != "" {
		t.Fatalf("the [personality] master switch off must force none, got %q", got)
	}

	random := NewWorker(WorkerConfig{Pipeline: domain.Pipeline{Name: "random", Personality: "random"}, PersonalityEnabled: true, Personalities: roster}, st, nil)
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		p := random.resolvePersonality(ctx)
		if p != "Edgar Allan Poe" && p != "Bill Gates" {
			t.Fatalf("random draw %q not in roster", p)
		}
		seen[p] = true
	}
	if len(seen) != 2 {
		t.Fatalf("random should eventually draw both roster entries, saw: %v", seen)
	}

	randomEmpty := NewWorker(WorkerConfig{Pipeline: domain.Pipeline{Name: "random-empty", Personality: "random"}, PersonalityEnabled: true}, st, nil)
	if got := randomEmpty.resolvePersonality(ctx); got != "" {
		t.Fatalf("\"random\" with an empty roster should degrade to none, got %q", got)
	}

	// The live dashboard override wins over the configured selector, and an
	// explicit "none" override forces no personality even though the
	// pipeline is configured with a fixed one — the dropdown's "clear
	// override" button is the only way back to "Bill Gates" here.
	overridden := NewWorker(WorkerConfig{Pipeline: domain.Pipeline{Name: "overridden", Personality: "Bill Gates"}, PersonalityEnabled: true, Personalities: roster}, st, nil)
	if err := st.SetPipelinePersonality(ctx, "overridden", "Edgar Allan Poe"); err != nil {
		t.Fatal(err)
	}
	if got := overridden.resolvePersonality(ctx); got != "Edgar Allan Poe" {
		t.Fatalf("live override should win over the configured selector, got %q", got)
	}
	if err := st.SetPipelinePersonality(ctx, "overridden", "none"); err != nil {
		t.Fatal(err)
	}
	if got := overridden.resolvePersonality(ctx); got != "" {
		t.Fatalf("explicit \"none\" override should force no personality, got %q", got)
	}
}

// The resolved personality must reach the composed prompt AND the pass log
// header — the log line is the operator's window into which flavor a pass
// ran under.
func TestPersonalityReachesPassLog(t *testing.T) {
	r := newRig(t, fakeagent.Scenario{Behavior: "happy"}, func(cfg *WorkerConfig) {
		cfg.Pipeline.Personality = "Bill Gates"
		cfg.PersonalityEnabled = true
	})
	ctx := context.Background()
	r.addTask("fix the bug")
	if err := r.worker.Loop(ctx, 1, false); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(r.cfg.LogDir, "iter-000001.log"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "persona: Bill Gates") {
		t.Fatalf("personality missing from log header:\n%s", data)
	}
	passes, err := r.st.RecentPasses(ctx, "main", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(passes) != 1 || passes[0].Personality != "Bill Gates" {
		t.Fatalf("personality not recorded on the pass row: %+v", passes)
	}
}
