package engine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/domain"
	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/gitx"
	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/prompt"
	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/statedir"
	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/store"
)

// Meta keys on merge-conflict tasks, written by the integrator and pinned by
// SHA so the resolution is reproducible regardless of later lane commits.
const (
	MetaLane          = "lane"
	MetaLaneBranch    = "laneBranch"
	MetaLaneTip       = "laneTip"
	MetaTrunkTip      = "trunkTip"
	MetaFiles         = "files"
	MetaResolveBranch = "resolveBranch"
	MetaResolveDir    = "resolveDir"
)

// runConflictPass resolves one integration conflict in an ephemeral
// worktree. The invariant that keeps trunk safe: the agent only ever edits
// the resolution worktree; the ENGINE reproduces the merge, verifies the
// result (no unmerged paths, no marker strings, gate green), and the
// resolution branch lands through the normal gated queue.
func (w *Worker) runConflictPass(ctx context.Context, pass *domain.Pass, task *domain.Task, res *resolved, sessionID string) (PassResult, error) {
	name := w.cfg.Pipeline.Name
	laneBranch := task.Meta[MetaLaneBranch]
	laneTip := task.Meta[MetaLaneTip]
	trunkTip := task.Meta[MetaTrunkTip]
	resolveBranch := task.Meta[MetaResolveBranch]
	resolveDir := task.Meta[MetaResolveDir]
	if laneBranch == "" || laneTip == "" || trunkTip == "" || resolveBranch == "" || resolveDir == "" {
		err := fmt.Errorf("engine: conflict task %s has incomplete meta", task.ID)
		return w.failSetup(ctx, pass, task, err)
	}

	cleanup := func(deleteBranch bool) {
		_ = gitx.RemoveWorktree(ctx, w.cfg.RepoDir, resolveDir, true)
		_ = gitx.PruneWorktrees(ctx, w.cfg.RepoDir)
		_ = os.RemoveAll(resolveDir)
		if deleteBranch {
			_ = gitx.DeleteBranch(ctx, w.cfg.RepoDir, resolveBranch, true)
		}
	}

	// Fresh attempt: clear any wreckage from a previous try.
	cleanup(true)
	if err := gitx.AddWorktree(ctx, w.cfg.RepoDir, resolveDir, resolveBranch, laneTip); err != nil {
		return w.failSetup(ctx, pass, task, err)
	}

	// Reproduce the conflict at the pinned SHAs.
	conflict, err := gitx.Merge(ctx, resolveDir, trunkTip, false)
	if err != nil {
		cleanup(true)
		return w.failSetup(ctx, pass, task, err)
	}
	if !conflict {
		// Trunk moved and the merge is clean now — nothing for an agent
		// to do; the merge commit already exists on the resolution branch.
		_ = w.st.CompleteTask(ctx, task.ID, name, "conflict no longer reproduces; clean merge committed")
		w.event(ctx, "conflict.resolved", name, pass.ID, map[string]any{"lane": laneBranch, "trivial": true})
		w.finishPass(ctx, pass, domain.PassDone, domain.OutcomeDone, domain.FailNone, 0, "")
		cleanup(false) // branch stays for the integrator to land
		return PassRan, nil
	}
	conflicted, _ := gitx.UnmergedFiles(ctx, resolveDir)

	// Prompt: base contract + conflict-specific task block, no spice —
	// resolution wants focus, not lateral thinking.
	snapshot, _ := w.bl.Snapshot(ctx)
	completions, _ := w.st.ListCompletions(ctx, 10)
	if err := statedir.Materialize(w.cfg.StateDir, task, snapshot, completions); err != nil {
		cleanup(true)
		return w.failSetup(ctx, pass, task, err)
	}
	_, full := prompt.Compose(prompt.Inputs{
		BaseContract: prompt.BaseContract(),
		Mechanics: prompt.Mechanics(prompt.MechanicsInputs{
			StateDir:  w.cfg.StateDir,
			RepoDir:   resolveDir,
			Branch:    resolveBranch,
			VerifyCmd: w.cfg.Verify,
			VerifyDir: w.cfg.VerifyDir,
		}),
		Goal:      readTrim(w.cfg.GoalPath),
		TaskBlock: conflictBlock(task, laneBranch, conflicted),
	})

	logPath := filepath.Join(w.cfg.LogDir, fmt.Sprintf("iter-%06d.log", pass.N))
	w.patchPass(ctx, pass.ID, store.PassPatch{LogPath: &logPath})
	logFile, err := w.openPassLog(logPath, pass, task, res, prompt.Spice{}, "", sessionID)
	if err != nil {
		cleanup(true)
		return w.failSetup(ctx, pass, task, err)
	}
	defer logFile.Close()

	w.setState(ctx, pass, domain.PassRunning)
	w.event(ctx, "pass.started", name, pass.ID, map[string]any{
		"n": pass.N, "task": task.Title, "conflict": true, "lane": laneBranch,
		"agent": res.bundle.Agent, "model": res.bundle.Model,
	})
	exitCode, tail, timedOut, runErr := w.spawn(ctx, pass, res, full, resolveDir, logPath, logFile, sessionID)
	if ctx.Err() != nil {
		_ = w.st.ReleaseTask(ctx, task.ID)
		cleanup(true)
		w.finishPass(ctx, pass, domain.PassFailed, domain.OutcomeFailed, domain.FailSetup, exitCode, "")
		return PassRan, ctx.Err()
	}

	w.setState(ctx, pass, domain.PassIngesting)
	fail := func(kind domain.FailKind, why string) (PassResult, error) {
		cleanup(true)
		w.failTask(ctx, task)
		w.event(ctx, "conflict.attempt_failed", name, pass.ID, map[string]any{"lane": laneBranch, "why": why})
		w.finishPass(ctx, pass, domain.PassFailed, domain.OutcomeFailed, kind, exitCode, "")
		return w.bumpBreaker(ctx, pass)
	}
	switch {
	case runErr != nil:
		cleanup(true)
		return w.failSetup(ctx, pass, task, runErr)
	case timedOut:
		return fail(domain.FailTimeout, "wedged")
	case exitCode != 0:
		// An expired login must halt HERE too — otherwise it burns the
		// whole ConflictRetryBudget × MaxTaskAttempts in blind retries.
		if res.caps.AuthProbe && isAuthFailure(tail) {
			cleanup(true)
			return w.haltAuth(ctx, pass, task, res, exitCode)
		}
		return fail(domain.FailExit, fmt.Sprintf("agent exit %d", exitCode))
	}

	// ENGINE verification — never trust the agent's word alone.
	if unmerged, _ := gitx.UnmergedFiles(ctx, resolveDir); len(unmerged) > 0 {
		return fail(domain.FailExit, fmt.Sprintf("%d paths still unmerged", len(unmerged)))
	}
	if bad := filesWithConflictMarkers(resolveDir, conflicted); len(bad) > 0 {
		return fail(domain.FailExit, "conflict markers left in: "+strings.Join(bad, ", "))
	}
	verifyDir := resolveDir
	if w.cfg.VerifyDir != "" && w.cfg.VerifyDir != "." {
		verifyDir = filepath.Join(resolveDir, w.cfg.VerifyDir)
	}
	if green, out := runGate(ctx, verifyDir, w.cfg.Verify, w.cfg.Pipeline.PassTimeout); !green {
		w.event(ctx, "gate.red", name, pass.ID, map[string]any{"where": "resolution", "tail": tailLines(out, 20)})
		return fail(domain.FailExit, "gate red on resolution")
	}

	progress := statedir.ReadProgress(w.cfg.StateDir)

	// Conclude the merge under our subject if the agent didn't commit.
	sha := ""
	if gitx.HasMergeHead(ctx, resolveDir) || mustDirty(ctx, resolveDir) {
		subject := fmt.Sprintf("ws(%s) resolve %s [%s]", name, laneBranch, res.drv.Name())
		trailers := [][2]string{{"Workshop-Task", task.ID}, {"Workshop-Pass", fmt.Sprint(pass.ID)}}
		if sha, err = gitx.CommitAll(ctx, resolveDir, gitx.BuildCommitMessage(subject, commitBody(progress), trailers)); err != nil {
			return fail(domain.FailExit, "merge commit failed: "+err.Error())
		}
	} else if head, err := gitx.RevParse(ctx, resolveDir, "HEAD"); err == nil {
		if len(head) > 7 {
			head = head[:7]
		}
		sha = head
	}

	result := progress.Result
	if result == "" {
		result = "resolved merge conflict on " + laneBranch
	}
	_ = w.st.CompleteTask(ctx, task.ID, name, result)
	w.event(ctx, "conflict.resolved", name, pass.ID, map[string]any{"lane": laneBranch, "sha": sha})

	w.consecFails = 0
	w.finishPass(ctx, pass, domain.PassDone, domain.OutcomeDone, domain.FailNone, exitCode, sha)
	cleanup(false) // free the worktree; the branch lands via the queue
	return PassRan, nil
}

