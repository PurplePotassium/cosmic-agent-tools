// Package engine runs the Workshop loops: per-pipeline workers executing the
// pass state machine (claim → prepare → run → ingest → commit), the breaker,
// and — in worktree mode — the merge-queue integrator.
package engine

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"golang.org/x/sync/semaphore"

	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/backlog"
	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/bus"
	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/domain"
	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/driver"
	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/gitx"
	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/proc"
	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/prompt"
	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/route"
	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/statedir"
	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/store"
)

// Halt reasons recorded on a pipeline.
const (
	HaltAuth    = "auth"
	HaltBreaker = "breaker"
)

// ErrHalted is returned by Loop when the pipeline stopped itself (auth
// failure or circuit breaker) rather than finishing its iterations.
var ErrHalted = errors.New("engine: pipeline halted")

// authRe spots auth-shaped FAILURE PHRASES in captured output. It must never
// match a pass that merely fails while WORKING ON auth-flavored code — paths
// like internal/auth/, test names, 401-handler chatter — because an auth halt
// is permanent until an operator intervenes. Every alternative is a failure
// phrase seen in real agent-CLI output; extend it with captured lines, never
// bare keywords.
var authRe = regexp.MustCompile(`(?i)(` +
	`invalid api[- ]?key` + `|` +
	`api[- ]?key .{0,20}(invalid|expired|revoked|missing)` + `|` +
	`(authentication|authorization)[_ ](error|failed|required)` + `|` +
	`not (logged|signed) in` + `|` +
	`please run .{0,30}\blog ?in\b` + `|` +
	`(oauth|access|refresh) token .{0,25}(expired|invalid|revoked)` + `|` +
	`credentials? (expired|invalid|revoked|not found)` + `|` +
	`no credential helper` + `|` +
	`keyring (locked|unavailable|denied)` + `|` +
	`\bauth (error|failed|failure)\b` + `|` +
	`\bnot authorized\b` + `|` +
	`re-?authenticate` + `|` +
	`sign-?in again` + `|` +
	`401 unauthorized` +
	`)`)

// WorkerConfig wires one pipeline's worker.
type WorkerConfig struct {
	Pipeline domain.Pipeline
	Multi    bool // more than one enabled pipeline (adds the scope block)

	RepoDir  string // working dir: the repo, or this pipeline's worktree
	StateDir string // this pipeline's agent-facing state dir
	LogDir   string

	GoalPath   string // .workshop/GOAL.md
	PromptsDir string // .workshop/prompts ("" = none)

	Verify    string
	VerifyDir string

	SkipPermissions bool
	ExtraArgs       []string

	KnownPipelines map[string]bool // valid proposal targets

	// Routing: per-task bundle resolution (pin > Types table > pipeline
	// bundle) and the classifier that types untyped shared-backlog tasks.
	Types            map[string]domain.Bundle
	ClassifierMode   string          // "off" | "heuristic" | "agent" (agent falls back to heuristic for now)
	Vocabulary       map[string]bool // known task types
	SuspectAuthAfter int             // blind-driver no-report failures before an auth alert (default 3)

	BreakerLimit    int           // consecutive failed passes before halt (default 5)
	MaxTaskAttempts int           // blocked/failed attempts before a task is stuck (default 3)
	RetryBackoff    time.Duration // task retry backoff (default 1m)
	SleepBetween    time.Duration // pause between passes
	IdlePoll        time.Duration // poll cadence when the backlog is empty (default 3s)

	SpiceEnabled bool
	Personas     []string
	Nouns        []string
	Rng          *rand.Rand

	// Multi-pipeline coordination (set by the supervisor):
	//   SyncTrunk — worktree mode: merge this (gated-green) trunk into the
	//     lane at pass start so agents build on integrated work.
	//   TreeMu — no-worktree multi-pipeline mode: passes serialize on the
	//     shared working tree (never concurrent on one tree).
	//   Sem — global cap on simultaneously RUNNING agents; held only
	//     around the agent-execution window.
	SyncTrunk string
	TreeMu    *sync.Mutex
	Sem       *semaphore.Weighted
}

