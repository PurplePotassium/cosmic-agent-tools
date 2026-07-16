package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/app"
	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/config"
	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/domain"
	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/driver"
	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/server"
)

type check struct {
	Name   string `json:"name"`
	Status string `json:"status"` // PASS | WARN | FAIL
	Detail string `json:"detail,omitempty"`
	Fix    string `json:"fix,omitempty"`
}

func cmdDoctor(args []string) int {
	fs := flag.NewFlagSet("doctor", flag.ExitOnError)
	repo := fs.String("repo", "", "repository path")
	asJSON := fs.Bool("json", false, "machine-readable output")
	_ = fs.Parse(args)

	ctx, cancel := interruptCtx()
	defer cancel()

	var checks []check
	add := func(name, status, detail, fix string) {
		checks = append(checks, check{name, status, detail, fix})
	}

	// git
	if out, err := exec.CommandContext(ctx, "git", "--version").Output(); err != nil {
		add("git", "FAIL", "git not found on PATH", "install git and re-open the terminal")
	} else {
		add("git", "PASS", string(trimNL(out)), "")
	}

	a, err := app.Open(ctx, *repo)
	if err != nil {
		add("repository", "FAIL", err.Error(), "run inside a git repo, or pass --repo")
		return doctorReport(checks, *asJSON)
	}
	defer a.Close()
	add("repository", "PASS", a.RepoDir, "")

	if len(a.Res().Warnings) == 0 {
		add("config", "PASS", "no warnings", "")
	}
	for _, w := range a.Res().Warnings {
		add("config", "WARN", w, "edit .workshop/config.toml and re-run workshop doctor")
	}

	// Each agent referenced by an enabled pipeline.
	agents := map[string]bool{}
	for _, p := range a.EnabledPipelines() {
		agents[p.Bundle.Agent] = true
	}
	for name := range agents {
		d, err := driver.New(name)
		if err != nil {
			add("agent:"+name, "FAIL", err.Error(), "fix the agent name in .workshop/config.toml")
			continue
		}
		caps, err := d.Probe(ctx)
		if err != nil {
			add("agent:"+name, "FAIL", err.Error(), "install the agent CLI or set its WORKSHOP_*_BIN override")
			continue
		}
		detail := "found"
		if caps.Effort {
			detail += ", supports effort levels"
		} else {
			detail += ", no effort flag (configured efforts will be ignored)"
		}
		if caps.Capture == driver.CaptureNone {
			detail += "; BLIND headless (self-report only, auth failures invisible)"
		}
		add("agent:"+name, "PASS", detail, "")
	}

	// agy art models: art-gen / art-gen-trans passes run on agy with one of
	// the allowed Gemini labels — verify agy actually offers one (quota-free
	// probe; see driver.(*Agy).ListModels). Skipped in fake-agent harnesses,
	// with the same truthy WORKSHOP_SKIP_AGY_VERIFY semantics as the app's
	// launch verification ("0" must not silently skip here but verify there).
	skipAgy := false
	switch strings.ToLower(strings.TrimSpace(os.Getenv("WORKSHOP_SKIP_AGY_VERIFY"))) {
	case "1", "true", "yes", "on":
		skipAgy = true
	}
	if os.Getenv("WORKSHOP_FAKE_BIN") == "" && !skipAgy {
		agyDrv := driver.NewAgy()
		if _, err := agyDrv.Probe(ctx); err != nil {
			add("art models", "WARN", "agy not installed — art-gen/art-gen-trans tasks will fail", "install the Antigravity CLI (agy), or don't queue art tasks")
		} else if models, err := agyDrv.ListModels(ctx); err != nil {
			add("art models", "WARN", err.Error(), "run `agy` interactively once (login), then re-run workshop doctor")
		} else {
			pick := ""
			for _, want := range domain.ArtAgyModels {
				if driver.AgyHasModel(models, want) {
					pick = want
					break
				}
			}
			if pick != "" {
				add("art models", "PASS", fmt.Sprintf("agy offers %s (art passes will use it)", pick), "")
			} else {
				add("art models", "FAIL", fmt.Sprintf("agy offers none of %v — saw %v", domain.ArtAgyModels, models), "update agy (`agy update`) or refresh its login, then re-run workshop doctor")
			}
		}
	}

	// State dir writable.
	probe := filepath.Join(a.StateDir, ".doctor-probe")
	if err := os.WriteFile(probe, []byte("ok"), 0o644); err != nil {
		add("state dir", "FAIL", err.Error(), "check permissions on "+a.StateDir)
	} else {
		os.Remove(probe)
		add("state dir", "PASS", a.StateDir, "")
	}

	// Port / server state.
	if si, err := server.ReadInfo(a.StateDir); err == nil {
		if pingServer(si.Port, si.Token) {
			add("server", "PASS", fmt.Sprintf("running (pid %d, port %d)", si.PID, si.Port), "")
		} else {
			add("server", "WARN", "stale server.json (server not responding)", "workshop stop clears it")
		}
	} else {
		port := a.Res().Config.Server.Port
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err != nil {
			add("port", "WARN", fmt.Sprintf("port %d is taken by another process", port), "workshop up falls back to a free port automatically")
		} else {
			ln.Close()
			add("port", "PASS", fmt.Sprintf("%d free", port), "")
		}
	}

	// Goal file.
	if _, err := os.Stat(config.GoalFile(a.RepoDir)); err != nil {
		add("goal", "WARN", ".workshop/GOAL.md missing", "workshop init scaffolds it")
	} else {
		add("goal", "PASS", ".workshop/GOAL.md present", "")
	}

	return doctorReport(checks, *asJSON)
}

func doctorReport(checks []check, asJSON bool) int {
	code := 0
	if asJSON {
		printJSON(checks)
	}
	for _, c := range checks {
		if !asJSON {
			line := fmt.Sprintf("%-4s %-14s %s", c.Status, c.Name, c.Detail)
			if c.Fix != "" && c.Status != "PASS" {
				line += "\n     fix: " + c.Fix
			}
			fmt.Println(line)
		}
		if c.Status == "FAIL" {
			code = 1
		}
	}
	return code
}

func printPaths(a *app.App) {
	fmt.Printf(`repo:           %s
repo config:    %s
goal:           %s
prompts:        %s
state dir:      %s
logs:           %s
user config:    %s
overrides:      %s
`,
		a.RepoDir,
		config.RepoConfigFile(a.RepoDir),
		config.GoalFile(a.RepoDir),
		config.PromptsDir(a.RepoDir),
		a.StateDir,
		filepath.Join(a.StateDir, "logs"),
		config.UserConfigFile(),
		config.OverridesFile(a.RepoDir),
	)
}

func printJSON(v any) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

func trimNL(b []byte) []byte {
	for len(b) > 0 && (b[len(b)-1] == '\n' || b[len(b)-1] == '\r') {
		b = b[:len(b)-1]
	}
	return b
}

var _ = time.Second // reserved for future timeout tuning
