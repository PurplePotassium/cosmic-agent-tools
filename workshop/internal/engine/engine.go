// Package engine is the per-project Workshop loop — the Go port of workshop.ps1.
// Each pass: read live agent/model + state → assemble the prompt (with the state-dir
// header injection + anti-circling spice) → spawn the coding agent in the project's
// working directory (per-driver: claude piped & captured, agy inheriting the console)
// → reconcile the agent's state-file writes back into the DB → auto-commit → record.
// It ports the full behavior: circuit-breaker, anti-circling, per-pass agent/model
// re-read, auto-routing, auth-failure detection, and stale index.lock cleanup.
package engine

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/agent"
	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/events"
	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/gitx"
	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/procctl"
	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/store"
)

// authRe matches captured output that signals an agent auth failure (which will not
// self-heal — the loop stops for the human). Narrow so a normal pass mentioning
// "token"/"login" doesn't read as an auth failure. Blind for agy (no captured output).
var authRe = regexp.MustCompile(`(?i)auth|credential|sign-?in|re-?authenticate|keyring|unauthorized|\b401\b`)

// maxConsecutiveFails trips the circuit-breaker (something is genuinely wrong).
const maxConsecutiveFails = 5

// Config holds the loop knobs that don't change per pass.
type Config struct {
	Random          bool
	SkipPermissions bool
	Iterations      int // 0 = run until context cancel
	SleepSeconds    int
	ExtraArgs       []string
	PersonaFlavor   string // "gamedev" | "plain"
}

// Engine runs one project's loop.
type Engine struct {
	store    *store.Store
	broker   *events.Broker
	log      *slog.Logger
	cfg      Config
	personas []string
	nouns    []string
	rng      *rand.Rand

	// per-pass liveness: lastLogAt marks the most recent streamed log line so the
	// status ticker can report "computing" vs "waiting on model" without a CPU probe.
	mu        sync.Mutex
	lastLogAt time.Time

	// fixed for the life of the loop
	projectID string
	name      string
	repo      string
	stateDir  string
	logDir    string
	branch    string
}

// New builds an engine for a project.
func New(st *store.Store, br *events.Broker, log *slog.Logger, p *store.Project, cfg Config, personas, nouns []string) *Engine {
	return &Engine{
		store:     st,
		broker:    br,
		log:       log.With("project", p.Name),
		cfg:       cfg,
		personas:  personas,
		nouns:     nouns,
		rng:       rand.New(rand.NewSource(time.Now().UnixNano())),
		projectID: p.ID,
		name:      p.Name,
		repo:      p.RepoPath,
		stateDir:  p.StateDir,
		logDir:    filepath.Join(p.StateDir, "logs"),
		branch:    p.Branch,
	}
}

func (e *Engine) stateHeader() string {
	return "## Workshop state directory (read this first)\n" +
		"Your working directory is the target repo. The operator state files this prompt refers to —\n" +
		"GOAL.md, backlog.json, completions.json, and progress.json — live in this folder instead:\n  " +
		e.stateDir + "\n" +
		"Always read and write those four files at that absolute path, not relative to your working directory.\n\n"
}

