package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/app"
	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/config"
	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/domain"
	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/driver"
	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/statedir"
	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/store"
)

func cmdTask(args []string) int {
	if len(args) == 0 {
		taskUsage()
		return 2
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "add":
		return taskAdd(rest)
	case "list", "ls":
		return taskList(rest)
	case "tag":
		return taskTag(rest)
	case "pin":
		return taskPin(rest)
	case "unpin":
		return taskUnpin(rest)
	case "mv":
		return taskMv(rest)
	case "rm":
		return taskRm(rest)
	default:
		fmt.Fprintf(os.Stderr, "unknown task subcommand %q\n\n", sub)
		taskUsage()
		return 2
	}
}

// parseMixed lets flags and positional args interleave (stdlib flag stops at
// the first positional, which silently eats trailing flags — a real footgun
// for `task add "title" --type art`).
func parseMixed(fs *flag.FlagSet, args []string) []string {
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			os.Exit(2) // ExitOnError semantics
		}
		rest := fs.Args()
		i := 0
		// A lone "-" is positional too: flag.Parse won't consume it, so
		// refusing it here would leave args unchanged and spin forever.
		for i < len(rest) && (rest[i] == "-" || !strings.HasPrefix(rest[i], "-")) {
			positional = append(positional, rest[i])
			i++
		}
		if i >= len(rest) {
			return positional
		}
		args = rest[i:]
	}
}

func taskUsage() {
	fmt.Print(`usage: workshop task <subcommand>

  add "title" [--detail d] [--type t] [--backlog shared|<pipeline>]
              [--pin [agent]:[model][:effort]] [--first]
  list        [--json] [--all]
  tag <id> <type>          ('-' clears the type -> re-classified)
  pin <id> [agent]:[model][:effort]   (empty segments keep routing's choice)
  unpin <id>
  mv <id> top|bottom|--to shared|<pipeline>
  rm <id>

The shared backlog is addressed as "shared"; any other name is a pipeline's
exclusive backlog.
`)
}

// resolveBacklogName maps the CLI's "shared"/pipeline-name onto the store's
// backlog key, validating pipeline names.
func resolveBacklogName(a *app.App, name string) (string, error) {
	switch strings.ToLower(name) {
	case "", statedir.SharedLabel:
		return domain.MainBacklog, nil
	}
	for _, p := range a.Res().Config.ResolvedPipelines() {
		if strings.EqualFold(p.Name, name) {
			return p.Name, nil
		}
	}
	return "", fmt.Errorf("no pipeline named %q (backlogs: shared%s)", name, pipelineNames(a))
}

func pipelineNames(a *app.App) string {
	var b strings.Builder
	for _, p := range a.Res().Config.ResolvedPipelines() {
		b.WriteString(", " + p.Name)
	}
	return b.String()
}

func parsePin(s string) (domain.Bundle, error) {
	parts := strings.SplitN(s, ":", 3)
	b := domain.Bundle{}
	if len(parts) > 0 {
		b.Agent = strings.TrimSpace(parts[0])
	}
	if len(parts) > 1 {
		b.Model = strings.TrimSpace(parts[1])
	}
	if len(parts) > 2 {
		b.Effort = strings.TrimSpace(parts[2])
	}
	if b.IsZero() {
		return b, fmt.Errorf("empty pin — use [agent]:[model][:effort], e.g. claude:claude-opus-4-8:high")
	}
	if !domain.ValidEffort(b.Effort) {
		return b, fmt.Errorf("effort %q is not one of %v", b.Effort, domain.Efforts)
	}
	// Validate the agent NAME here, at pin time — a task pinned to an
	// unknown agent would fail every pass at setup instead.
	if b.Agent != "" {
		if _, err := driver.New(b.Agent); err != nil {
			return b, err
		}
	}
	return b, nil
}

func taskAdd(args []string) int {
	fs := flag.NewFlagSet("task add", flag.ExitOnError)
	repo := fs.String("repo", "", "repository path")
	detail := fs.String("detail", "", "task detail")
	typ := fs.String("type", "", "task type (empty = auto-classified)")
	backlogName := fs.String("backlog", "shared", "shared, or a pipeline name")
	pin := fs.String("pin", "", "[agent]:[model][:effort] pin")
	first := fs.Bool("first", false, "place at the top of its backlog")
	pos := parseMixed(fs, args)
	if len(pos) < 1 {
		fmt.Fprintln(os.Stderr, "error: task add needs a title")
		return 2
	}
	title := strings.Join(pos, " ")

	ctx, cancel := interruptCtx()
	defer cancel()
	a, err := openApp(ctx, *repo)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 2
	}
	defer a.Close()

	backlog, err := resolveBacklogName(a, *backlogName)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 2
	}
	task := &domain.Task{Backlog: backlog, Type: strings.ToLower(*typ), Title: title, Detail: *detail}
	if *pin != "" {
		if task.Pin, err = parsePin(*pin); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			return 2
		}
	}
	added, err := a.Store.AddTask(ctx, task, *first)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	fmt.Printf("added %s to %s: %s\n", added.ID, backlogLabel(added.Backlog), added.Title)
	return 0
}

