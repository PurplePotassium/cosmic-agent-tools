//go:build e2e

// Package e2e drives the REAL workshop binary end-to-end: TestMain builds
// cmd/workshop into a temp dir, each test scaffolds a throwaway git repo with
// a checked-in .workshop/config.toml routing to the fake driver, seeds the
// backlog through the CLI, runs a bounded `workshop run`, and asserts on the
// repo's git history and `workshop status --json`. This is the gate's proof
// that the orchestrator itself — not just its packages — still works.
//
// Everything is hermetic: WORKSHOP_STATE_DIR, the user-global config dir, and
// git's global/system config are all pointed at temp locations, so these
// tests never touch real workshop state and are safe to run while another
// workshop instance is live on this machine.
//
// Run with: go test -tags e2e ./e2e
package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// exePath is the workshop binary under test, built once in TestMain.
var exePath string

func TestMain(m *testing.M) {
	if _, err := exec.LookPath("git"); err != nil {
		fmt.Fprintln(os.Stderr, "e2e: git not on PATH, skipping")
		os.Exit(0)
	}
	tmp, err := os.MkdirTemp("", "workshop-e2e-*")
	if err != nil {
		fmt.Fprintln(os.Stderr, "e2e:", err)
		os.Exit(1)
	}
	exePath = filepath.Join(tmp, "workshop")
	if runtime.GOOS == "windows" {
		exePath += ".exe"
	}
	build := exec.Command("go", "build", "-o", exePath, "./cmd/workshop")
	build.Dir = ".." // module root
	if out, err := build.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "e2e: go build: %v\n%s", err, out)
		os.RemoveAll(tmp)
		os.Exit(1)
	}
	code := m.Run()
	os.RemoveAll(tmp)
	os.Exit(code)
}

// rig is one scaffolded target repo plus the hermetic environment the binary
// runs under.
type rig struct {
	t    *testing.T
	repo string
	env  []string
}

