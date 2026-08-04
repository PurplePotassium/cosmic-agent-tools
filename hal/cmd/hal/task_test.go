package main

import (
	"context"
	"flag"
	"slices"
	"testing"
	"time"

	"github.com/PurplePotassium/cosmic-agent-tools/hal/internal/app"
	"github.com/PurplePotassium/cosmic-agent-tools/hal/internal/domain"
	"github.com/PurplePotassium/cosmic-agent-tools/hal/internal/store"
)

// A lone "-" is a positional arg the flag package refuses to consume; the
// interleave loop must swallow it too or it spins forever at 100% CPU.
func TestParseMixedLoneDash(t *testing.T) {
	fs := flag.NewFlagSet("add", flag.ContinueOnError)
	typ := fs.String("type", "", "")

	done := make(chan []string, 1)
	go func() { done <- parseMixed(fs, []string{"fix", "-", "thing", "--type", "art"}) }()

	select {
	case got := <-done:
		if want := []string{"fix", "-", "thing"}; !slices.Equal(got, want) {
			t.Fatalf("positional = %v, want %v", got, want)
		}
		if *typ != "art" {
			t.Fatalf("interleaved flag lost: type=%q", *typ)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("parseMixed spun forever on a lone dash")
	}
}

// The original motivation for parseMixed: flags AFTER positionals must not
// be silently eaten as positionals.
func TestParseMixedInterleaves(t *testing.T) {
	fs := flag.NewFlagSet("add", flag.ContinueOnError)
	typ := fs.String("type", "", "")
	first := fs.Bool("first", false, "")

	got := parseMixed(fs, []string{"paint the sky", "--type", "art", "--first"})
	if want := []string{"paint the sky"}; !slices.Equal(got, want) {
		t.Fatalf("positional = %v, want %v", got, want)
	}
	if *typ != "art" || !*first {
		t.Fatalf("flags lost: type=%q first=%v", *typ, *first)
	}
}

// "--" is the conventional end-of-flags escape: everything after it is
// positional VERBATIM, even things that look like flags — and it must never
// be re-fed to fs.Parse (which would die on "flag provided but not defined").
func TestParseMixedDoubleDash(t *testing.T) {
	fs := flag.NewFlagSet("add", flag.ContinueOnError)
	typ := fs.String("type", "", "")

	got := parseMixed(fs, []string{"fix", "--type", "art", "--", "--fix-the-dashboard", "-x", "--"})
	if want := []string{"fix", "--fix-the-dashboard", "-x", "--"}; !slices.Equal(got, want) {
		t.Fatalf("positional = %v, want %v", got, want)
	}
	if *typ != "art" {
		t.Fatalf("flag before -- lost: type=%q", *typ)
	}
}

// A leading "--" (nothing but positionals follow) is the documented way to
// add a task whose title starts with a dash.
func TestParseMixedLeadingDoubleDash(t *testing.T) {
	fs := flag.NewFlagSet("add", flag.ContinueOnError)
	typ := fs.String("type", "", "")

	got := parseMixed(fs, []string{"--", "--type"})
	if want := []string{"--type"}; !slices.Equal(got, want) {
		t.Fatalf("positional = %v, want %v", got, want)
	}
	if *typ != "" {
		t.Fatalf("--type after -- must not be parsed as a flag; type=%q", *typ)
	}
}

// The mandatory end-to-end repro for the "--" fix: `task add -- "--title"`
// must actually create the task instead of dying in flag parsing.
func TestTaskAddLeadingDashTitle(t *testing.T) {
	repo := initTestRepo(t)

	if code := taskAdd([]string{"--repo", repo, "--", "--leading-dash-title"}); code != 0 {
		t.Fatalf("task add -- \"--leading-dash-title\" exited %d, want 0", code)
	}

	tasks := listAllTasks(t, repo)
	if len(tasks) != 1 || tasks[0].Title != "--leading-dash-title" {
		t.Fatalf("task not created verbatim; got %+v", tasks)
	}
}

// task rm must accept interleaved flags like its siblings:
// `task rm <id> --repo <path>` used to die with a usage error because rm
// alone still used bare fs.Parse.
func TestTaskRmTrailingFlags(t *testing.T) {
	repo := initTestRepo(t)

	if code := taskAdd([]string{"--repo", repo, "doomed", "task"}); code != 0 {
		t.Fatalf("task add exited %d", code)
	}
	tasks := listAllTasks(t, repo)
	if len(tasks) != 1 {
		t.Fatalf("setup: want 1 task, got %d", len(tasks))
	}

	if code := taskRm([]string{tasks[0].ID, "--repo", repo}); code != 0 {
		t.Fatalf("task rm <id> --repo exited %d, want 0", code)
	}
	if left := listAllTasks(t, repo); len(left) != 0 {
		t.Fatalf("task not removed; %d left", len(left))
	}
}

// listAllTasks reads every task straight from the store (no CLI output
// scraping).
func listAllTasks(t *testing.T, repo string) []*domain.Task {
	t.Helper()
	ctx := context.Background()
	a, err := app.Open(ctx, repo)
	if err != nil {
		t.Fatalf("app.Open: %v", err)
	}
	defer a.Close()
	tasks, err := a.Store.ListTasks(ctx, store.TaskFilter{})
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	return tasks
}