func (c *WorkerConfig) fillDefaults() {
	if c.BreakerLimit <= 0 {
		c.BreakerLimit = 5
	}
	if c.MaxTaskAttempts <= 0 {
		c.MaxTaskAttempts = 3
	}
	if c.RetryBackoff <= 0 {
		c.RetryBackoff = time.Minute
	}
	if c.IdlePoll <= 0 {
		c.IdlePoll = 3 * time.Second
	}
	if c.SuspectAuthAfter <= 0 {
		c.SuspectAuthAfter = 3
	}
	if c.Rng == nil {
		c.Rng = rand.New(rand.NewSource(time.Now().UnixNano()))
	}
	if c.KnownPipelines == nil {
		c.KnownPipelines = map[string]bool{c.Pipeline.Name: true}
	}
}

// Worker drives one pipeline.
type Worker struct {
	cfg     WorkerConfig
	drivers map[string]driver.Driver // cache, keyed by agent name
	st      *store.Store
	bl      *backlog.Service
	bus     *bus.Bus

	consecFails    int
	consecNoReport int // blind-driver failures with no progress start-write
}

// NewWorker builds a worker. Drivers are resolved per pass from the routed
// bundle (a task pinned to another agent runs on that agent).
func NewWorker(cfg WorkerConfig, st *store.Store, b *bus.Bus) *Worker {
	cfg.fillDefaults()
	return &Worker{cfg: cfg, drivers: map[string]driver.Driver{}, st: st, bl: backlog.New(st), bus: b}
}

// modeFlags resolves (Invent, AcceptProposals) for this pass: the live
// dashboard override when one is set, else the configured pipeline mode.
// Re-read every pass so an operator's mode change takes effect on the very
// next pass, no restart required — the mode counterpart of resolve's bundle
// override.
func (w *Worker) modeFlags(ctx context.Context) (invent, acceptProposals bool) {
	mode, _ := w.st.PipelineMode(ctx, w.cfg.Pipeline.Name)
	if mode == "" {
		return w.cfg.Pipeline.Invent, w.cfg.Pipeline.AcceptProposals
	}
	return domain.ModeFlags(mode)
}

// resolved is one pass's routing outcome.
type resolved struct {
	bundle domain.Bundle
	drv    driver.Driver
	caps   driver.Capabilities
}

func (w *Worker) resolve(ctx context.Context, task *domain.Task) (*resolved, error) {
	// The live override is re-read from the store EVERY pass so the
	// operator can switch agent/model for the next pass without a restart.
	override, _ := w.st.PipelineBundle(ctx, w.cfg.Pipeline.Name)
	bundle := route.Resolve(task, override, w.cfg.Types, w.cfg.Pipeline.Bundle)
	drv, ok := w.drivers[bundle.Agent]
	if !ok {
		var err error
		if drv, err = driver.New(bundle.Agent); err != nil {
			return nil, err
		}
		w.drivers[bundle.Agent] = drv
	}
	caps, err := drv.Probe(ctx)
	if err != nil {
		return nil, err
	}
	return &resolved{bundle: bundle, drv: drv, caps: caps}, nil
}

// classifyUntyped assigns types to untyped open tasks on the shared backlog
// so type-filtered pipelines can see them.
func (w *Worker) classifyUntyped(ctx context.Context) {
	mode := w.cfg.ClassifierMode
	if mode == "" {
		mode = "heuristic"
	}
	if mode == "off" || len(w.cfg.Vocabulary) == 0 {
		return
	}
	main := domain.MainBacklog
	open, err := w.st.ListTasks(ctx, store.TaskFilter{
		Backlog:  &main,
		Statuses: []domain.TaskStatus{domain.TaskOpen},
	})
	if err != nil {
		return
	}
	for _, t := range open {
		if t.Type != "" {
			continue
		}
		if typ := route.Classify(t.Title, t.Detail, w.cfg.Vocabulary); typ != "" {
			if _, err := w.st.UpdateTask(ctx, t.ID, store.TaskPatch{Type: &typ}); err == nil {
				w.event(ctx, "task.classified", w.cfg.Pipeline.Name, 0, map[string]any{
					"task": t.ID, "type": typ,
				})
			}
		}
	}
}

// PassResult classifies one RunPass call.
type PassResult int

const (
	PassRan    PassResult = iota // a pass executed (success or failure)
	PassIdle                     // nothing eligible to work on
	PassHalted                   // pipeline halted (auth/breaker) — stop looping
)

