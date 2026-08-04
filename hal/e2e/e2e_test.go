//go:build e2e

// Package e2e drives the REAL hal binary end-to-end: TestMain builds
// cmd/hal into a temp dir, each test scaffolds a throwaway git repo with
// a checked-in .hal/config.toml, exercises the CLI/server, and asserts
// on the results. This is the gate's proof that the orchestrator itself —
// not just its packages — still works.
//
// Everything is hermetic: HAL_STATE_DIR, the user-global config dir, and
// git's global/system config are all pointed at temp locations, so these
// tests never touch real hal state and are safe to run while another
// hal instance is live on this machine.
//
// The autonomous pass loop (and its run/invent/merge-queue tests) was
// removed; the workflow engine's own e2e coverage is a later phase. What
// remains here is the infrastructure: CLI surfaces, server boot/auth, stop.
//
// Run with: go test -tags e2e ./e2e
package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// exePath is the hal binary under test, built once in TestMain.
var exePath string

func TestMain(m *testing.M) {
	if _, err := exec.LookPath("git"); err != nil {
		fmt.Fprintln(os.Stderr, "e2e: git not on PATH, skipping")
		os.Exit(0)
	}
	tmp, err := os.MkdirTemp("", "hal-e2e-*")
	if err != nil {
		fmt.Fprintln(os.Stderr, "e2e:", err)
		os.Exit(1)
	}
	exePath = filepath.Join(tmp, "hal")
	if runtime.GOOS == "windows" {
		exePath += ".exe"
	}
	build := exec.Command("go", "build", "-o", exePath, "./cmd/hal")
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

// newRig creates <tempdir>/repo (a git repo on main with .hal/ committed)
// and the isolated env.
func newRig(t *testing.T, configToml string) *rig {
	t.Helper()
	parent := t.TempDir()
	repo := filepath.Join(parent, "repo")
	if err := os.MkdirAll(filepath.Join(repo, ".hal"), 0o755); err != nil {
		t.Fatal(err)
	}

	// Hermetic git identity: global config is a temp file, system config is
	// dev-null, so machine-level settings (signing, hooks) can't leak in.
	gitGlobal := filepath.Join(parent, "gitconfig")
	if err := os.WriteFile(gitGlobal, []byte("[user]\n\tname = E2E\n\temail = e2e@example.com\n[init]\n\tdefaultBranch = main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stateRoot := filepath.Join(parent, "state")
	userCfg := filepath.Join(parent, "usercfg") // empty: no user-global hal config
	if err := os.MkdirAll(userCfg, 0o755); err != nil {
		t.Fatal(err)
	}
	scenario := filepath.Join(parent, "scenario.json")
	if err := os.WriteFile(scenario, []byte(`{"behavior":"happy"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	env := append(os.Environ(),
		"HAL_STATE_DIR="+stateRoot,
		"HAL_FAKE_BIN="+exePath,
		"HAL_FAKE_SCENARIO="+scenario,
		"GIT_CONFIG_GLOBAL="+gitGlobal,
		"GIT_CONFIG_SYSTEM="+os.DevNull,
		// os.UserConfigDir: APPDATA on Windows, XDG on Unix — isolate both.
		"APPDATA="+userCfg,
		"XDG_CONFIG_HOME="+userCfg,
	)
	r := &rig{t: t, repo: repo, env: env}

	if err := os.WriteFile(filepath.Join(repo, ".hal", "config.toml"), []byte(configToml), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".hal", "GOAL.md"), []byte("E2E scripted goal.\n"), 0o644); err != nil {
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

// hal runs the built binary in the repo and returns combined output +
// exit code. A run that outlives the deadline is a test failure, not a hang.
func (r *rig) hal(timeout time.Duration, args ...string) (string, int) {
	r.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, exePath, args...)
	cmd.Dir = r.repo
	cmd.Env = r.env
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		r.t.Fatalf("hal %v timed out after %v\n%s", args, timeout, out)
	}
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		r.t.Fatalf("hal %v: %v\n%s", args, err, out)
	}
	return string(out), code
}

// mustHal asserts exit 0.
func (r *rig) mustHal(timeout time.Duration, args ...string) string {
	r.t.Helper()
	out, code := r.hal(timeout, args...)
	if code != 0 {
		r.t.Fatalf("hal %v exited %d:\n%s", args, code, out)
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

// status is the subset of `hal status --json` the tests assert on.
type status struct {
	OpenTasks   int `json:"openTasks"`
	Completions []struct {
		Title string `json:"title"`
	} `json:"completions"`
}

func (r *rig) status() status {
	r.t.Helper()
	out := r.mustHal(time.Minute, "status", "--json")
	// The CLI wraps the snapshot: {"serverRunning": bool, "status": {...}}.
	var wrapper struct {
		Status status `json:"status"`
	}
	if err := json.Unmarshal([]byte(out), &wrapper); err != nil {
		r.t.Fatalf("status --json: %v\n%s", err, out)
	}
	return wrapper.Status
}

// TestTaskInboxRoundTrip: the ideas inbox via the real CLI — add, list,
// status count, rm.
func TestTaskInboxRoundTrip(t *testing.T) {
	r := newRig(t, `
[project]
name  = "e2e-inbox"
trunk = "main"
`)
	r.mustHal(time.Minute, "task", "add", "first idea", "--detail", "some detail")
	r.mustHal(time.Minute, "task", "add", "second idea")

	if st := r.status(); st.OpenTasks != 2 {
		t.Fatalf("openTasks = %d, want 2", st.OpenTasks)
	}
	list := r.mustHal(time.Minute, "task", "list")
	if !strings.Contains(list, "first idea") || !strings.Contains(list, "second idea") {
		t.Fatalf("task list missing entries:\n%s", list)
	}
	// rm the first id off the listing.
	id := ""
	for _, line := range strings.Split(list, "\n") {
		fields := strings.Fields(line)
		if len(fields) > 0 && strings.HasPrefix(fields[0], "ws-") {
			id = fields[0]
			break
		}
	}
	if id == "" {
		t.Fatalf("no task id in listing:\n%s", list)
	}
	r.mustHal(time.Minute, "task", "rm", id)
	if st := r.status(); st.OpenTasks != 1 {
		t.Fatalf("openTasks after rm = %d, want 1", st.OpenTasks)
	}
}

// TestDeprecatedConfigStillBoots: a pass-loop era config file must not brick
// the CLI — it loads with deprecation warnings on stderr and the commands
// still work.
func TestDeprecatedConfigStillBoots(t *testing.T) {
	r := newRig(t, `
[project]
name  = "e2e-legacy"
trunk = "main"

[safety]
breaker_failures = 2

[[pipelines]]
name  = "main"
agent = "fake"

[spice]
personas = "gamedev"
`)
	out := r.mustHal(time.Minute, "status", "--json")
	if !strings.Contains(out, "openTasks") {
		t.Fatalf("status did not produce the new snapshot:\n%s", out)
	}
	// Warnings ride stderr of any command; task add prints them too.
	warnOut := r.mustHal(time.Minute, "task", "add", "still works")
	joined := out + warnOut
	if !strings.Contains(joined, "[[pipelines]] is ignored") {
		t.Fatalf("no [[pipelines]] deprecation warning surfaced:\n%s", joined)
	}
}

// TestBugReport: `hal bug "<desc>"` produces one self-contained report of
// the live state (description, environment, git HEAD, config, status) and saves
// a copy to the state dir — the whole point being it needs no follow-up
// questions to hand to another agent.
func TestBugReport(t *testing.T) {
	r := newRig(t, `
[project]
name   = "e2e-bug"
trunk  = "main"
verify = "git log -1 --oneline"
`)
	r.mustHal(time.Minute, "task", "add", "a queued task")

	const desc = "engine wedges on ctrl-c"
	out := r.mustHal(time.Minute, "bug", desc)
	for _, want := range []string{
		"# Hal bug report", desc,
		"## Environment", "## Repository", "## Config",
		"e2e-bug",  // the scaffolded project name, from the embedded config
		"saved to", // the on-disk copy the operator can attach
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("bug report missing %q\n%s", want, out)
		}
	}

	// --json is machine-readable and carries the operator's description through
	// verbatim, and never the running server's token (redacted by design).
	jsonOut := r.mustHal(time.Minute, "bug", desc, "--json")
	var rep struct {
		Description string `json:"description"`
		Repo        struct {
			Dir  string `json:"dir"`
			Head string `json:"head"`
		} `json:"repo"`
	}
	if err := json.Unmarshal([]byte(jsonOut), &rep); err != nil {
		t.Fatalf("bug --json did not parse: %v\n%s", err, jsonOut)
	}
	if rep.Description != desc {
		t.Fatalf("description not carried through: %q", rep.Description)
	}
	if rep.Repo.Head == "" || rep.Repo.Dir == "" {
		t.Fatalf("report missing git state: %+v", rep.Repo)
	}
	if strings.Contains(strings.ToLower(jsonOut), "token") {
		t.Fatalf("bug report leaked a token:\n%s", jsonOut)
	}

	// --logs is opt-in and must not appear unless asked: the default report
	// carries no pass-log section, and the flag stays robust (no pass has run
	// here, so it simply omits the section rather than erroring).
	if strings.Contains(out, "## Pass log") {
		t.Fatalf("default report should not include a pass log:\n%s", out)
	}
	if withLogs := r.mustHal(time.Minute, "bug", desc, "--logs"); !strings.Contains(withLogs, "# Hal bug report") {
		t.Fatalf("bug --logs did not produce a report:\n%s", withLogs)
	}
}

// TestServerStop: `up` boots the server (workflow engine idle), and a
// token-authenticated POST /server/stop shuts the whole process down cleanly.
func TestServerStop(t *testing.T) {
	r := newRig(t, `
[project]
name  = "e2e-stop"
trunk = "main"
`)
	stateRoot := ""
	for _, kv := range r.env {
		if strings.HasPrefix(kv, "HAL_STATE_DIR=") {
			stateRoot = strings.TrimPrefix(kv, "HAL_STATE_DIR=")
		}
	}
	if stateRoot == "" {
		t.Fatal("rig env missing HAL_STATE_DIR")
	}

	ctx, cancel := context.WithCancel(context.Background())
	up := exec.CommandContext(ctx, exePath, "up", "--no-open", "--port", strconv.Itoa(freePort(t)))
	up.Dir = r.repo
	up.Env = r.env
	if err := up.Start(); err != nil {
		cancel()
		t.Fatalf("start up: %v", err)
	}
	defer func() {
		cancel()
		_ = up.Wait()
	}()

	si := waitServerJSON(t, stateRoot)
	base := "http://127.0.0.1:" + strconv.Itoa(si.Port)
	client := &http.Client{Timeout: 5 * time.Second}

	stopReq, _ := http.NewRequest("POST", base+"/api/v1/server/stop", nil)
	stopReq.Header.Set("X-Hal-Token", si.Token)
	resp, err := client.Do(stopReq)
	if err != nil {
		t.Fatalf("POST server/stop: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(body), "stopping") {
		t.Fatalf("stop: code=%d body=%s", resp.StatusCode, body)
	}

	done := make(chan error, 1)
	go func() { done <- up.Wait() }()
	select {
	case <-done:
		// process exited — clean stop
	case <-time.After(20 * time.Second):
		t.Fatal("hal did not exit after POST /server/stop")
	}
}
