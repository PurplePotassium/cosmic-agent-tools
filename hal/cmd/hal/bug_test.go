package main

import (
	"strings"
	"testing"

	"github.com/PurplePotassium/cosmic-agent-tools/hal/internal/app"
	"github.com/PurplePotassium/cosmic-agent-tools/hal/internal/domain"
)

// A fully-populated report renders every section a future agent needs to repro:
// the description, the environment, git state, server, paths, and status.
func TestFormatBugReport(t *testing.T) {
	rep := &bugReport{
		Generated:   "2026-07-04T06:40:00Z",
		Description: "engine wedges on Ctrl+C",
		Hal:    halInfo{Version: "dev", Go: "go1.26.4", OS: "windows", Arch: "amd64", NumCPU: 8, Git: "git version 2.44.0"},
		Repo: repoInfo{
			Dir: `C:\repo`, Branch: "main", Head: "deadbeef", Dirty: true,
			Changed: []string{"a.go", "b.go"},
		},
		Server: &bugServerInfo{Running: true, PID: 4321, Port: 7777},
		Paths:  [][2]string{{"state dir", `C:\state`}},
		Status: &app.Status{
			OpenTasks: 3,
			Workflows: []domain.Workflow{{
				ID: "2026-08-03-fix-thing-aa11", Stage: domain.StageImplement,
				Status: domain.WorkflowError, Error: "auth: agent exit 1",
			}},
			Warnings: []string{"config warning here"},
		},
	}
	out := formatBugReport(rep)

	for _, want := range []string{
		"engine wedges on Ctrl+C",
		"go1.26.4", "windows/amd64", "git version 2.44.0",
		"deadbeef", "dirty", "`a.go`",
		"pid 4321, port 7777",
		"ideas inbox: 3 open",
		"workflow `2026-08-03-fix-thing-aa11`", "stage=implement", "status=error", "auth: agent exit 1",
		"config warning here",
		"## Config",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("report missing %q\n---\n%s", want, out)
		}
	}
}

// The report is meant to leave the machine — it must never carry the running
// server's session token (a loopback CSRF credential). bugServerInfo has no
// token field, so a leak can only happen by someone adding one; guard it.
func TestBugReportOmitsServerToken(t *testing.T) {
	rep := &bugReport{
		Server: &bugServerInfo{Running: true, PID: 1, Port: 2},
		Paths:  [][2]string{},
	}
	out := strings.ToLower(formatBugReport(rep))
	if strings.Contains(out, "token") {
		t.Fatalf("bug report leaked a token reference:\n%s", out)
	}
}

// A bare report (no server, no status, no description) still renders every
// section header without panicking — the broken-state case a bug is filed in.
func TestFormatBugReportEmpty(t *testing.T) {
	out := formatBugReport(&bugReport{Paths: [][2]string{}})
	for _, want := range []string{
		"no description provided",
		"no server record",
		"status snapshot unavailable",
		"## Environment", "## Repository", "## Config",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("empty report missing %q\n---\n%s", want, out)
		}
	}
	// --logs is opt-in: with no Log the report carries no pass-log section.
	if strings.Contains(out, "## Pass log") {
		t.Errorf("default report should not include a pass log section:\n%s", out)
	}
}

// With --logs the report renders the embedded tail under its own section,
// labelled with the pipeline and iteration it came from.
func TestFormatBugReportWithLog(t *testing.T) {
	rep := &bugReport{
		Paths: [][2]string{},
		Log: &bugLog{
			Pipeline: "main", PassN: 41, Outcome: "failed",
			Tail: "gate red\nexit status 1",
		},
	}
	out := formatBugReport(rep)
	for _, want := range []string{
		"## Pass log",
		"pipeline `main`, iter 41 (failed)",
		"gate red", "exit status 1",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("report missing %q\n---\n%s", want, out)
		}
	}
}

