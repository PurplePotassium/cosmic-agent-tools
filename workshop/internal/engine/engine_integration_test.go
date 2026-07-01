package engine

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/events"
	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/gitx"
	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/store"
)

// TestMain lets this test binary double as a fake coding agent: when the engine spawns
// "claude" with WORKSHOP_CLAUDE_BIN pointed at os.Args[0] and WORKSHOP_FAKE_AGENT=1 in
// the environment, the child re-enters here, makes a change in its cwd, and exits 0.
func TestMain(m *testing.M) {
	if os.Getenv("WORKSHOP_FAKE_AGENT") == "1" {
		runFakeAgent()
		return
	}
	os.Exit(m.Run())
}

func runFakeAgent() {
	// Drain the prompt on stdin (claude mode) so the parent's copy completes cleanly.
	_, _ = io.Copy(io.Discard, os.Stdin)
	// Make a unique change in the working directory so the loop has something to commit.
	name := fmt.Sprintf("agent-change-%d.txt", time.Now().UnixNano())
	_ = os.WriteFile(name, []byte("work\n"), 0o644)
	os.Exit(0)
}

// TestBoundedRunCommits is the end-to-end smoke test: a bounded run drives the fake
// agent for 2 passes and lands 2 "ralph iter" commits in the target repo — the
// cross-platform equivalent of the plan's `--iterations 2` verification.
func TestBoundedRunCommits(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	repo := filepath.Join(dir, "repo")
	state := filepath.Join(dir, "state")
	mustMkdir(t, repo)
	mustMkdir(t, filepath.Join(state, "logs"))

	// init a repo with an identity + an initial commit
	mustGit(t, repo, "init")
	mustGit(t, repo, "config", "user.email", "t@example.com")
	mustGit(t, repo, "config", "user.name", "tester")
	mustGit(t, repo, "config", "commit.gpgsign", "false")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGit(t, repo, "add", "-A")
	mustGit(t, repo, "commit", "-m", "init")

	if err := os.WriteFile(filepath.Join(state, "PROMPT.md"), []byte("do one increment"), 0o644); err != nil {
		t.Fatal(err)
	}

	st, err := store.Open(filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	p := &store.Project{
		ID: "p", Name: "repo", RepoPath: repo, StateDir: state,
		Agent: "claude", Model: "claude-sonnet-4-6", Status: store.StatusStopped,
	}
	if err := st.CreateProject(p); err != nil {
		t.Fatal(err)
	}

	// Point the engine's "claude" at THIS test binary in fake-agent mode.
	t.Setenv("WORKSHOP_CLAUDE_BIN", os.Args[0])
	t.Setenv("WORKSHOP_FAKE_AGENT", "1")

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	eng := New(st, events.New(), log, p, Config{Random: false, SkipPermissions: false, Iterations: 2}, nil, nil)
	if err := eng.Run(context.Background()); err != nil {
		t.Fatalf("engine run: %v", err)
	}

	out, _ := gitx.Run(repo, "log", "--format=%s")
	if n := strings.Count(out, "ralph iter"); n < 2 {
		t.Fatalf("expected >=2 'ralph iter' commits, got %d:\n%s", n, out)
	}
	// The engine should have recorded two ok iterations.
	its, _ := st.ListIterations("p", 10)
	okCount := 0
	for _, it := range its {
		if it.Status == "ok" {
			okCount++
		}
	}
	if okCount < 2 {
		t.Fatalf("expected 2 ok iterations recorded, got %d", okCount)
	}
}

func mustMkdir(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
}

func mustGit(t *testing.T, repo string, args ...string) {
	t.Helper()
	if out, err := gitx.Run(repo, args...); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}
