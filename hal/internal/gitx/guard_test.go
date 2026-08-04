package gitx

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"testing"
)

func guardRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dir := t.TempDir()
	mustGit(t, dir, "init", "-q", "-b", "main")
	mustGit(t, dir, "config", "user.name", "Test")
	mustGit(t, dir, "config", "user.email", "t@e.st")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGit(t, dir, "add", "-A")
	mustGit(t, dir, "commit", "-q", "-m", "initial")
	return dir
}

// Ref/branch names that parse as git options must be rejected, never passed
// through: "checkout --detach" silently detaches HEAD, "reset --keep"
// silently changes reset semantics.
func TestFlagLikeNamesRejected(t *testing.T) {
	ctx := context.Background()
	dir := guardRepo(t)

	if err := CheckoutBranch(ctx, dir, "--detach"); err == nil {
		t.Fatal("checkout --detach accepted")
	}
	if br, err := CurrentBranch(ctx, dir); err != nil || br != "main" {
		t.Fatalf("HEAD detached by rejected checkout: %q %v", br, err)
	}
	if err := ResetHard(ctx, dir, "--keep"); err == nil {
		t.Fatal("reset --keep accepted")
	}
	if _, err := RevParse(ctx, dir, "--verify"); err == nil {
		t.Fatal("rev-parse --verify accepted")
	}
	if _, err := Merge(ctx, dir, "--squash", false); err == nil {
		t.Fatal("merge --squash accepted")
	}
	if err := CreateBranch(ctx, dir, "-b", "main"); err == nil {
		t.Fatal("branch -b accepted")
	}
	if err := DeleteBranch(ctx, dir, "--all", true); err == nil {
		t.Fatal("branch -D --all accepted")
	}
	if err := AddWorktree(ctx, dir, "--force", "x", "main"); err == nil {
		t.Fatal("worktree add --force accepted")
	}
}

// RevParse output must be a clean SHA even when git writes warnings on a
// SUCCESSFUL exit — an ambiguous refname warning on stderr must not pollute
// the parsed value (it used to, via CombinedOutput).
func TestRevParseIgnoresStderrWarnings(t *testing.T) {
	ctx := context.Background()
	dir := guardRepo(t)
	// A branch AND a tag named "x": rev-parse x succeeds with a warning.
	mustGit(t, dir, "branch", "x")
	mustGit(t, dir, "tag", "x")

	sha, err := RevParse(ctx, dir, "x")
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^[0-9a-f]{40,64}$`).MatchString(sha) {
		t.Fatalf("polluted rev-parse output: %q", sha)
	}
}

// Non-ASCII paths must come back verbatim (stat-able), not C-quoted octal.
func TestStatusPorcelainNonASCIIPaths(t *testing.T) {
	ctx := context.Background()
	dir := guardRepo(t)
	name := "über-art.md"
	if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	paths, err := StatusPorcelain(ctx, dir)
	if err != nil || len(paths) != 1 {
		t.Fatalf("paths=%v err=%v", paths, err)
	}
	if paths[0] != name {
		t.Fatalf("got %q, want %q", paths[0], name)
	}
	if _, err := os.Stat(filepath.Join(dir, paths[0])); err != nil {
		t.Fatalf("returned path does not exist on disk: %v", err)
	}
}

func TestCommitInfo(t *testing.T) {
	ctx := context.Background()
	dir := guardRepo(t)

	parents, subject, err := CommitInfo(ctx, dir, "HEAD")
	if err != nil || len(parents) != 0 || subject != "initial" {
		t.Fatalf("root commit: parents=%v subject=%q err=%v", parents, subject, err)
	}

	mustGit(t, dir, "checkout", "-q", "-b", "feat")
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte("b\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGit(t, dir, "add", "-A")
	mustGit(t, dir, "commit", "-q", "-m", "feat work")
	mustGit(t, dir, "checkout", "-q", "main")
	mustGit(t, dir, "merge", "--no-ff", "-q", "--no-edit", "feat")

	parents, subject, err = CommitInfo(ctx, dir, "HEAD")
	if err != nil || len(parents) != 2 {
		t.Fatalf("merge commit: parents=%v err=%v", parents, err)
	}
	if got := mergeSubject(subject); got != "feat" {
		t.Fatalf("merge subject %q parsed branch %q", subject, got)
	}
}

// mergeSubject mirrors the integrator's subject parsing for the test.
func mergeSubject(subject string) string {
	re := regexp.MustCompile(`^Merge branch '([^']+)'`)
	m := re.FindStringSubmatch(subject)
	if m == nil {
		return ""
	}
	return m[1]
}

// Worktree path comparison must not case-fold on case-sensitive filesystems:
// folding would adopt a DIFFERENT directory's worktree.
func TestNormPathCaseSensitivity(t *testing.T) {
	a, b := normPath("/tmp/Lane1"), normPath("/tmp/lane1")
	switch runtime.GOOS {
	case "windows", "darwin":
		if a != b {
			t.Fatalf("case-insensitive fs must fold: %q vs %q", a, b)
		}
	default:
		if a == b {
			t.Fatalf("case-sensitive fs must not fold: %q", a)
		}
	}
}
