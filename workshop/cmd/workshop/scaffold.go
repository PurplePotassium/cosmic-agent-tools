package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/config"
	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/gitx"
)

func cmdInit(args []string) int {
	fs := flag.NewFlagSet("init", flag.ExitOnError)
	repo := fs.String("repo", "", "repository path (default: enclosing repo of cwd)")
	pipelines := fs.String("pipelines", "", "comma-separated pipeline names to scaffold uncommented")
	game := fs.Bool("game", false, "game-dev flavor: gamedev spice pools + art/audio type examples")
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

	writeIfAbsent(config.RepoConfigFile(root), scaffoldConfig(filepath.Base(root), *game, splitNames(*pipelines)))
	writeIfAbsent(config.GoalFile(root), scaffoldGoal)

	for _, p := range wrote {
		fmt.Println("created", p)
	}
	for _, p := range skipped {
		fmt.Println("kept   ", p, "(use --force to overwrite)")
	}
	fmt.Println(`
Next steps:
  1. Edit .workshop/GOAL.md — the north star every pass reads first.
  2. (Optional) set project.verify in .workshop/config.toml to your test command.
  3. workshop            # opens the dashboard and starts the loop
     workshop run        # or: a bounded 3-pass smoke run, no browser`)
	return 0
}

func splitNames(s string) []string {
	var out []string
	for _, n := range strings.Split(s, ",") {
		if n = strings.TrimSpace(n); n != "" {
			out = append(out, n)
		}
	}
	return out
}

const scaffoldGoal = `# Goal

<Describe, in a paragraph or two, what "done" looks like for this project.
Every Workshop pass reads this first and must move toward it. Edit freely —
the loop picks up changes on the next pass.>
`

func scaffoldConfig(name string, game bool, pipelineNames []string) string {
	var b strings.Builder
	b.WriteString(`# Workshop project config — versioned with your repo.
# EVERY key is optional: an empty file runs one 'main' pipeline (claude, all
# task types, no worktrees). Uncomment what you need.

[project]
name = "` + name + `"
# trunk  = "main"          # branch pipelines fork from / merge into (default: current)
# verify = "npm test"      # gate command, exit 0 = pass — strongly recommended

# [git]
# worktrees = "auto"       # auto | on | off — auto enables worktrees when >1 pipeline

# [safety]
# max_concurrent   = 2     # simultaneous agent passes across pipelines
# breaker_failures = 5     # consecutive failed passes -> pipeline halts
# wedge_minutes    = 35    # in-flight pass older than this is killed (keep above agy's 30m print-timeout)

`)
	if game {
		b.WriteString(`[spice]                    # anti-circling persona/noun injection
personas = "gamedev"
nouns    = "gamedev"
`)
	} else {
		b.WriteString(`# [spice]                  # anti-circling persona/noun injection
# personas = "general"     # general | gamedev | path/to/pool.txt
# nouns    = "general"
`)
	}
	b.WriteString(`
# ---- Task-type routing: type -> {agent, model, effort} -------------------
# A task's routing precedence: per-task pin > this table > pipeline bundle.
# effort: low|medium|high|xhigh|max (ignored by agents that don't support it)
#
# [types.code]
# agent  = "claude"
# model  = "claude-opus-4-8"
# effort = "high"
#
# [types.merge-conflict]   # defining this enables agent-resolved conflicts
# agent  = "claude"
# model  = "claude-opus-4-8"
# effort = "high"
`)
	if game {
		b.WriteString(`#
# [types.art]
# agent = "agy"            # blind headless: progress.json self-report only
# model = "gemini-3-flash" # agy model ids fail SILENTLY when wrong
#
# [types.audio]
# agent = "agy"
# model = "gemini-3-flash"
`)
	}
	b.WriteString(`
# ---- Pipelines: parallel worker lanes -------------------------------------
# Each pipeline always drains its OWN backlog first (any type), and also the
# shared backlog (filtered by 'types') unless drain_main = false.
`)
	if len(pipelineNames) == 0 {
		b.WriteString(`#
# [[pipelines]]
# name    = "code"
# types   = ["code", "tests", "docs", "merge-conflict"]
# agent   = "claude"
# effort  = "high"
#
# [[pipelines]]
# name       = "art"
# types      = ["art", "audio"]
# agent      = "agy"
# model      = "gemini-3-flash"
# invent     = false       # blind driver: only works operator-queued tasks
# drain_main = true
# mode       = "discover"  # goal (default, invents+proposes) | discover (proposes only) | drain (backlog-only)
`)
	} else {
		for _, n := range pipelineNames {
			fmt.Fprintf(&b, `
[[pipelines]]
name  = %q
types = [%q]
agent = "claude"
`, n, n)
		}
	}
	return b.String()
}
