//go:build windows

package proc

import (
	"bufio"
	"os"
	"os/exec"
	"strconv"
	"testing"
	"time"
)

// waitGone polls the tasklist ground truth until pid disappears.
func waitGone(t *testing.T, pid int, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if !isAlive(pid) {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("process %d still alive after %v", pid, within)
}

// KillTree on an Adopt'ed pid must reach a grandchild whose intermediate
// parent already exited — the case taskkill /T cannot handle (the parent-PID
// walk is broken and the root is dead), so only the TerminateJobObject path
// can kill it. The gated-spawner helper spawns its sleeper strictly after
// Adopt (stdin gate), so the sleeper is inside the job by construction.
func TestKillTreeAdoptedReachesOrphanedGrandchild(t *testing.T) {
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), "PROC_TEST_ROLE=gated-spawner")
	Configure(cmd, Piped)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	spawnerPID := cmd.Process.Pid

	Adopt(spawnerPID)
	if _, err := stdin.Write([]byte{'\n'}); err != nil {
		t.Fatal(err)
	}

	sc := bufio.NewScanner(stdout)
	if !sc.Scan() {
		t.Fatalf("no pid line from gated-spawner: %v", sc.Err())
	}
	grandchild, err := strconv.Atoi(sc.Text())
	if err != nil {
		t.Fatalf("bad pid line %q", sc.Text())
	}
	defer func() { _ = KillTree(grandchild) }() // belt and braces on failure paths

	// The spawner exits here: the grandchild is now an orphan with no
	// parent-PID chain back to the root.
	if err := cmd.Wait(); err != nil {
		t.Fatalf("gated-spawner did not exit cleanly: %v", err)
	}
	if !isAlive(grandchild) {
		t.Fatal("grandchild not alive after its parent exited")
	}

	if err := KillTree(spawnerPID); err != nil {
		t.Fatal(err)
	}
	waitGone(t, grandchild, 5*time.Second)
}

// Finished closes the pid's job handle; KILL_ON_JOB_CLOSE must reap a
// still-running adoptee. This pins the crash guarantee in-process: when the
// Workshop dies, the OS closes our handles and the job takes the tree with
// it — Finished exercises exactly that close.
func TestFinishedKillOnCloseReapsRunningAdoptee(t *testing.T) {
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), "PROC_TEST_ROLE=sleeper")
	Configure(cmd, Piped)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid
	defer func() {
		_ = KillTree(pid) // belt and braces on failure paths
		_ = cmd.Wait()
	}()

	Adopt(pid)
	if !isAlive(pid) {
		t.Fatal("sleeper not alive after start")
	}
	Finished(pid)
	waitGone(t, pid, 5*time.Second)
}
