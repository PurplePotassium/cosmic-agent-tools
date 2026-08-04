// Package app is the facade that wires config, store, bus, and the
// workflow/art engines together. The CLI and HTTP server consume only this package (plus domain),
// never the internals directly.
package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/PurplePotassium/cosmic-agent-tools/hal/internal/bus"
	"github.com/PurplePotassium/cosmic-agent-tools/hal/internal/config"
	"github.com/PurplePotassium/cosmic-agent-tools/hal/internal/domain"
	"github.com/PurplePotassium/cosmic-agent-tools/hal/internal/driver"
	"github.com/PurplePotassium/cosmic-agent-tools/hal/internal/gitx"
	"github.com/PurplePotassium/cosmic-agent-tools/hal/internal/proc"
	"github.com/PurplePotassium/cosmic-agent-tools/hal/internal/statedir"
	"github.com/PurplePotassium/cosmic-agent-tools/hal/internal/store"
	"github.com/PurplePotassium/cosmic-agent-tools/hal/internal/workflow"
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

	// agyMu is the process-wide agy serialization lock (see artjob.ArtRunner):
	// art jobs hold it exclusively across their whole agy sequence, and
	// on-demand agy probes (the settings panel's "re-verify models") take the
	// shared slot — one discipline for every agy spawn. Zero value ready.
	agyMu sync.RWMutex
	// artVerifying is set while an agy art-model verification is in flight
	// (launch probe or dashboard re-verify), so the settings panel can show
	// progress and re-clicks don't stack probes.
	artVerifying atomic.Bool
	// artJobRunning is the art-job single-flight guard (see RunArtJob).
	artJobRunning atomic.Bool
	// artDrv overrides the art orchestrator driver (tests inject the fake;
	// nil = claude).
	artDrv driver.Driver

	// env.go's probe cache (tool versions are slow to spawn, the settings
	// panel may poll).
	envMu sync.Mutex
	envAt time.Time
	env   *EnvInfo

	// The interactive workflow engine (see workflows.go). Built lazily so
	// read-only CLI paths never construct it.
	wfMu      sync.Mutex
	wfManager *workflow.Manager
}

// SetArtDriver injects the art orchestrator driver — the seam tests use to
// substitute the fake driver for the real claude CLI.
func (a *App) SetArtDriver(d driver.Driver) { a.artDrv = d }

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
	if _, serr := os.Stat(filepath.Join(stateDir, "hal.db")); os.IsNotExist(serr) {
		if twins := config.SiblingProjectDirs(root); len(twins) > 0 {
			res.Warnings = append(res.Warnings, fmt.Sprintf(
				"no hal state for this repo path yet, but state exists for another repo named %q — if you moved the repo, copy %s to %s to keep its backlog and history",
				filepath.Base(root), strings.Join(twins, " or "), stateDir))
		}
	}

	st, err := store.Open(filepath.Join(stateDir, "hal.db"))
	if err != nil {
		return nil, err
	}
	return New(root, stateDir, res, st, bus.New(st)), nil
}

// Close releases resources.
func (a *App) Close() error { return a.Store.Close() }

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

// acquireEngineLock takes the per-repo engine singleton lock, clearing a
// stale lock left by a crashed engine. It returns the release func.
//
// The whole check-remove-create sequence runs under an OS lock on a sidecar
// mutex file, so stale-lock takeover is atomic across processes. Anything
// weaker races: a rename-claim of the stale file can just as easily rename
// away the winner's freshly created lock (rename acts on whatever is at the
// path, not the file that was inspected) and let two engines run.
func (a *App) acquireEngineLock() (func(), error) {
	path := EngineLockPath(a.StateDir)
	unlock, err := statedir.LockFile(path + ".mutex")
	if err != nil {
		return nil, fmt.Errorf("cannot acquire the engine lock mutex: %w", err)
	}
	defer unlock()

	if pid, started, ok := ReadEngineLock(a.StateDir); ok && pid != os.Getpid() && proc.AliveSince(pid, started) {
		return nil, fmt.Errorf("another hal engine is already running for this repo (pid %d) — stop it first (`hal stop`, --force if wedged)", pid)
	}
	// Dead holder (or no lock at all): take over. Remove-then-create is safe
	// here — every writer holds the mutex.
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("cannot clear the stale engine lock at %s: %w", path, err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("cannot acquire the engine lock at %s: %w", path, err)
	}
	enc := json.NewEncoder(f)
	_ = enc.Encode(engineLock{PID: os.Getpid(), Started: time.Now().UTC()})
	f.Close()
	return func() { _ = os.Remove(path) }, nil
}

// Status is the one-shot project snapshot for CLI/HTTP: the ideas inbox
// (open tasks), live/recent workflows, recent completions, and recent
// commits. The old per-pipeline lane details died with the pass loop.
type Status struct {
	Repo          string               `json:"repo"`
	StateDir      string               `json:"stateDir"`
	OpenTasks     int                  `json:"openTasks"` // ideas-inbox size
	Workflows     []domain.Workflow    `json:"workflows,omitempty"`
	Completions   []*domain.Completion `json:"completions"`
	RecentCommits []gitx.Commit        `json:"recentCommits"`
	Warnings      []string             `json:"warnings,omitempty"`
}

// Snapshot assembles the status.
func (a *App) Snapshot(ctx context.Context) (*Status, error) {
	res := a.Res()
	st := &Status{Repo: a.RepoDir, StateDir: a.StateDir, Warnings: res.Warnings}

	open, err := a.Store.ListTasks(ctx, store.TaskFilter{
		Statuses: []domain.TaskStatus{domain.TaskOpen},
	})
	if err != nil {
		return nil, err
	}
	st.OpenTasks = len(open)

	// Live workflows (all=false filters to the non-terminal ones).
	st.Workflows, _ = a.Store.ListWorkflows(ctx, false)

	if st.Completions, err = a.Store.ListCompletions(ctx, 10); err != nil {
		return nil, err
	}
	st.RecentCommits, _ = gitx.RecentCommits(ctx, a.RepoDir, 8)
	return st, nil
}
