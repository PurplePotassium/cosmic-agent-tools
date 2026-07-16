// Package app is the facade that wires config, store, bus, and engine
// together. The CLI and HTTP server consume only this package (plus domain),
// never the internals directly.
package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/semaphore"

	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/bus"
	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/chroma"
	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/config"
	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/domain"
	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/driver"
	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/engine"
	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/gitx"
	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/proc"
	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/prompt"
	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/route"
	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/statedir"
	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/store"
)

// App is one opened project.
type App struct {
	RepoDir  string
	StateDir string
	Store    *store.Store
	Bus      *bus.Bus

	// res is written by reloadConfig (dashboard pipeline API) while HTTP
	// handlers, snapshots, and Halt-relaunches read it concurrently — a
	// plain field is a data race.
	res        atomic.Pointer[config.Result]
	pipelineMu sync.Mutex // serializes the overrides-file read-modify-write

	// Self-evaluator state (see inquiry.go). Zero value ready.
	inqMu     sync.Mutex
	inqSeq    int64
	inqList   []*Inquiry
	inqCancel context.CancelFunc
}

// Res returns the current resolved-config snapshot. Grab it ONCE per
// operation and read only that snapshot: the dashboard's pipeline API can
// reload config live, and re-reading mid-operation may observe a different
// resolution.
func (a *App) Res() *config.Result { return a.res.Load() }

// New assembles an App from parts. Open is the production path; New exists
// for tests that stub the pieces.
func New(repoDir, stateDir string, res *config.Result, st *store.Store, b *bus.Bus) *App {
	a := &App{RepoDir: repoDir, StateDir: stateDir, Store: st, Bus: b}
	a.res.Store(res)
	return a
}

// Open resolves the repo (repoOverride or cwd), loads layered config, and
// opens the state store.
func Open(ctx context.Context, repoOverride string) (*App, error) {
	dir := repoOverride
	if dir == "" {
		wd, err := os.Getwd()
		if err != nil {
			return nil, err
		}
		dir = wd
	}
	if !gitx.IsRepo(ctx, dir) {
		return nil, fmt.Errorf("%s is not inside a git repository (run `git init` first, or pass --repo)", dir)
	}
	root, err := gitx.Root(ctx, dir)
	if err != nil {
		return nil, err
	}

	res, err := config.Load(config.UserConfigFile(), config.RepoConfigFile(root), config.OverridesFile(root), os.Getenv)
	if err != nil {
		return nil, err
	}
	// Unknown agent names in the routing table surface at load, not at pass
	// time — a task routed to a typo'd agent would burn retry attempts
	// failing pass setup instead.
	for t, b := range res.Config.Types {
		if b.Agent != "" {
			if _, derr := driver.New(b.Agent); derr != nil {
				res.Warnings = append(res.Warnings, fmt.Sprintf("types.%s: %v", t, derr))
			}
		}
	}

	stateDir := config.ProjectStateDir(root)
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return nil, err
	}
	// A fresh state dir for a repo whose basename matches an existing
	// project usually means the repo was moved on disk (state is keyed by
	// absolute path) — say so instead of silently starting from scratch.
	if _, serr := os.Stat(filepath.Join(stateDir, "workshop.db")); os.IsNotExist(serr) {
		if twins := config.SiblingProjectDirs(root); len(twins) > 0 {
			res.Warnings = append(res.Warnings, fmt.Sprintf(
				"no workshop state for this repo path yet, but state exists for another repo named %q — if you moved the repo, copy %s to %s to keep its backlog and history",
				filepath.Base(root), strings.Join(twins, " or "), stateDir))
		}
	}

	st, err := store.Open(filepath.Join(stateDir, "workshop.db"))
	if err != nil {
		return nil, err
	}
	return New(root, stateDir, res, st, bus.New(st)), nil
}

// Close releases resources.
func (a *App) Close() error { return a.Store.Close() }

// EnabledPipelines returns the enabled resolved pipelines.
func (a *App) EnabledPipelines() []domain.Pipeline {
	var out []domain.Pipeline
	for _, p := range a.Res().Config.ResolvedPipelines() {
		if p.Enabled {
			out = append(out, p)
		}
	}
	return out
}