// Loop runs passes until ctx cancels, the pipeline halts, or — when
// iterations > 0 — that many passes ran. Bounded runs (and untilDrained)
// exit when the backlog has nothing eligible instead of idling.
func (w *Worker) Loop(ctx context.Context, iterations int, untilDrained bool) error {
	ran := 0
	bounded := iterations > 0
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		res, err := w.RunPass(ctx)
		if err != nil && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
			return err
		}
		if err != nil {
			// A store/setup failure is not "drained": a bounded run must
			// exit nonzero, and even a live loop must say WHY it is idling
			// instead of silently spinning on a dead database.
			w.event(ctx, "pass.error", w.cfg.Pipeline.Name, 0, map[string]any{"error": err.Error()})
			if bounded || untilDrained {
				return err
			}
		}
		switch res {
		case PassHalted:
			// Bounded runs report the halt; a live (unbounded) worker
			// parks instead, so clearing the halt (operator resume, auth
			// fixed) picks the loop back up without a restart.
			if bounded || untilDrained {
				return ErrHalted
			}
			if !sleepCtx(ctx, w.cfg.IdlePoll) {
				return ctx.Err()
			}
		case PassIdle:
			if bounded || untilDrained {
				return nil
			}
			if !sleepCtx(ctx, w.cfg.IdlePoll) {
				return ctx.Err()
			}
		case PassRan:
			ran++
			if bounded && ran >= iterations {
				return nil
			}
			if w.cfg.SleepBetween > 0 && !sleepCtx(ctx, w.cfg.SleepBetween) {
				return ctx.Err()
			}
		}
	}
}

// RunPass executes one pass of the state machine.
func (w *Worker) RunPass(ctx context.Context) (PassResult, error) {
	if w.cfg.TreeMu != nil {
		w.cfg.TreeMu.Lock()
		defer w.cfg.TreeMu.Unlock()
	}
	name := w.cfg.Pipeline.Name

	if reason, err := w.st.HaltedReason(ctx, name); err != nil {
		return PassIdle, err
	} else if reason != "" {
		return PassHalted, nil
	}

	// Hygiene before any git op: stale locks and orphaned merges block
	// everything downstream.
	if _, err := gitx.CleanStaleLock(ctx, w.cfg.RepoDir, time.Minute); err == nil {
		_ = gitx.MergeAbort(ctx, w.cfg.RepoDir)
	}

	// Worktree mode: pull the last-known-green trunk ref into the lane
	// before working. A sync conflict does NOT skip the pass: the pass
	// proceeds on the un-synced lane so the tip keeps advancing (the
	// integrator's conflict-task machinery owns the conflict, and
	// skip-until-advanced needs new commits to release). Returning idle
	// here would livelock the lane — it could never advance past the
	// conflict, and could never claim its own resolution task either.
	if w.cfg.SyncTrunk != "" {
		if dirty, err := gitx.IsDirty(ctx, w.cfg.RepoDir); err == nil && !dirty {
			if n, err := gitx.AheadCount(ctx, w.cfg.RepoDir, w.cfg.Pipeline.Branch, w.cfg.SyncTrunk); err == nil && n > 0 {
				if conflict, err := gitx.Merge(ctx, w.cfg.RepoDir, w.cfg.SyncTrunk, false); err == nil && conflict {
					_ = gitx.MergeAbort(ctx, w.cfg.RepoDir)
					w.event(ctx, "integration.sync_conflict", name, 0, map[string]any{
						"trunk": w.cfg.SyncTrunk, "action": "pass continues un-synced",
					})
				}
			}
		}
	}

	// CLAIM. Classify untyped shared tasks first so type-filtered claims
	// can see them.
	w.classifyUntyped(ctx)
	task, err := w.bl.Claim(ctx, w.cfg.Pipeline, 0)
	if err != nil {
		return PassIdle, fmt.Errorf("engine: claim: %w", err)
	}
	invent, _ := w.modeFlags(ctx)
	if task == nil && !invent {
		return PassIdle, nil
	}

	pass, err := w.st.StartPass(ctx, name)
	if err != nil {
		if task != nil {
			_ = w.st.ReleaseTask(ctx, task.ID)
		}
		return PassIdle, fmt.Errorf("engine: start pass: %w", err)
	}
	if task != nil {
		w.patchPass(ctx, pass.ID, store.PassPatch{TaskID: &task.ID})
		w.event(ctx, "task.claimed", name, pass.ID, map[string]any{"task": task.ID, "title": task.Title})
	}

	// PREPARE: route the task to its bundle/driver, then compose.
	w.setState(ctx, pass, domain.PassPreparing)
	res, err := w.resolve(ctx, task)
	if err != nil {
		return w.failSetup(ctx, pass, task, err)
	}

	// Session-capable runtimes get a workshop-minted session id so the
	// agent's own full transcript (tool calls, reasoning) stays correlated
	// to this pass for forensics.
	sessionID := ""
	if res.caps.Sessions {
		sessionID = uuid.NewString()
		w.patchPass(ctx, pass.ID, store.PassPatch{SessionID: &sessionID})
	}

	// Merge-conflict tasks run a dedicated resolution flow in an
	// ephemeral worktree — never in this pipeline's own tree.
	if task != nil && task.Type == "merge-conflict" && task.Meta["laneTip"] != "" {
		return w.runConflictPass(ctx, pass, task, res, sessionID)
	}

	full, spice, err := w.preparePass(ctx, task)
	if err != nil {
		return w.failSetup(ctx, pass, task, err)
	}
	if spice.Mode != "" {
		w.patchPass(ctx, pass.ID, store.PassPatch{Spice: &spice.Mode})
	}

	logPath := filepath.Join(w.cfg.LogDir, fmt.Sprintf("iter-%06d.log", pass.N))
	w.patchPass(ctx, pass.ID, store.PassPatch{LogPath: &logPath})
	logFile, err := w.openPassLog(logPath, pass, task, res, spice, sessionID)
	if err != nil {
		return w.failSetup(ctx, pass, task, err)
	}
	defer logFile.Close()

	// RUN.
	w.setState(ctx, pass, domain.PassRunning)
	w.event(ctx, "pass.started", name, pass.ID, map[string]any{
		"n": pass.N, "task": passTaskTitle(task), "spice": spice.Mode,
		"agent": res.bundle.Agent, "model": res.bundle.Model, "effort": res.bundle.Effort,
	})
	exitCode, tail, timedOut, runErr := w.spawn(ctx, pass, res, full, w.cfg.RepoDir, logPath, logFile, sessionID)
	if ctx.Err() != nil {
		// Hard stop: release the claim untouched; the kill already happened.
		if task != nil {
			_ = w.st.ReleaseTask(ctx, task.ID)
		}
		w.finishPass(ctx, pass, domain.PassFailed, domain.OutcomeFailed, domain.FailSetup, exitCode, "")
		return PassRan, ctx.Err()
	}

	// INGEST + COMMIT.
	w.setState(ctx, pass, domain.PassIngesting)
	return w.settlePass(ctx, pass, task, res, logFile, exitCode, tail, timedOut, runErr)
}