// Run executes the loop until Iterations is reached or ctx is cancelled. It returns
// a non-nil error only on a fatal condition (auth failure or the circuit-breaker).
func (e *Engine) Run(ctx context.Context) error {
	if err := os.MkdirAll(e.logDir, 0o755); err != nil {
		return err
	}
	gitx.CheckoutBranch(e.repo, e.branch)

	runID, _ := e.store.StartRun(e.projectID)
	header := e.stateHeader()

	i := 0
	consecutiveFails := 0
	finalStatus := "stopped"
	defer func() { _ = e.store.EndRun(runID, i, finalStatus) }()

	for e.cfg.Iterations == 0 || i < e.cfg.Iterations {
		if ctx.Err() != nil {
			return nil
		}
		i++
		stamp := time.Now().Format("2006-01-02_15-04-05")

		// --- live agent/model selection (re-read each pass) + auto-routing ---
		proj, err := e.store.GetProject(e.projectID)
		if err != nil {
			return err
		}
		ag, md := proj.Agent, proj.Model
		if ag == "auto" || md == "auto" {
			title, detail := e.topBacklog()
			sel := agent.Classify(title, detail)
			ag, md = sel.Agent, sel.Model
			e.log.Info("auto-route", "to", ag+"/"+md, "reason", sel.Reason)
		}
		driver := agent.Resolve(ag, md, e.cfg.SkipPermissions, e.cfg.ExtraArgs)

		// --- stale lock cleanup ---
		if cleaned, age := gitx.CleanStaleLock(e.repo); cleaned {
			e.log.Warn("cleaned stale index.lock", "ageSec", age)
		}

		// --- materialize agent-facing state from the DB ---
		snap, err := materializeState(e.stateDir, e.store, e.projectID)
		if err != nil {
			e.log.Warn("materialize state failed", "err", err)
		}

		// --- assemble the prompt (re-read PROMPT.md each pass so UI edits apply) ---
		basePrompt := e.readPrompt()
		iterPrompt := header + basePrompt
		spiceMode := "plain"
		if e.cfg.Random {
			sp := getSpice(e.personas, e.nouns, e.rng)
			iterPrompt = header + basePrompt + "\n\n---\n" + sp.prefix + sp.suffix
			spiceMode = sp.mode
		}

		iterLabel := fmt.Sprintf("iteration %d%s [%s] %s", i, iterTotal(e.cfg.Iterations), spiceMode, stamp)
		e.log.Info("pass start", "iter", i, "agent", ag, "model", md, "spice", spiceMode)

		logPath := filepath.Join(e.logDir, fmt.Sprintf("iter-%04d-%s.log", i, stamp))
		e.writeLogHeader(logPath, iterLabel, md, spiceMode)

		itID, _ := e.store.AddIteration(store.Iteration{
			ProjectID: e.projectID, RunID: runID, Num: i, Agent: ag, Model: md,
			Spice: spiceMode, LogPath: logPath, Status: "running",
		})
		e.broker.Emit(events.TypeIteration, e.projectID, map[string]any{
			"num": i, "phase": "start", "agent": ag, "model": md, "spice": spiceMode, "stamp": stamp,
		})
		e.emitStatus(true, i, ag, md)

		// --- spawn the agent (with a live status ticker pushing elapsed/dirty over SSE) ---
		stopTicker := e.startStatusTicker(ctx, i, ag, md)
		passOut, exitErr := e.spawn(ctx, driver, iterPrompt, logPath, i, stamp)
		stopTicker()

		if ctx.Err() != nil {
			_ = e.store.FinishIteration(itID, "", "", "cancelled")
			return nil
		}

		if exitErr != nil {
			if isAuthFailure(passOut) {
				_ = e.store.FinishIteration(itID, "", "", "auth-fail")
				e.log.Error("agent auth failure — re-authenticate; the loop cannot fix this")
				e.emitStatusErr("auth failure")
				finalStatus = "auth-fail"
				return fmt.Errorf("agent auth failure: %w", exitErr)
			}
			consecutiveFails++
			_ = e.store.FinishIteration(itID, "", "", "failed")
			// still capture whatever the agent self-reported (e.g. "blocked").
			prog := reconcileProgressOnly(e.stateDir, e.store, e.projectID)
			e.broker.Emit(events.TypeProgress, e.projectID, prog)
			e.log.Warn("agent exited non-zero (non-auth) — skipping commit",
				"err", exitErr, "consecutive", consecutiveFails)
			if consecutiveFails >= maxConsecutiveFails {
				finalStatus = "error"
				e.emitStatusErr(fmt.Sprintf("%d consecutive agent failures", maxConsecutiveFails))
				return fmt.Errorf("%d consecutive agent failures", maxConsecutiveFails)
			}
			e.sleepBetween(ctx, i)
			continue
		}
		consecutiveFails = 0

		// --- reconcile the agent's state writes back into the DB ---
		prog := reconcileState(e.stateDir, e.store, e.projectID, snap)
		e.broker.Emit(events.TypeProgress, e.projectID, prog)

		// --- auto-commit whatever the pass changed ---
		msg := fmt.Sprintf("ralph iter %d [%s] %s", i, ag, stamp)
		sha, cerr := gitx.CommitAll(e.repo, msg)
		stat := ""
		if cerr != nil {
			e.log.Warn("git commit failed — changes staged; next pass retries", "err", cerr)
		} else if sha != "" {
			stat = gitx.ShowStat(e.repo)
			e.appendLog(logPath, "\n"+strings.Repeat("-", 72)+"\n--- files changed this pass ---\n"+stat)
			e.broker.Emit(events.TypeCommit, e.projectID, map[string]any{
				"sha": sha, "subject": msg, "iter": i, "time": stamp,
			})
			e.log.Info("committed", "sha", sha)
		}
		_ = e.store.FinishIteration(itID, sha, stat, "ok")
		e.emitStatus(true, i, ag, md)

		e.sleepBetween(ctx, i)
	}
	finalStatus = "done"
	e.log.Info("loop done", "iterations", i)
	return nil
}

