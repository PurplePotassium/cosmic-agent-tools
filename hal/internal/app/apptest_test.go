package app

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/PurplePotassium/cosmic-agent-tools/hal/internal/bus"
	"github.com/PurplePotassium/cosmic-agent-tools/hal/internal/config"
	"github.com/PurplePotassium/cosmic-agent-tools/hal/internal/store"
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
	st, err := store.Open(filepath.Join(stateDir, "hal.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return New(repo, stateDir, &config.Result{Config: config.Default()}, st, bus.New(st))
}
