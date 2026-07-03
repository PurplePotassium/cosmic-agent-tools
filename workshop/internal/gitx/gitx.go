// Package gitx is a thin, deliberate wrapper over the git CLI. Every helper
// takes the working directory explicitly — there is no ambient repo state —
// and parses only porcelain/plumbing output formats that git documents as
// stable.
package gitx

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Error wraps a failed git invocation with its output for diagnostics.
type Error struct {
	Args   []string
	Output string
	Err    error
}

func (e *Error) Error() string {
	return fmt.Sprintf("git %s: %v\n%s", strings.Join(e.Args, " "), e.Err, strings.TrimSpace(e.Output))
}

func (e *Error) Unwrap() error { return e.Err }

// run executes git in dir, returning trimmed combined output.
func run(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	s := strings.TrimRight(string(out), "\r\n")
	if err != nil {
		return s, &Error{Args: args, Output: s, Err: err}
	}
	return s, nil
}

// IsRepo reports whether dir is inside a git work tree.
func IsRepo(ctx context.Context, dir string) bool {
	out, err := run(ctx, dir, "rev-parse", "--is-inside-work-tree")
	return err == nil && out == "true"
}

// Root returns the top-level directory of the work tree containing dir.
func Root(ctx context.Context, dir string) (string, error) {
	out, err := run(ctx, dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", err
	}
	return filepath.FromSlash(out), nil
}

// GitDir returns the absolute git directory for dir. For linked worktrees
// this resolves to .git/worktrees/<name>/ — where that worktree's index.lock
// and MERGE_HEAD live.
func GitDir(ctx context.Context, dir string) (string, error) {
	out, err := run(ctx, dir, "rev-parse", "--git-dir")
	if err != nil {
		return "", err
	}
	gd := filepath.FromSlash(out)
	if !filepath.IsAbs(gd) {
		gd = filepath.Join(dir, gd)
	}
	return gd, nil
}

// CurrentBranch returns the checked-out branch name ("" when detached).
func CurrentBranch(ctx context.Context, dir string) (string, error) {
	out, err := run(ctx, dir, "branch", "--show-current")
	if err != nil {
		return "", err
	}
	return out, nil
}

// BranchExists reports whether a local branch exists.
func BranchExists(ctx context.Context, dir, branch string) bool {
	_, err := run(ctx, dir, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch)
	return err == nil
}

// CheckoutBranch checks out an existing branch.
func CheckoutBranch(ctx context.Context, dir, branch string) error {
	_, err := run(ctx, dir, "checkout", "-q", branch)
	return err
}

// CreateBranch creates (without checking out) a branch at base.
func CreateBranch(ctx context.Context, dir, branch, base string) error {
	_, err := run(ctx, dir, "branch", branch, base)
	return err
}

// DeleteBranch deletes a local branch; force ignores unmerged state.
func DeleteBranch(ctx context.Context, dir, branch string, force bool) error {
	flag := "-d"
	if force {
		flag = "-D"
	}
	_, err := run(ctx, dir, "branch", flag, branch)
	return err
}

// RevParse resolves a ref to a full SHA.
func RevParse(ctx context.Context, dir, ref string) (string, error) {
	return run(ctx, dir, "rev-parse", ref)
}

// StatusPorcelain returns the changed paths (git status --porcelain lines,
// path portion only).
func StatusPorcelain(ctx context.Context, dir string) ([]string, error) {
	out, err := run(ctx, dir, "status", "--porcelain")
	if err != nil {
		return nil, err
	}
	if out == "" {
		return nil, nil
	}
	var paths []string
	for _, line := range strings.Split(out, "\n") {
		if len(line) > 3 {
			p := strings.TrimSpace(line[3:])
			// Renames appear as "old -> new".
			if i := strings.Index(p, " -> "); i >= 0 {
				p = p[i+4:]
			}
			paths = append(paths, strings.Trim(p, `"`))
		}
	}
	return paths, nil
}

// IsDirty reports whether the work tree has any changes.
func IsDirty(ctx context.Context, dir string) (bool, error) {
	paths, err := StatusPorcelain(ctx, dir)
	return len(paths) > 0, err
}

// AheadCount returns how many commits branch is ahead of base.
func AheadCount(ctx context.Context, dir, base, branch string) (int, error) {
	out, err := run(ctx, dir, "rev-list", "--count", base+".."+branch)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(out))
}

// BuildCommitMessage assembles a subject plus git trailers.
func BuildCommitMessage(subject string, trailers [][2]string) string {
	if len(trailers) == 0 {
		return subject
	}
	var b strings.Builder
	b.WriteString(subject)
	b.WriteString("\n\n")
	for _, tr := range trailers {
		fmt.Fprintf(&b, "%s: %s\n", tr[0], tr[1])
	}
	return strings.TrimRight(b.String(), "\n")
}

// CommitAll stages everything and commits with the given message, returning
// the short SHA. If the repo has no committer identity configured it retries
// with a Workshop fallback identity rather than failing the pass.
func CommitAll(ctx context.Context, dir, message string) (string, error) {
	if _, err := run(ctx, dir, "add", "-A"); err != nil {
		return "", err
	}
	_, err := run(ctx, dir, "commit", "-q", "-m", message)
	if err != nil {
		var ge *Error
		if isIdentityError(err, &ge) {
			_, err = run(ctx, dir,
				"-c", "user.name=Workshop", "-c", "user.email=workshop@localhost",
				"commit", "-q", "-m", message)
		}
		if err != nil {
			return "", err
		}
	}
	return run(ctx, dir, "rev-parse", "--short", "HEAD")
}

