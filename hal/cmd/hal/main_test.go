package main

import (
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/PurplePotassium/cosmic-agent-tools/hal/internal/server"
	"github.com/PurplePotassium/cosmic-agent-tools/hal/internal/statedir"
)

// initTestRepo makes a throwaway git repo and points all hal state (and
// the user-config layer) at temp dirs so CLI-level tests never touch the real
// %LOCALAPPDATA%\hal or %APPDATA%\hal.
func initTestRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	repo := t.TempDir()
	cmd := exec.Command("git", "init")
	cmd.Dir = repo
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	t.Setenv("HAL_STATE_DIR", t.TempDir())
	t.Setenv("APPDATA", t.TempDir()) // isolate the user config layer (Windows)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	return repo
}

// classifyServerRecord must only remove server.json when the recorded process
// is provably dead — a live-but-unresponsive server keeps its record so a
// second `up` cannot orphan it (the cmdUp clobber bug).
func TestClassifyServerRecord(t *testing.T) {
	started := time.Now().UTC()
	writeRecord := func(t *testing.T) string {
		t.Helper()
		dir := t.TempDir()
		info := server.ServerInfo{PID: 4321, Port: 12345, Token: "tok", Started: started}
		if err := statedir.WriteJSON(server.InfoPath(dir), info); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	yes := func(int, string) bool { return true }
	no := func(int, string) bool { return false }
	alive := func(pid int, ts time.Time) bool { return true }
	dead := func(pid int, ts time.Time) bool { return false }

	t.Run("no record", func(t *testing.T) {
		if _, state := classifyServerRecord(t.TempDir(), no, dead); state != recordNone {
			t.Fatalf("state = %v, want recordNone", state)
		}
	})

	t.Run("responding server is surfaced, record kept", func(t *testing.T) {
		dir := writeRecord(t)
		si, state := classifyServerRecord(dir, yes, dead)
		if state != recordResponding || si == nil || si.PID != 4321 {
			t.Fatalf("state = %v si = %+v, want recordResponding pid 4321", state, si)
		}
		if _, err := os.Stat(server.InfoPath(dir)); err != nil {
			t.Fatalf("server.json must survive: %v", err)
		}
	})

	t.Run("unresponsive but alive keeps its record", func(t *testing.T) {
		dir := writeRecord(t)
		si, state := classifyServerRecord(dir, no, alive)
		if state != recordUnresponsive || si == nil || si.PID != 4321 {
			t.Fatalf("state = %v si = %+v, want recordUnresponsive pid 4321", state, si)
		}
		if _, err := os.Stat(server.InfoPath(dir)); err != nil {
			t.Fatalf("a live server's server.json must NOT be removed: %v", err)
		}
	})

	t.Run("dead process record is removed", func(t *testing.T) {
		dir := writeRecord(t)
		if _, state := classifyServerRecord(dir, no, dead); state != recordStale {
			t.Fatalf("state = %v, want recordStale", state)
		}
		if _, err := os.Stat(server.InfoPath(dir)); !os.IsNotExist(err) {
			t.Fatalf("stale server.json should be removed; stat err = %v", err)
		}
	})

	// The alive check receives the recorded PID and start time, so PID reuse
	// can be detected by the production proc.AliveSince.
	t.Run("alive check gets the recorded identity", func(t *testing.T) {
		dir := writeRecord(t)
		var gotPID int
		var gotStart time.Time
		classifyServerRecord(dir, no, func(pid int, ts time.Time) bool {
			gotPID, gotStart = pid, ts
			return false
		})
		if gotPID != 4321 || !gotStart.Equal(started) {
			t.Fatalf("alive(%d, %v), want alive(4321, %v)", gotPID, gotStart, started)
		}
	})
}
