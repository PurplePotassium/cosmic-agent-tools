//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// serverJSON mirrors the fields of internal/server.ServerInfo the dashboard
// test needs (port + token), read straight off <stateDir>/server.json.
type serverJSON struct {
	Port  int    `json:"port"`
	Token string `json:"token"`
}

// freePort grabs an ephemeral port and releases it; `up` reads the real bound
// port back into server.json, so a lost race just means it falls back to
// another port and we still read the truth.
func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

// waitServerJSON polls the hermetic state root for the server.json `up` writes
// once it has bound its port, and returns the port + token from it.
func waitServerJSON(t *testing.T, stateRoot string) serverJSON {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		var found string
		filepath.WalkDir(stateRoot, func(p string, d os.DirEntry, err error) error {
			if err == nil && !d.IsDir() && d.Name() == "server.json" {
				found = p
				return filepath.SkipAll
			}
			return nil
		})
		if found != "" {
			if data, err := os.ReadFile(found); err == nil {
				var si serverJSON
				if json.Unmarshal(data, &si) == nil && si.Port != 0 && si.Token != "" {
					return si
				}
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("server.json never appeared under the state root")
	return serverJSON{}
}

// TestDashboardLoadsAndStreams proves the read-auth wiring end to end against
// the real binary: the SPA shell stays open, the API reads are token-gated
// (403 without, 200 with), and the SSE stream authenticates via its query
// parameter (EventSource can't set a header). This is what pins the W-12b JS
// change — the token now rides every read — so it can't silently regress.
func TestDashboardLoadsAndStreams(t *testing.T) {
	r := newRig(t, `
[project]
name   = "e2e-dash"
trunk  = "main"
verify = "git log -1 --oneline"

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

	// Start `up` parked (start_stopped defaults true) so the server serves but
	// no pass runs. Its own context lets us kill it deterministically.
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

	get := func(t *testing.T, path, token string) (int, string) {
		t.Helper()
		req, err := http.NewRequest("GET", base+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		if token != "" {
			req.Header.Set("X-Workshop-Token", token)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(body)
	}

	// The SPA shell is ungated — the browser can't send the fragment token on
	// the initial navigation.
	if code, body := get(t, "/", ""); code != 200 || len(body) == 0 {
		t.Fatalf("GET / (dashboard shell): code=%d len=%d", code, len(body))
	}

	// The top-right liveness label reports how many agents are mid-pass
	// ("live - N agents active"), never a hardcoded count — pin the served
	// bundle so a revert to a static label is caught.
	if code, body := get(t, "/app.js", ""); code != 200 || !strings.Contains(body, "agent${active === 1") {
		t.Fatalf("GET /app.js (active-agent count): code=%d, missing live active-count wiring", code)
	}

	// A read route is gated: 403 without the token, 200 with it.
	if code, _ := get(t, "/api/v1/status", ""); code != 403 {
		t.Fatalf("GET /api/v1/status without token: code=%d, want 403", code)
	}
	if code, body := get(t, "/api/v1/status", si.Token); code != 200 || !strings.Contains(body, "\"repo\"") {
		t.Fatalf("GET /api/v1/status with token: code=%d body=%s", code, body)
	}

	// SSE authenticates via the query parameter and starts streaming (the
	// handler flushes a `retry:` hint on connect).
	sctx, scancel := context.WithTimeout(ctx, 5*time.Second)
	defer scancel()
	sreq, _ := http.NewRequestWithContext(sctx, "GET", base+"/api/v1/events?token="+si.Token, nil)
	sresp, err := client.Do(sreq)
	if err != nil {
		t.Fatalf("SSE connect: %v", err)
	}
	defer sresp.Body.Close()
	if sresp.StatusCode != 200 {
		t.Fatalf("SSE status = %d, want 200", sresp.StatusCode)
	}
	if ct := sresp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("SSE Content-Type = %q, want text/event-stream", ct)
	}
	buf := make([]byte, 64)
	n, _ := sresp.Body.Read(buf)
	if !strings.Contains(string(buf[:n]), "retry:") {
		t.Fatalf("SSE stream did not open with a retry hint; got %q", buf[:n])
	}

	// SSE without a token is rejected before the stream opens.
	if code, _ := get(t, "/api/v1/events", ""); code != 403 {
		t.Fatalf("SSE without token: code=%d, want 403", code)
	}
}
