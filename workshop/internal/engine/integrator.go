package engine

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/gw1108/cosmic-agent-tools/workshop/internal/bus"
	"github.com/gw1108/cosmic-agent-tools/workshop/internal/domain"
	"github.com/gw1108/cosmic-agent-tools/workshop/internal/gitx"
	"github.com/gw1108/cosmic-agent-tools/workshop/internal/store"
)

// IntegratorConfig wires the merge queue.
type IntegratorConfig struct {
	RepoDir string // the MAIN checkout — the integrator is its only writer
	Trunk   string
	Lanes   []domain.Pipeline // worktree lanes (Name + Branch set)

	GateCmd     string
	GateDir     string // repo-relative
	GateTimeout time.Duration

	// ConflictTasks: on a merge conflict, enqueue a merge-conflict task
	// (pinned by SHA) instead of only skip-until-advanced. Enabled by the
	// app when a merge-conflict route exists.
	ConflictTasks       bool
	ConflictRetryBudget int // resolution attempts per lane tip (default 2)

	BranchPrefix string // e.g. "workshop/"
	Interval     time.Duration
}

// Integrator is the Bors-style merge queue: --no-ff merges in rotation, gate
// the combined result, bisect out regressors. Conflicts are never
// machine-resolved here — they become agent tasks (or skip until the lane
// advances). It never force-pushes; trunk is local, so the worst case is
// always "a lane didn't land".
type Integrator struct {
	cfg   IntegratorConfig
	st    *store.Store
	bus   *bus.Bus
	round int
}

// NewIntegrator builds the integrator.
func NewIntegrator(cfg IntegratorConfig, st *store.Store, b *bus.Bus) *Integrator {
	if cfg.ConflictRetryBudget <= 0 {
		cfg.ConflictRetryBudget = 2
	}
	if cfg.Interval <= 0 {
		cfg.Interval = time.Minute
	}
	return &Integrator{cfg: cfg, st: st, bus: b}
}

// Loop runs rounds until ctx cancels.
func (ig *Integrator) Loop(ctx context.Context) {
	for {
		if _, err := ig.RunRound(ctx); err != nil && ctx.Err() != nil {
			return
		}
		if !sleepCtx(ctx, ig.cfg.Interval) {
			return
		}
	}
}

type pendingLane struct {
	pipeline   string
	branch     string // branch to merge this round (lane branch, or its resolution branch)
	tip        string
	laneTip    string // the LANE tip this entry represents (== tip unless resolution)
	resolution bool
}

// RunRound executes one queue round. Returns how many lanes landed.
func (ig *Integrator) RunRound(ctx context.Context) (int, error) {
	repo := ig.cfg.RepoDir

	// Hygiene: a Ctrl-C'd previous round must not wedge this one.
	_, _ = gitx.CleanStaleLock(ctx, repo, time.Minute)
	_ = gitx.MergeAbort(ctx, repo)
	_ = gitx.PruneWorktrees(ctx, repo)

	// Protect the operator: this round will merge and possibly reset
	// --hard. Versioned intent edits (.workshop/**) get their own commit;
	// ANY other dirty file means a human is mid-edit in the main checkout
	// — skip the whole round rather than destroy their work.
	if paths, err := gitx.StatusPorcelain(ctx, repo); err == nil && len(paths) > 0 {
		intentOnly := true
		for _, p := range paths {
			if p != ".workshop" && !strings.HasPrefix(filepath.ToSlash(p), ".workshop/") {
				intentOnly = false
				break
			}
		}
		if !intentOnly {
			ig.event(ctx, "integration.skipped", "", map[string]any{
				"why": "main checkout has uncommitted changes", "files": paths,
			})
			return 0, nil
		}
		if _, err := gitx.CommitAll(ctx, repo, "workshop: goal/config update"); err != nil {
			return 0, err
		}
	}

	if cur, err := gitx.CurrentBranch(ctx, repo); err != nil {
		return 0, err
	} else if cur != ig.cfg.Trunk {
		if err := gitx.CheckoutBranch(ctx, repo, ig.cfg.Trunk); err != nil {
			return 0, fmt.Errorf("integrator: checkout trunk: %w", err)
		}
	}

	pending, err := ig.collectPending(ctx)
	if err != nil || len(pending) == 0 {
		return 0, err
	}

	// Deterministic rotation: the bisect keeps earlier lanes when a
	// combination goes red, so a fixed order would always punish the same
	// lane. No RNG — reproducible.
	shift := ig.round % len(pending)
	ig.round++
	rotated := append(append([]pendingLane{}, pending[shift:]...), pending[:shift]...)

	preMerge, err := gitx.RevParse(ctx, repo, "HEAD")
	if err != nil {
		return 0, err
	}

	// Merge everything pending, --no-ff so one lane peels with reset HEAD^.
	var merged []pendingLane
	for _, lane := range rotated {
		conflict, err := gitx.Merge(ctx, repo, lane.branch, true)
		if err != nil {
			_ = gitx.MergeAbort(ctx, repo)
			continue
		}
		if conflict {
			files, _ := gitx.UnmergedFiles(ctx, repo)
			_ = gitx.MergeAbort(ctx, repo)
			ig.handleConflict(ctx, lane, files)
			continue
		}
		merged = append(merged, lane)
	}
	if len(merged) == 0 {
		return 0, nil
	}

	// Gate the combined stack.
	if green, out := ig.gate(ctx); green {
		for _, lane := range merged {
			ig.land(ctx, lane)
		}
		ig.event(ctx, "integration.landed", "", map[string]any{
			"lanes": laneNames(merged), "count": len(merged),
		})
		return len(merged), nil
	} else {
		ig.event(ctx, "gate.red", "", map[string]any{"where": "combined", "tail": tailLines(out, 20)})
	}

	// Red: bisect — reset, re-apply one at a time, drop the first regressor.
	if err := gitx.ResetHard(ctx, repo, preMerge); err != nil {
		return 0, err
	}
	landed := 0
	var kept []pendingLane
	for _, lane := range merged {
		conflict, err := gitx.Merge(ctx, repo, lane.branch, true)
		if err != nil || conflict {
			_ = gitx.MergeAbort(ctx, repo)
			// Conflicts only atop the kept set: skip until it advances.
			ig.markSeen(ctx, lane)
			ig.event(ctx, "integration.dropped", lane.pipeline, map[string]any{"why": "conflict-in-combination"})
			continue
		}
		if green, out := ig.gate(ctx); green {
			ig.land(ctx, lane)
			kept = append(kept, lane)
			landed++
			continue
		} else {
			_ = out
		}
		// This lane regresses the kept set (or trunk itself): peel its
		// merge commit and block it.
		if err := gitx.ResetHard(ctx, repo, "HEAD^"); err != nil {
			return landed, err
		}
		li, _ := ig.st.GetIntegration(ctx, lane.pipeline)
		li.LastSeenTip = lane.laneTip
		li.Blocked = true
		li.ProvenCulprit = len(kept) == 0 // red ALONE on trunk: proven, not just suspected
		if li.ProvenCulprit {
			li.BlockedBy = "gate"
		} else {
			li.BlockedBy = strings.Join(laneNames(kept), "+")
		}
		_ = ig.st.PutIntegration(ctx, li)
		ig.event(ctx, "integration.dropped", lane.pipeline, map[string]any{
			"why": "gate-red", "provenCulprit": li.ProvenCulprit, "blockedBy": li.BlockedBy,
		})
	}
	return landed, nil
}