// preparePass materializes state files and composes the prompt.
func (w *Worker) preparePass(ctx context.Context, task *domain.Task) (string, prompt.Spice, error) {
	snapshot, err := w.bl.Snapshot(ctx)
	if err != nil {
		return "", prompt.Spice{}, err
	}
	completions, err := w.st.ListCompletions(ctx, 20)
	if err != nil {
		return "", prompt.Spice{}, err
	}
	if err := statedir.Materialize(w.cfg.StateDir, task, snapshot, completions); err != nil {
		return "", prompt.Spice{}, err
	}

	branch, _ := gitx.CurrentBranch(ctx, w.cfg.RepoDir)
	base := prompt.BaseContract()
	if o := w.fragment("base.md"); o != "" {
		base = o
	}
	var taskBlock, typeFragment string
	if task != nil {
		taskBlock = prompt.TaskBlock(task)
		if task.Type != "" {
			typeFragment = w.fragment(filepath.Join("types", task.Type+".md"))
		}
	} else {
		taskBlock = prompt.InventBlock(w.cfg.Pipeline)
	}
	var spice prompt.Spice
	if w.cfg.SpiceEnabled {
		spice = prompt.NewSpice(w.cfg.Rng, w.cfg.Personas, w.cfg.Nouns)
	}
	_, full := prompt.Compose(prompt.Inputs{
		BaseContract: base,
		Mechanics: prompt.Mechanics(prompt.MechanicsInputs{
			StateDir:  w.cfg.StateDir,
			RepoDir:   w.cfg.RepoDir,
			Branch:    branch,
			VerifyCmd: w.cfg.Verify,
			VerifyDir: w.cfg.VerifyDir,
		}),
		Goal:             readTrim(w.cfg.GoalPath),
		ProjectFragment:  w.fragment("project.md"),
		ScopeBlock:       prompt.ScopeBlock(w.cfg.Pipeline, w.cfg.Multi),
		PipelineFragment: w.fragment(filepath.Join("pipelines", w.cfg.Pipeline.Name+".md")),
		TaskBlock:        taskBlock,
		TypeFragment:     typeFragment,
		Spice:            spice,
	})
	return full, spice, nil
}

