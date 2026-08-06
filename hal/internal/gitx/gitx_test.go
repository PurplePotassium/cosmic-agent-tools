package gitx

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func mustGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := run(context.Background(), dir, args...)
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return out
}

func initRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dir := t.TempDir()
	mustGit(t, dir, "init", "-q", "-b", "main")
	mustGit(t, dir, "config", "user.name", "Test")
	mustGit(t, dir, "config", "user.email", "test@example.com")
	writeAndCommit(t, dir, "README.md", "hello\n", "initial")
	return dir
}

func writeAndCommit(t *testing.T, dir, name, content, subject string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGit(t, dir, "add", "-A")
	mustGit(t, dir, "commit", "-q", "-m", subject)
}

func TestCommitAllWithTrailers(t *testing.T) {
	ctx := context.Background()
	dir := initRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	msg := BuildCommitMessage("ws(main) iter 1 [claude]", "Fixed the thing.\n\nDecisions: kept it simple.", [][2]string{
		{"Hal-Task", "ws-123"}, {"Hal-Pass", "7"},
	})
	sha, err := CommitAll(ctx, dir, msg)
	if err != nil {
		t.Fatal(err)
	}
	if len(sha) < 6 {
		t.Fatalf("short sha: %q", sha)
	}
	body := mustGit(t, dir, "log", "-1", "--format=%B")
	if !strings.Contains(body, "Hal-Task: ws-123") || !strings.Contains(body, "Hal-Pass: 7") {
		t.Fatalf("trailers missing:\n%s", body)
	}
	trailerOut := mustGit(t, dir, "log", "-1", "--format=%(trailers:key=Hal-Task,valueonly)")
	if strings.TrimSpace(trailerOut) != "ws-123" {
		t.Fatalf("trailer parse: %q", trailerOut)
	}
	commits, err := RecentCommits(ctx, dir, 5)
	if err != nil || len(commits) != 2 || commits[0].Subject != "ws(main) iter 1 [claude]" {
		t.Fatalf("commits: %+v, %v", commits, err)
	}
}

func TestBranchExistsErr(t *testing.T) {
	ctx := context.Background()
	dir := initRepo(t)

	// Present and absent both resolve cleanly (nil error); only the boolean
	// differs. This is the distinction BranchExists collapses.
	if ok, err := BranchExistsErr(ctx, dir, "main"); !ok || err != nil {
		t.Fatalf("present branch: ok=%v err=%v", ok, err)
	}
	if ok, err := BranchExistsErr(ctx, dir, "nope"); ok || err != nil {
		t.Fatalf("absent branch: ok=%v err=%v", ok, err)
	}

	// A git error (here: not a repo) must surface as an error, not as a false
	// "branch absent" — the engine relies on this to retry through a transient
	// Windows .git grip instead of taking itself down.
	nonRepo := t.TempDir()
	if ok, err := BranchExistsErr(ctx, nonRepo, "main"); ok || err == nil {
		t.Fatalf("git error should not read as absent: ok=%v err=%v", ok, err)
	}
}

func TestMergeConflictAndClean(t *testing.T) {
	ctx := context.Background()
	dir := initRepo(t)
	mustGit(t, dir, "branch", "feat")
	writeAndCommit(t, dir, "README.md", "main version\n", "main edit")

	mustGit(t, dir, "checkout", "-q", "feat")
	writeAndCommit(t, dir, "README.md", "feat version\n", "feat edit")
	mustGit(t, dir, "checkout", "-q", "main")

	conflict, err := Merge(ctx, dir, "feat", true)
	if err != nil {
		t.Fatal(err)
	}
	if !conflict {
		t.Fatal("expected conflict")
	}
	if !HasMergeHead(ctx, dir) {
		t.Fatal("MERGE_HEAD should exist mid-conflict")
	}
	files, err := UnmergedFiles(ctx, dir)
	if err != nil || len(files) != 1 || files[0] != "README.md" {
		t.Fatalf("unmerged: %v, %v", files, err)
	}
	if err := MergeAbort(ctx, dir); err != nil {
		t.Fatal(err)
	}
	if HasMergeHead(ctx, dir) {
		t.Fatal("MERGE_HEAD survived abort")
	}

	// A disjoint change on a fresh branch merges cleanly.
	mustGit(t, dir, "checkout", "-q", "-b", "feat2", "main")
	writeAndCommit(t, dir, "other.txt", "disjoint\n", "feat2 other")
	mustGit(t, dir, "checkout", "-q", "main")
	preSHA := mustGit(t, dir, "rev-parse", "HEAD")
	conflict, err = Merge(ctx, dir, "feat2", true)
	if err != nil || conflict {
		t.Fatalf("clean merge: conflict=%v err=%v", conflict, err)
	}
	// --no-ff means one merge commit we can peel with reset --hard HEAD^.
	if err := ResetHard(ctx, dir, "HEAD^"); err != nil {
		t.Fatal(err)
	}
	if got := mustGit(t, dir, "rev-parse", "HEAD"); got != preSHA {
		t.Fatalf("peel failed: %s vs %s", got, preSHA)
	}
}

