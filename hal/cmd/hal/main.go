// Command hal runs operator-gated agent workflows against the git repo
// it is invoked in, with a local dashboard to drive and watch them.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"
	"time"

	"github.com/PurplePotassium/cosmic-agent-tools/hal/internal/app"
	"github.com/PurplePotassium/cosmic-agent-tools/hal/internal/fakeagent"
	"github.com/PurplePotassium/cosmic-agent-tools/hal/internal/proc"
	"github.com/PurplePotassium/cosmic-agent-tools/hal/internal/server"
)

// Version is stamped by the release build (-ldflags "-X main.Version=...").
var Version = "dev"

func main() {
	// Hidden re-entry point: the fake driver execs this binary as a
	// scripted agent for tests and smoke runs.
	if len(os.Args) > 1 && os.Args[1] == "_fake-agent" {
		os.Exit(fakeagent.Main())
	}
	// agy passthrough: everything after "agy-run" goes to agy verbatim, no
	// flag parsing here (the args belong to agy, not hal).
	if len(os.Args) > 1 && os.Args[1] == "agy-run" {
		os.Exit(cmdAgyRun(os.Args[2:]))
	}

	cmd := "up"
	args := os.Args[1:]
	if len(args) > 0 {
		switch {
		case args[0] == "-h", args[0] == "-help", args[0] == "--help":
			cmd = "help"
		case args[0] == "-v", args[0] == "--version":
			cmd = "version"
		case !strings.HasPrefix(args[0], "-"):
			cmd, args = args[0], args[1:]
		}
	}

	var code int
	switch cmd {
	case "up":
		code = cmdUp(args)
	case "init":
		code = cmdInit(args)
	case "task":
		code = cmdTask(args)
	case "status":
		code = cmdStatus(args)
	case "stop":
		code = cmdStop(args)
	case "doctor":
		code = cmdDoctor(args)
	case "bug":
		code = cmdBug(args)
	case "path":
		code = cmdPath(args)
	case "migrate":
		code = cmdMigrate(args)
	case "version":
		fmt.Println("hal", Version)
	case "help", "-h", "--help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", cmd)
		usage()
		code = 2
	}
	os.Exit(code)
}

func usage() {
	fmt.Print(`hal — operator-gated agent workflows with a live dashboard

Hal drives coding-agent workflows (claude = Claude Code, agy = Gemini
CLI for image generation) against the git repo it is invoked in. Each
workflow is a live conversation that moves through fixed, human-approved
stages (refine → research → design → plan → implement); validation runs
sweep completed implementations on demand or automatically. The
dashboard is where you create workflows, chat, review artifacts, and approve
stage handoffs. Configuration lives in .hal/config.toml — created with
the default settings on first launch (`+"`hal init`"+` scaffolds it too,
along with GOAL.md).

usage: hal [command] [flags]

  up       start the server + workflow engine in the foreground (default)
  init     scaffold .hal/ (config.toml + GOAL.md + .claude/agents)
  task     ideas inbox: add, list, rm
  status   one-shot snapshot (--json for machines)
  stop     gracefully stop the running server (--force kills a hung engine)
  doctor   check the environment (git, agents, config, state dir)
  bug      write a self-contained bug report (env, git, config, status; --logs adds the last art/inquiry log)
  agy-run  run agy with the given args in a hidden console (agy drops output
           on pipes) — how art-job orchestrators invoke it; exits with agy's code
  path     print resolved directories and config files
  migrate  import GOAL/PROMPT/backlog from the old PowerShell hal
  version  print the version

Run any command with -h for its flags.
`)
}

func interruptCtx() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt)
}

func openApp(ctx context.Context, repo string) (*app.App, error) {
	a, err := app.Open(ctx, repo)
	if err != nil {
		return nil, err
	}
	for _, w := range a.Res().Warnings {
		fmt.Fprintln(os.Stderr, "config warning:", w)
	}
	return a, nil
}