// spawn runs one agent pass per driver mode and returns captured output lines plus
// the process's exit error (nil on exit 0).
func (e *Engine) spawn(ctx context.Context, d agent.Driver, prompt, logPath string, iter int, stamp string) ([]string, error) {
	if d.Mode == agent.ModeStdin {
		return e.spawnClaude(ctx, d, prompt, logPath)
	}
	return e.spawnAgy(ctx, d, prompt, logPath, iter, stamp)
}

// spawnClaude pipes the prompt over stdin and streams merged stdout+stderr live to
// the iter log, the console, and SSE — while capturing it for the auth-fail scan.
func (e *Engine) spawnClaude(ctx context.Context, d agent.Driver, prompt, logPath string) ([]string, error) {
	cmd := exec.CommandContext(ctx, d.Exe, d.Args...)
	cmd.Dir = e.repo
	cmd.Stdin = strings.NewReader(prompt)
	procctl.Prepare(cmd)
	cmd.Cancel = func() error { return procctl.KillTree(cmd) }
	cmd.WaitDelay = 10 * time.Second

	// Same *os.File for stdout+stderr = a true 2>&1 merge with no extra goroutines/races.
	pr, pw, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	cmd.Stdout = pw
	cmd.Stderr = pw

	logf, _ := os.OpenFile(logPath, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o644)

	if err := cmd.Start(); err != nil {
		pw.Close()
		pr.Close()
		if logf != nil {
			logf.Close()
		}
		return nil, err
	}
	pw.Close() // parent drops its write end so the reader hits EOF when the child exits

	var out []string
	sc := bufio.NewScanner(pr)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := sc.Text()
		out = append(out, line)
		e.mu.Lock()
		e.lastLogAt = time.Now()
		e.mu.Unlock()
		if logf != nil {
			fmt.Fprintln(logf, line)
		}
		fmt.Println(line)
		e.broker.Emit(events.TypeLog, e.projectID, map[string]any{"line": line})
	}
	pr.Close()
	if logf != nil {
		logf.Close()
	}
	return out, cmd.Wait()
}

// spawnAgy runs agy WITHOUT piping (its stdout is uncapturable/hangs under non-TTY);
// agy inherits the console and writes its operational --log-file, which we tail as the
// only textual record. The real window into an agy pass is progress.json.
func (e *Engine) spawnAgy(ctx context.Context, d agent.Driver, prompt, logPath string, iter int, stamp string) ([]string, error) {
	agyLog := filepath.Join(e.logDir, fmt.Sprintf("agy-%04d-%s.log", iter, stamp))
	args := append(append([]string{}, d.Args...), "-p", prompt, "--log-file", agyLog)
	cmd := exec.CommandContext(ctx, d.Exe, args...)
	cmd.Dir = e.repo
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	procctl.Prepare(cmd)
	cmd.Cancel = func() error { return procctl.KillTree(cmd) }
	cmd.WaitDelay = 10 * time.Second

	runErr := cmd.Run()

	out := []string{fmt.Sprintf("(agy output is LIVE in the loop console; tailable agy log: %s)", agyLog)}
	if b, err := os.ReadFile(agyLog); err == nil {
		out = append(out, tailLines(string(b), 200)...)
	}
	e.appendLog(logPath, strings.Join(out, "\n"))
	return out, runErr
}

