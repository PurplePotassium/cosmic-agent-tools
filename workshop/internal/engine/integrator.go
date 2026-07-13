package engine

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/bus"
	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/domain"
	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/gitx"
	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/store"
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

// GreenRef is the git ref recording the last trunk state a gate proved green
// (or the pre-merge baseline before any round ran). Workers sync their lanes
// from THIS ref, never from live trunk: mid-round trunk holds unvetted
// combined merges for the whole gate duration and may be reset --hard, so a
// lane syncing live trunk could absorb later-bisected-out commits and then be
// blamed as "proven culprit" for work that was never its own.
const GreenRef = "refs/workshop/green"

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
		if _, err := ig.RunRound(ctx); err != nil {
			if ctx.Err() != nil {
				return
			}
			// A persistently failing queue must not be an invisible no-op:
			// workers keep committing to lanes while nothing ever lands.
			// Bounded runs surface this through the supervisor; the live
			// loop only has the event stream.
			ig.event(ctx, "integration.error", "", map[string]any{"op": "round", "error": err.Error()})
		}
		if !sleepCtx(ctx, ig.cfg.Interval) {
			return
		}
	}
}

// intentCommitSubject is the subject RunRound gives the operator's dirty
// .workshop/** edits. reconcileTrunk keys on it: intent commits ride ABOVE
// crash merges and must be replayed over the reconciled trunk, never treated
// as the "human commit" that stops the unvetted-merge strip.
const intentCommitSubject = "workshop: goal/config update"

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
	intentDirty := false
	if paths, err := gitx.StatusPorcelain(ctx, repo); err == nil && len(paths) > 0 {
		intentDirty = true
		for _, p := range paths {
			if p != ".workshop" && !strings.HasPrefix(filepath.ToSlash(p), ".workshop/") {
				intentDirty = false
				break
			}
		}
		if !intentDirty {
			ig.event(ctx, "integration.skipped", "", map[string]any{
				"why": "main checkout has uncommitted changes", "files": paths,
			})
			return 0, nil
		}
	}

	if cur, err := gitx.CurrentBranch(ctx, repo); err != nil {
		return 0, err
	} else if cur != ig.cfg.Trunk {
		// Checkout BEFORE the intent commit: dirty .workshop files ride
		// along, so a goal edit made while another branch was checked out
		// lands on trunk, not on that branch.
		if err := gitx.CheckoutBranch(ctx, repo, ig.cfg.Trunk); err != nil {
			if intentDirty {
				ig.event(ctx, "integration.skipped", "", map[string]any{
					"why": "cannot switch to trunk with pending .workshop edits", "error": err.Error(),
				})
				return 0, nil
			}
			return 0, fmt.Errorf("integrator: checkout trunk: %w", err)
		}
	}
	if intentDirty {
		if _, err := gitx.CommitAll(ctx, repo, intentCommitSubject); err != nil {
			return 0, err
		}
	}

	// Baseline the green ref on first contact: pre-round trunk is what
	// lanes forked from, so it is the safest sync point until a gate says
	// otherwise.
	if !gitx.RefExists(ctx, repo, GreenRef) {
		ig.recordGreen(ctx)
	}

	// A previous round that died between its merges and the gate verdict
	// left unvetted workshop merge commits on trunk. Landing on top of them
	// would silently bless ungated work — and the next recordGreen would
	// bake them into every lane's sync point. Strip them before anything
	// else sees this round.
	if err := ig.reconcileTrunk(ctx); err != nil {
		return 0, err
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
			// Non-conflict failure (lock contention, odd repo state):
			// surface it and retry next round — never silently. The tip is
			// NOT marked seen, so the lane stays pending.
			_ = gitx.MergeAbort(ctx, repo)
			ig.event(ctx, "integration.merge_failed", lane.pipeline, map[string]any{
				"lane": lane.branch, "error": err.Error(),
			})
			continue
		}
		if conflict {
			files, _ := gitx.UnmergedFiles(ctx, repo)
			_ = gitx.MergeAbort(ctx, repo)
			ig.handleConflict(ctx, lane, files, preMerge)
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
		ig.recordGreen(ctx)
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
		if err != nil {
			// Non-conflict failure: do NOT markSeen — that would strand the
			// lane's committed work until a new commit appears. Surface and
			// retry next round.
			_ = gitx.MergeAbort(ctx, repo)
			ig.event(ctx, "integration.merge_failed", lane.pipeline, map[string]any{
				"lane": lane.branch, "error": err.Error(),
			})
			continue
		}
		if conflict {
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
		li := ig.integrationState(ctx, lane.pipeline)
		if li == nil {
			continue // bookkeeping skipped; the lane re-bisects next round
		}
		li.LastSeenTip = lane.laneTip
		li.Blocked = true
		li.ProvenCulprit = len(kept) == 0 // red ALONE on trunk: proven, not just suspected
		if li.ProvenCulprit {
			li.BlockedBy = "gate"
		} else {
			li.BlockedBy = strings.Join(laneNames(kept), "+")
		}
		ig.retireResolution(ctx, lane, li)
		_ = ig.st.PutIntegration(ctx, li)
		ig.event(ctx, "integration.dropped", lane.pipeline, map[string]any{
			"why": "gate-red", "provenCulprit": li.ProvenCulprit, "blockedBy": li.BlockedBy,
		})
	}
	// The bisect leaves trunk at the kept set, which the gate proved green
	// lane by lane (or at preMerge, the previous green, when nothing kept).
	ig.recordGreen(ctx)
	return landed, nil
}