func cmdUp(args []string) int {
	fs := flag.NewFlagSet("up", flag.ExitOnError)
	repo := fs.String("repo", "", "repository path (default: enclosing repo of cwd)")
	port := fs.Int("port", 0, "port override (default: config server.port)")
	noOpen := fs.Bool("no-open", false, "don't open the browser")
	// Tolerated no-op from the pass-loop era: workflows are inherently idle
	// until the operator drives them, so there is nothing to "start running".
	_ = fs.Bool("start-running", false, "deprecated: ignored (workflows idle until driven from the dashboard)")
	_ = fs.Parse(args)

	ctx, cancel := interruptCtx()
	defer cancel()

	ensureRepoConfig(ctx, *repo)
	a, err := openApp(ctx, *repo)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 2
	}
	defer a.Close()

	// A second `hal up` in the same repo just surfaces the running one.
	switch si, state := classifyServerRecord(a.StateDir, pingServer, proc.AliveSince); state {
	case recordResponding:
		url := fmt.Sprintf("http://127.0.0.1:%d/", si.Port)
		fmt.Printf("hal already running for this repo (pid %d) — %s\n", si.PID, url)
		if !*noOpen && a.Res().Config.Server.OpenBrowser {
			openBrowser(url + "#token=" + si.Token)
		}
		return 0
	case recordUnresponsive:
		fmt.Fprintf(os.Stderr, "error: another hal appears to be running for this repo (pid %d) but is not responding — leaving its server.json in place; use `hal stop --force` to kill it\n", si.PID)
		return 1
	}

	// `up` runs the interactive workflow engine: live conversations gated by
	// operator approvals.
	ctl := app.NewEngineControl(func(ctx context.Context, startStopped bool) error {
		return a.RunWorkflows(ctx)
	})
	srv := server.New(a, cancel, ctl.Halt)
	srv.Version = Version
	wantPort := a.Res().Config.Server.Port
	if *port != 0 {
		wantPort = *port
	}
	bound, err := srv.Start(wantPort)
	if err != nil {
		// Port taken: fall back to an ephemeral port and say so.
		bound, err = srv.Start(0)
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			return 2
		}
		fmt.Printf("port %d is taken — using %d instead\n", wantPort, bound)
	}
	defer func() {
		shCtx, shCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shCancel()
		srv.Shutdown(shCtx)
	}()

	url := fmt.Sprintf("http://127.0.0.1:%d/", bound)
	fmt.Printf("hal %s\n  repo:  %s\n  state: %s\n  url:   %s\n", Version, a.RepoDir, a.StateDir, url)
	fmt.Println("  workflows are driven from the dashboard — create one there to start.")

	ctl.Start(ctx, false)

	// Auto-diagnose failed workflow turns: when a workflow enters the error
	// state, fire the read-only self-evaluator so its answer lands beside the
	// banner in the dashboard without the operator having to ask.
	stopAutoInquiry := a.StartAutoInquiry(ctx)
	defer stopAutoInquiry()

	if !*noOpen && a.Res().Config.Server.OpenBrowser {
		openBrowser(url + "#token=" + srv.Token())
	}

	select {
	case <-ctx.Done():
		fmt.Println("\nstopping...")
		<-ctl.Done()
	case err := <-ctl.Done():
		switch {
		case err == nil || errors.Is(err, context.Canceled):
			// shutting down
		default:
			fmt.Fprintln(os.Stderr, "engine error:", err)
			return 1
		}
	}
	return 0
}

// serverRecordState classifies a pre-existing server.json during `up`.
type serverRecordState int

const (
	recordNone         serverRecordState = iota // no (readable) server.json
	recordResponding                            // ping OK — surface the running server
	recordUnresponsive                          // ping failed but the PID is alive — never clobber
	recordStale                                 // ping failed AND the PID is dead — record removed
)

// classifyServerRecord decides what `up` does with an existing server.json.
// ping and alive are injectable (pingServer / proc.AliveSince in production).
// The record is only removed when its process is provably dead: a slow or
// wedged-but-alive server must keep its server.json, or a second `up` would
// orphan the live instance (mirrors cmdStop's AliveSince fallback).
func classifyServerRecord(stateDir string, ping func(port int, token string) bool, alive func(pid int, started time.Time) bool) (*server.ServerInfo, serverRecordState) {
	si, err := server.ReadInfo(stateDir)
	if err != nil {
		return nil, recordNone
	}
	if ping(si.Port, si.Token) {
		return si, recordResponding
	}
	if alive(si.PID, si.Started) {
		return si, recordUnresponsive
	}
	_ = os.Remove(server.InfoPath(stateDir))
	return si, recordStale
}

