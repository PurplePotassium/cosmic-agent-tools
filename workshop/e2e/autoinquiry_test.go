//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestAutoInquiryFiresOnBreakerHalt pins the breaker→auto-diagnosis wiring
// against the REAL binary: a pipeline whose fake agent always crashes trips its
// circuit breaker, and cmdUp's StartAutoInquiry watcher fires the self-evaluator
// without anyone asking — so GET /api/v1/inquiries surfaces an Auto-flagged
// inquiry naming the halted pipeline. The unit test covers the App method in
// isolation; this proves `workshop up` actually installs the watcher end to end.
func TestAutoInquiryFiresOnBreakerHalt(t *testing.T) {
	r := newRig(t, `
[project]
name   = "e2e-autoinquiry"
trunk  = "main"
verify = "git log -1 --oneline"

[safety]
breaker_failures = 2

[[pipelines]]
name  = "main"
agent = "fake"

# Route the self-evaluator to the fake agent too: the auto-diagnosis needs a
# probeable, capturable driver in the hermetic env (the default route is claude,
# which isn't installed here).
[types.inquiry]
agent = "fake"
`)

	// Make every pass crash so the breaker trips after two consecutive fails.
	scenario, stateRoot := "", ""
	for _, kv := range r.env {
		if v, ok := strings.CutPrefix(kv, "WORKSHOP_FAKE_SCENARIO="); ok {
			scenario = v
		}
		if v, ok := strings.CutPrefix(kv, "WORKSHOP_STATE_DIR="); ok {
			stateRoot = v
		}
	}
	if scenario == "" || stateRoot == "" {
		t.Fatal("rig env missing WORKSHOP_FAKE_SCENARIO / WORKSHOP_STATE_DIR")
	}
	if err := os.WriteFile(scenario, []byte(`{"behavior":"crash"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	// Run the real orchestrator with pipelines live (--start-running) so the
	// engine actually drives passes and the breaker can trip. A halt leaves the
	// server up for inspection, so the inquiries endpoint stays serveable.
	ctx, cancel := context.WithCancel(context.Background())
	up := exec.CommandContext(ctx, exePath, "up", "--no-open", "--start-running", "--port", strconv.Itoa(freePort(t)))
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

	// Only the fields the assertion needs off app.Inquiry.
	type inquiry struct {
		Question string `json:"question"`
		Auto     bool   `json:"auto"`
	}

	deadline := time.Now().Add(60 * time.Second)
	for {
		req, _ := http.NewRequest("GET", base+"/api/v1/inquiries", nil)
		req.Header.Set("X-Workshop-Token", si.Token)
		if resp, err := client.Do(req); err == nil {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode == 200 {
				var list []inquiry
				if json.Unmarshal(body, &list) == nil {
					for _, inq := range list {
						if !inq.Auto {
							continue
						}
						if !strings.Contains(inq.Question, "main") ||
							!strings.Contains(strings.ToLower(inq.Question), "breaker") {
							t.Fatalf("auto-inquiry question should name the halted pipeline and the breaker: %q", inq.Question)
						}
						return // watcher fired end to end
					}
				}
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("no Auto-flagged inquiry appeared after the breaker halt; cmdUp may not be wiring StartAutoInquiry")
		}
		time.Sleep(200 * time.Millisecond)
	}
}