// spawn runs the agent process in dir and streams/captures its output.
func (w *Worker) spawn(ctx context.Context, pass *domain.Pass, res *resolved, fullPrompt, dir, logPath string, logFile *os.File, sessionID string) (exitCode int, tail []string, timedOut bool, err error) {
	// The concurrency cap applies only to the agent-execution window, so N
	// pipelines can exist with M running agents.
	if w.cfg.Sem != nil {
		if err := w.cfg.Sem.Acquire(ctx, 1); err != nil {
			return -1, nil, false, err
		}
		defer w.cfg.Sem.Release(1)
	}
	if res.bundle.Effort != "" && !res.caps.Effort {
		w.event(ctx, "driver.effort_ignored", w.cfg.Pipeline.Name, pass.ID, map[string]any{
			"agent": res.drv.Name(), "effort": res.bundle.Effort,
		})
	}
	plan, err := res.drv.Plan(driver.InvokeSpec{
		Prompt:          fullPrompt,
		Model:           res.bundle.Model,
		Effort:          res.bundle.Effort,
		SkipPermissions: w.cfg.SkipPermissions,
		ExtraArgs:       w.cfg.ExtraArgs,
		OpLogPath:       logPath + ".op.log",
		SessionID:       sessionID,
		WorkDir:         dir,
	})
	if err != nil {
		return -1, nil, false, err
	}

	passCtx := ctx
	var cancel context.CancelFunc
	if t := w.cfg.Pipeline.PassTimeout; t > 0 {
		passCtx, cancel = context.WithTimeout(ctx, t)
		defer cancel()
	}
	cmd := exec.CommandContext(passCtx, plan.Exe, plan.Args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"WORKSHOP_PASS_STATE_DIR="+w.cfg.StateDir,
		"WORKSHOP_PASS_REPO_DIR="+dir,
		fmt.Sprintf("WORKSHOP_PASS_N=%d", pass.N),
	)
	cmd.Cancel = func() error {
		if cmd.Process != nil {
			return proc.KillTree(cmd.Process.Pid)
		}
		return nil
	}
	cmd.WaitDelay = 15 * time.Second
	if plan.StdinPrompt {
		cmd.Stdin = strings.NewReader(fullPrompt)
	}
	proc.Configure(cmd, plan.Mode)

	drained := make(chan struct{})
	var drainedTail []string // owned by the drain goroutine until `drained` closes
	var pr, pw *os.File
	if plan.Mode == proc.Piped {
		pr, pw, err = os.Pipe()
		if err != nil {
			return -1, nil, false, err
		}
		cmd.Stdout = pw
		cmd.Stderr = pw
		go func() {
			defer close(drained)
			sc := bufio.NewScanner(pr)
			sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
			for sc.Scan() {
				line := sc.Text()
				fmt.Fprintln(logFile, line)
				drainedTail = appendTail(drainedTail, line)
				w.bus.PublishEphemeral(domain.Event{
					Type: "pass.log", Pipeline: w.cfg.Pipeline.Name, Pass: pass.ID,
					TS: time.Now().UTC(), Payload: map[string]any{"line": line},
				})
			}
			if err := sc.Err(); err != nil {
				// An over-long line (> the 1MB buffer cap) ends the scan
				// early — the rest of the pass runs blind. Say so instead
				// of silently presenting a truncated log/tail.
				marker := fmt.Sprintf("(workshop: output capture truncated: %v)", err)
				fmt.Fprintln(logFile, marker)
				drainedTail = appendTail(drainedTail, marker)
			}
		}()
	} else {
		close(drained)
		fmt.Fprintln(logFile, "(driver output not capturable — see the agent's own log and progress.json)")
	}

	if err := cmd.Start(); err != nil {
		if pw != nil {
			pw.Close()
			pr.Close()
		}
		return -1, nil, false, err
	}
	proc.Adopt(cmd.Process.Pid)
	defer proc.Finished(cmd.Process.Pid)
	if pw != nil {
		pw.Close() // parent's write end; child holds its own dup
	}
	waitErr := cmd.Wait()
	if pr != nil {
		// Drain what the child wrote; a lingering grandchild holding the
		// pipe must not hang the pass (KillTree usually prevents this).
		// The tail is only safe to read once the goroutine is done — on
		// timeout it stays nil rather than racing.
		select {
		case <-drained:
			tail = drainedTail
		case <-time.After(5 * time.Second):
		}
		pr.Close()
	}

	exitCode = cmd.ProcessState.ExitCode()
	timedOut = passCtx.Err() == context.DeadlineExceeded && ctx.Err() == nil
	if waitErr != nil && exitCode == 0 {
		exitCode = -1
	}

	// Archive the runtime's own full transcript next to the pass log —
	// runtimes prune their session stores, this copy is ours. Best-effort:
	// a crash before the runtime flushed simply leaves no file to copy.
	if plan.TranscriptPath != "" {
		_ = statedir.CopyFile(plan.TranscriptPath, TranscriptArchivePath(logPath))
	}
	return exitCode, tail, timedOut, nil
}