// WorkerOpts carries the supervisor-level wiring for one worker.
type WorkerOpts struct {
	Dir       string // working dir override ("" = repo dir)
	SyncTrunk string // worktree mode: trunk merged into the lane at pass start
	TreeMu    *sync.Mutex
	Sem       *semaphore.Weighted
	AgyMu     *sync.RWMutex // engine-wide agy serialization (art passes hold it exclusively)
}

// BuildWorker wires one pipeline's worker.
func (a *App) BuildWorker(p domain.Pipeline, multi bool, opts WorkerOpts) (*engine.Worker, error) {
	// Validate the pipeline's default agent early; per-task routing may
	// still bring in other drivers at pass time.
	if _, err := driver.New(p.Bundle.Agent); err != nil {
		return nil, err
	}
	cfg := a.Res().Config
	var err error

	var personas, nouns []string
	if cfg.Spice.Enabled {
		if personas, err = prompt.LoadPool("personas", a.resolvePool(cfg.Spice.Personas)); err != nil {
			return nil, err
		}
		if nouns, err = prompt.LoadPool("nouns", a.resolvePool(cfg.Spice.Nouns)); err != nil {
			return nil, err
		}
	}
	known := map[string]bool{}
	for _, pl := range cfg.ResolvedPipelines() {
		known[pl.Name] = true
	}

	exportBase, err := a.ExportBase()
	if err != nil {
		return nil, err
	}
	exportDir := ""
	if exportBase != "" {
		exportDir = filepath.Join(exportBase, p.Name)
	}

	workDir := opts.Dir
	if workDir == "" {
		workDir = a.RepoDir
	}
	wc := engine.WorkerConfig{
		Pipeline:            p,
		Multi:               multi,
		RepoDir:             workDir,
		StateDir:            statedir.PipelineDir(a.StateDir, p.Name),
		LogDir:              filepath.Join(a.StateDir, "logs", p.Name),
		GoalPath:            config.GoalFile(a.RepoDir),
		PromptsDir:          config.PromptsDir(a.RepoDir),
		Verify:              cfg.Project.Verify,
		VerifyDir:           cfg.Project.VerifyDir,
		SkipPermissions:     cfg.Safety.SkipPermissions,
		ExtraArgs:           p.ExtraArgs,
		KnownPipelines:      known,
		BreakerLimit:        cfg.Safety.BreakerFailures,
		SleepBetween:        time.Duration(cfg.Safety.SleepSeconds) * time.Second,
		Types:               cfg.Types,
		ClassifierMode:      cfg.Classifier.Mode,
		Vocabulary:          route.Vocabulary(cfg.Types, cfg.ResolvedPipelines()),
		SpiceEnabled:        cfg.Spice.Enabled,
		Personas:            personas,
		Nouns:               nouns,
		PersonalityEnabled:  cfg.Personality.Enabled,
		Personalities:       cfg.Personality.List,
		SyncTrunk:           opts.SyncTrunk,
		TreeMu:              opts.TreeMu,
		Sem:                 opts.Sem,
		AgyMu:               opts.AgyMu,
		ArtRemover:          cfg.Art.Remover,
		CorridorkeyDir:      chroma.DiscoverCorridorKey(cfg.Art.CorridorkeyDir),
		ExportDir:           exportDir,
		ExportHumanReadable: cfg.Export.HumanReadable,
	}
	return engine.NewWorker(wc, a.Store, a.Bus), nil
}

