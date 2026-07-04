package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/app"
	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/config"
	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/gitx"
	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/server"
)

// bugReport is one self-contained snapshot of everything a future agent needs
// to reproduce a workshop issue: the operator's one-line description, the build
// + host environment, the git state, the resolved config, and the live engine
// status. It carries NO secrets — the running server's session token is
// deliberately excluded (see gatherServer) so the report is safe to paste into
// an issue tracker or hand to another agent verbatim.
type bugReport struct {
	Generated   string         `json:"generated"`
	Description string         `json:"description"`
	Workshop    workshopInfo   `json:"workshop"`
	Repo        repoInfo       `json:"repo"`
	Server      *bugServerInfo `json:"server,omitempty"`
	Paths       [][2]string    `json:"paths"`
	Config      app.ConfigView `json:"config"`
	Status      *app.Status    `json:"status,omitempty"`
}

type workshopInfo struct {
	Version string `json:"version"`
	Go      string `json:"go"`
	OS      string `json:"os"`
	Arch    string `json:"arch"`
	NumCPU  int    `json:"numCPU"`
	Git     string `json:"git,omitempty"`
}

type repoInfo struct {
	Dir     string   `json:"dir"`
	Branch  string   `json:"branch,omitempty"`
	Head    string   `json:"head,omitempty"`
	Dirty   bool     `json:"dirty"`
	Changed []string `json:"changed,omitempty"`
}

// bugServerInfo reports whether a server is running and how to find it — but
// NEVER its token. A bug report is meant to leave the machine; the token is a
// loopback CSRF credential that would let anyone who sees the report drive the
// running engine.
type bugServerInfo struct {
	Running bool `json:"running"`
	PID     int  `json:"pid,omitempty"`
	Port    int  `json:"port,omitempty"`
}

// maxChangedPaths caps the dirty-file list so a report generated on a tree with
// thousands of build artifacts stays readable.
const maxChangedPaths = 60

func cmdBug(args []string) int {
	fs := flag.NewFlagSet("bug", flag.ExitOnError)
	repo := fs.String("repo", "", "repository path")
	asJSON := fs.Bool("json", false, "machine-readable output")
	out := fs.String("out", "", "write the report to this file (default: <state dir>/bug-report-<time>.md)")
	pos := parseMixed(fs, args)
	description := strings.TrimSpace(strings.Join(pos, " "))

	ctx, cancel := interruptCtx()
	defer cancel()
	a, err := openApp(ctx, *repo)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 2
	}
	defer a.Close()

	rep := gatherBugReport(ctx, a, description)

	if *asJSON {
		printJSON(rep)
		return 0
	}

	md := formatBugReport(rep)
	// Always print to stdout so the report can be piped or copied directly —
	// "as easy as possible to send". Also drop a copy on disk so the operator
	// can attach the file, and tell them where (on stderr, keeping stdout the
	// clean report).
	fmt.Print(md)
	path := *out
	if path == "" {
		path = filepath.Join(a.StateDir, "bug-report-"+time.Now().UTC().Format("20060102-150405")+".md")
	}
	if err := os.WriteFile(path, []byte(md), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "warning: could not save report to a file:", err)
	} else {
		fmt.Fprintln(os.Stderr, "\nsaved to", path)
	}
	return 0
}

// gatherBugReport collects the report. Every probe is best-effort: a broken
// repo or a missing HEAD must still yield a usable report, since a broken state
// is exactly when someone files a bug.
func gatherBugReport(ctx context.Context, a *app.App, description string) *bugReport {
	rep := &bugReport{
		Generated:   time.Now().UTC().Format(time.RFC3339),
		Description: description,
		Workshop: workshopInfo{
			Version: Version,
			Go:      runtime.Version(),
			OS:      runtime.GOOS,
			Arch:    runtime.GOARCH,
			NumCPU:  runtime.NumCPU(),
			Git:     gitVersion(ctx),
		},
		Config: a.ConfigSnapshot(),
	}

	rep.Repo.Dir = a.RepoDir
	rep.Repo.Branch, _ = gitx.CurrentBranch(ctx, a.RepoDir)
	rep.Repo.Head, _ = gitx.RevParse(ctx, a.RepoDir, "HEAD")
	if changed, err := gitx.StatusPorcelain(ctx, a.RepoDir); err == nil {
		rep.Repo.Dirty = len(changed) > 0
		if len(changed) > maxChangedPaths {
			changed = append(changed[:maxChangedPaths:maxChangedPaths],
				fmt.Sprintf("… and %d more", len(changed)-maxChangedPaths))
		}
		rep.Repo.Changed = changed
	}

	if si, err := server.ReadInfo(a.StateDir); err == nil {
		rep.Server = &bugServerInfo{
			Running: pingServer(si.Port, si.Token),
			PID:     si.PID,
			Port:    si.Port,
		}
	}

	rep.Paths = [][2]string{
		{"repo", a.RepoDir},
		{"repo config", config.RepoConfigFile(a.RepoDir)},
		{"goal", config.GoalFile(a.RepoDir)},
		{"state dir", a.StateDir},
		{"logs", filepath.Join(a.StateDir, "logs")},
		{"user config", config.UserConfigFile()},
		{"overrides", config.OverridesFile(a.RepoDir)},
	}

	if snap, err := a.Snapshot(ctx); err == nil {
		rep.Status = snap
	}
	return rep
}

