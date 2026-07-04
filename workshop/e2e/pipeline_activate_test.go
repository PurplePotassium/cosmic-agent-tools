//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestAddedPipelineActivatesWithoutRestart proves the auto-activation wiring
// end to end against the real binary: a lane added through the dashboard API
// against a LIVE (parked) engine gets its own worker without the operator
// restarting anything. `up` starts parked, so the new lane must come up parked
// too (operator-halted) — the engine seeds that halt only for lanes it built
// workers for, so halted=="operator" on "art" is proof the relaunched engine
// now backs it, AND proof it isn't silently running. Before the fix the POST
// only edited config, so "art" would sit in status with halted=="" forever
// (config-only, no worker) until a manual halt/resume.
func TestAddedPipelineActivatesWithoutRestart(t *testing.T) {
	// worktrees off keeps this focused on the activation wiring: adding a 2nd
	// lane under "auto" would flip the engine into worktree mode on relaunch —
	// a heavier topology change orthogonal to "does the new lane get a worker".
	r := newRig(t, `
[project]
name   = "e2e-activate"
trunk  = "main"
verify = "git log -1 --oneline"

[git]
worktrees = "off"

[[pipelines]]
name  = "main"
agent = "fake"
`)
	stateRoot := ""
	for _, kv := range r.env {
		if strings.HasPrefix(kv, "WORKSHOP_STATE_DIR=") {
			stateRoot = strings.TrimPrefix(kv, "WORKSHOP_STATE_DIR=")
		}
	}
	if stateRoot == "" {
		t.Fatal("rig env missing WORKSHOP_STATE_DIR")
	}

	// Start `up` parked (start_stopped defaults true): the engine is live (so
	// Halt can relaunch it) but nothing runs.
	ctx, cancel := context.WithCancel(context.Background())
	up := exec.CommandContext(ctx, exePath, "up", "--no-open", "--port", strconv.Itoa(freePort(t)))
	up.Dir = r.repo
	up.Env = r.env
	if err := up.Start(); err != nil {
		cancel()
		t.Fatalf("start up: %v", err)
	}
	// A hard ctx-kill during the active relaunch can orphan an in-flight git
	// child that still holds repo/.git, which fails t.TempDir's RemoveAll on
	// Windows. So the happy path stops the server gracefully and waits for the
	// process to reap its children first; the defer stays as the crash backstop.
	var waitOnce sync.Once
	waitProc := func() { waitOnce.Do(func() { _ = up.Wait() }) }
	defer func() {
		cancel()
		waitProc()
	}()

	si := waitServerJSON(t, stateRoot)
	base := "http://127.0.0.1:" + strconv.Itoa(si.Port)
	client := &http.Client{Timeout: 5 * time.Second}

	// halted returns the halt reason of pipeline `name` in the live status, or
	// "" if it is running or absent. A GET failure means the engine process
	// died (its relaunch errored) — a fatal, since the whole point is that the
	// server stays up across the add.
	halted := func(name string) string {
		sreq, _ := http.NewRequest("GET", base+"/api/v1/status", nil)
		sreq.Header.Set("X-Workshop-Token", si.Token)
		sresp, err := client.Do(sreq)
		if err != nil {
			t.Fatalf("GET status (engine process died — relaunch must keep the server up): %v", err)
		}
		defer sresp.Body.Close()
		body, _ := io.ReadAll(sresp.Body)
		var st struct {
			Pipelines []struct {
				Name   string `json:"name"`
				Halted string `json:"halted"`
			} `json:"pipelines"`
		}
		if err := json.Unmarshal(body, &st); err != nil {
			t.Fatalf("status json: %v\n%s", err, body)
		}
		for _, p := range st.Pipelines {
			if p.Name == name {
				return p.Halted
			}
		}
		return ""
	}

	waitHalted := func(name string) {
		deadline := time.Now().Add(20 * time.Second)
		for halted(name) != "operator" {
			if time.Now().After(deadline) {
				t.Fatalf("pipeline %q never came up parked (halted==operator)", name)
			}
			time.Sleep(100 * time.Millisecond)
		}
	}

	// Wait for the first run to come up parked, then add immediately. The add
	// triggers a Halt+relaunch, and a Halt landing near startup can leave a
	// killed git child briefly gripping repo/.git — but the relaunch's trunk
	// probe now retries through that transient error (internal/app), so no
	// settling sleep is needed and the add exercises the tighter relaunch race
	// instead of avoiding it.
	waitHalted("main")

	// Add "art" through the same API the dashboard's "add pipeline" uses.
	req, _ := http.NewRequest("POST", base+"/api/v1/pipelines", strings.NewReader(`{"name":"art","agent":"fake"}`))
	req.Header.Set("X-Workshop-Token", si.Token)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST pipelines: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("POST pipelines: code=%d body=%s", resp.StatusCode, body)
	}

	// The relaunched engine must build a worker for "art" and bring it up
	// parked — no manual halt/resume in between.
	waitHalted("art")

	// Stop gracefully so no git child outlives the temp dir on Windows.
	stopReq, _ := http.NewRequest("POST", base+"/api/v1/server/stop", nil)
	stopReq.Header.Set("X-Workshop-Token", si.Token)
	if sr, err := client.Do(stopReq); err == nil {
		sr.Body.Close()
	}
	waitProc()
}