// ExportBase resolves the [export] destination root for this project ("" =
// export off). A relative configured dir resolves against the repository
// root, but the destination must lie OUTSIDE the repository and its
// worktrees: passes commit anything dirty in the working tree, so exported
// evidence inside it would be swept into project history by the next pass.
// Refusing here (engine start) keeps read-only commands like `status` usable
// with a misconfigured dir.
func (a *App) ExportBase() (string, error) {
	dir := a.Res().Config.Export.Dir
	if dir == "" {
		return "", nil
	}
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(a.RepoDir, dir)
	}
	dir = filepath.Clean(dir)
	norm := func(p string) string {
		p = filepath.Clean(p)
		if runtime.GOOS == "windows" {
			p = strings.ToLower(p)
		}
		return p
	}
	rel, err := filepath.Rel(norm(a.RepoDir), norm(dir))
	inside := err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
	// Worktrees are siblings named <repo>-wt-<pipeline> — same sweep hazard.
	if inside || strings.HasPrefix(norm(dir), norm(a.RepoDir)+"-wt-") {
		return "", fmt.Errorf("export.dir %s is inside the repository (or a pipeline worktree) — passes commit anything dirty there, so exported evidence would land in project history; choose a folder outside %s", dir, a.RepoDir)
	}
	return dir, nil
}

// EngineLockPath returns the engine singleton lock file for a state dir.
func EngineLockPath(stateDir string) string { return filepath.Join(stateDir, "engine.lock") }

type engineLock struct {
	PID     int       `json:"pid"`
	Started time.Time `json:"started"`
}

// ReadEngineLock returns the pid recorded in the engine lock and when that
// process wrote it. The instant matters: probe the pid with
// proc.AliveSince(pid, started), never plain Alive — a reused PID would
// otherwise read as a live engine (and `stop --force` would KillTree the
// stranger holding it). A zero started (lock from an older binary) makes
// AliveSince fall back to plain liveness.
func ReadEngineLock(stateDir string) (pid int, started time.Time, ok bool) {
	var el engineLock
	if err := statedir.ReadJSON(EngineLockPath(stateDir), &el); err != nil || el.PID == 0 {
		return 0, time.Time{}, false
	}
	return el.PID, el.Started, true
}

// staleLockSeq disambiguates stale-lock claim names within one process (two
// goroutines racing acquireEngineLock share a pid).
var staleLockSeq atomic.Int64

// acquireEngineLock takes the per-repo engine singleton lock, clearing a
// stale lock left by a crashed engine. It returns the release func.
func (a *App) acquireEngineLock() (func(), error) {
	path := EngineLockPath(a.StateDir)
	for attempt := 0; attempt < 5; attempt++ {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			enc := json.NewEncoder(f)
			_ = enc.Encode(engineLock{PID: os.Getpid(), Started: time.Now().UTC()})
			f.Close()
			return func() { _ = os.Remove(path) }, nil
		}
		pid, started, ok := ReadEngineLock(a.StateDir)
		if ok && pid != os.Getpid() && proc.AliveSince(pid, started) {
			return nil, fmt.Errorf("another workshop engine is already running for this repo (pid %d) — stop it first (`workshop stop`, --force if wedged)", pid)
		}
		if !ok && attempt < 2 {
			// Unreadable could be another engine mid-write (created, not yet
			// encoded) rather than a crash's leftover; give the writer a beat
			// before treating the lock as stale.
			time.Sleep(20 * time.Millisecond)
			continue
		}
		// Stale lock from a crashed engine. Claim it atomically before
		// deleting: rename to a unique name, so of two racers exactly one
		// succeeds and removes it. A bare check-then-remove here could delete
		// the OTHER racer's freshly created lock (both failed O_EXCL, both
		// saw the dead pid) and let two engines run. The loser's rename
		// fails, it loops, and then sees the winner's fresh lock — alive pid,
		// the proper error above.
		claimed := fmt.Sprintf("%s.stale-%d-%d", path, os.Getpid(), staleLockSeq.Add(1))
		if err := os.Rename(path, claimed); err == nil {
			_ = os.Remove(claimed)
		} else {
			// Lost the claim race: let the winner finish recreating its
			// fresh lock so the next probe sees a live holder.
			time.Sleep(25 * time.Millisecond)
		}
	}
	return nil, fmt.Errorf("cannot acquire the engine lock at %s", path)
}