// TranscriptArchivePath is where a pass's agent transcript is archived,
// derived from its pass-log path (iter-000042.log -> iter-000042.transcript.jsonl).
func TranscriptArchivePath(logPath string) string {
	return strings.TrimSuffix(logPath, ".log") + ".transcript.jsonl"
}

// settlePass classifies the finished pass, reconciles state, and commits.
func (w *Worker) settlePass(ctx context.Context, pass *domain.Pass, task *domain.Task, res *resolved, logFile *os.File, exitCode int, tail []string, timedOut bool, runErr error) (PassResult, error) {
	name := w.cfg.Pipeline.Name
	blind := res.caps.Capture == driver.CaptureNone

	if runErr != nil {
		return w.failSetup(ctx, pass, task, runErr)
	}

	if timedOut {
		w.event(ctx, "wedge.killed", name, pass.ID, map[string]any{"timeoutMin": int(w.cfg.Pipeline.PassTimeout.Minutes())})
		w.commitFailedWork(ctx, pass, task, res)
		w.failTask(ctx, task)
		w.finishPass(ctx, pass, domain.PassFailed, domain.OutcomeFailed, domain.FailTimeout, exitCode, "")
		return w.bumpBreaker(ctx, pass)
	}

	if exitCode != 0 {
		if res.caps.AuthProbe && isAuthFailure(tail) {
			return w.haltAuth(ctx, pass, task, res, exitCode)
		}
		if blind {
			// A blind driver can't tell us WHY it failed. A failure with
			// no progress start-write smells like dead auth or a silently
			// rejected model id — surface it once the pattern repeats.
			if statedir.ReadProgress(w.cfg.StateDir).Phase == "" {
				w.consecNoReport++
				if w.consecNoReport == w.cfg.SuspectAuthAfter {
					w.event(ctx, "auth.suspected", name, pass.ID, map[string]any{
						"agent": res.drv.Name(),
						"note": fmt.Sprintf("%d consecutive failures with no self-report — check auth interactively (run `%s` once) and verify the model id",
							w.consecNoReport, res.drv.Name()),
					})
				}
			}
		}
		w.commitFailedWork(ctx, pass, task, res)
		w.failTask(ctx, task)
		w.finishPass(ctx, pass, domain.PassFailed, domain.OutcomeFailed, domain.FailExit, exitCode, "")
		return w.bumpBreaker(ctx, pass)
	}

	// Success path: the agent's self-report drives bookkeeping.
	progress := statedir.ReadProgress(w.cfg.StateDir)
	// The decisions self-report (judgment calls, deviations) is per-pass
	// forensic material; progress.json gets overwritten next pass, so the
	// pass log is its durable home.
	if progress.Decisions != "" && logFile != nil {
		fmt.Fprintf(logFile, "\n--- decisions ---\n%s\n", progress.Decisions)
	}
	outcome := domain.OutcomeDone
	switch progress.Phase {
	case "done":
		if task != nil {
			if err := w.st.CompleteTask(ctx, task.ID, name, progress.Result); err == nil {
				w.event(ctx, "task.done", name, pass.ID, map[string]any{"task": task.ID, "title": task.Title})
			}
		} else {
			title := progress.Task
			if title == "" {
				title = "(invented task)"
			}
			_ = w.st.AddCompletion(ctx, &domain.Completion{Pipeline: name, Title: title, Result: progress.Result})
			w.event(ctx, "task.done", name, pass.ID, map[string]any{"title": title, "invented": true})
		}
	case "blocked", "reverted":
		outcome = domain.PassOutcome(progress.Phase)
		w.failTask(ctx, task)
		w.event(ctx, "task.failed", name, pass.ID, map[string]any{
			"task": passTaskTitle(task), "phase": progress.Phase, "note": progress.Note,
		})
	default:
		// Contract breach: exit 0 but no final report. If work landed we
		// keep it; the task is retried either way since we can't trust it.
		outcome = domain.OutcomeNoChange
		w.failTask(ctx, task)
		w.event(ctx, "task.failed", name, pass.ID, map[string]any{
			"task": passTaskTitle(task), "phase": progress.Phase, "note": "no final self-report",
		})
	}

	if _, acceptProposals := w.modeFlags(ctx); acceptProposals {
		if added, err := w.bl.Ingest(ctx, name, statedir.ReadProposals(w.cfg.StateDir), w.cfg.KnownPipelines, 2); err == nil {
			for _, t := range added {
				w.event(ctx, "task.created", name, pass.ID, map[string]any{"task": t.ID, "title": t.Title, "origin": "agent"})
			}
		}
	}

	sha := w.commitIfDirty(ctx, pass, task, res)
	if sha == "" && outcome == domain.OutcomeDone {
		outcome = domain.OutcomeNoChange
	}

	w.consecFails = 0
	w.consecNoReport = 0
	w.finishPass(ctx, pass, domain.PassDone, outcome, domain.FailNone, exitCode, sha)
	return PassRan, nil
}