func isIdentityError(err error, ge **Error) bool {
	e, ok := err.(*Error)
	if !ok {
		if u, ok2 := err.(interface{ Unwrap() error }); ok2 {
			if e2, ok3 := u.Unwrap().(*Error); ok3 {
				e = e2
			} else {
				return false
			}
		} else {
			return false
		}
	}
	*ge = e
	out := strings.ToLower(e.Output)
	return strings.Contains(out, "please tell me who you are") || strings.Contains(out, "empty ident")
}

// Commit is one entry of the recent-commit feed.
type Commit struct {
	SHA     string    `json:"sha"`
	Subject string    `json:"subject"`
	When    time.Time `json:"when"`
}

// RecentCommits returns the newest n commits on HEAD.
func RecentCommits(ctx context.Context, dir string, n int) ([]Commit, error) {
	out, err := run(ctx, dir, "log", "-n", strconv.Itoa(n), "--format=%h%x1f%s%x1f%ct")
	if err != nil {
		// An empty repo (no commits yet) is not an error for a feed.
		if strings.Contains(err.Error(), "does not have any commits") {
			return nil, nil
		}
		return nil, err
	}
	var commits []Commit
	for _, line := range strings.Split(out, "\n") {
		parts := strings.Split(line, "\x1f")
		if len(parts) != 3 {
			continue
		}
		secs, _ := strconv.ParseInt(parts[2], 10, 64)
		commits = append(commits, Commit{SHA: parts[0], Subject: parts[1], When: time.Unix(secs, 0).UTC()})
	}
	return commits, nil
}

// ShowStat returns the stat summary of a commit (for pass logs).
func ShowStat(ctx context.Context, dir, ref string) (string, error) {
	return run(ctx, dir, "show", "--stat", "--format=commit %h  %s", ref)
}

// CleanStaleLock removes an index.lock older than maxAge from dir's git dir
// (worktree-aware). A live git operation never holds the lock that long; a
// crashed agent's orphan lock otherwise blocks every git op.
func CleanStaleLock(ctx context.Context, dir string, maxAge time.Duration) (bool, error) {
	gd, err := GitDir(ctx, dir)
	if err != nil {
		return false, err
	}
	lock := filepath.Join(gd, "index.lock")
	info, err := os.Stat(lock)
	if err != nil {
		return false, nil // no lock — nothing to do
	}
	if time.Since(info.ModTime()) < maxAge {
		return false, nil
	}
	if err := os.Remove(lock); err != nil {
		return false, err
	}
	return true, nil
}

// HasMergeHead reports whether dir is mid-merge (MERGE_HEAD present).
func HasMergeHead(ctx context.Context, dir string) bool {
	gd, err := GitDir(ctx, dir)
	if err != nil {
		return false
	}
	_, statErr := os.Stat(filepath.Join(gd, "MERGE_HEAD"))
	return statErr == nil
}

// Merge merges ref into the current branch. It returns conflict=true when
// the merge stopped on conflicts (caller decides: abort, or hand to an
// agent). A missing committer identity is retried with the Workshop fallback
// identity (same as CommitAll) rather than failing the merge. Other failures
// return an error.
func Merge(ctx context.Context, dir, ref string, noFF bool) (conflict bool, err error) {
	args := []string{"merge", "--no-edit"}
	if noFF {
		args = append(args, "--no-ff")
	}
	args = append(args, ref)
	_, err = run(ctx, dir, args...)
	if err != nil {
		var ge *Error
		if isIdentityError(err, &ge) {
			_ = MergeAbort(ctx, dir) // clear any half-started merge state first
			withIdent := append([]string{
				"-c", "user.name=Workshop", "-c", "user.email=workshop@localhost",
			}, args...)
			_, err = run(ctx, dir, withIdent...)
		}
	}
	if err == nil {
		return false, nil
	}
	// Conflict signature: unmerged paths or MERGE_HEAD left behind.
	if HasMergeHead(ctx, dir) {
		return true, nil
	}
	if files, uerr := UnmergedFiles(ctx, dir); uerr == nil && len(files) > 0 {
		return true, nil
	}
	return false, err
}

// MergeAbort aborts an in-progress merge (no-op if none).
func MergeAbort(ctx context.Context, dir string) error {
	if !HasMergeHead(ctx, dir) {
		return nil
	}
	_, err := run(ctx, dir, "merge", "--abort")
	return err
}

// MergeContinueCommit commits a fully resolved in-progress merge.
func MergeContinueCommit(ctx context.Context, dir string) error {
	_, err := run(ctx, dir, "commit", "--no-edit")
	if err != nil {
		var ge *Error
		if isIdentityError(err, &ge) {
			_, err = run(ctx, dir,
				"-c", "user.name=Workshop", "-c", "user.email=workshop@localhost",
				"commit", "--no-edit")
		}
	}
	return err
}

// UnmergedFiles lists paths still in conflict.
func UnmergedFiles(ctx context.Context, dir string) ([]string, error) {
	out, err := run(ctx, dir, "diff", "--name-only", "--diff-filter=U")
	if err != nil {
		return nil, err
	}
	if out == "" {
		return nil, nil
	}
	return strings.Split(out, "\n"), nil
}

// ResetHard resets the work tree and HEAD to ref.
func ResetHard(ctx context.Context, dir, ref string) error {
	_, err := run(ctx, dir, "reset", "--hard", "-q", ref)
	return err
}

// UpdateRef points a fully-qualified ref (e.g. refs/workshop/green) at target.
func UpdateRef(ctx context.Context, dir, ref, target string) error {
	_, err := run(ctx, dir, "update-ref", ref, target)
	return err
}

// RefExists reports whether a ref resolves to a commit.
func RefExists(ctx context.Context, dir, ref string) bool {
	_, err := run(ctx, dir, "rev-parse", "--verify", "--quiet", ref+"^{commit}")
	return err == nil
}
