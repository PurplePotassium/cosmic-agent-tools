package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/config"
	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/domain"
	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/statedir"
)

// cmdMigrate imports state from the legacy PowerShell workshop directory.
func cmdMigrate(args []string) int {
	fs := flag.NewFlagSet("migrate", flag.ExitOnError)
	repo := fs.String("repo", "", "repository path")
	from := fs.String("from", "", "path to the OLD PowerShell workshop directory (holds workshop.config.ps1)")
	force := fs.Bool("force", false, "re-run even though this state dir already imported a legacy backlog")
	_ = fs.Parse(args)
	if *from == "" {
		fmt.Fprintln(os.Stderr, `usage: workshop migrate --from <old-workshop-dir>

Imports from the legacy PowerShell workshop:
  GOAL.md          -> .workshop/GOAL.md              (versioned)
  PROMPT.md        -> .workshop/prompts/project.md   (trim it: the pass contract is built in now)
  backlog.json     -> the shared backlog
  completions.json -> completion history
The old directory is never modified. workshop.config.ps1 has no direct
equivalent — set project.trunk / project.verify / [spice] in
.workshop/config.toml instead.`)
		return 2
	}

	ctx, cancel := interruptCtx()
	defer cancel()
	a, err := openApp(ctx, *repo)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 2
	}
	defer a.Close()

	// Task/completion imports mint fresh IDs, so a re-run would duplicate
	// the entire backlog and history — refuse unless forced.
	marker := filepath.Join(a.StateDir, "migrated.json")
	if _, err := os.Stat(marker); err == nil && !*force {
		fmt.Fprintln(os.Stderr, "error: this state dir already imported a legacy workshop (re-running duplicates every task); pass --force to do it anyway")
		return 1
	}

	failed := false
	copied := func(what, dest string) { fmt.Printf("  %-18s -> %s\n", what, dest) }
	skip := func(what, why string) { fmt.Printf("  %-18s    skipped (%s)\n", what, why) }
	failw := func(what string, err error) {
		failed = true
		fmt.Fprintf(os.Stderr, "  %-18s    FAILED: %v\n", what, err)
	}

	fmt.Println("migrating from", *from)

	// GOAL.md
	if data, err := os.ReadFile(filepath.Join(*from, "GOAL.md")); err == nil {
		dest := config.GoalFile(a.RepoDir)
		if _, err := os.Stat(dest); err == nil {
			skip("GOAL.md", "destination exists")
		} else {
			_ = os.MkdirAll(filepath.Dir(dest), 0o755)
			if err := statedir.WriteFileAtomic(dest, data); err == nil {
				copied("GOAL.md", dest)
			} else {
				failw("GOAL.md", err)
			}
		}
	} else {
		skip("GOAL.md", "not found")
	}

	// PROMPT.md -> project fragment
	if data, err := os.ReadFile(filepath.Join(*from, "PROMPT.md")); err == nil {
		dest := filepath.Join(config.PromptsDir(a.RepoDir), "project.md")
		if _, err := os.Stat(dest); err == nil {
			skip("PROMPT.md", "destination exists")
		} else {
			header := "<!-- Migrated from the PowerShell workshop's PROMPT.md.\n" +
				"     The pass contract (offboard/verify/bookkeeping) is BUILT IN now —\n" +
				"     keep only your project-specific notes (reading list, guardrails,\n" +
				"     verify details) and delete the rest. -->\n\n"
			_ = os.MkdirAll(filepath.Dir(dest), 0o755)
			if err := statedir.WriteFileAtomic(dest, append([]byte(header), data...)); err == nil {
				copied("PROMPT.md", dest)
			} else {
				failw("PROMPT.md", err)
			}
		}
	} else {
		skip("PROMPT.md", "not found")
	}

	// backlog.json -> shared backlog
	type oldTask struct {
		ID      string   `json:"id"`
		Title   string   `json:"title"`
		Detail  string   `json:"detail"`
		Created string   `json:"created"`
		Files   []string `json:"files"`
	}
	var oldBacklog []oldTask
	if err := statedir.ReadJSON(filepath.Join(*from, "backlog.json"), &oldBacklog); err == nil {
		n := 0
		for _, ot := range oldBacklog {
			if strings.TrimSpace(ot.Title) == "" {
				continue
			}
			task := &domain.Task{Title: ot.Title, Detail: ot.Detail, Files: ot.Files, Origin: domain.OriginOperator}
			if t, err := time.Parse(time.RFC3339, ot.Created); err == nil {
				task.Created = t
			}
			if _, err := a.Store.AddTask(ctx, task, false); err == nil {
				n++
			} else {
				failw("backlog.json", err)
			}
		}
		copied(fmt.Sprintf("backlog.json (%d)", n), "shared backlog")
	} else {
		skip("backlog.json", "not found or empty")
	}

	// completions.json -> history
	type oldCompletion struct {
		ID        string `json:"id"`
		Title     string `json:"title"`
		Result    string `json:"result"`
		Completed string `json:"completed"`
	}
	var oldComps []oldCompletion
	if err := statedir.ReadJSON(filepath.Join(*from, "completions.json"), &oldComps); err == nil {
		n := 0
		for _, oc := range oldComps {
			if strings.TrimSpace(oc.Title) == "" {
				continue
			}
			c := &domain.Completion{TaskID: oc.ID, Title: oc.Title, Result: oc.Result}
			if t, err := time.Parse(time.RFC3339, oc.Completed); err == nil {
				c.Completed = t
			}
			if err := a.Store.AddCompletion(ctx, c); err == nil {
				n++
			} else {
				failw("completions.json", err)
			}
		}
		copied(fmt.Sprintf("completions.json (%d)", n), "completion history")
	} else {
		skip("completions.json", "not found or empty")
	}

	_ = statedir.WriteJSON(marker, map[string]string{
		"from": *from, "when": time.Now().UTC().Format(time.RFC3339),
	})
	if failed {
		fmt.Fprintln(os.Stderr, "\nmigration finished WITH ERRORS — review the FAILED lines above")
		return 1
	}

	fmt.Println(`
done. Next:
  1. Review .workshop/prompts/project.md and delete the migrated boilerplate.
  2. Set project.verify (your gate command) in .workshop/config.toml.
  3. workshop task list   # confirm the imported backlog
  4. workshop             # go`)
	return 0
}
