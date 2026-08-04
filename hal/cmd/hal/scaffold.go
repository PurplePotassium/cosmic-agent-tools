package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/PurplePotassium/cosmic-agent-tools/hal/internal/config"
	"github.com/PurplePotassium/cosmic-agent-tools/hal/internal/gitx"
	"github.com/PurplePotassium/cosmic-agent-tools/hal/internal/prompt"
)

// ensureRepoConfig writes the default .hal/config.toml (the same template
// `hal init` scaffolds) the first launch in a repo that has none, so the
// defaults are visible and editable instead of implicit. Best-effort by
// design: any failure just leaves config resolution to the built-in defaults,
// and a repo that isn't a git repo falls through to openApp's real error.
func ensureRepoConfig(ctx context.Context, repoOverride string) {
	dir := repoOverride
	if dir == "" {
		if wd, err := os.Getwd(); err == nil {
			dir = wd
		}
	}
	if dir == "" || !gitx.IsRepo(ctx, dir) {
		return
	}
	root, err := gitx.Root(ctx, dir)
	if err != nil {
		return
	}
	path := config.RepoConfigFile(root)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	// O_EXCL: two concurrent launches must not truncate each other's write.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	_, werr := f.WriteString(scaffoldConfig(filepath.Base(root), false))
	if cerr := f.Close(); werr != nil || cerr != nil {
		// A half-written TOML file would fail config load on every future
		// start — worse than no file at all.
		_ = os.Remove(path)
		return
	}
	fmt.Printf("first launch in this repo — created %s with the default settings (`hal init` also adds GOAL.md)\n", path)
}

func cmdInit(args []string) int {
	fs := flag.NewFlagSet("init", flag.ExitOnError)
	repo := fs.String("repo", "", "repository path (default: enclosing repo of cwd)")
	game := fs.Bool("game", false, "game-dev flavor: art-job guidance comments in the scaffolded config")
	force := fs.Bool("force", false, "overwrite existing scaffold files")
	_ = fs.Parse(args)

	ctx, cancel := interruptCtx()
	defer cancel()

	dir := *repo
	if dir == "" {
		dir, _ = os.Getwd()
	}
	if !gitx.IsRepo(ctx, dir) {
		fmt.Fprintf(os.Stderr, "error: %s is not inside a git repository — run `git init` first\n", dir)
		return 2
	}
	root, err := gitx.Root(ctx, dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 2
	}

	wsDir := filepath.Join(root, config.RepoConfigDir)
	if err := os.MkdirAll(wsDir, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 2
	}

	wrote := []string{}
	skipped := []string{}
	writeIfAbsent := func(path, content string) {
		if _, err := os.Stat(path); err == nil && !*force {
			skipped = append(skipped, path)
			return
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(2)
		}
		wrote = append(wrote, path)
	}

	writeIfAbsent(config.RepoConfigFile(root), scaffoldConfig(filepath.Base(root), *game))
	writeIfAbsent(config.GoalFile(root), scaffoldGoal)

	// The workflow stage prompts call these sub-agents by name (locator /
	// analyzer / pattern-finder split); .claude/agents/ is the claude CLI's
	// native discovery path, so seeding them needs no engine plumbing.
	agentsDir := filepath.Join(root, ".claude", "agents")
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 2
	}
	for name, content := range prompt.AgentAssets() {
		writeIfAbsent(filepath.Join(agentsDir, name), content)
	}

	// The validate stage ends with a quick agent_play playthrough when the
	// repo carries the agent_play toolkit, driven by the `smoke` entry in
	// agent_play.config.json — the configured level select / setup for one
	// representative path. Seed an example entry so games adopt the
	// convention. The config is the toolkit's own file and may hold real
	// settings (godotProject, port, ...), so an existing one is never
	// rewritten — not even with --force; the entry to merge is printed
	// instead.
	smokeSeeded, smokeHint := false, ""
	apCfg := filepath.Join(root, "agent_play", "agent_play.config.json")
	if fi, err := os.Stat(filepath.Dir(apCfg)); err == nil && fi.IsDir() {
		if b, err := os.ReadFile(apCfg); os.IsNotExist(err) {
			writeIfAbsent(apCfg, scaffoldAgentPlayConfig)
			smokeSeeded = true
		} else if err == nil && !strings.Contains(string(b), `"smoke"`) {
			smokeHint = "note: " + apCfg + " has no \"smoke\" entry — the validate stage\n" +
				"reads it for its quick agent playthrough. Merge in, adjusted to your game:\n" +
				scaffoldSmokeEntry + "\n"
		}
	}

	for _, p := range wrote {
		fmt.Println("created", p)
	}
	for _, p := range skipped {
		fmt.Println("kept   ", p, "(use --force to overwrite)")
	}
	if smokeHint != "" {
		fmt.Print(smokeHint)
	}
	fmt.Println("\nNext steps:")
	n := 0
	step := func(s string) { n++; fmt.Printf("  %d. %s\n", n, s) }
	step("Edit .hal/GOAL.md — the north star every workflow reads first.")
	step("(Optional) set project.verify in .hal/config.toml to your test command.")
	if smokeSeeded {
		step("Point the smoke entry in agent_play/agent_play.config.json at your game's representative path.")
	}
	step("hal            # opens the dashboard; workflows are driven from it")
	return 0
}

