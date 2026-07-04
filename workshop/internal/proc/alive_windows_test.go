//go:build windows

package proc

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

func isAlive(pid int) bool {
	out, err := exec.Command("tasklist", "/FI", "PID eq "+strconv.Itoa(pid), "/NH", "/FO", "CSV").Output()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), `"`+strconv.Itoa(pid)+`"`)
}

// A process that genuinely exits with code 259 (STILL_ACTIVE) must be reported
// dead. The old GetExitCodeProcess check trusted the 259 sentinel and reported
// such a process alive forever. cmd.Wait is deferred (not called before the
// assertion) so Go keeps its process handle open — the process object stays
// openable after exit, so Alive actually exercises the exited-handle path
// instead of just seeing OpenProcess fail.
func TestAliveFalseAfterExit259(t *testing.T) {
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), "PROC_TEST_ROLE=exit259")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid
	t.Cleanup(func() { _ = cmd.Wait() })

	// Wait for the OS to actually terminate the helper (tasklist ground
	// truth), without reaping it.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && isAlive(pid) {
		time.Sleep(50 * time.Millisecond)
	}
	if isAlive(pid) {
		t.Fatal("helper exiting with 259 did not terminate in time")
	}
	if Alive(pid) {
		t.Fatalf("Alive(%d) is true after the process exited with code 259", pid)
	}
}