// recordGreen points GreenRef at the current trunk HEAD.
func (ig *Integrator) recordGreen(ctx context.Context) {
	if head, err := gitx.RevParse(ctx, ig.cfg.RepoDir, "HEAD"); err == nil {
		_ = gitx.UpdateRef(ctx, ig.cfg.RepoDir, GreenRef, head)
	}
}

// integrationState fetches a lane's integration row for a read-modify-write.
// On a store error (busy DB, cancelled ctx) it returns nil after surfacing an
// event: callers must skip their bookkeeping for the round — the lane simply
// retries next round — rather than write through a nil row and panic the
// integrator mid-merge.
func (ig *Integrator) integrationState(ctx context.Context, pipeline string) *domain.LaneIntegration {
	li, err := ig.st.GetIntegration(ctx, pipeline)
	if err != nil {
		ig.event(ctx, "integration.error", pipeline, map[string]any{
			"op": "get-lane-state", "error": err.Error(),
		})
		return nil
	}
	return li
}

// retireResolution deletes a resolution branch and unparks its lane. EVERY
// terminal path for a resolution lane must run this: a parked lane whose
// ConflictTaskID points at a finished task is otherwise re-offered each round
// — re-gating a proven-red resolution forever, or minting a fresh conflict
// task every time the branch re-conflicts.
func (ig *Integrator) retireResolution(ctx context.Context, lane pendingLane, li *domain.LaneIntegration) {
	if !lane.resolution {
		return
	}
	_ = gitx.DeleteBranch(ctx, ig.cfg.RepoDir, lane.branch, true)
	li.ConflictTaskID = ""
}

// reconcileTrunk strips unvetted workshop merge commits a crashed round left
// on trunk. It walks first-parent from HEAD toward GreenRef, peeling only
// commits that are 2-parent merges of workshop-prefixed branches, and stops
// at the first human/intent commit. Stripped lanes get their seen-tip
// forgotten so their (still committed) work re-enters the queue and lands
// through the gate like any other round.
func (ig *Integrator) reconcileTrunk(ctx context.Context) error {
	repo := ig.cfg.RepoDir
	green, err := gitx.RevParse(ctx, repo, GreenRef)
	if err != nil {
		return nil //nolint:nilerr // no green ref yet: nothing to reconcile against
	}
	head, err := gitx.RevParse(ctx, repo, "HEAD")
	if err != nil || head == green {
		return nil //nolint:nilerr // unparseable/empty HEAD: nothing to reconcile
	}
	mergePrefix := "Merge branch '" + ig.cfg.BranchPrefix
	target := head
	var strippedBranches []string
	var intents []string // intent commits above/between crash merges, newest first
walk:
	for i := 0; target != green && i < 100; i++ {
		parents, subject, err := gitx.CommitInfo(ctx, repo, target)
		if err != nil {
			break
		}
		switch {
		case len(parents) == 2 && strings.HasPrefix(subject, mergePrefix):
			strippedBranches = append(strippedBranches, mergeSubjectBranch(subject))
			target = parents[0]
		case len(parents) == 1 && subject == intentCommitSubject:
			// Our own goal/config commit — RunRound creates it BEFORE this
			// reconcile runs, so it routinely sits on top of the very crash
			// merges we're here to strip. Walk past it and replay it below;
			// treating it as a stop commit would bury the unvetted merges
			// forever (they'd never re-land through the gate).
			intents = append(intents, target)
			target = parents[0]
		default:
			break walk // human commit: everything below it stays
		}
	}
	if len(strippedBranches) == 0 {
		return nil // only intent/human commits above green: nothing unvetted
	}
	if err := gitx.ResetHard(ctx, repo, target); err != nil {
		return fmt.Errorf("integrator: reconcile trunk: %w", err)
	}
	// Replay the operator's goal/config commits over the reconciled trunk —
	// they are legitimate work the reset just removed.
	for i := len(intents) - 1; i >= 0; i-- { // oldest first
		if err := gitx.CherryPick(ctx, repo, intents[i]); err != nil {
			// A conflicting replay must not destroy operator edits: abort
			// the pick and put trunk back exactly as found. This round then
			// degrades to the old skip-nothing behavior instead of losing
			// the goal edit.
			_ = gitx.CherryPickAbort(ctx, repo)
			if rerr := gitx.ResetHard(ctx, repo, head); rerr != nil {
				return fmt.Errorf("integrator: reconcile rollback: %v (during replay: %v)", rerr, err)
			}
			ig.event(ctx, "integration.skipped", "", map[string]any{
				"why": "cannot replay goal/config commit over reconciled trunk", "error": err.Error(),
			})
			return nil
		}
	}
	// Forget the stripped lanes' seen-tips: a crashed round may have already
	// marked them landed, which would otherwise strand their work (0 ahead
	// never happens again — the commits are gone from trunk).
	for _, branch := range strippedBranches {
		for _, p := range ig.cfg.Lanes {
			if p.Branch != branch && !strings.HasPrefix(branch, ig.cfg.BranchPrefix+"resolve/"+p.Name+"-") {
				continue
			}
			if li := ig.integrationState(ctx, p.Name); li != nil && li.LastSeenTip != "" {
				li.LastSeenTip = ""
				_ = ig.st.PutIntegration(ctx, li)
			}
		}
	}
	ig.event(ctx, "integration.reconciled", "", map[string]any{
		"stripped": strippedBranches, "resetTo": target,
	})
	return nil
}