// scaffoldSmokeEntry is the example `smoke` entry for the agent_play
// toolkit's config — the level select / setup the validate stage plays as
// its smoke test. A standalone fragment so the scaffolded file and the
// "merge this in" hint cannot drift apart.
const scaffoldSmokeEntry = `  "smoke": {
    "_comment": "Read by hal's validate stage: level select / setup for a quick agent playthrough of ONE common path covering the game's major features. Edit level/seed/path for your game; keep steps small.",
    "level": "level_1",
    "seed": 123,
    "steps": 40,
    "path": "spawn -> core mechanic -> first hazard -> level exit"
  }`

const scaffoldAgentPlayConfig = `{
  "_comment": "agent_play toolkit config — see agent_play/README.md for the toolkit's own fields (godotProject, exportPreset, model, port). hal init seeded the smoke entry below.",
` + scaffoldSmokeEntry + `
}
`

const scaffoldGoal = `# Goal

<Describe, in a paragraph or two, what "done" looks like for this project.
Every Hal workflow reads this first and must move toward it. Edit
freely — new turns pick up changes immediately.>
`

func scaffoldConfig(name string, game bool) string {
	var b strings.Builder
	b.WriteString(`# Hal project config — versioned with your repo.
# EVERY key is optional: an empty file works. Uncomment what you need.
# Workflows are driven from the dashboard: each one is a live conversation
# moving through fixed, operator-approved stages
# (refine -> research -> design -> plan -> implement -> validate).

[project]
name = "` + name + `"
# trunk  = "main"          # the branch workflows work on (default: current)
# verify = "npm test"      # gate command, exit 0 = pass — strongly recommended

# [safety]
# skip_permissions = true  # implement/validate turns run --dangerously-skip-permissions
# max_concurrent   = 2     # simultaneous agent turn processes

# [workflow]
# artifact_dir = ".hal/workflows"  # repo-relative root for stage artifacts
# turn_minutes = 20                     # per-turn ceiling (one agent process)
#
# Per-stage model/effort overrides (the agent is always claude):
# [workflow.stages.design]
# model  = "claude-opus-4-8"
# effort = "high"
`)
	if game {
		b.WriteString(`
# ---- Art jobs (dashboard "generate art" — claude orchestrates agy) --------
# [art]
# remover  = "ffmpeg"        # green/blue-screen keyer for transparent assets:
#                            # ffmpeg (fast, needs ffmpeg on PATH) | corridorkey (neural)
# removers = ["ffmpeg", "corridorkey"]  # multi-keyer comparison, primary first
# corridorkey_dir = 'C:\tools\CorridorKey'
`)
	} else {
		b.WriteString(`
# [art]                      # dashboard art jobs (claude orchestrates the agy image model)
# remover = "ffmpeg"         # green/blue-screen keyer: ffmpeg | corridorkey
`)
	}
	b.WriteString(`
# [export]                   # mirror art/inquiry evidence (logs, transcripts)
# dir = 'C:\hal-audits' # MUST lie outside the repository
# human_readable = true      # also render transcripts as markdown
`)
	return b.String()
}