func conflictBlock(task *domain.Task, laneBranch string, conflicted []string) string {
	var b strings.Builder
	b.WriteString("## YOUR TASK THIS PASS — RESOLVE A MERGE CONFLICT\n\n")
	fmt.Fprintf(&b, "id: %s\n", task.ID)
	fmt.Fprintf(&b, "The working directory is MID-MERGE: pipeline branch %q merging in the trunk.\n", laneBranch)
	b.WriteString("Conflicted files:\n")
	for _, f := range conflicted {
		fmt.Fprintf(&b, "  - %s\n", f)
	}
	b.WriteString(`
Rules:
1. Resolve each conflict SEMANTICALLY — understand what both sides intend and
   combine them; never just pick a side blindly.
2. Edit only the conflicted files (plus the minimum needed to make both
   changes coexist). Do NOT restructure anything.
3. Remove every conflict marker (<<<<<<< ======= >>>>>>>).
4. Run the verify command; it must pass.
5. Stage the resolved files with ` + "`git add`" + `. Do NOT run git commit and do
   NOT abort the merge — the engine commits.
6. Write progress.json as usual (phase done + a one-line result describing
   how you reconciled the sides).`)
	return b.String()
}

// filesWithConflictMarkers scans the given files for leftover markers.
func filesWithConflictMarkers(dir string, files []string) []string {
	var bad []string
	for _, f := range files {
		data, err := os.ReadFile(filepath.Join(dir, f))
		if err != nil {
			continue
		}
		s := string(data)
		if strings.Contains(s, "<<<<<<< ") || strings.Contains(s, ">>>>>>> ") {
			bad = append(bad, f)
		}
	}
	return bad
}

func mustDirty(ctx context.Context, dir string) bool {
	dirty, err := gitx.IsDirty(ctx, dir)
	return err == nil && dirty
}

func tailLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

var _ = time.Second
