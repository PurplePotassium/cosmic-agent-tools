// Command workshop is the single binary: it loads config, opens the SQLite store,
// detects the current repo as a project (scaffolding its out-of-tree state), starts
// the localhost HTTP server (REST + SSE + embedded SPA), opens a browser, and runs
// the supervisor. `workshop --iterations N` is a bounded smoke run: it drives the
// detected project for N passes and exits. Replaces start/stop-workshop.ps1.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/config"
	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/engine"
	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/events"
	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/gitx"
	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/project"
	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/server"
	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/store"
	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/supervisor"
	"github.com/PurplePotassium/cosmic-agent-tools/workshop/web"
)

// Build metadata, injected by goreleaser via -ldflags -X.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	for _, a := range os.Args[1:] {
		if a == "--version" || a == "-version" || a == "-v" {
			fmt.Printf("workshop %s (commit %s, built %s)\n", version, commit, date)
			return
		}
	}
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "workshop:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load(os.Args[1:])
	if err != nil {
		return err // flag.ContinueOnError already printed usage
	}

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	if err := os.MkdirAll(cfg.BaseDir, 0o755); err != nil {
		return fmt.Errorf("create base dir: %w", err)
	}

	st, err := store.Open(filepath.Join(cfg.BaseDir, "workshop.db"))
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer st.Close()

	broker := events.New()
	sup := supervisor.New(st, broker, log, engine.Config{
		Random:          cfg.Random,
		SkipPermissions: cfg.SkipPermissions,
		SleepSeconds:    cfg.SleepSeconds,
		PersonaFlavor:   cfg.PersonaFlavor,
	}, cfg.MaxConcurrent)

	// Detect the current directory (or --repo) as a project.
	detected, err := detectProject(st, cfg, log)
	if err != nil {
		log.Warn("could not auto-detect a project", "err", err)
	}

	srv := server.New(server.Deps{
		Store:   st,
		Sup:     sup,
		Broker:  broker,
		Log:     log,
		BaseDir: cfg.BaseDir,
		UIPort:  cfg.Port,
		Dist:    web.Dist(),
	})

	addr := net.JoinHostPort(cfg.Addr, fmt.Sprint(cfg.Port))
	httpSrv := &http.Server{Addr: addr, Handler: srv.Handler()}

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", addr, err)
	}
	url := fmt.Sprintf("http://%s", addr)
	log.Info("Workshop", "url", url, "baseDir", cfg.BaseDir)
	if detected != nil {
		log.Info("project", "name", detected.Name, "repo", detected.RepoPath, "stateDir", detected.StateDir)
	}

	go func() {
		if err := httpSrv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("http server", "err", err)
		}
	}()

	// --- bounded smoke run: drive the detected project N passes, then exit ---
	if cfg.Iterations > 0 {
		if detected == nil {
			return errors.New("--iterations needs a git repo (run inside one, or pass --repo)")
		}
		log.Info("bounded run", "iterations", cfg.Iterations, "project", detected.Name)
		if err := sup.Start(detected.ID, cfg.Iterations); err != nil {
			return err
		}
		sup.Wait(detected.ID)
		shutdown(httpSrv, sup, log)
		return nil
	}

	// --- interactive server: open a browser, run until SIGINT ---
	if cfg.Open {
		go openBrowser(url, log)
	}

	sigc := make(chan os.Signal, 1)
	signal.Notify(sigc, os.Interrupt, syscall.SIGTERM)
	<-sigc
	log.Info("shutting down…")
	shutdown(httpSrv, sup, log)
	return nil
}

// detectProject registers the current repo as a project (reusing an existing row for
// the same repo) and scaffolds its out-of-tree state. Returns nil when cwd/--repo is
// not a git repo — the server still runs so a project can be added from the UI.
func detectProject(st *store.Store, cfg config.Config, log *slog.Logger) (*store.Project, error) {
	repo := cfg.Repo
	if repo == "" {
		wd, err := os.Getwd()
		if err != nil {
			return nil, err
		}
		repo = wd
	}
	abs, _ := filepath.Abs(repo)
	if !gitx.IsRepo(abs) {
		log.Info("not a git repository — add a project from the UI", "path", abs)
		return nil, nil
	}

	id := project.Slug(abs)
	if existing, err := st.GetProject(id); err == nil {
		// keep launch-time defaults fresh only if the operator changed them via flags
		return existing, project.Scaffold(existing)
	}

	p := project.New(cfg.BaseDir, abs, cfg.Branch, cfg.Agent, cfg.Model, cfg.Preview)
	if err := project.Scaffold(p); err != nil {
		return nil, err
	}
	if err := st.CreateProject(p); err != nil {
		return nil, err
	}
	return p, nil
}

func shutdown(httpSrv *http.Server, sup *supervisor.Supervisor, log *slog.Logger) {
	sup.StopAll()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := httpSrv.Shutdown(ctx); err != nil {
		log.Warn("http shutdown", "err", err)
	}
}

// openBrowser opens url in the platform default browser (best-effort).
func openBrowser(url string, log *slog.Logger) {
	// tiny grace so the listener is definitely accepting before the tab loads
	time.Sleep(300 * time.Millisecond)
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("cmd", "/c", "start", "", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Start(); err != nil {
		log.Info("open your browser to", "url", url)
	}
}