// resolvePool resolves custom pool paths relative to the repo config dir.
func (a *App) resolvePool(nameOrPath string) string {
	switch nameOrPath {
	case "", "general", "gamedev":
		return nameOrPath
	}
	if filepath.IsAbs(nameOrPath) {
		return nameOrPath
	}
	return filepath.Join(a.RepoDir, config.RepoConfigDir, nameOrPath)
}

// RunHeadless drives every enabled pipeline (concurrently, with worktrees +
// the merge queue when enabled) until the iteration bound, drain, or ctx
// cancel. It is the engine behind both `workshop run` and `workshop up`.
//
// When startStopped is set (the `up` default), every enabled pipeline is
// parked (operator-halted) before any worker starts, so the server + dashboard
// come up but nothing runs until the operator resumes a pipeline.
func (a *App) RunHeadless(ctx context.Context, iterations int, untilDrained, startStopped bool) error {
	pipelines := a.EnabledPipelines()
	if len(pipelines) == 0 {
		return fmt.Errorf("no enabled pipelines")
	}
	// Exactly one engine per repo: a second `run`/`up` would put two
	// workers on the same worktrees and branches.
	release, err := a.acquireEngineLock()
	if err != nil {
		return err
	}
	defer release()
	// A previous crash may have left pass rows open; close them so
	// liveness is derived from truth. Scoped to engine startup (under the
	// lock) — running it on every app.Open let a mere `workshop status`
	// mark another process's in-flight pass as failed.
	_, _ = a.Store.CleanupOrphanPasses(ctx)
	// Same for claims: a crash between claim and settle leaves the task
	// status='claimed' forever — invisible to Claim (open-only) and its title
	// blocked by proposal dedupe. No pass can be live here (engine lock is
	// held, no worker started), so every claim is an orphan.
	_, _ = a.Store.ReleaseOrphanClaims(ctx)
	// Cap the append-only tables; an always-on loop grows them unboundedly.
	_ = a.Store.Prune(ctx, 20000, 5000)
	// Come up parked: seed the operator halt under the engine lock, before
	// any worker starts, so each worker's first-pass HaltedReason check sees
	// it and parks. Uses the same "operator" reason the dashboard's resume
	// button clears (see SetPipelineDesired).
	if startStopped {
		for _, p := range pipelines {
			if err := a.Store.SetHalted(ctx, p.Name, "operator"); err != nil {
				return err
			}
		}
	}
	cfg := a.Res().Config

	trunk := cfg.Project.Trunk
	if trunk == "" {
		var err error
		if trunk, err = gitx.CurrentBranch(ctx, a.RepoDir); err != nil || trunk == "" {
			return fmt.Errorf("cannot determine trunk branch (set project.trunk, or check out a branch): %v", err)
		}
	}
	// Probe the trunk, retrying through a transient git error rather than
	// mislabelling a live repo's trunk as missing. When Halt (dashboard or
	// add-pipeline auto-relaunch) cancels a run mid git-op, the killed git
	// child keeps a brief grip on repo/.git on Windows, so this first probe of
	// the relaunch can transiently fail; treating that as "absent" would take
	// the whole engine down. A clean "ref absent" still fails fast.
	var trunkExists bool
	for attempt := 0; ; attempt++ {
		var err error
		if trunkExists, err = gitx.BranchExistsErr(ctx, a.RepoDir, trunk); err == nil {
			break
		} else if attempt >= 10 {
			return fmt.Errorf("cannot verify trunk branch %q: %w", trunk, err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
	if !trunkExists {
		return fmt.Errorf("trunk branch %q does not exist", trunk)
	}

	// Worktree mode is all-or-nothing: the integrator owns the main
	// checkout, so no worker may share it while lanes exist.
	worktrees := cfg.WorktreesEnabled()
	for _, p := range pipelines {
		if p.Worktree != nil && *p.Worktree {
			worktrees = true
		}
	}

	multi := len(pipelines) > 1
	maxConc := cfg.Safety.MaxConcurrent
	if maxConc < 1 {
		maxConc = 1
	}
	sem := semaphore.NewWeighted(int64(maxConc))
	// One agy serialization lock per engine: art passes take it exclusively
	// (their conversation-resume record is a shared file every agy run
	// rewrites), ordinary agy passes share it, claude passes ignore it.
	agyMu := &sync.RWMutex{}

	// Verify agy's art models in the background: art passes read the
	// verified label from the kv store per pass, so the result applies the
	// moment the probe lands — no need to hold up engine start on it. The
	// probe shares agyMu (it is an agy run like any other).
	a.VerifyArtModelsAsync(ctx, agyMu)

	var workers []*engine.Worker
	var lanes []domain.Pipeline

	if worktrees {
		_ = gitx.PruneWorktrees(ctx, a.RepoDir)
		for i := range pipelines {
			p := &pipelines[i]
			p.Branch = cfg.Git.BranchPrefix + p.Name
			p.Dir = a.RepoDir + "-wt-" + p.Name
			// Same rationale as the trunk probe above: on a Halt-triggered
			// relaunch a killed git child can briefly keep a grip on
			// repo/.git (Windows), so the first worktree ops can transiently
			// fail; retry rather than take the whole server down.
			for attempt := 0; ; attempt++ {
				if err := gitx.AddWorktree(ctx, a.RepoDir, p.Dir, p.Branch, trunk); err == nil {
					break
				} else if attempt >= 10 {
					return fmt.Errorf("worktree for %s: %w", p.Name, err)
				}
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(50 * time.Millisecond):
				}
			}
			// Lanes sync from the gate-proven green ref, never live trunk
			// (mid-round trunk holds unvetted merges that may be reset away).
			w, err := a.BuildWorker(*p, multi, WorkerOpts{Dir: p.Dir, SyncTrunk: engine.GreenRef, Sem: sem, AgyMu: agyMu})
			if err != nil {
				return err
			}
			workers = append(workers, w)
			lanes = append(lanes, *p)
		}
	} else {
		if cur, _ := gitx.CurrentBranch(ctx, a.RepoDir); cur != trunk {
			if err := gitx.CheckoutBranch(ctx, a.RepoDir, trunk); err != nil {
				return fmt.Errorf("checkout %s: %w", trunk, err)
			}
		}
		var treeMu *sync.Mutex
		if multi {
			// Plural pipelines on one tree: serialized passes, never
			// concurrent (safe but slower — worktrees are the fix).
			treeMu = &sync.Mutex{}
		}
		for i := range pipelines {
			p := &pipelines[i]
			p.Branch = trunk
			p.Dir = a.RepoDir
			w, err := a.BuildWorker(*p, multi, WorkerOpts{TreeMu: treeMu, Sem: sem, AgyMu: agyMu})
			if err != nil {
				return err
			}
			workers = append(workers, w)
		}
	}

	var integ *engine.Integrator
	if len(lanes) > 0 {
		_, hasConflictRoute := cfg.Types["merge-conflict"]
		integ = engine.NewIntegrator(engine.IntegratorConfig{
			RepoDir:       a.RepoDir,
			Trunk:         trunk,
			Lanes:         lanes,
			GateCmd:       cfg.Project.Verify,
			GateDir:       cfg.Project.VerifyDir,
			ConflictTasks: hasConflictRoute,
			BranchPrefix:  cfg.Git.BranchPrefix,
			Interval:      45 * time.Second,
		}, a.Store, a.Bus)
	}

	spec := engine.RunSpec{
		Workers:      workers,
		Iterations:   iterations,
		UntilDrained: untilDrained,
	}
	if integ != nil {
		// RunSpec.Integrator is an interface: assigning a nil *Integrator
		// directly would make it non-nil and enable the bounded drain.
		spec.Integrator = integ
	}
	return engine.NewSupervisor(a.Store, a.Bus).Run(ctx, spec)
}

// PipelineStatus is one pipeline's live snapshot. Agent/Model/Effort are the
// EFFECTIVE bundle (config with the live override applied); Override carries
// the raw override when one is active.
type PipelineStatus struct {
	Name         string `json:"name"`
	Mode         string `json:"mode"`                   // EFFECTIVE goal | discover | drain — config with any live override applied
	ModeOverride string `json:"modeOverride,omitempty"` // raw override when one is active
	// Personality is the EFFECTIVE selector ("" | "none" | "random" | a roster
	// entry) — config with any live override applied; PersonalityOverride
	// carries the raw override when one is active. Mirrors Mode/ModeOverride.
	Personality         string          `json:"personality,omitempty"`
	PersonalityOverride string          `json:"personalityOverride,omitempty"`
	Enabled             bool            `json:"enabled"`
	Agent               string          `json:"agent"`
	Model               string          `json:"model,omitempty"`
	Effort              string          `json:"effort,omitempty"`
	Override            *domain.Bundle  `json:"override,omitempty"`
	Halted              string          `json:"halted,omitempty"`
	Running             *domain.Pass    `json:"running,omitempty"` // the in-flight pass
	LastPass            *domain.Pass    `json:"lastPass,omitempty"`
	Progress            domain.Progress `json:"progress"`
	ProgressAgeSec      int64           `json:"progressAgeSec"`
	BacklogExclusive    int             `json:"backlogExclusive"`
}

// Status is the one-shot project snapshot for CLI/HTTP.
type Status struct {
	Repo          string               `json:"repo"`
	StateDir      string               `json:"stateDir"`
	SharedBacklog int                  `json:"sharedBacklog"`
	Pipelines     []PipelineStatus     `json:"pipelines"`
	Completions   []*domain.Completion `json:"completions"`
	RecentCommits []gitx.Commit        `json:"recentCommits"`
	Warnings      []string             `json:"warnings,omitempty"`
}

// Snapshot assembles the status.
func (a *App) Snapshot(ctx context.Context) (*Status, error) {
	res := a.Res()
	st := &Status{Repo: a.RepoDir, StateDir: a.StateDir, Warnings: res.Warnings}

	open, err := a.Store.ListTasks(ctx, store.TaskFilter{
		Statuses: []domain.TaskStatus{domain.TaskOpen, domain.TaskClaimed},
	})
	if err != nil {
		return nil, err
	}
	counts := map[string]int{}
	for _, t := range open {
		counts[t.Backlog]++
	}
	st.SharedBacklog = counts[domain.MainBacklog]

	for _, p := range res.Config.ResolvedPipelines() {
		override, _ := a.Store.PipelineBundle(ctx, p.Name)
		eff := route.Effective(override, p.Bundle)
		mode := p.Mode()
		modeOverride, _ := a.Store.PipelineMode(ctx, p.Name)
		if modeOverride != "" {
			mode = modeOverride
		}
		personality := p.Personality
		personalityOverride, _ := a.Store.PipelinePersonality(ctx, p.Name)
		if personalityOverride != "" {
			personality = personalityOverride
		}
		ps := PipelineStatus{
			Name: p.Name, Mode: mode, ModeOverride: modeOverride, Enabled: p.Enabled,
			Personality: personality, PersonalityOverride: personalityOverride,
			Agent: eff.Agent, Model: eff.Model, Effort: eff.Effort,
			BacklogExclusive: counts[p.Name],
		}
		if !override.IsZero() {
			o := override
			ps.Override = &o
		}
		ps.Halted, _ = a.Store.HaltedReason(ctx, p.Name)
		ps.Running, _ = a.Store.RunningPass(ctx, p.Name)
		if passes, err := a.Store.RecentPasses(ctx, p.Name, 1); err == nil && len(passes) > 0 {
			ps.LastPass = passes[0]
		}
		dir := statedir.PipelineDir(a.StateDir, p.Name)
		ps.Progress = statedir.ReadProgress(dir)
		if info, err := os.Stat(filepath.Join(dir, statedir.ProgressFile)); err == nil {
			ps.ProgressAgeSec = int64(time.Since(info.ModTime()).Seconds())
		}
		st.Pipelines = append(st.Pipelines, ps)
	}

	if st.Completions, err = a.Store.ListCompletions(ctx, 10); err != nil {
		return nil, err
	}
	st.RecentCommits, _ = gitx.RecentCommits(ctx, a.RepoDir, 8)
	return st, nil
}