func gitVersion(ctx context.Context) string {
	out, err := exec.CommandContext(ctx, "git", "--version").Output()
	if err != nil {
		return ""
	}
	return string(trimNL(out))
}

// formatBugReport renders a report as a single self-contained markdown
// document. It is pure (no I/O) so its shape can be asserted in tests.
func formatBugReport(r *bugReport) string {
	var b strings.Builder
	b.WriteString("# Workshop bug report\n\n")

	b.WriteString("## What's wrong\n\n")
	if r.Description == "" {
		b.WriteString("_(no description provided — re-run `workshop bug \"<what went wrong>\"`)_\n\n")
	} else {
		b.WriteString(r.Description + "\n\n")
	}

	b.WriteString("## Environment\n\n")
	fmt.Fprintf(&b, "- workshop: `%s`\n", r.Workshop.Version)
	fmt.Fprintf(&b, "- go: `%s`\n", r.Workshop.Go)
	fmt.Fprintf(&b, "- os/arch: `%s/%s` (%d CPU)\n", r.Workshop.OS, r.Workshop.Arch, r.Workshop.NumCPU)
	if r.Workshop.Git != "" {
		fmt.Fprintf(&b, "- git: `%s`\n", r.Workshop.Git)
	}
	fmt.Fprintf(&b, "- generated: %s\n\n", r.Generated)

	b.WriteString("## Repository\n\n")
	fmt.Fprintf(&b, "- dir: `%s`\n", r.Repo.Dir)
	if r.Repo.Branch != "" {
		fmt.Fprintf(&b, "- branch: `%s`\n", r.Repo.Branch)
	}
	if r.Repo.Head != "" {
		fmt.Fprintf(&b, "- HEAD: `%s`\n", r.Repo.Head)
	}
	fmt.Fprintf(&b, "- working tree: %s\n", cleanDirty(r.Repo.Dirty))
	if len(r.Repo.Changed) > 0 {
		b.WriteString("- changed paths:\n")
		for _, p := range r.Repo.Changed {
			fmt.Fprintf(&b, "  - `%s`\n", p)
		}
	}
	b.WriteString("\n")

	b.WriteString("## Server\n\n")
	if r.Server == nil {
		b.WriteString("- no server record for this repo\n\n")
	} else {
		fmt.Fprintf(&b, "- running: %v (pid %d, port %d)\n\n", r.Server.Running, r.Server.PID, r.Server.Port)
	}

	b.WriteString("## Paths\n\n")
	for _, kv := range r.Paths {
		fmt.Fprintf(&b, "- %s: `%s`\n", kv[0], kv[1])
	}
	b.WriteString("\n")

	b.WriteString("## Status\n\n")
	if r.Status == nil {
		b.WriteString("_(status snapshot unavailable)_\n\n")
	} else {
		fmt.Fprintf(&b, "- shared backlog: %d open\n", r.Status.SharedBacklog)
		for _, p := range r.Status.Pipelines {
			fmt.Fprintf(&b, "- pipeline `%s`: mode=%s agent=%s", p.Name, p.Mode, p.Agent)
			if p.Halted != "" {
				fmt.Fprintf(&b, " HALTED(%s)", p.Halted)
			}
			if p.LastPass != nil {
				fmt.Fprintf(&b, " last=[iter %d %s %s]", p.LastPass.N, p.LastPass.Outcome, p.LastPass.CommitSHA)
			}
			if p.Progress.Phase != "" {
				fmt.Fprintf(&b, " progress=%s(%ds ago)", p.Progress.Phase, p.ProgressAgeSec)
			}
			b.WriteString("\n")
		}
		for _, w := range r.Status.Warnings {
			fmt.Fprintf(&b, "- warning: %s\n", w)
		}
		b.WriteString("\n")
	}

	b.WriteString("## Config\n\n")
	b.WriteString("```json\n")
	b.WriteString(jsonBlock(r.Config))
	b.WriteString("\n```\n")
	return b.String()
}

func cleanDirty(dirty bool) string {
	if dirty {
		return "dirty (uncommitted changes)"
	}
	return "clean"
}

func jsonBlock(v any) string {
	out, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Sprintf("(could not encode: %v)", err)
	}
	return string(out)
}