// newRig creates <tempdir>/repo (a git repo on main with .workshop/ committed)
// and the isolated env. The repo lives one level down so worktree lanes
// (<repo>-wt-<name>) stay inside the temp dir and get cleaned up with it.
func newRig(t *testing.T, configToml string) *rig {
	t.Helper()
	parent := t.TempDir()
	repo := filepath.Join(parent, "repo")
	if err := os.MkdirAll(filepath.Join(repo, ".workshop"), 0o755); err != nil {
		t.Fatal(err)
	}

	// Hermetic git identity: global config is a temp file, system config is
	// dev-null, so machine-level settings (signing, hooks) can't leak in.
	gitGlobal := filepath.Join(parent, "gitconfig")
	if err := os.WriteFile(gitGlobal, []byte("[user]\n\tname = E2E\n\temail = e2e@example.com\n[init]\n\tdefaultBranch = main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stateRoot := filepath.Join(parent, "state")
	userCfg := filepath.Join(parent, "usercfg") // empty: no user-global workshop config
	if err := os.MkdirAll(userCfg, 0o755); err != nil {
		t.Fatal(err)
	}
	scenario := filepath.Join(parent, "scenario.json")
	if err := os.WriteFile(scenario, []byte(`{"behavior":"happy"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	env := append(os.Environ(),
		"WORKSHOP_STATE_DIR="+stateRoot,
		"WORKSHOP_FAKE_BIN="+exePath,
		"WORKSHOP_FAKE_SCENARIO="+scenario,
		"GIT_CONFIG_GLOBAL="+gitGlobal,
		"GIT_CONFIG_SYSTEM="+os.DevNull,
		// os.UserConfigDir: APPDATA on Windows, XDG on Unix — isolate both.
		"APPDATA="+userCfg,
		"XDG_CONFIG_HOME="+userCfg,
	)
	r := &rig{t: t, repo: repo, env: env}

	if err := os.WriteFile(filepath.Join(repo, ".workshop", "config.toml"), []byte(configToml), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".workshop", "GOAL.md"), []byte("E2E scripted goal.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("e2e target\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r.git("init", "-q", "-b", "main")
	r.git("add", "-A")
	r.git("commit", "-q", "-m", "initial")
	return r
}

// workshop runs the built binary in the repo and returns combined output +
// exit code. A run that outlives the deadline is a test failure, not a hang.
func (r *rig) workshop(timeout time.Duration, args ...string) (string, int) {
	r.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, exePath, args...)
	cmd.Dir = r.repo
	cmd.Env = r.env
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		r.t.Fatalf("workshop %v timed out after %v\n%s", args, timeout, out)
	}
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		r.t.Fatalf("workshop %v: %v\n%s", args, err, out)
	}
	return string(out), code
}

// mustWorkshop asserts exit 0.
func (r *rig) mustWorkshop(timeout time.Duration, args ...string) string {
	r.t.Helper()
	out, code := r.workshop(timeout, args...)
	if code != 0 {
		r.t.Fatalf("workshop %v exited %d:\n%s", args, code, out)
	}
	return out
}

func (r *rig) git(args ...string) string {
	r.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = r.repo
	cmd.Env = r.env
	out, err := cmd.CombinedOutput()
	if err != nil {
		r.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

// status is the subset of `workshop status --json` the tests assert on.
type status struct {
	SharedBacklog int `json:"sharedBacklog"`
	Pipelines     []struct {
		Name             string `json:"name"`
		Halted           string `json:"halted"`
		BacklogExclusive int    `json:"backlogExclusive"`
	} `json:"pipelines"`
	Completions []struct {
		Title string `json:"title"`
	} `json:"completions"`
}

func (r *rig) status() status {
	r.t.Helper()
	out := r.mustWorkshop(time.Minute, "status", "--json")
	// The CLI wraps the snapshot: {"serverRunning": bool, "status": {...}}.
	var wrapper struct {
		Status status `json:"status"`
	}
	if err := json.Unmarshal([]byte(out), &wrapper); err != nil {
		r.t.Fatalf("status --json: %v\n%s", err, out)
	}
	return wrapper.Status
}

// TestSinglePipelineDrainsBacklog: the flagship loop. Two tasks queued via the
// CLI, a bounded run drains them into two verified commits on main, and the
// status snapshot agrees.
func TestSinglePipelineDrainsBacklog(t *testing.T) {
	r := newRig(t, `
[project]
name   = "e2e-single"
trunk  = "main"
verify = "git log -1 --oneline"

[safety]
breaker_failures = 2

[[pipelines]]
name  = "main"
agent = "fake"
`)
	r.mustWorkshop(time.Minute, "task", "add", "first e2e task")
	r.mustWorkshop(time.Minute, "task", "add", "second e2e task")

	r.mustWorkshop(3*time.Minute, "run", "--iterations", "2", "--until-drained", "--timeout", "2m")

	subjects := strings.Split(strings.TrimSpace(r.git("log", "--format=%s")), "\n")
	if len(subjects) != 3 { // initial + one commit per pass
		t.Fatalf("commit subjects: %v", subjects)
	}
	for _, s := range subjects[:2] {
		if !strings.HasPrefix(s, "ws(main) iter ") {
			t.Fatalf("unexpected pass commit subject %q (all: %v)", s, subjects)
		}
	}
	if _, err := os.Stat(filepath.Join(r.repo, "fake-repo.txt")); err != nil {
		t.Fatalf("fake agent's edit missing from the tree: %v", err)
	}

	st := r.status()
	if st.SharedBacklog != 0 {
		t.Fatalf("backlog not drained: %+v", st)
	}
	if len(st.Completions) != 2 {
		t.Fatalf("completions: %+v", st.Completions)
	}
	for _, p := range st.Pipelines {
		if p.Halted != "" {
			t.Fatalf("pipeline %s halted %q", p.Name, p.Halted)
		}
	}
}

// TestTwoPipelinesLandThroughMergeQueue: the riskiest orchestrator surface —
// two lanes in sibling worktrees, each does one pass on its own branch, and
// the gated integrator lands both on trunk via --no-ff merges.
func TestTwoPipelinesLandThroughMergeQueue(t *testing.T) {
	r := newRig(t, `
[project]
name   = "e2e-multi"
trunk  = "main"
verify = "git log -1 --oneline"

[git]
worktrees = "on"

[safety]
breaker_failures = 2

[[pipelines]]
name  = "alpha"
agent = "fake"

[[pipelines]]
name  = "beta"
agent = "fake"
`)
	r.mustWorkshop(time.Minute, "task", "add", "alpha lane work", "--backlog", "alpha")
	r.mustWorkshop(time.Minute, "task", "add", "beta lane work", "--backlog", "beta")

	r.mustWorkshop(4*time.Minute, "run", "--iterations", "1", "--until-drained", "--timeout", "3m")

	// Both lanes' edits reached trunk. The fake agent writes
	// fake-<basename(workdir)>.txt, and lanes work in <repo>-wt-<name>.
	tree := r.git("ls-tree", "-r", "--name-only", "main")
	for _, want := range []string{"fake-repo-wt-alpha.txt", "fake-repo-wt-beta.txt"} {
		if !strings.Contains(tree, want) {
			t.Fatalf("%s not on trunk; tree:\n%s", want, tree)
		}
	}
	merges := strings.Fields(strings.TrimSpace(r.git("log", "--merges", "--format=%H")))
	if len(merges) != 2 {
		t.Fatalf("expected 2 --no-ff merge commits on trunk, got %d\n%s",
			len(merges), r.git("log", "--graph", "--oneline"))
	}

	st := r.status()
	if st.SharedBacklog != 0 {
		t.Fatalf("shared backlog not empty: %+v", st)
	}
	for _, p := range st.Pipelines {
		if p.BacklogExclusive != 0 {
			t.Fatalf("lane %s backlog not drained: %+v", p.Name, st)
		}
		if p.Halted != "" {
			t.Fatalf("pipeline %s halted %q", p.Name, p.Halted)
		}
	}
	if len(st.Completions) != 2 {
		t.Fatalf("completions: %+v", st.Completions)
	}
}