// collectPending decides which branches enter this round.
func (ig *Integrator) collectPending(ctx context.Context) ([]pendingLane, error) {
	var pending []pendingLane
	for _, p := range ig.cfg.Lanes {
		li, err := ig.st.GetIntegration(ctx, p.Name)
		if err != nil {
			return nil, err
		}

		// A lane with an open conflict task is parked — unless the task
		// finished, in which case its RESOLUTION branch is what lands.
		if li.ConflictTaskID != "" {
			if lane, ok := ig.resolutionPending(ctx, p, li); ok {
				pending = append(pending, lane)
			}
			continue
		}

		if !gitx.BranchExists(ctx, ig.cfg.RepoDir, p.Branch) {
			continue
		}
		tip, err := gitx.RevParse(ctx, ig.cfg.RepoDir, p.Branch)
		if err != nil {
			continue
		}
		if tip == li.LastSeenTip {
			continue // nothing new (also how blocked lanes stay skipped)
		}
		ahead, err := gitx.AheadCount(ctx, ig.cfg.RepoDir, ig.cfg.Trunk, p.Branch)
		if err != nil || ahead == 0 {
			continue
		}
		if li.Blocked {
			// Tip advanced past the blocked one: give it another chance.
			li.Blocked = false
			li.BlockedBy = ""
			li.ProvenCulprit = false
			_ = ig.st.PutIntegration(ctx, li)
		}
		pending = append(pending, pendingLane{pipeline: p.Name, branch: p.Branch, tip: tip, laneTip: tip})
	}
	return pending, nil
}

// resolutionPending checks a parked lane's conflict task: done -> its
// resolution branch is pending; failed/stuck -> fall back to
// skip-until-advanced and clean up.
func (ig *Integrator) resolutionPending(ctx context.Context, p domain.Pipeline, li *domain.LaneIntegration) (pendingLane, bool) {
	task, err := ig.st.GetTask(ctx, li.ConflictTaskID)
	if err != nil {
		// Task vanished: unpark.
		li.ConflictTaskID = ""
		_ = ig.st.PutIntegration(ctx, li)
		return pendingLane{}, false
	}
	switch task.Status {
	case domain.TaskDone:
		branch := task.Meta[MetaResolveBranch]
		if !gitx.BranchExists(ctx, ig.cfg.RepoDir, branch) {
			li.ConflictTaskID = ""
			_ = ig.st.PutIntegration(ctx, li)
			return pendingLane{}, false
		}
		tip, err := gitx.RevParse(ctx, ig.cfg.RepoDir, branch)
		if err != nil {
			return pendingLane{}, false
		}
		return pendingLane{
			pipeline: p.Name, branch: branch, tip: tip,
			laneTip: task.Meta[MetaLaneTip], resolution: true,
		}, true
	case domain.TaskStuck, domain.TaskFailed, domain.TaskCancelled:
		// Resolution failed for good: skip the lane until it advances.
		_ = gitx.DeleteBranch(ctx, ig.cfg.RepoDir, task.Meta[MetaResolveBranch], true)
		li.LastSeenTip = task.Meta[MetaLaneTip]
		li.ConflictTaskID = ""
		li.ConflictAttempts = 0
		_ = ig.st.PutIntegration(ctx, li)
		ig.event(ctx, "conflict.abandoned", p.Name, map[string]any{
			"lane": p.Branch, "task": task.ID,
		})
		return pendingLane{}, false
	default:
		return pendingLane{}, false // open/claimed: still being worked
	}
}