// mergeSubjectBranch extracts X from "Merge branch 'X'" / "Merge branch 'X' into Y".
func mergeSubjectBranch(subject string) string {
	rest, ok := strings.CutPrefix(subject, "Merge branch '")
	if !ok {
		return ""
	}
	if i := strings.IndexByte(rest, '\''); i >= 0 {
		return rest[:i]
	}
	return ""
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
	if errors.Is(err, store.ErrNotFound) {
		// Task vanished (operator deleted it): unpark.
		li.ConflictTaskID = ""
		_ = ig.st.PutIntegration(ctx, li)
		return pendingLane{}, false
	}
	if err != nil {
		// Transient store error: stay parked and retry next round. Unparking
		// here would mint a DUPLICATE conflict task for the same lane tip —
		// two passes sharing one resolve worktree, each destroying the
		// other's work in cleanup.
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

// handleConflict reacts to a branch conflicting with trunk during phase 1.
// trunkPin is the pre-round trunk SHA the conflict task gets pinned to — NOT
// live trunk, which mid-round holds other lanes' unvetted merges that the
// gate may reset away (see GreenRef): pinning those would let bisected-out
// commits ride back in through a resolution, or leave the pin unreachable.
func (ig *Integrator) handleConflict(ctx context.Context, lane pendingLane, files []string, trunkPin string) {
	li := ig.integrationState(ctx, lane.pipeline)
	if li == nil {
		return // no task minted this round; the conflict recurs and retries
	}

	if !lane.resolution {
		// A lane-branch conflict opens a fresh cycle for this tip: attempts
		// left from a previous tip's spent budget must not park it. (A
		// resolution branch conflicting keeps counting against its lane
		// tip's budget — that is what bounds the resolve->re-conflict loop.)
		li.ConflictAttempts = 0
	}

	if !ig.cfg.ConflictTasks || li.ConflictAttempts >= ig.cfg.ConflictRetryBudget {
		// Skip-until-advanced (the prior-art behavior, and the fallback
		// once the retry budget is spent). Attempts stay spent for this
		// tip; a new tip resets them above.
		li.LastSeenTip = lane.laneTip
		ig.retireResolution(ctx, lane, li)
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
			MetaTrunkTip:      trunkPin,
			MetaFiles:         strings.Join(files, ","),
			MetaResolveBranch: resolveBranch,
			MetaResolveDir:    resolveDir,
		},
	}, true) // conflicts gate a whole lane: front of the shared backlog
	if err != nil {
		return
	}
	// A re-conflicted resolution is superseded by the task just minted.
	ig.retireResolution(ctx, lane, li)
	li.ConflictTaskID = task.ID
	li.ConflictAttempts++
	_ = ig.st.PutIntegration(ctx, li)
	ig.event(ctx, "conflict.enqueued", lane.pipeline, map[string]any{
		"lane": lane.branch, "task": task.ID, "files": files, "attempt": li.ConflictAttempts,
	})
}

// land records a lane (or its resolution) as integrated.
func (ig *Integrator) land(ctx context.Context, lane pendingLane) {
	li := ig.integrationState(ctx, lane.pipeline)
	if li == nil {
		return // stale bookkeeping self-heals: a landed tip is 0 ahead
	}
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
	li := ig.integrationState(ctx, lane.pipeline)
	if li == nil {
		return
	}
	li.LastSeenTip = lane.laneTip
	ig.retireResolution(ctx, lane, li)
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