func cmdStatus(args []string) int {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	repo := fs.String("repo", "", "repository path")
	asJSON := fs.Bool("json", false, "machine-readable output")
	_ = fs.Parse(args)

	ctx, cancel := interruptCtx()
	defer cancel()
	a, err := openApp(ctx, *repo)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 2
	}
	defer a.Close()

	snap, err := a.Snapshot(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	serverUp := false
	if si, err := server.ReadInfo(a.StateDir); err == nil && pingServer(si.Port, si.Token) {
		serverUp = true
	}

	if *asJSON {
		printJSON(map[string]any{"serverRunning": serverUp, "status": snap})
		return 0
	}
	fmt.Printf("repo:   %s\nserver: %v\nideas inbox: %d open\n", snap.Repo, serverUp, snap.OpenTasks)
	for _, w := range snap.Workflows {
		fmt.Printf("workflow %-28s stage=%-9s status=%s", w.ID, w.Stage, w.Status)
		if w.Error != "" {
			fmt.Printf(" error=%s", w.Error)
		}
		fmt.Println()
	}
	if len(snap.RecentCommits) > 0 {
		fmt.Println("recent commits:")
		for _, c := range snap.RecentCommits {
			fmt.Printf("  %s  %s\n", c.SHA, c.Subject)
		}
	}
	return 0
}

func cmdStop(args []string) int {
	fs := flag.NewFlagSet("stop", flag.ExitOnError)
	repo := fs.String("repo", "", "repository path")
	force := fs.Bool("force", false, "kill the engine's process tree when it doesn't respond to a graceful stop")
	_ = fs.Parse(args)

	ctx, cancel := interruptCtx()
	defer cancel()
	a, err := openApp(ctx, *repo)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 2
	}
	defer a.Close()

	forceKill := func(pid int) int {
		if err := proc.KillTree(pid); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			return 1
		}
		_ = os.Remove(server.InfoPath(a.StateDir))
		_ = os.Remove(app.EngineLockPath(a.StateDir))
		fmt.Printf("killed hal engine (pid %d) and its process tree\n", pid)
		return 0
	}

	si, err := server.ReadInfo(a.StateDir)
	if err != nil {
		// No server record — a serverless engine may still hold the
		// engine lock.
		if pid, started, ok := app.ReadEngineLock(a.StateDir); ok && proc.AliveSince(pid, started) {
			if *force {
				return forceKill(pid)
			}
			fmt.Printf("a headless hal engine is running (pid %d) — Ctrl+C it, or re-run with --force to kill it\n", pid)
			return 1
		}
		fmt.Println("no hal server is running for this repo")
		return 0
	}
	req, _ := http.NewRequestWithContext(ctx, "POST",
		fmt.Sprintf("http://127.0.0.1:%d/api/v1/server/stop", si.Port), nil)
	req.Header.Set("X-Hal-Token", si.Token)
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		if proc.AliveSince(si.PID, si.Started) {
			// A hung engine: server.json is live but the process ignores us.
			if *force {
				return forceKill(si.PID)
			}
			fmt.Printf("the server is not responding but its process (pid %d) is alive — re-run with --force to kill it\n", si.PID)
			return 1
		}
		fmt.Println("server.json found but the server is not responding — removing the stale record")
		_ = os.Remove(server.InfoPath(a.StateDir))
		return 0
	}
	resp.Body.Close()
	fmt.Println("stop requested")
	return 0
}

func cmdPath(args []string) int {
	fs := flag.NewFlagSet("path", flag.ExitOnError)
	repo := fs.String("repo", "", "repository path")
	_ = fs.Parse(args)

	ctx, cancel := interruptCtx()
	defer cancel()
	a, err := openApp(ctx, *repo)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 2
	}
	defer a.Close()
	printPaths(a)
	return 0
}

func pingServer(port int, token string) bool {
	req, err := http.NewRequest("GET", fmt.Sprintf("http://127.0.0.1:%d/api/v1/status", port), nil)
	if err != nil {
		return false
	}
	// /status is a token-gated read now, so the liveness probe must
	// authenticate — the caller has the running instance's token from
	// server.json.
	req.Header.Set("X-Hal-Token", token)
	resp, err := (&http.Client{Timeout: 2 * time.Second}).Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		// `start` mangles URLs with fragments; FileProtocolHandler doesn't.
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	_ = cmd.Start()
}
