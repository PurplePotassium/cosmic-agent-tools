package gitx

import (
	"context"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
)

// Worktree is one entry of `git worktree list --porcelain`.
type Worktree struct {
	Path     string
	Head     string
	Branch   string // short name; "" when detached or bare
	Bare     bool
	Detached bool
}

// Worktrees lists all worktrees of the repo containing repoDir, parsed from
// the porcelain format (never the human format — path normalization on
// Windows bit the PowerShell predecessor).
func Worktrees(ctx context.Context, repoDir string) ([]Worktree, error) {
	out, err := runOut(ctx, repoDir, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	var list []Worktree
	var cur *Worktree
	flush := func() {
		if cur != nil {
			list = append(list, *cur)
			cur = nil
		}
	}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		switch {
		case strings.HasPrefix(line, "worktree "):
			flush()
			cur = &Worktree{Path: filepath.FromSlash(strings.TrimPrefix(line, "worktree "))}
		case cur == nil:
			// skip stray lines defensively
		case strings.HasPrefix(line, "HEAD "):
			cur.Head = strings.TrimPrefix(line, "HEAD ")
		case strings.HasPrefix(line, "branch "):
			cur.Branch = strings.TrimPrefix(strings.TrimPrefix(line, "branch "), "refs/heads/")
		case line == "bare":
			cur.Bare = true
		case line == "detached":
			cur.Detached = true
		}
	}
	flush()
	return list, nil
}

// FindWorktree returns the worktree checked out at path (normalized compare),
// or nil.
func FindWorktree(ctx context.Context, repoDir, path string) (*Worktree, error) {
	list, err := Worktrees(ctx, repoDir)
	if err != nil {
		return nil, err
	}
	want := normPath(path)
	for i := range list {
		if normPath(list[i].Path) == want {
			return &list[i], nil
		}
	}
	return nil, nil
}

func normPath(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		abs = p
	}
	norm := filepath.ToSlash(filepath.Clean(abs))
	// Case-fold only where the filesystem does: on a case-sensitive fs,
	// folding would match a DIFFERENT directory's worktree and adopt it.
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		norm = strings.ToLower(norm)
	}
	return norm
}

// AddWorktree creates (or adopts) a worktree at path on branch. If the branch
// already exists it is checked out there; otherwise it is created at base.
// Callers should PruneWorktrees first so stale admin data doesn't block the
// add. An existing worktree is adopted only when it has the expected branch
// checked out — a stale worktree from a different branch_prefix would
// otherwise commit to the wrong branch while the integrator watches the
// right one.
func AddWorktree(ctx context.Context, repoDir, path, branch, base string) error {
	if err := rejectFlagLike(path, branch, base); err != nil {
		return err
	}
	if wt, err := FindWorktree(ctx, repoDir, path); err != nil {
		return err
	} else if wt != nil {
		if wt.Branch != branch {
			return fmt.Errorf("gitx: worktree %s has branch %q checked out, expected %q — remove the directory (or fix git.branch_prefix) and retry", path, wt.Branch, branch)
		}
		return nil // already present — adopt
	}
	if BranchExists(ctx, repoDir, branch) {
		_, err := run(ctx, repoDir, "worktree", "add", path, branch)
		return err
	}
	_, err := run(ctx, repoDir, "worktree", "add", "-b", branch, path, base)
	return err
}

// PruneWorktrees drops stale worktree admin data (deleted dirs, etc.).
func PruneWorktrees(ctx context.Context, repoDir string) error {
	_, err := run(ctx, repoDir, "worktree", "prune")
	return err
}

// RemoveWorktree removes a worktree; force discards local changes.
func RemoveWorktree(ctx context.Context, repoDir, path string, force bool) error {
	args := []string{"worktree", "remove"}
	if force {
		args = append(args, "--force")
	}
	args = append(args, path)
	_, err := run(ctx, repoDir, args...)
	return err
}
