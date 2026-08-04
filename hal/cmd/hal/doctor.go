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

	"github.com/PurplePotassium/cosmic-agent-tools/hal/internal/app"
	"github.com/PurplePotassium/cosmic-agent-tools/hal/internal/config"
	"github.com/PurplePotassium/cosmic-agent-tools/hal/internal/domain"
	"github.com/PurplePotassium/cosmic-agent-tools/hal/internal/driver"
	"github.com/PurplePotassium/cosmic-agent-tools/hal/internal/server"
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
		add("config", "WARN", w, "edit .hal/config.toml and re-run hal doctor")
	}

	// claude — the interactive workflow engine (and the art-job orchestrator)
	// runs on it.
	if caps, err := driver.NewClaude().Probe(ctx); err != nil {
		add("agent:claude", "FAIL", err.Error(), "install Claude Code, or set HAL_CLAUDE_BIN")
	} else {
		detail := "found"
		if caps.Effort {
			detail += ", supports effort levels"
		} else {
			detail += ", no effort flag (configured efforts will be ignored)"
		}
		add("agent:claude", "PASS", detail, "")
	}

	// .claude/agents — the workflow stage prompts call these sub-agents by
	// name; `hal init` seeds them.
	agentsDir := filepath.Join(a.RepoDir, ".claude", "agents")
	if entries, err := os.ReadDir(agentsDir); err != nil || len(entries) == 0 {
		add(".claude/agents", "WARN", "no sub-agent definitions found — workflow stages reference them", "run `hal init` to seed .claude/agents")
	} else {
		add(".claude/agents", "PASS", fmt.Sprintf("%d agent definitions", len(entries)), "")
	}

	// agy art models: art-gen / art-gen-trans passes run a claude
	// orchestrator that hands agy one of the allowed Gemini labels — verify
	// agy actually offers one (quota-free probe; see
	// driver.(*Agy).ListModels). Skipped in fake-agent harnesses,
	// with the same truthy HAL_SKIP_AGY_VERIFY semantics as the app's
	// launch verification ("0" must not silently skip here but verify there).
	skipAgy := false
	switch strings.ToLower(strings.TrimSpace(os.Getenv("HAL_SKIP_AGY_VERIFY"))) {
	case "1", "true", "yes", "on":
		skipAgy = true
	}
	if os.Getenv("HAL_FAKE_BIN") == "" && !skipAgy {
		agyDrv := driver.NewAgy()
		if _, err := agyDrv.Probe(ctx); err != nil {
			add("art models", "WARN", "agy not installed — art-gen/art-gen-trans tasks will fail", "install the Antigravity CLI (agy), or don't queue art tasks")
		} else if models, err := agyDrv.ListModels(ctx); err != nil {
			add("art models", "WARN", err.Error(), "run `agy` interactively once (login), then re-run hal doctor")
		} else {
			pick := ""
			for _, want := range domain.ArtAgyModels {
				if driver.AgyHasModel(models, want) {
					pick = want
					break
				}
			}
			if pick != "" {
				add("art models", "PASS", fmt.Sprintf("agy offers %s (art passes hand it to agy)", pick), "")
			} else {
				add("art models", "FAIL", fmt.Sprintf("agy offers none of %v — saw %v", domain.ArtAgyModels, models), "update agy (`agy update`) or refresh its login, then re-run hal doctor")
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
			add("server", "WARN", "stale server.json (server not responding)", "hal stop clears it")
		}
	} else {
		port := a.Res().Config.Server.Port
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err != nil {
			add("port", "WARN", fmt.Sprintf("port %d is taken by another process", port), "hal up falls back to a free port automatically")
		} else {
			ln.Close()
			add("port", "PASS", fmt.Sprintf("%d free", port), "")
		}
	}

	// Goal file.
	if _, err := os.Stat(config.GoalFile(a.RepoDir)); err != nil {
		add("goal", "WARN", ".hal/GOAL.md missing", "hal init scaffolds it")
	} else {
		add("goal", "PASS", ".hal/GOAL.md present", "")
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
