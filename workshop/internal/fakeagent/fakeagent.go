// Package fakeagent is the scripted stand-in for a real coding agent. The
// engine's fake driver execs the workshop binary (or a test binary) back
// into this entry point, letting every path of the pass state machine be
// driven deterministically: happy passes, blocked reports, crashes, auth
// failures, and wedges.
//
// Configuration comes from environment variables the engine sets on every
// agent child:
//
//	WORKSHOP_PASS_STATE_DIR  — the pipeline's agent-facing state dir
//	WORKSHOP_PASS_REPO_DIR   — the working directory (repo/worktree)
//	WORKSHOP_FAKE_SCENARIO   — path to a scenario JSON file (optional;
//	                           missing = the default happy behavior)
package fakeagent

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/gw1108/cosmic-agent-tools/workshop/internal/domain"
	"github.com/gw1108/cosmic-agent-tools/workshop/internal/statedir"
)

// Scenario scripts one fake pass.
type Scenario struct {
	// Behavior: "happy" (default), "blocked", "reverted", "silent" (no
	// progress writes), "crash" (nonzero exit), "auth" (auth-looking
	// failure), "sleep" (wedge bait).
	Behavior string `json:"behavior"`

	SleepMs   int               `json:"sleepMs"`   // pre-exit sleep (sleep behavior: main wait)
	ExitCode  int               `json:"exitCode"`  // crash/auth exit code (default 1)
	Print     []string          `json:"print"`     // lines to emit on stdout
	Proposals []domain.Proposal `json:"proposals"` // written to proposals.json on happy passes
	WriteFile string            `json:"writeFile"` // repo-relative file to touch (default fake-work.txt)
	NoEdit    bool              `json:"noEdit"`    // happy pass without any repo edit
}

// Main runs one fake pass; returns the process exit code.
func Main() int {
	// A real agent consumes the prompt; drain stdin so the parent's write
	// never blocks — but only when stdin is actually a pipe. In blind
	// (own-console) mode stdin is the console and reading it would hang.
	if info, err := os.Stdin.Stat(); err == nil && info.Mode()&os.ModeCharDevice == 0 {
		_, _ = io.Copy(io.Discard, os.Stdin)
	}

	stateDir := os.Getenv("WORKSHOP_PASS_STATE_DIR")
	repoDir := os.Getenv("WORKSHOP_PASS_REPO_DIR")
	if stateDir == "" || repoDir == "" {
		fmt.Fprintln(os.Stderr, "fakeagent: missing WORKSHOP_PASS_STATE_DIR / WORKSHOP_PASS_REPO_DIR")
		return 2
	}

	sc := Scenario{Behavior: "happy"}
	if p := os.Getenv("WORKSHOP_FAKE_SCENARIO"); p != "" {
		if err := statedir.ReadJSON(p, &sc); err != nil {
			fmt.Fprintf(os.Stderr, "fakeagent: scenario %s: %v\n", p, err)
			return 2
		}
		if sc.Behavior == "" {
			sc.Behavior = "happy"
		}
	}

	for _, line := range sc.Print {
		fmt.Println(line)
	}

	// What am I working on?
	var task domain.Task
	title := "(invented task)"
	if err := statedir.ReadJSON(filepath.Join(stateDir, statedir.TaskFile), &task); err == nil && task.Title != "" {
		title = task.Title
	}

	writeProgress := func(p domain.Progress) {
		p.Updated = time.Now().UTC().Format(time.RFC3339)
		_ = statedir.WriteJSON(filepath.Join(stateDir, statedir.ProgressFile), p)
	}

	switch sc.Behavior {
	case "silent":
		return 0
	case "crash":
		fmt.Println("something went wrong, giving up")
		if sc.ExitCode == 0 {
			sc.ExitCode = 1
		}
		return sc.ExitCode
	case "auth":
		fmt.Println("Error: unauthorized - credential expired, please sign-in again")
		if sc.ExitCode == 0 {
			sc.ExitCode = 1
		}
		return sc.ExitCode
	case "sleep":
		writeProgress(domain.Progress{Phase: "working", Task: title, Plan: "sleeping forever"})
		time.Sleep(time.Duration(sc.SleepMs) * time.Millisecond)
		return 0
	case "blocked", "reverted":
		writeProgress(domain.Progress{Phase: "working", Task: title, Plan: "attempting"})
		writeProgress(domain.Progress{Phase: sc.Behavior, Task: title, Note: "scripted " + sc.Behavior})
		return 0
	}

	// happy path
	writeProgress(domain.Progress{Phase: "working", Task: title, Plan: "one scripted increment"})
	if !sc.NoEdit {
		name := sc.WriteFile
		if name == "" {
			name = "fake-work.txt"
		}
		path := filepath.Join(repoDir, name)
		f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			fmt.Fprintln(os.Stderr, "fakeagent:", err)
			return 1
		}
		fmt.Fprintf(f, "%s @ %s\n", title, time.Now().UTC().Format(time.RFC3339Nano))
		f.Close()
	}
	if len(sc.Proposals) > 0 {
		_ = statedir.WriteJSON(filepath.Join(stateDir, statedir.ProposalsFile), sc.Proposals)
	}
	if sc.SleepMs > 0 {
		time.Sleep(time.Duration(sc.SleepMs) * time.Millisecond)
	}
	writeProgress(domain.Progress{Phase: "done", Task: title, Result: "completed: " + title})
	fmt.Println("fake pass complete:", title)
	return 0
}