func taskList(args []string) int {
	fs := flag.NewFlagSet("task list", flag.ExitOnError)
	repo := fs.String("repo", "", "repository path")
	asJSON := fs.Bool("json", false, "machine-readable output")
	all := fs.Bool("all", false, "include done/failed/cancelled tasks")
	parseMixed(fs, args)

	ctx, cancel := interruptCtx()
	defer cancel()
	a, err := openApp(ctx, *repo)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 2
	}
	defer a.Close()

	filter := store.TaskFilter{Statuses: []domain.TaskStatus{domain.TaskOpen, domain.TaskClaimed, domain.TaskStuck}}
	if *all {
		filter.Statuses = nil
	}
	tasks, err := a.Store.ListTasks(ctx, filter)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	if *asJSON {
		printJSON(tasks)
		return 0
	}
	if len(tasks) == 0 {
		fmt.Println("no tasks")
		return 0
	}
	current := "\x00"
	for _, t := range tasks {
		if t.Backlog != current {
			current = t.Backlog
			fmt.Printf("\n== %s ==\n", backlogLabel(current))
		}
		flags := ""
		if !t.Pin.IsZero() {
			flags += " pin=" + pinLabel(t.Pin)
		}
		if t.Status == domain.TaskClaimed {
			flags += " CLAIMED:" + t.ClaimedBy
		}
		if t.Status == domain.TaskStuck {
			flags += fmt.Sprintf(" STUCK(%d attempts)", t.Attempts)
		}
		if *all && (t.Status == domain.TaskDone || t.Status == domain.TaskFailed || t.Status == domain.TaskCancelled) {
			flags += " " + strings.ToUpper(string(t.Status))
		}
		typ := t.Type
		if typ == "" {
			typ = "-"
		}
		fmt.Printf("  %-30s %-10s %s%s\n", t.ID, typ, t.Title, flags)
	}
	return 0
}

func backlogLabel(b string) string {
	if b == domain.MainBacklog {
		return statedir.SharedLabel
	}
	return b
}

func pinLabel(b domain.Bundle) string {
	s := b.Agent + ":" + b.Model
	if b.Effort != "" {
		s += ":" + b.Effort
	}
	return s
}

func taskTag(args []string) int {
	fs := flag.NewFlagSet("task tag", flag.ExitOnError)
	repo := fs.String("repo", "", "repository path")
	pos := parseMixed(fs, args)
	if len(pos) != 2 {
		fmt.Fprintln(os.Stderr, "usage: workshop task tag <id> <type|->")
		return 2
	}
	id, typ := pos[0], strings.ToLower(pos[1])
	if typ == "-" {
		typ = ""
	}
	return patchTask(*repo, id, store.TaskPatch{Type: &typ}, func(t *domain.Task) string {
		if t.Type == "" {
			return "cleared type of " + t.ID + " (will be re-classified)"
		}
		return "tagged " + t.ID + " as " + t.Type
	})
}

func taskPin(args []string) int {
	fs := flag.NewFlagSet("task pin", flag.ExitOnError)
	repo := fs.String("repo", "", "repository path")
	pos := parseMixed(fs, args)
	if len(pos) != 2 {
		fmt.Fprintln(os.Stderr, "usage: workshop task pin <id> [agent]:[model][:effort]")
		return 2
	}
	pin, err := parsePin(pos[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 2
	}
	return patchTask(*repo, pos[0], store.TaskPatch{Pin: &pin}, func(t *domain.Task) string {
		return "pinned " + t.ID + " to " + pinLabel(t.Pin)
	})
}

func taskUnpin(args []string) int {
	fs := flag.NewFlagSet("task unpin", flag.ExitOnError)
	repo := fs.String("repo", "", "repository path")
	pos := parseMixed(fs, args)
	if len(pos) != 1 {
		fmt.Fprintln(os.Stderr, "usage: workshop task unpin <id>")
		return 2
	}
	empty := domain.Bundle{}
	return patchTask(*repo, pos[0], store.TaskPatch{Pin: &empty}, func(t *domain.Task) string {
		return "unpinned " + t.ID
	})
}

func patchTask(repo, id string, patch store.TaskPatch, describe func(*domain.Task) string) int {
	ctx, cancel := interruptCtx()
	defer cancel()
	a, err := openApp(ctx, repo)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 2
	}
	defer a.Close()
	t, err := a.Store.UpdateTask(ctx, id, patch)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	fmt.Println(describe(t))
	return 0
}

func taskMv(args []string) int {
	fs := flag.NewFlagSet("task mv", flag.ExitOnError)
	repo := fs.String("repo", "", "repository path")
	to := fs.String("to", "", "target backlog: shared or a pipeline name")
	pos := parseMixed(fs, args)
	if len(pos) < 1 {
		fmt.Fprintln(os.Stderr, "usage: workshop task mv <id> top|bottom|--to <backlog>")
		return 2
	}
	id := pos[0]

	ctx, cancel := interruptCtx()
	defer cancel()
	a, err := openApp(ctx, *repo)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 2
	}
	defer a.Close()

	if *to != "" {
		backlog, err := resolveBacklogName(a, *to)
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			return 2
		}
		t, err := a.Store.MoveTask(ctx, id, backlog)
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			return 1
		}
		fmt.Printf("moved %s to %s\n", t.ID, backlogLabel(t.Backlog))
		return 0
	}

	place := "bottom"
	if len(pos) > 1 {
		place = strings.ToLower(pos[1])
	}
	t, err := a.Store.GetTask(ctx, id)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	switch place {
	case "top":
		err = a.Store.ReorderBacklog(ctx, t.Backlog, []string{id})
	case "bottom":
		_, err = a.Store.MoveTask(ctx, id, t.Backlog)
	default:
		fmt.Fprintln(os.Stderr, "error: position must be top or bottom (or use --to)")
		return 2
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	fmt.Printf("moved %s to the %s of %s\n", id, place, backlogLabel(t.Backlog))
	return 0
}

func taskRm(args []string) int {
	fs := flag.NewFlagSet("task rm", flag.ExitOnError)
	repo := fs.String("repo", "", "repository path")
	_ = fs.Parse(args)
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: workshop task rm <id>")
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
	if err := a.DeleteTask(ctx, fs.Arg(0)); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	fmt.Println("removed", fs.Arg(0))
	return 0
}

var _ = config.RepoConfigDir // keep import while subcommands grow
