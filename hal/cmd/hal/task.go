package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/PurplePotassium/cosmic-agent-tools/hal/internal/domain"
	"github.com/PurplePotassium/cosmic-agent-tools/hal/internal/store"
)

// The `task` command is the ideas inbox: a lightweight list of things worth
// doing, kept until the operator promotes one into a workflow (the dashboard's
// "promote" action) or deletes it. The old pass-loop machinery — claiming,
// type routing, pins, per-pipeline backlogs — died with the loop.
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
// for `task add "title" --detail x`).
func parseMixed(fs *flag.FlagSet, args []string) []string {
	// Honor the conventional "--" end-of-flags marker: everything after the
	// first literal "--" is positional, verbatim, and must never be re-fed to
	// fs.Parse — otherwise `task add -- "--leading-dash-title"` explodes with
	// "flag provided but not defined".
	var tail []string
	for i, a := range args {
		if a == "--" {
			args, tail = args[:i], args[i+1:]
			break
		}
	}
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
			return append(positional, tail...)
		}
		args = rest[i:]
	}
}

func taskUsage() {
	fmt.Print(`usage: hal task <subcommand>

  add "title" [--detail d] [--first]
  list        [--json] [--all]
  rm <id>

Tasks are the ideas inbox: queue thoughts here, then promote one into a
workflow from the dashboard when you're ready to work it.
`)
}

func taskAdd(args []string) int {
	fs := flag.NewFlagSet("task add", flag.ExitOnError)
	repo := fs.String("repo", "", "repository path")
	detail := fs.String("detail", "", "task detail")
	first := fs.Bool("first", false, "place at the top of the inbox")
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

	task := &domain.Task{Title: title, Detail: *detail}
	added, err := a.Store.AddTask(ctx, task, *first)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	fmt.Printf("added %s: %s\n", added.ID, added.Title)
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
	for _, t := range tasks {
		flags := ""
		if *all && (t.Status == domain.TaskDone || t.Status == domain.TaskFailed || t.Status == domain.TaskCancelled) {
			flags += " " + strings.ToUpper(string(t.Status))
		}
		fmt.Printf("  %-30s %s%s\n", t.ID, t.Title, flags)
	}
	return 0
}

func taskRm(args []string) int {
	fs := flag.NewFlagSet("task rm", flag.ExitOnError)
	repo := fs.String("repo", "", "repository path")
	pos := parseMixed(fs, args)
	if len(pos) != 1 {
		fmt.Fprintln(os.Stderr, "usage: hal task rm <id>")
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
	if err := a.DeleteTask(ctx, pos[0]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	fmt.Println("removed", pos[0])
	return 0
}
