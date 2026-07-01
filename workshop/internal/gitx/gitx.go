// Package gitx wraps the `git` CLI (shelled out via os/exec, matching the current
// behavior — git is already required). Shared by the engine (per-pass commit) and the
// server (status/commit feed) so there's one place that knows how to talk to git.
package gitx

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Commit is one entry in the recent-commits feed.
type Commit struct {
	SHA     string `json:"sha"`
	Subject string `json:"subject"`
	Time    string `json:"time"`
}

// Run executes `git -C dir args...` and returns combined stdout+stderr. git writes
// warnings/notices to stderr on SUCCESS, so a non-nil error is the ONLY failure signal.
func Run(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	return strings.TrimRight(buf.String(), "\r\n"), err
}

// GitDir resolves the repo's git directory (handles worktrees/submodules).
func GitDir(repo string) string {
	out, err := Run(repo, "rev-parse", "--git-dir")
	if err != nil {
		return ""
	}
	gd := strings.TrimSpace(out)
	if gd == "" {
		return ""
	}
	if !filepath.IsAbs(gd) {
		gd = filepath.Join(repo, gd)
	}
	return gd
}

// CleanStaleLock removes an orphaned index.lock older than 60s (a crashed git/agent
// leaves one that blocks every subsequent git op). Returns (removed, ageSeconds).
func CleanStaleLock(repo string) (bool, int) {
	gd := GitDir(repo)
	if gd == "" {
		return false, 0
	}
	lock := filepath.Join(gd, "index.lock")
	fi, err := os.Stat(lock)
	if err != nil {
		return false, 0
	}
	age := time.Since(fi.ModTime())
	if age.Seconds() > 60 && os.Remove(lock) == nil {
		return true, int(age.Seconds())
	}
	return false, 0
}

// StatusPorcelain returns non-empty `git status --porcelain` lines.
func StatusPorcelain(repo string) ([]string, error) {
	out, err := Run(repo, "status", "--porcelain")
	if err != nil {
		return nil, err
	}
	var lines []string
	for _, l := range strings.Split(out, "\n") {
		if strings.TrimSpace(l) != "" {
			lines = append(lines, l)
		}
	}
	return lines, nil
}

// DirtyFiles extracts up to max changed file paths (for the live-activity panel).
func DirtyFiles(repo string, max int) []string {
	lines, err := StatusPorcelain(repo)
	if err != nil {
		return nil
	}
	out := []string{}
	for _, l := range lines {
		s := strings.TrimSpace(l)
		if i := strings.Index(s, " "); i >= 0 {
			s = strings.TrimSpace(s[i:])
		}
		if a := strings.Index(s, " -> "); a >= 0 {
			s = s[a+4:]
		}
		out = append(out, s)
		if len(out) >= max {
			break
		}
	}
	return out
}

// CommitAll stages everything and commits with message. Returns the short HEAD sha on
// success, or ("", nil) if the tree was clean.
func CommitAll(repo, message string) (string, error) {
	lines, err := StatusPorcelain(repo)
	if err != nil {
		return "", err
	}
	if len(lines) == 0 {
		return "", nil
	}
	if _, err := Run(repo, "add", "-A"); err != nil {
		return "", err
	}
	if _, err := Run(repo, "commit", "-q", "-m", message); err != nil {
		return "", err
	}
	sha, _ := Run(repo, "rev-parse", "--short", "HEAD")
	return strings.TrimSpace(sha), nil
}

// ShowStat returns `git show --stat` for HEAD (files changed this pass).
func ShowStat(repo string) string {
	out, _ := Run(repo, "show", "--stat", "--format=commit %h  %s", "HEAD")
	return out
}

// CheckoutBranch checks out branch if it exists (best-effort).
func CheckoutBranch(repo, branch string) {
	if branch == "" {
		return
	}
	if _, err := Run(repo, "rev-parse", "--verify", branch); err == nil {
		_, _ = Run(repo, "checkout", branch)
	}
}

// Commits returns the most recent n commits as a feed.
func Commits(repo string, n int) []Commit {
	out, err := Run(repo, "log", "-"+itoa(n), "--format=%h|%s|%cd", "--date=format:%H:%M:%S")
	if err != nil || strings.TrimSpace(out) == "" {
		return []Commit{}
	}
	var feed []Commit
	for _, l := range strings.Split(out, "\n") {
		if strings.TrimSpace(l) == "" {
			continue
		}
		parts := strings.SplitN(l, "|", 3)
		c := Commit{SHA: parts[0]}
		if len(parts) > 1 {
			c.Subject = parts[1]
		}
		if len(parts) > 2 {
			c.Time = parts[2]
		}
		feed = append(feed, c)
	}
	return feed
}

// IsRepo reports whether dir is inside a git working tree.
func IsRepo(dir string) bool {
	out, err := Run(dir, "rev-parse", "--is-inside-work-tree")
	return err == nil && strings.TrimSpace(out) == "true"
}

func itoa(n int) string {
	if n <= 0 {
		return "8"
	}
	// small positive ints only
	digits := ""
	for n > 0 {
		digits = string(rune('0'+n%10)) + digits
		n /= 10
	}
	return digits
}
