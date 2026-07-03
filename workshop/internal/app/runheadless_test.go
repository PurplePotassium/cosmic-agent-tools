package app

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/bus"
	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/config"
	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/engine"
	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/store"
)

// initRepo makes a temp git repo with an initial commit on main.
func initRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	repo := t.TempDir()
	git := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q", "-b", "main")
	git("config", "user.name", "Test")
	git("config", "user.email", "test@example.com")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", "-A")
	git("commit", "-q", "-m", "initial")
	return repo
}

// newTestApp builds an App directly (bypassing Open) so the test never reads
// the real user-global config or state dir — everything is temp.
func newTestApp(t *testing.T, repo string) *App {
	t.Helper()
	stateDir := t.TempDir()
	st, err := store.Open(filepath.Join(stateDir, "workshop.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return &App{
		RepoDir:  repo,
		StateDir: stateDir,
		Res:      &config.Result{Config: config.Default()},
		Store:    st,
		Bus:      bus.New(st),
	}
}

// TestRunHeadlessStartStoppedParksPipelines pins the `up` default: seeding the
// operator halt before any worker runs makes a bounded run return ErrHalted
// immediately and leaves the implicit pipeline in the operator-stopped state
// the dashboard's resume button clears. No agent is ever invoked (the worker
// parks on its first pass), so the default "claude" driver needs nothing on
// PATH.
func TestRunHeadlessStartStoppedParksPipelines(t *testing.T) {
	a := newTestApp(t, initRepo(t))
	ctx := context.Background()

	err := a.RunHeadless(ctx, 1, false, true)
	if !errors.Is(err, engine.ErrHalted) {
		t.Fatalf("RunHeadless(startStopped=true) err = %v, want ErrHalted", err)
	}
	reason, err := a.Store.HaltedReason(ctx, config.DefaultPipelineName)
	if err != nil {
		t.Fatal(err)
	}
	if reason != "operator" {
		t.Fatalf("halt reason = %q, want %q", reason, "operator")
	}
}
