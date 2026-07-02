// Package app is the facade that wires config, store, bus, and engine
// together. The CLI and HTTP server consume only this package (plus domain),
// never the internals directly.
package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/sync/semaphore"

	"github.com/gw1108/cosmic-agent-tools/workshop/internal/bus"
	"github.com/gw1108/cosmic-agent-tools/workshop/internal/config"
	"github.com/gw1108/cosmic-agent-tools/workshop/internal/domain"
	"github.com/gw1108/cosmic-agent-tools/workshop/internal/driver"
	"github.com/gw1108/cosmic-agent-tools/workshop/internal/engine"
	"github.com/gw1108/cosmic-agent-tools/workshop/internal/gitx"
	"github.com/gw1108/cosmic-agent-tools/workshop/internal/prompt"
	"github.com/gw1108/cosmic-agent-tools/workshop/internal/route"
	"github.com/gw1108/cosmic-agent-tools/workshop/internal/statedir"
	"github.com/gw1108/cosmic-agent-tools/workshop/internal/store"
)

// App is one opened project.
type App struct {
	RepoDir  string
	StateDir string
	Res      *config.Result
	Store    *store.Store
	Bus      *bus.Bus
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

	stateDir := config.ProjectStateDir(root)
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return nil, err
	}
	st, err := store.Open(filepath.Join(stateDir, "workshop.db"))
	if err != nil {
		return nil, err
	}
	return &App{
		RepoDir:  root,
		StateDir: stateDir,
		Res:      res,
		Store:    st,
		Bus:      bus.New(st),
	}, nil
}

// Close releases resources.
func (a *App) Close() error { return a.Store.Close() }

// EnabledPipelines returns the enabled resolved pipelines.
func (a *App) EnabledPipelines() []domain.Pipeline {
	var out []domain.Pipeline
	for _, p := range a.Res.Config.ResolvedPipelines() {
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
}

// BuildWorker wires one pipeline's worker.
func (a *App) BuildWorker(p domain.Pipeline, multi bool, opts WorkerOpts) (*engine.Worker, error) {
	// Validate the pipeline's default agent early; per-task routing may
	// still bring in other drivers at pass time.
	if _, err := driver.New(p.Bundle.Agent); err != nil {
		return nil, err
	}
	cfg := a.Res.Config
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
	for _, pl := range a.Res.Config.ResolvedPipelines() {
		known[pl.Name] = true
	}

	workDir := opts.Dir
	if workDir == "" {
		workDir = a.RepoDir
	}
	wc := engine.WorkerConfig{
		Pipeline:        p,
		Multi:           multi,
		RepoDir:         workDir,
		StateDir:        statedir.PipelineDir(a.StateDir, p.Name),
		LogDir:          filepath.Join(a.StateDir, "logs", p.Name),
		GoalPath:        config.GoalFile(a.RepoDir),
		PromptsDir:      config.PromptsDir(a.RepoDir),
		Verify:          cfg.Project.Verify,
		VerifyDir:       cfg.Project.VerifyDir,
		SkipPermissions: cfg.Safety.SkipPermissions,
		KnownPipelines:  known,
		BreakerLimit:    cfg.Safety.BreakerFailures,
		Types:           cfg.Types,
		ClassifierMode:  cfg.Classifier.Mode,
		Vocabulary:      route.Vocabulary(cfg.Types, cfg.ResolvedPipelines()),
		SpiceEnabled:    cfg.Spice.Enabled,
		Personas:        personas,
		Nouns:           nouns,
		SyncTrunk:       opts.SyncTrunk,
		TreeMu:          opts.TreeMu,
		Sem:             opts.Sem,
	}
	return engine.NewWorker(wc, a.Store, a.Bus), nil
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
func (a *App) RunHeadless(ctx context.Context, iterations int, untilDrained bool) error {
	pipelines := a.EnabledPipelines()
	if len(pipelines) == 0 {
		return fmt.Errorf("no enabled pipelines")
	}
	cfg := a.Res.Config

	trunk := cfg.Project.Trunk
	if trunk == "" {
		var err error
		if trunk, err = gitx.CurrentBranch(ctx, a.RepoDir); err != nil || trunk == "" {
			return fmt.Errorf("cannot determine trunk branch (set project.trunk, or check out a branch): %v", err)
		}
	}
	if !gitx.BranchExists(ctx, a.RepoDir, trunk) {
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

	var workers []*engine.Worker
	var lanes []domain.Pipeline

	if worktrees {
		_ = gitx.PruneWorktrees(ctx, a.RepoDir)
		for i := range pipelines {
			p := &pipelines[i]
			p.Branch = cfg.Git.BranchPrefix + p.Name
			p.Dir = a.RepoDir + "-wt-" + p.Name
			if err := gitx.AddWorktree(ctx, a.RepoDir, p.Dir, p.Branch, trunk); err != nil {
				return fmt.Errorf("worktree for %s: %w", p.Name, err)
			}
			w, err := a.BuildWorker(*p, multi, WorkerOpts{Dir: p.Dir, SyncTrunk: trunk, Sem: sem})
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
			w, err := a.BuildWorker(*p, multi, WorkerOpts{TreeMu: treeMu, Sem: sem})
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

	return engine.NewSupervisor(a.Store, a.Bus).Run(ctx, engine.RunSpec{
		Workers:      workers,
		Integrator:   integ,
		Iterations:   iterations,
		UntilDrained: untilDrained,
	})
}

// PipelineStatus is one pipeline's live snapshot.
type PipelineStatus struct {
	Name            string          `json:"name"`
	Enabled         bool            `json:"enabled"`
	Agent           string          `json:"agent"`
	Model           string          `json:"model,omitempty"`
	Effort          string          `json:"effort,omitempty"`
	Halted          string          `json:"halted,omitempty"`
	LastPass        *domain.Pass    `json:"lastPass,omitempty"`
	Progress        domain.Progress `json:"progress"`
	ProgressAgeSec  int64           `json:"progressAgeSec"`
	BacklogExclusive int            `json:"backlogExclusive"`
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
	st := &Status{Repo: a.RepoDir, StateDir: a.StateDir, Warnings: a.Res.Warnings}

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

	for _, p := range a.Res.Config.ResolvedPipelines() {
		ps := PipelineStatus{
			Name: p.Name, Enabled: p.Enabled,
			Agent: p.Bundle.Agent, Model: p.Bundle.Model, Effort: p.Bundle.Effort,
			BacklogExclusive: counts[p.Name],
		}
		ps.Halted, _ = a.Store.HaltedReason(ctx, p.Name)
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