// handleConflict reacts to a lane branch conflicting with trunk.
func (ig *Integrator) handleConflict(ctx context.Context, lane pendingLane, files []string) {
	li, _ := ig.st.GetIntegration(ctx, lane.pipeline)

	trunkTip, err := gitx.RevParse(ctx, ig.cfg.RepoDir, ig.cfg.Trunk)
	if err != nil {
		return
	}

	if !ig.cfg.ConflictTasks || li.ConflictAttempts >= ig.cfg.ConflictRetryBudget {
		// Skip-until-advanced (the prior-art behavior, and the fallback
		// once the retry budget is spent).
		li.LastSeenTip = lane.laneTip
		li.ConflictAttempts = 0
		_ = ig.st.PutIntegration(ctx, li)
		ig.event(ctx, "integration.conflict", lane.pipeline, map[string]any{
			"lane": lane.branch, "files": files, "action": "skip-until-advanced",
		})
		return
	}

	short := lane.laneTip
	if len(short) > 7 {
		short = short[:7]
	}
	resolveBranch := ig.cfg.BranchPrefix + "resolve/" + lane.pipeline + "-" + short
	resolveDir := ig.cfg.RepoDir + "-wt-resolve-" + lane.pipeline + "-" + short

	task, err := ig.st.AddTask(ctx, &domain.Task{
		Backlog: domain.MainBacklog,
		Type:    "merge-conflict",
		Title:   fmt.Sprintf("Resolve merge conflict: %s vs trunk", lane.branch),
		Detail: fmt.Sprintf("Branch %s conflicts with %s in: %s",
			lane.branch, ig.cfg.Trunk, strings.Join(files, ", ")),
		Origin: domain.OriginSystem,
		Meta: map[string]string{
			MetaLane:          lane.pipeline,
			MetaLaneBranch:    lane.branch,
			MetaLaneTip:       lane.laneTip,
			MetaTrunkTip:      trunkTip,
			MetaFiles:         strings.Join(files, ","),
			MetaResolveBranch: resolveBranch,
			MetaResolveDir:    resolveDir,
		},
	}, true) // conflicts gate a whole lane: front of the shared backlog
	if err != nil {
		return
	}
	li.ConflictTaskID = task.ID
	li.ConflictAttempts++
	_ = ig.st.PutIntegration(ctx, li)
	ig.event(ctx, "conflict.enqueued", lane.pipeline, map[string]any{
		"lane": lane.branch, "task": task.ID, "files": files, "attempt": li.ConflictAttempts,
	})
}

// land records a lane (or its resolution) as integrated.
func (ig *Integrator) land(ctx context.Context, lane pendingLane) {
	li, _ := ig.st.GetIntegration(ctx, lane.pipeline)
	li.LastSeenTip = lane.laneTip
	li.Blocked = false
	li.BlockedBy = ""
	li.ProvenCulprit = false
	if lane.resolution {
		// The resolution landed: retire its branch and the parked state.
		_ = gitx.DeleteBranch(ctx, ig.cfg.RepoDir, lane.branch, true)
		li.ConflictTaskID = ""
		li.ConflictAttempts = 0
	}
	_ = ig.st.PutIntegration(ctx, li)
}

func (ig *Integrator) markSeen(ctx context.Context, lane pendingLane) {
	li, _ := ig.st.GetIntegration(ctx, lane.pipeline)
	li.LastSeenTip = lane.laneTip
	_ = ig.st.PutIntegration(ctx, li)
}

func (ig *Integrator) gate(ctx context.Context) (bool, string) {
	dir := ig.cfg.RepoDir
	if ig.cfg.GateDir != "" && ig.cfg.GateDir != "." {
		dir = filepath.Join(dir, ig.cfg.GateDir)
	}
	return runGate(ctx, dir, ig.cfg.GateCmd, ig.cfg.GateTimeout)
}

func (ig *Integrator) event(ctx context.Context, typ, pipeline string, payload map[string]any) {
	if ig.bus == nil {
		return
	}
	ig.bus.Publish(ctx, domain.Event{Type: typ, Pipeline: pipeline, Payload: payload})
}

func laneNames(lanes []pendingLane) []string {
	names := make([]string, len(lanes))
	for i, l := range lanes {
		names[i] = l.pipeline
	}
	return names
}
