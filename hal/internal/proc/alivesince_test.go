package proc

import (
	"os"
	"os/exec"
	"runtime"
	"testing"
	"time"
)

// creationTimeSupported: platforms where AliveSince can actually read a
// process creation time; elsewhere it degrades to plain liveness.
func creationTimeSupported() bool {
	return runtime.GOOS == "windows" || runtime.GOOS == "linux"
}

func TestAliveSinceSelf(t *testing.T) {
	// This process started before "now", so it is consistent with a lock
	// recorded now.
	if !AliveSince(os.Getpid(), time.Now()) {
		t.Fatal("AliveSince(self, now) = false, want true")
	}
	// A zero recorded instant (lock written by an older binary) falls back to
	// plain liveness.
	if !AliveSince(os.Getpid(), time.Time{}) {
		t.Fatal("AliveSince(self, zero) = false, want true (liveness fallback)")
	}
}

// A recorded instant of 1970 predates this process's creation: exactly the
// PID-reuse signature (the recorder must have started — and died — before we
// were created), so the probe must report "not the same process".
func TestAliveSinceDetectsReusedPID(t *testing.T) {
	if !creationTimeSupported() {
		t.Skipf("creation time unavailable on %s; AliveSince is liveness-only there", runtime.GOOS)
	}
	if AliveSince(os.Getpid(), time.Unix(1, 0)) {
		t.Fatal("AliveSince(self, 1970) = true, want false (creation after the recorded instant)")
	}
}

func TestAliveSinceDeadPID(t *testing.T) {
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), "PROC_TEST_ROLE=sleeper")
	Configure(cmd, Piped)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid
	recorded := time.Now()
	if !AliveSince(pid, recorded) {
		t.Fatal("AliveSince(live sleeper, now) = false, want true")
	}
	if err := KillTree(pid); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	deadline := time.Now().Add(5 * time.Second)
	for AliveSince(pid, recorded) {
		if time.Now().After(deadline) {
			t.Fatal("AliveSince still true 5s after the process was killed")
		}
		time.Sleep(50 * time.Millisecond)
	}
}