// --- helpers ---

// commitFailedWork commits a failed pass's half-done work only when this
// worker writes to a gated lane (worktree mode), where the merge queue keeps
// broken trees off trunk and the commit keeps pass boundaries bisectable. In
// simple mode there is no gate — committing would land a broken tree directly
// on the user's branch, so the tree is left dirty instead (the old loop's
// behavior; the next pass builds on or sweeps it up).
func (w *Worker) commitFailedWork(ctx context.Context, pass *domain.Pass, task *domain.Task, res *resolved) {
	if w.cfg.SyncTrunk == "" {
		return
	}
	w.commitIfDirty(ctx, pass, task, res)
}

func (w *Worker) commitIfDirty(ctx context.Context, pass *domain.Pass, task *domain.Task, res *resolved) string {
	dirty, err := gitx.IsDirty(ctx, w.cfg.RepoDir)
	if err != nil || !dirty {
		return ""
	}
	subject := fmt.Sprintf("ws(%s) iter %d [%s]", w.cfg.Pipeline.Name, pass.N, res.drv.Name())
	trailers := [][2]string{{"Workshop-Pass", fmt.Sprint(pass.ID)}}
	if task != nil {
		trailers = append(trailers, [2]string{"Workshop-Task", task.ID})
	}
	sha, err := gitx.CommitAll(ctx, w.cfg.RepoDir, gitx.BuildCommitMessage(subject, trailers))
	if err != nil {
		w.event(ctx, "commit.failed", w.cfg.Pipeline.Name, pass.ID, map[string]any{"error": err.Error()})
		return ""
	}
	w.patchPass(ctx, pass.ID, store.PassPatch{CommitSHA: &sha})
	w.event(ctx, "commit", w.cfg.Pipeline.Name, pass.ID, map[string]any{"sha": sha, "subject": subject})
	return sha
}

func (w *Worker) failTask(ctx context.Context, task *domain.Task) {
	if task == nil {
		return
	}
	if t, err := w.st.FailTask(ctx, task.ID, w.cfg.RetryBackoff, w.cfg.MaxTaskAttempts); err == nil && t.Status == domain.TaskStuck {
		w.event(ctx, "task.stuck", w.cfg.Pipeline.Name, 0, map[string]any{"task": t.ID, "title": t.Title, "attempts": t.Attempts})
	}
}

func (w *Worker) bumpBreaker(ctx context.Context, pass *domain.Pass) (PassResult, error) {
	w.consecFails++
	if w.consecFails >= w.cfg.BreakerLimit {
		_ = w.st.SetHalted(ctx, w.cfg.Pipeline.Name, HaltBreaker)
		w.event(ctx, "breaker.tripped", w.cfg.Pipeline.Name, pass.ID, map[string]any{"consecutiveFails": w.consecFails})
		return PassHalted, nil
	}
	return PassRan, nil
}

