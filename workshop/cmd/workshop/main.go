// Command workshop runs autonomous coding-agent loops against the git repo
// it is invoked in, with a local dashboard to watch and steer them.
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

	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/app"
	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/engine"
	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/fakeagent"
	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/proc"
	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/server"
)

// Version is stamped by the release build (-ldflags "-X main.Version=...").
var Version = "dev"

func main() {
	// Hidden re-entry point: the fake driver execs this binary as a
	// scripted agent for tests and smoke runs.
	if len(os.Args) > 1 && os.Args[1] == "_fake-agent" {
		os.Exit(fakeagent.Main())
	}

	cmd := "up"
	args := os.Args[1:]
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}

	var code int
	switch cmd {
	case "up":
		code = cmdUp(args)
	case "run":
		code = cmdRun(args)
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
	case "path":
		code = cmdPath(args)
	case "migrate":
		code = cmdMigrate(args)
	case "version":
		fmt.Println("workshop", Version)
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
	fmt.Print(`workshop — autonomous coding-agent loops with a live dashboard

usage: workshop [command] [flags]

  up       start the server + enabled pipelines in the foreground (default)
  run      headless bounded run (CI/smoke): --iterations N, --until-drained
  init     scaffold .workshop/ (config.toml + GOAL.md) into this repo
  task     backlog operations: add, list, tag, pin, mv, rm
  status   one-shot snapshot (--json for machines)
  stop     gracefully stop the running server (--force kills a hung engine)
  doctor   check the environment (git, agents, config, state dir)
  path     print resolved directories and config files
  migrate  import GOAL/PROMPT/backlog from the old PowerShell workshop
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

func cmdRun(args []string) int {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	repo := fs.String("repo", "", "repository path (default: enclosing repo of cwd)")
	iterations := fs.Int("iterations", 3, "passes to run per pipeline (0 = until stopped)")
	untilDrained := fs.Bool("until-drained", false, "stop when the backlog has nothing eligible")
	timeout := fs.Duration("timeout", 0, "wall-clock limit for the whole run (0 = none)")
	_ = fs.Parse(args)

	ctx, cancel := interruptCtx()
	defer cancel()
	if *timeout > 0 {
		var tcancel context.CancelFunc
		ctx, tcancel = context.WithTimeout(ctx, *timeout)
		defer tcancel()
	}

	a, err := openApp(ctx, *repo)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 2
	}
	defer a.Close()

	err = a.RunHeadless(ctx, *iterations, *untilDrained, false)
	switch {
	case err == nil:
		fmt.Println("run complete")
		return 0
	case errors.Is(err, engine.ErrHalted):
		fmt.Fprintln(os.Stderr, "run halted: pipeline stopped itself (auth failure or breaker) — see `workshop status`")
		return 1
	case errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded):
		fmt.Fprintln(os.Stderr, "run interrupted")
		return 1
	default:
		fmt.Fprintln(os.Stderr, "run failed:", err)
		return 1
	}
}

func cmdUp(args []string) int {
	fs := flag.NewFlagSet("up", flag.ExitOnError)
	repo := fs.String("repo", "", "repository path (default: enclosing repo of cwd)")
	port := fs.Int("port", 0, "port override (default: config server.port)")
	noOpen := fs.Bool("no-open", false, "don't open the browser")
	startRunning := fs.Bool("start-running", false, "start pipelines running immediately (overrides server.start_stopped)")
	_ = fs.Parse(args)

	ctx, cancel := interruptCtx()
	defer cancel()

	a, err := openApp(ctx, *repo)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 2
	}
	defer a.Close()

	// A second `workshop up` in the same repo just surfaces the running one.
	if si, err := server.ReadInfo(a.StateDir); err == nil {
		url := fmt.Sprintf("http://127.0.0.1:%d/", si.Port)
		if pingServer(si.Port) {
			fmt.Printf("workshop already running for this repo (pid %d) — %s\n", si.PID, url)
			if !*noOpen && a.Res().Config.Server.OpenBrowser {
				openBrowser(url + "#token=" + si.Token)
			}
			return 0
		}
		_ = os.Remove(server.InfoPath(a.StateDir))
	}

	ctl := app.NewEngineControl(func(ctx context.Context, startStopped bool) error {
		return a.RunHeadless(ctx, a.Res().Config.Safety.MaxIterations, false, startStopped)
	})
	srv := server.New(a, cancel, ctl.Halt)
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
	pipelines := a.EnabledPipelines()
	fmt.Printf("workshop %s\n  repo:  %s\n  state: %s\n  url:   %s\n  pipelines:", Version, a.RepoDir, a.StateDir, url)
	for _, p := range pipelines {
		fmt.Printf(" %s(%s)", p.Name, p.Bundle.Agent)
	}
	startStopped := a.Res().Config.Server.StartStopped && !*startRunning
	if startStopped {
		fmt.Println("\n  pipelines start stopped — resume them from the dashboard (or launch with --start-running).")
	} else {
		fmt.Println("\n  Ctrl+C stops the loop and kills any in-flight pass.")
	}

	ctl.Start(ctx, startStopped)

	if !*noOpen && a.Res().Config.Server.OpenBrowser {
		openBrowser(url + "#token=" + srv.Token())
	}

	select {
	case <-ctx.Done():
		fmt.Println("\nstopping...")
		<-ctl.Done()
	case err := <-ctl.Done():
		switch {
		case err == nil:
			fmt.Println("loop finished (iteration bound reached); server still running — Ctrl+C to exit")
			<-ctx.Done()
		case errors.Is(err, engine.ErrHalted):
			fmt.Fprintln(os.Stderr, "pipeline halted (auth/breaker) — server still running for inspection; Ctrl+C to exit")
			<-ctx.Done()
		case errors.Is(err, context.Canceled):
			// shutting down
		default:
			fmt.Fprintln(os.Stderr, "loop error:", err)
			return 1
		}
	}
	return 0
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
	if si, err := server.ReadInfo(a.StateDir); err == nil && pingServer(si.Port) {
		serverUp = true
	}

	if *asJSON {
		printJSON(map[string]any{"serverRunning": serverUp, "status": snap})
		return 0
	}
	fmt.Printf("repo:   %s\nserver: %v\nshared backlog: %d open\n", snap.Repo, serverUp, snap.SharedBacklog)
	for _, p := range snap.Pipelines {
		state := "idle"
		if p.Halted == "operator" {
			state = "stopped"
		} else if p.Halted != "" {
			state = "HALTED(" + p.Halted + ")"
		}
		last := "-"
		if p.LastPass != nil {
			last = fmt.Sprintf("iter %d %s %s", p.LastPass.N, p.LastPass.Outcome, p.LastPass.CommitSHA)
		}
		fmt.Printf("pipeline %-12s %-8s mode=%s agent=%s own-backlog=%d last=[%s]", p.Name, state, p.Mode, p.Agent, p.BacklogExclusive, last)
		if p.Progress.Phase != "" {
			fmt.Printf(" progress=%s(%ds ago)", p.Progress.Phase, p.ProgressAgeSec)
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
		fmt.Printf("killed workshop engine (pid %d) and its process tree\n", pid)
		return 0
	}

	si, err := server.ReadInfo(a.StateDir)
	if err != nil {
		// No server record — a headless `workshop run` may still hold the
		// engine lock.
		if pid, ok := app.ReadEngineLock(a.StateDir); ok && proc.Alive(pid) {
			if *force {
				return forceKill(pid)
			}
			fmt.Printf("a headless workshop engine is running (pid %d) — Ctrl+C it, or re-run with --force to kill it\n", pid)
			return 1
		}
		fmt.Println("no workshop server is running for this repo")
		return 0
	}
	req, _ := http.NewRequestWithContext(ctx, "POST",
		fmt.Sprintf("http://127.0.0.1:%d/api/v1/server/stop", si.Port), nil)
	req.Header.Set("X-Workshop-Token", si.Token)
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		if proc.Alive(si.PID) {
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

func pingServer(port int) bool {
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/api/v1/status", port))
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