func (e *Engine) topBacklog() (title, detail string) {
	items, err := e.store.ListBacklog(e.projectID)
	if err != nil || len(items) == 0 {
		return "", ""
	}
	return items[0].Title, items[0].Detail
}

func (e *Engine) readPrompt() string {
	b, err := os.ReadFile(filepath.Join(e.stateDir, "PROMPT.md"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func (e *Engine) writeLogHeader(path, label, model, spice string) {
	body := strings.Join([]string{
		"=== " + label + " ===",
		"model  : " + model,
		"spice  : " + spice + "  (anti-circling lens for THIS pass, not the task)",
		strings.Repeat("-", 72),
		"",
	}, "\n")
	_ = os.WriteFile(path, []byte(body+"\n"), 0o644)
}

func (e *Engine) appendLog(path, s string) {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintln(f, s)
}

func (e *Engine) sleepBetween(ctx context.Context, i int) {
	if e.cfg.SleepSeconds <= 0 {
		return
	}
	if e.cfg.Iterations != 0 && i >= e.cfg.Iterations {
		return
	}
	select {
	case <-ctx.Done():
	case <-time.After(time.Duration(e.cfg.SleepSeconds) * time.Second):
	}
}

// startStatusTicker pushes a status event every few seconds DURING a pass so the
// dashboard shows live elapsed time, the dirty tree, and a computing/waiting hint —
// all over SSE, so the browser never has to poll. Returns a stop func.
func (e *Engine) startStatusTicker(ctx context.Context, iter int, ag, md string) func() {
	passStart := time.Now()
	e.mu.Lock()
	e.lastLogAt = passStart
	e.mu.Unlock()

	stop := make(chan struct{})
	go func() {
		t := time.NewTicker(3 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ctx.Done():
				return
			case <-t.C:
				e.mu.Lock()
				computing := time.Since(e.lastLogAt) < 6*time.Second
				e.mu.Unlock()
				e.broker.Emit(events.TypeStatus, e.projectID, map[string]any{
					"alive":       true,
					"passSeconds": int(time.Since(passStart).Seconds()),
					"computing":   computing,
					"selAgent":    ag,
					"selModel":    md,
					"lastIter":    iter,
					"dirtyFiles":  gitx.DirtyFiles(e.repo, 12),
				})
			}
		}
	}()
	return func() { close(stop) }
}

func (e *Engine) emitStatus(alive bool, iter int, ag, md string) {
	e.broker.Emit(events.TypeStatus, e.projectID, map[string]any{
		"alive": alive, "lastIter": iter, "selAgent": ag, "selModel": md,
		"dirtyFiles": gitx.DirtyFiles(e.repo, 12),
	})
}

func (e *Engine) emitStatusErr(reason string) {
	e.broker.Emit(events.TypeStatus, e.projectID, map[string]any{
		"alive": false, "error": reason,
	})
}

func isAuthFailure(lines []string) bool {
	for _, l := range lines {
		if authRe.MatchString(l) {
			return true
		}
	}
	return false
}

// reconcileProgressOnly reads just progress.json (used on a failed pass so a "blocked"
// self-report still reaches the UI without touching backlog/completions).
func reconcileProgressOnly(dir string, st *store.Store, projectID string) store.Progress {
	var prog store.Progress
	if err := readJSONFile(filepath.Join(dir, progressFile), &prog); err == nil {
		_ = st.SetProgress(projectID, prog)
	}
	return prog
}

func iterTotal(n int) string {
	if n == 0 {
		return ""
	}
	return fmt.Sprintf("/%d", n)
}

func tailLines(s string, n int) []string {
	lines := strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines
}
