// Package app is the facade that wires config, store, bus, and engine
// together. The CLI and HTTP server consume only this package (plus domain),
// never the internals directly.
package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/gw1108/cosmic-agent-tools/workshop/internal/bus"
	"github.com/gw1108/cosmic-agent-tools/workshop/internal/config"
	"github.com/gw1108/cosmic-agent-tools/workshop/internal/domain"
	"github.com/gw1108/cosmic-agent-tools/workshop/internal/driver"
	"github.com/gw1108/cosmic-agent-tools/workshop/internal/engine"
	"github.com/gw1108/cosmic-agent-tools/workshop/internal/gitx"
	"github.com/gw1108/cosmic-agent-tools/workshop/internal/prompt"
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

// BuildWorker wires one pipeline's worker (simple mode: works in the repo
// dir; worktree mode arrives with the integrator).
func (a *App) BuildWorker(p domain.Pipeline, multi bool) (*engine.Worker, error) {
	drv, err := driver.New(p.Bundle.Agent)
	if err != nil {
		return nil, err
	}
	cfg := a.Res.Config

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

	wc := engine.WorkerConfig{
		Pipeline:        p,
		Multi:           multi,
		RepoDir:         a.RepoDir,
		StateDir:        statedir.PipelineDir(a.StateDir, p.Name),
		LogDir:          filepath.Join(a.StateDir, "logs", p.Name),
		GoalPath:        config.GoalFile(a.RepoDir),
		PromptsDir:      config.PromptsDir(a.RepoDir),
		Verify:          cfg.Project.Verify,
		VerifyDir:       cfg.Project.VerifyDir,
		SkipPermissions: cfg.Safety.SkipPermissions,
		KnownPipelines:  known,
		BreakerLimit:    cfg.Safety.BreakerFailures,
		SpiceEnabled:    cfg.Spice.Enabled,
		Personas:        personas,
		Nouns:           nouns,
	}
	return engine.NewWorker(wc, drv, a.Store, a.Bus), nil
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

// RunHeadless drives the first enabled pipeline for a bounded run (the
// `workshop run` smoke mode). Multi-pipeline concurrency lands with the
// supervisor.
func (a *App) RunHeadless(ctx context.Context, iterations int, untilDrained bool) error {
	pipelines := a.EnabledPipelines()
	if len(pipelines) == 0 {
		return fmt.Errorf("no enabled pipelines")
	}
	p := pipelines[0]
	if len(pipelines) > 1 {
		fmt.Fprintf(os.Stderr, "note: %d pipelines enabled; headless run drives only %q for now\n", len(pipelines), p.Name)
	}
	if trunk := a.Res.Config.Project.Trunk; trunk != "" && gitx.BranchExists(ctx, a.RepoDir, trunk) {
		if cur, _ := gitx.CurrentBranch(ctx, a.RepoDir); cur != trunk {
			if err := gitx.CheckoutBranch(ctx, a.RepoDir, trunk); err != nil {
				return fmt.Errorf("checkout %s: %w", trunk, err)
			}
		}
	}
	w, err := a.BuildWorker(p, len(pipelines) > 1)
	if err != nil {
		return err
	}
	return w.Loop(ctx, iterations, untilDrained)
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
