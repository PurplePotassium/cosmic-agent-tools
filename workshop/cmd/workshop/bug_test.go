package main

import (
	"strings"
	"testing"

	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/app"
	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/domain"
)

// A fully-populated report renders every section a future agent needs to repro:
// the description, the environment, git state, server, paths, and status.
func TestFormatBugReport(t *testing.T) {
	rep := &bugReport{
		Generated:   "2026-07-04T06:40:00Z",
		Description: "engine wedges on Ctrl+C",
		Workshop:    workshopInfo{Version: "dev", Go: "go1.26.4", OS: "windows", Arch: "amd64", NumCPU: 8, Git: "git version 2.44.0"},
		Repo: repoInfo{
			Dir: `C:\repo`, Branch: "main", Head: "deadbeef", Dirty: true,
			Changed: []string{"a.go", "b.go"},
		},
		Server: &bugServerInfo{Running: true, PID: 4321, Port: 7777},
		Paths:  [][2]string{{"state dir", `C:\state`}},
		Status: &app.Status{
			SharedBacklog: 3,
			Pipelines: []app.PipelineStatus{{
				Name: "main", Mode: "goal", Agent: "claude", Halted: "operator",
				LastPass: &domain.Pass{N: 39, Outcome: "committed", CommitSHA: "abc123"},
				Progress: domain.Progress{Phase: "working"}, ProgressAgeSec: 12,
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
		"shared backlog: 3 open",
		"pipeline `main`", "HALTED(operator)", "iter 39 committed abc123", "progress=working(12s ago)",
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

// selectLogPass prefers a halted pipeline's last pass, and otherwise the most
// recent pass across all pipelines (highest, monotonic, ID).
func TestSelectLogPass(t *testing.T) {
	if p := selectLogPass(nil); p != nil {
		t.Fatalf("nil status must yield no pass, got %+v", p)
	}
	if p := selectLogPass(&app.Status{Pipelines: []app.PipelineStatus{{Name: "main"}}}); p != nil {
		t.Fatalf("no last pass must yield nil, got %+v", p)
	}

	// A halted lane wins even though the other lane's pass is newer.
	halted := selectLogPass(&app.Status{Pipelines: []app.PipelineStatus{
		{Name: "a", LastPass: &domain.Pass{ID: 5, Pipeline: "a"}},
		{Name: "b", Halted: "breaker", LastPass: &domain.Pass{ID: 3, Pipeline: "b"}},
	}})
	if halted == nil || halted.Pipeline != "b" {
		t.Fatalf("halted lane should win, got %+v", halted)
	}

	// With nothing halted, the newest pass (highest ID) wins.
	newest := selectLogPass(&app.Status{Pipelines: []app.PipelineStatus{
		{Name: "a", LastPass: &domain.Pass{ID: 5, Pipeline: "a"}},
		{Name: "b", LastPass: &domain.Pass{ID: 9, Pipeline: "b"}},
	}})
	if newest == nil || newest.ID != 9 {
		t.Fatalf("newest pass should win, got %+v", newest)
	}
}
