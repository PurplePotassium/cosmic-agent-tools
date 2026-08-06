package main

import (
	"strings"
	"testing"
)

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
