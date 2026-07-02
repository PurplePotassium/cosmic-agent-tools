package engine

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/gw1108/cosmic-agent-tools/workshop/internal/bus"
	"github.com/gw1108/cosmic-agent-tools/workshop/internal/domain"
	"github.com/gw1108/cosmic-agent-tools/workshop/internal/fakeagent"
	"github.com/gw1108/cosmic-agent-tools/workshop/internal/gitx"
	"github.com/gw1108/cosmic-agent-tools/workshop/internal/statedir"
	"github.com/gw1108/cosmic-agent-tools/workshop/internal/store"
)

// queueRig is a repo with a trunk, N worktree lanes, a store, and an
// integrator with an injectable gate command — the deterministic merge-queue
// test bench.
type queueRig struct {
	t     *testing.T
	repo  string
	state string
	st    *store.Store
	b     *bus.Bus
	lanes []domain.Pipeline
	ig    *Integrator
	cfg   IntegratorConfig
}

func (r *queueRig) git(dir string, args ...string) string {
	r.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		r.t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

func newQueueRig(t *testing.T, laneNames []string, gateCmd string, conflictTasks bool) *queueRig {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	r := &queueRig{t: t, repo: filepath.Join(t.TempDir(), "repo"), state: t.TempDir()}
	if err := os.MkdirAll(r.repo, 0o755); err != nil {
		t.Fatal(err)
	}
	r.git(r.repo, "init", "-q", "-b", "main")
	r.git(r.repo, "config", "user.name", "Test")
	r.git(r.repo, "config", "user.email", "t@e.st")
	if err := os.WriteFile(filepath.Join(r.repo, "README.md"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r.git(r.repo, "add", "-A")
	r.git(r.repo, "commit", "-q", "-m", "initial")

	ctx := context.Background()
	for _, name := range laneNames {
		p := domain.Pipeline{Name: name, Branch: "workshop/" + name, Dir: r.repo + "-wt-" + name, Enabled: true}
		if err := gitx.AddWorktree(ctx, r.repo, p.Dir, p.Branch, "main"); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.RemoveAll(p.Dir) })
		r.lanes = append(r.lanes, p)
	}

	st, err := store.Open(filepath.Join(r.state, "workshop.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	r.st = st
	r.b = bus.New(st)
	r.cfg = IntegratorConfig{
		RepoDir:       r.repo,
		Trunk:         "main",
		Lanes:         r.lanes,
		GateCmd:       gateCmd,
		ConflictTasks: conflictTasks,
		BranchPrefix:  "workshop/",
	}
	r.ig = NewIntegrator(r.cfg, st, r.b)
	return r
}

// laneCommit writes a file in the lane's worktree and commits.
func (r *queueRig) laneCommit(lane int, name, content, subject string) {
	r.t.Helper()
	dir := r.lanes[lane].Dir
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		r.t.Fatal(err)
	}
	r.git(dir, "add", "-A")
	r.git(dir, "commit", "-q", "-m", subject)
}

func (r *queueRig) trunkFiles() map[string]bool {
	r.t.Helper()
	out := r.git(r.repo, "ls-tree", "-r", "--name-only", "main")
	files := map[string]bool{}
	for _, f := range strings.Split(out, "\n") {
		files[strings.TrimSpace(f)] = true
	}
	return files
}

// gateBreakCmd is red iff a file named BREAK exists in the tree.
func gateBreakCmd() string {
	if runtime.GOOS == "windows" {
		return "if exist BREAK exit 1"
	}
	return "test ! -f BREAK"
}

// gateComboCmd is red iff BOTH a.txt and b.txt exist.
func gateComboCmd() string {
	if runtime.GOOS == "windows" {
		return "if exist a.txt if exist b.txt exit 1"
	}
	return "! { test -f a.txt && test -f b.txt; }"
}

func TestQueueAllGreenLands(t *testing.T) {
	r := newQueueRig(t, []string{"code", "art"}, "", false)
	ctx := context.Background()
	r.laneCommit(0, "code.txt", "x", "code work")
	r.laneCommit(1, "art.txt", "y", "art work")

	landed, err := r.ig.RunRound(ctx)
	if err != nil || landed != 2 {
		t.Fatalf("landed=%d err=%v", landed, err)
	}
	files := r.trunkFiles()
	if !files["code.txt"] || !files["art.txt"] {
		t.Fatalf("trunk files: %v", files)
	}
	// Tips recorded: a second round has nothing to do.
	landed, err = r.ig.RunRound(ctx)
	if err != nil || landed != 0 {
		t.Fatalf("second round: landed=%d err=%v", landed, err)
	}
}

func TestQueueRedAloneIsProvenCulprit(t *testing.T) {
	// "bad" comes first in rotation order, so the bisect tests it ALONE on
	// trunk — red there is PROVEN culpability, not suspicion.
	r := newQueueRig(t, []string{"bad", "good"}, gateBreakCmd(), false)
	ctx := context.Background()
	r.laneCommit(1, "fine.txt", "ok", "good work")
	r.laneCommit(0, "BREAK", "boom", "bad work")

	landed, err := r.ig.RunRound(ctx)
	if err != nil || landed != 1 {
		t.Fatalf("landed=%d err=%v", landed, err)
	}
	files := r.trunkFiles()
	if !files["fine.txt"] || files["BREAK"] {
		t.Fatalf("trunk files: %v", files)
	}
	li, _ := r.st.GetIntegration(ctx, "bad")
	if !li.Blocked || !li.ProvenCulprit || li.BlockedBy != "gate" {
		t.Fatalf("bad lane integration: %+v", li)
	}
	good, _ := r.st.GetIntegration(ctx, "good")
	if good.Blocked {
		t.Fatalf("good lane blocked: %+v", good)
	}

	// The blocked lane unblocks once its tip advances (with a fix).
	r.git(r.lanes[0].Dir, "rm", "-q", "BREAK")
	r.git(r.lanes[0].Dir, "commit", "-q", "-m", "remove the breaker")
	landed, err = r.ig.RunRound(ctx)
	if err != nil || landed != 1 {
		t.Fatalf("after fix: landed=%d err=%v", landed, err)
	}
	li, _ = r.st.GetIntegration(ctx, "bad")
	if li.Blocked {
		t.Fatalf("still blocked after advancing: %+v", li)
	}
}

func TestQueueRedInCombinationBlamesSuspectSet(t *testing.T) {
	r := newQueueRig(t, []string{"alpha", "beta"}, gateComboCmd(), false)
	ctx := context.Background()
	r.laneCommit(0, "a.txt", "a", "alpha half")
	r.laneCommit(1, "b.txt", "b", "beta half")

	landed, err := r.ig.RunRound(ctx)
	if err != nil || landed != 1 {
		t.Fatalf("landed=%d err=%v", landed, err)
	}
	// Round 0: rotation starts at alpha, so alpha lands, beta is dropped —
	// and blamed as a SUSPECT against the kept set, never proven.
	files := r.trunkFiles()
	if !files["a.txt"] || files["b.txt"] {
		t.Fatalf("trunk files: %v", files)
	}
	li, _ := r.st.GetIntegration(ctx, "beta")
	if !li.Blocked || li.ProvenCulprit || li.BlockedBy != "alpha" {
		t.Fatalf("beta integration: %+v", li)
	}
}

func TestQueueRotationSpreadsOrder(t *testing.T) {
	r := newQueueRig(t, []string{"one", "two"}, "", false)
	ctx := context.Background()

	r.laneCommit(0, "one-1.txt", "x", "one r1")
	r.laneCommit(1, "two-1.txt", "y", "two r1")
	if _, err := r.ig.RunRound(ctx); err != nil {
		t.Fatal(err)
	}
	r.laneCommit(0, "one-2.txt", "x", "one r2")
	r.laneCommit(1, "two-2.txt", "y", "two r2")
	if _, err := r.ig.RunRound(ctx); err != nil {
		t.Fatal(err)
	}

	commits, err := gitx.RecentCommits(ctx, r.repo, 10)
	if err != nil {
		t.Fatal(err)
	}
	var merges []string
	for _, c := range commits {
		if strings.HasPrefix(c.Subject, "Merge branch") {
			merges = append(merges, c.Subject)
		}
	}
	if len(merges) != 4 {
		t.Fatalf("merge commits: %v", merges)
	}
	// Newest first. Round 1 order: one, two. Round 2 rotated: two, one.
	round2Last, round1Last := merges[0], merges[2]
	if !strings.Contains(round1Last, "workshop/two") || !strings.Contains(round2Last, "workshop/one") {
		t.Fatalf("rotation not applied:\n%s", strings.Join(merges, "\n"))
	}
}

func TestQueueConflictWithoutRouteSkipsUntilAdvanced(t *testing.T) {
	r := newQueueRig(t, []string{"solo"}, "", false)
	ctx := context.Background()
	// Trunk and lane both edit README.md.
	if err := os.WriteFile(filepath.Join(r.repo, "README.md"), []byte("trunk side\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r.git(r.repo, "commit", "-q", "-am", "trunk edit")
	r.laneCommit(0, "README.md", "lane side\n", "lane edit")

	landed, err := r.ig.RunRound(ctx)
	if err != nil || landed != 0 {
		t.Fatalf("landed=%d err=%v", landed, err)
	}
	li, _ := r.st.GetIntegration(ctx, "solo")
	tip := r.git(r.lanes[0].Dir, "rev-parse", "HEAD")
	if li.LastSeenTip != tip || li.ConflictTaskID != "" {
		t.Fatalf("expected skip-until-advanced: %+v (tip %s)", li, tip)
	}
	// No task materialized.
	open, _ := r.st.ListTasks(ctx, store.TaskFilter{})
	if len(open) != 0 {
		t.Fatalf("no tasks expected: %v", open)
	}
}

func TestQueueConflictEnqueuesTaskAndParksLane(t *testing.T) {
	r := newQueueRig(t, []string{"solo"}, "", true)
	ctx := context.Background()
	if err := os.WriteFile(filepath.Join(r.repo, "README.md"), []byte("trunk side\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r.git(r.repo, "commit", "-q", "-am", "trunk edit")
	r.laneCommit(0, "README.md", "lane side\n", "lane edit")

	if _, err := r.ig.RunRound(ctx); err != nil {
		t.Fatal(err)
	}
	li, _ := r.st.GetIntegration(ctx, "solo")
	if li.ConflictTaskID == "" || li.ConflictAttempts != 1 {
		t.Fatalf("integration: %+v", li)
	}
	task, err := r.st.GetTask(ctx, li.ConflictTaskID)
	if err != nil {
		t.Fatal(err)
	}
	if task.Type != "merge-conflict" || task.Origin != domain.OriginSystem || task.Backlog != domain.MainBacklog {
		t.Fatalf("task: %+v", task)
	}
	for _, key := range []string{MetaLaneBranch, MetaLaneTip, MetaTrunkTip, MetaResolveBranch, MetaResolveDir} {
		if task.Meta[key] == "" {
			t.Fatalf("meta %s missing: %v", key, task.Meta)
		}
	}
	if !strings.Contains(task.Meta[MetaFiles], "README.md") {
		t.Fatalf("conflicted files: %v", task.Meta)
	}

	// Parked: another round does nothing (task still open).
	landed, err := r.ig.RunRound(ctx)
	if err != nil || landed != 0 {
		t.Fatalf("parked lane ran: landed=%d err=%v", landed, err)
	}
}

// The full causal chain: conflict -> task -> agent resolution in an
// ephemeral worktree -> engine verification -> gated landing on trunk.
func TestQueueConflictResolutionLands(t *testing.T) {
	r := newQueueRig(t, []string{"solo"}, "", true)
	ctx := context.Background()
	if err := os.WriteFile(filepath.Join(r.repo, "README.md"), []byte("trunk side\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r.git(r.repo, "commit", "-q", "-am", "trunk edit")
	r.laneCommit(0, "README.md", "lane side\n", "lane edit")
	laneTip := r.git(r.lanes[0].Dir, "rev-parse", "HEAD")

	if _, err := r.ig.RunRound(ctx); err != nil {
		t.Fatal(err)
	}
	li, _ := r.st.GetIntegration(ctx, "solo")
	if li.ConflictTaskID == "" {
		t.Fatal("no conflict task")
	}

	// A worker (any pipeline routing merge-conflict) resolves it.
	worker := r.conflictWorker(t, "resolve")
	res, err := worker.RunPass(ctx)
	if err != nil || res != PassRan {
		t.Fatalf("resolution pass: res=%v err=%v", res, err)
	}
	task, _ := r.st.GetTask(ctx, li.ConflictTaskID)
	if task.Status != domain.TaskDone {
		t.Fatalf("task after resolution: %+v", task)
	}
	if !gitx.BranchExists(ctx, r.repo, task.Meta[MetaResolveBranch]) {
		t.Fatal("resolution branch missing")
	}

	// Next round lands the resolution.
	landed, err := r.ig.RunRound(ctx)
	if err != nil || landed != 1 {
		t.Fatalf("landing round: landed=%d err=%v", landed, err)
	}
	content, err := os.ReadFile(filepath.Join(r.repo, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	// The fake resolver keeps both sides.
	if !strings.Contains(string(content), "trunk side") || !strings.Contains(string(content), "lane side") {
		t.Fatalf("resolved content: %q", content)
	}
	li, _ = r.st.GetIntegration(ctx, "solo")
	if li.ConflictTaskID != "" || li.ConflictAttempts != 0 || li.LastSeenTip != laneTip {
		t.Fatalf("integration after landing: %+v (laneTip %s)", li, laneTip)
	}
	if gitx.BranchExists(ctx, r.repo, task.Meta[MetaResolveBranch]) {
		t.Fatal("resolution branch not cleaned up")
	}
}

// Resolution keeps failing -> task goes stuck -> the integrator falls back
// to skip-until-advanced and cleans up.
func TestQueueResolutionFailureFallsBack(t *testing.T) {
	r := newQueueRig(t, []string{"solo"}, "", true)
	ctx := context.Background()
	if err := os.WriteFile(filepath.Join(r.repo, "README.md"), []byte("trunk side\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r.git(r.repo, "commit", "-q", "-am", "trunk edit")
	r.laneCommit(0, "README.md", "lane side\n", "lane edit")
	laneTip := r.git(r.lanes[0].Dir, "rev-parse", "HEAD")

	if _, err := r.ig.RunRound(ctx); err != nil {
		t.Fatal(err)
	}
	li, _ := r.st.GetIntegration(ctx, "solo")

	worker := r.conflictWorker(t, "crash")
	for i := 0; i < 3; i++ { // MaxTaskAttempts drives it to stuck
		// Clear the retry backoff so the next attempt is claimable now.
		if task, err := r.st.GetTask(ctx, li.ConflictTaskID); err == nil && task.Status == domain.TaskOpen {
			r.clearBackoff(t, task.ID)
		}
		if res, err := worker.RunPass(ctx); err != nil || (res != PassRan && res != PassIdle) {
			t.Fatalf("attempt %d: res=%v err=%v", i+1, res, err)
		}
	}
	task, _ := r.st.GetTask(ctx, li.ConflictTaskID)
	if task.Status != domain.TaskStuck {
		t.Fatalf("task should be stuck: %+v", task)
	}

	landed, err := r.ig.RunRound(ctx)
	if err != nil || landed != 0 {
		t.Fatalf("fallback round: landed=%d err=%v", landed, err)
	}
	li, _ = r.st.GetIntegration(ctx, "solo")
	if li.ConflictTaskID != "" || li.LastSeenTip != laneTip {
		t.Fatalf("fallback integration: %+v", li)
	}
	if gitx.BranchExists(ctx, r.repo, task.Meta[MetaResolveBranch]) {
		t.Fatal("resolution branch survived fallback")
	}
}

// conflictWorker builds a worker whose fake agent runs the given behavior,
// claiming merge-conflict tasks from the shared backlog.
func (r *queueRig) conflictWorker(t *testing.T, behavior string) *Worker {
	t.Helper()
	scenarioPath := filepath.Join(r.state, "scenario.json")
	if err := statedir.WriteJSON(scenarioPath, fakeagent.Scenario{Behavior: behavior}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WORKSHOP_FAKE_BIN", os.Args[0])
	t.Setenv("WORKSHOP_FAKE_SCENARIO", scenarioPath)
	goal := filepath.Join(r.state, "GOAL.md")
	_ = os.WriteFile(goal, []byte("goal"), 0o644)

	cfg := WorkerConfig{
		Pipeline: domain.Pipeline{
			Name:        "fixer",
			Bundle:      domain.Bundle{Agent: "fake"},
			TaskTypes:   []string{"merge-conflict"},
			DrainMain:   true,
			Enabled:     true,
			Branch:      "workshop/fixer",
			PassTimeout: 2 * time.Minute,
		},
		RepoDir:      r.repo, // worktree admin runs against the main repo
		StateDir:     statedir.PipelineDir(r.state, "fixer"),
		LogDir:       filepath.Join(r.state, "logs", "fixer"),
		GoalPath:     goal,
		SpiceEnabled: false,
		RetryBackoff: time.Millisecond,
	}
	return NewWorker(cfg, r.st, r.b)
}

func (r *queueRig) clearBackoff(t *testing.T, id string) {
	t.Helper()
	// FailTask sets not_before; tests shrink it via a tiny RetryBackoff,
	// so a short sleep suffices.
	time.Sleep(5 * time.Millisecond)
}