// failSetup concludes a pass that never reached the agent. The task takes a
// failure (attempts + retry backoff, stuck after MaxTaskAttempts) — releasing
// it untouched would let e.g. a task pinned to an unknown agent be re-claimed
// in a zero-sleep spin loop forever — and the failure counts toward the
// breaker like any other failed pass.
func (w *Worker) failSetup(ctx context.Context, pass *domain.Pass, task *domain.Task, err error) (PassResult, error) {
	w.failTask(ctx, task)
	w.event(ctx, "pass.setup_failed", w.cfg.Pipeline.Name, pass.ID, map[string]any{"error": err.Error()})
	w.finishPass(ctx, pass, domain.PassFailed, domain.OutcomeFailed, domain.FailSetup, -1, "")
	res, _ := w.bumpBreaker(ctx, pass)
	return res, err
}

func (w *Worker) finishPass(ctx context.Context, pass *domain.Pass, state domain.PassState, outcome domain.PassOutcome, failure domain.FailKind, exitCode int, sha string) {
	now := time.Now().UTC()
	patch := store.PassPatch{State: &state, Outcome: &outcome, Failure: &failure, Ended: &now}
	if exitCode >= 0 || failure != domain.FailNone {
		patch.ExitCode = &exitCode
	}
	if sha != "" {
		patch.CommitSHA = &sha
	}
	w.patchPass(ctx, pass.ID, patch)
	w.event(ctx, "pass.finished", w.cfg.Pipeline.Name, pass.ID, map[string]any{
		"n": pass.N, "outcome": string(outcome), "failure": string(failure), "sha": sha, "exit": exitCode,
	})
}

func (w *Worker) setState(ctx context.Context, pass *domain.Pass, s domain.PassState) {
	w.patchPass(ctx, pass.ID, store.PassPatch{State: &s})
}

func (w *Worker) patchPass(ctx context.Context, id int64, p store.PassPatch) {
	_ = w.st.UpdatePass(ctx, id, p)
}

func (w *Worker) event(ctx context.Context, typ, pipeline string, passID int64, payload map[string]any) {
	if w.bus == nil {
		return
	}
	w.bus.Publish(ctx, domain.Event{Type: typ, Pipeline: pipeline, Pass: passID, Payload: payload})
}

func (w *Worker) fragment(rel string) string {
	if w.cfg.PromptsDir == "" {
		return ""
	}
	return readTrim(filepath.Join(w.cfg.PromptsDir, rel))
}

func (w *Worker) openPassLog(path string, pass *domain.Pass, task *domain.Task, res *resolved, spice prompt.Spice, sessionID string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	fmt.Fprintf(f, "=== ws(%s) iter %d ===\n", w.cfg.Pipeline.Name, pass.N)
	fmt.Fprintf(f, "agent  : %s\nmodel  : %s\neffort : %s\n",
		res.drv.Name(), res.bundle.Model, res.bundle.Effort)
	if sessionID != "" {
		fmt.Fprintf(f, "session: %s\n", sessionID)
	}
	fmt.Fprintf(f, "task   : %s\nspice  : %s\nstarted: %s\n%s\n",
		passTaskTitle(task), spice.Mode, pass.Started.Format(time.RFC3339), strings.Repeat("-", 72))
	return f, nil
}

// haltAuth parks the pipeline on an auth-shaped failure (auth never
// self-heals — burning retries hides it), releases the task, and finishes
// the pass as FailAuth. Shared by normal and conflict-resolution passes.
func (w *Worker) haltAuth(ctx context.Context, pass *domain.Pass, task *domain.Task, res *resolved, exitCode int) (PassResult, error) {
	if task != nil {
		_ = w.st.ReleaseTask(ctx, task.ID)
	}
	name := w.cfg.Pipeline.Name
	_ = w.st.SetHalted(ctx, name, HaltAuth)
	w.event(ctx, "auth.halt", name, pass.ID, map[string]any{"agent": res.drv.Name()})
	w.finishPass(ctx, pass, domain.PassFailed, domain.OutcomeFailed, domain.FailAuth, exitCode, "")
	return PassHalted, nil
}

func isAuthFailure(tail []string) bool {
	for _, line := range tail {
		if authRe.MatchString(line) {
			return true
		}
	}
	return false
}

func appendTail(tail []string, line string) []string {
	const keep = 400
	tail = append(tail, line)
	if len(tail) > keep {
		tail = tail[len(tail)-keep:]
	}
	return tail
}

func passTaskTitle(t *domain.Task) string {
	if t == nil {
		return "(invent)"
	}
	return t.Title
}

func readTrim(path string) string {
	if path == "" {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(string(data), "\uFEFF"))
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