func TestWorktreeLifecycle(t *testing.T) {
	ctx := context.Background()
	dir := initRepo(t)
	wtPath := dir + "-wt-code"
	t.Cleanup(func() { os.RemoveAll(wtPath) })

	if err := AddWorktree(ctx, dir, wtPath, "hal/code", "main"); err != nil {
		t.Fatal(err)
	}
	// Adding again adopts silently.
	if err := AddWorktree(ctx, dir, wtPath, "hal/code", "main"); err != nil {
		t.Fatalf("adopt: %v", err)
	}
	wt, err := FindWorktree(ctx, dir, wtPath)
	if err != nil || wt == nil || wt.Branch != "hal/code" {
		t.Fatalf("find: %+v, %v", wt, err)
	}
	list, err := Worktrees(ctx, dir)
	if err != nil || len(list) != 2 {
		t.Fatalf("list: %+v, %v", list, err)
	}

	// The worktree commits independently; main repo sees the branch advance.
	if err := os.WriteFile(filepath.Join(wtPath, "wt.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := CommitAll(ctx, wtPath, "ws(code) iter 1 [claude]"); err != nil {
		t.Fatal(err)
	}
	n, err := AheadCount(ctx, dir, "main", "hal/code")
	if err != nil || n != 1 {
		t.Fatalf("ahead: %d, %v", n, err)
	}

	if err := RemoveWorktree(ctx, dir, wtPath, false); err != nil {
		t.Fatal(err)
	}
	if wt, _ := FindWorktree(ctx, dir, wtPath); wt != nil {
		t.Fatal("worktree survived remove")
	}

	// Deleting the dir behind git's back then pruning must recover.
	wtPath2 := dir + "-wt-art"
	t.Cleanup(func() { os.RemoveAll(wtPath2) })
	if err := AddWorktree(ctx, dir, wtPath2, "hal/art", "main"); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(wtPath2); err != nil {
		t.Fatal(err)
	}
	if err := PruneWorktrees(ctx, dir); err != nil {
		t.Fatal(err)
	}
	if err := AddWorktree(ctx, dir, wtPath2, "hal/art", "main"); err != nil {
		t.Fatalf("re-add after prune: %v", err)
	}
}

func TestCleanStaleLock(t *testing.T) {
	ctx := context.Background()
	dir := initRepo(t)
	gd, err := GitDir(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	lock := filepath.Join(gd, "index.lock")
	if err := os.WriteFile(lock, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	// Fresh lock: left alone.
	removed, err := CleanStaleLock(ctx, dir, time.Minute)
	if err != nil || removed {
		t.Fatalf("fresh lock removed=%v err=%v", removed, err)
	}

	// Backdate it: removed.
	old := time.Now().Add(-2 * time.Minute)
	if err := os.Chtimes(lock, old, old); err != nil {
		t.Fatal(err)
	}
	removed, err = CleanStaleLock(ctx, dir, time.Minute)
	if err != nil || !removed {
		t.Fatalf("stale lock removed=%v err=%v", removed, err)
	}
	if _, err := os.Stat(lock); !os.IsNotExist(err) {
		t.Fatal("lock still present")
	}
}
