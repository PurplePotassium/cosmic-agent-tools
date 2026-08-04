package driver

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"
)

// The test binary doubles as an npm-style shim: it prints help, spawns a
// grandchild that inherits the stdout pipe and lingers, then exits — the
// exact shape of claude.cmd -> node on Windows.
func TestMain(m *testing.M) {
	switch os.Getenv("CLAUDE_PROBE_ROLE") {
	case "shim":
		fmt.Println("usage: claude [options]  --effort <level>")
		child := exec.Command(os.Args[0])
		child.Env = append(os.Environ(), "CLAUDE_PROBE_ROLE=lingerer")
		child.Stdout = os.Stdout // inherit the probe's pipe write-end
		child.Stderr = os.Stderr
		if err := child.Start(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0) // exit NOW; the grandchild keeps the pipe open
	case "lingerer":
		time.Sleep(15 * time.Second)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// Probe must return once the shim process exits, even while a grandchild
// still holds the stdout pipe open. Without WaitDelay, CombinedOutput waits
// for pipe EOF and the probe hangs far past its "15s timeout".
func TestProbeReturnsPastLingeringGrandchild(t *testing.T) {
	t.Setenv("HAL_CLAUDE_BIN", os.Args[0])
	t.Setenv("CLAUDE_PROBE_ROLE", "shim")

	start := time.Now()
	c := NewClaude()
	caps, err := c.Probe(context.Background())
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if elapsed > 8*time.Second {
		t.Fatalf("probe blocked %v on a lingering grandchild (WaitDelay regression)", elapsed)
	}
	// The pipe never reached EOF, so help parsing conservatively degrades.
	if caps.Effort {
		t.Log("note: effort detected despite WaitDelay abandon — help arrived before exit")
	}
}
