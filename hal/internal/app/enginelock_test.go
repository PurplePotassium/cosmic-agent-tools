package app

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"testing"
	"time"

	"github.com/PurplePotassium/cosmic-agent-tools/hal/internal/proc"
	"github.com/PurplePotassium/cosmic-agent-tools/hal/internal/statedir"
)

// TestSleeperHelper is not a real test: it is the body of the live-process
// helper the engine-lock tests spawn
// (os.Args[0] -test.run=^TestSleeperHelper$ with APP_TEST_SLEEPER set).
func TestSleeperHelper(t *testing.T) {
	if os.Getenv("APP_TEST_SLEEPER") == "" {
		t.Skip("subprocess helper body, not a test")
	}
	time.Sleep(60 * time.Second)
}

func startSleeper(t *testing.T) int {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestSleeperHelper$")
	cmd.Env = append(os.Environ(), "APP_TEST_SLEEPER=1")
	proc.Configure(cmd, proc.Piped)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = proc.KillTree(cmd.Process.Pid)
		_ = cmd.Wait()
	})
	return cmd.Process.Pid
}

func writeEngineLock(t *testing.T, stateDir string, pid int, started time.Time) {
	t.Helper()
	if err := statedir.WriteJSON(EngineLockPath(stateDir), engineLock{PID: pid, Started: started}); err != nil {
		t.Fatal(err)
	}
}

// A lock left by a crashed engine (dead pid) must be taken over, and the lock
// must then record the new owner.
func TestAcquireEngineLockTakesOverDeadHolder(t *testing.T) {
	a := newTestApp(t, initRepo(t))

	// A subprocess that runs zero tests exits immediately: a real, dead pid.
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	writeEngineLock(t, a.StateDir, cmd.Process.Pid, time.Now())

	release, err := a.acquireEngineLock()
	if err != nil {
		t.Fatalf("takeover of a dead holder's lock failed: %v", err)
	}
	defer release()
	pid, _, ok := ReadEngineLock(a.StateDir)
	if !ok || pid != os.Getpid() {
		t.Fatalf("lock after takeover holds pid %d, want ours %d", pid, os.Getpid())
	}
}

// A live holder must be refused — two engines on one repo would share
// worktrees and branches.
func TestAcquireEngineLockRefusesLiveHolder(t *testing.T) {
	a := newTestApp(t, initRepo(t))
	pid := startSleeper(t)
	// Written after the spawn, so the holder's creation time is consistent
	// with the recorded instant — a genuinely live engine.
	writeEngineLock(t, a.StateDir, pid, time.Now())

	if _, err := a.acquireEngineLock(); err == nil {
		t.Fatal("acquire succeeded despite a live engine holding the lock")
	}
}

// TestEngineLockRacerHelper is not a real test: it is the subprocess body for
// the stale-lock race test below (os.Args[0] -test.run=^TestEngineLockRacerHelper$
// with APP_TEST_LOCK_RACER_DIR set). It races for the engine lock, reports a
// verdict on stdout, and — when it won — holds the lock until stdin closes so
// no other racer can legitimately acquire after a release.
func TestEngineLockRacerHelper(t *testing.T) {
	stateDir := os.Getenv("APP_TEST_LOCK_RACER_DIR")
	if stateDir == "" {
		t.Skip("subprocess helper body, not a test")
	}
	a := &App{StateDir: stateDir}
	release, err := a.acquireEngineLock()
	if err != nil {
		fmt.Println("RACER-LOST")
		return
	}
	fmt.Println("RACER-WON")
	_, _ = io.Copy(io.Discard, os.Stdin)
	release()
}

// Several processes racing to clear the same stale (dead-pid) lock must
// produce exactly one owner. The old check-then-remove cleanup let a losing
// racer delete the winner's FRESH lock — two engines on one repo; the
// rename-claim makes the takeover atomic.
func TestAcquireEngineLockStaleRaceSingleWinner(t *testing.T) {
	a := newTestApp(t, initRepo(t))

	// A subprocess that runs zero tests exits immediately: a real, dead pid.
	dead := exec.Command(os.Args[0], "-test.run=^$")
	if err := dead.Run(); err != nil {
		t.Fatal(err)
	}
	writeEngineLock(t, a.StateDir, dead.Process.Pid, time.Now())

	const racers = 4
	type verdict struct {
		won bool
		err error
	}
	verdicts := make(chan verdict, racers)
	var stdins []io.WriteCloser
	var cmds []*exec.Cmd
	for i := 0; i < racers; i++ {
		cmd := exec.Command(os.Args[0], "-test.run=^TestEngineLockRacerHelper$")
		cmd.Env = append(os.Environ(), "APP_TEST_LOCK_RACER_DIR="+a.StateDir)
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
		stdins = append(stdins, stdin)
		cmds = append(cmds, cmd)
		go func() {
			sc := bufio.NewScanner(stdout)
			for sc.Scan() {
				switch sc.Text() {
				case "RACER-WON":
					verdicts <- verdict{won: true}
					return
				case "RACER-LOST":
					verdicts <- verdict{won: false}
					return
				}
			}
			verdicts <- verdict{err: fmt.Errorf("racer exited without a verdict")}
		}()
	}
	t.Cleanup(func() {
		for _, w := range stdins {
			_ = w.Close()
		}
		for _, c := range cmds {
			_ = c.Wait()
		}
	})

	wins := 0
	for i := 0; i < racers; i++ {
		select {
		case v := <-verdicts:
			if v.err != nil {
				t.Error(v.err)
			}
			if v.won {
				wins++
			}
		case <-time.After(60 * time.Second):
			t.Fatal("racers did not all report a verdict")
		}
	}
	if wins != 1 {
		t.Fatalf("stale-lock race produced %d winners, want exactly 1", wins)
	}
}

// A live process whose creation time postdates the recorded start is a
// PID-reuse impostor, not the engine that wrote the lock: the lock is stale
// and must be taken over (deferring to the stranger would wedge the repo;
// `stop --force` would kill it).
func TestAcquireEngineLockTakesOverReusedPID(t *testing.T) {
	if runtime.GOOS != "windows" && runtime.GOOS != "linux" {
		t.Skipf("creation time unavailable on %s; AliveSince is liveness-only there", runtime.GOOS)
	}
	a := newTestApp(t, initRepo(t))
	pid := startSleeper(t) // created now
	// Recorded 1970: the writer must have started (and died) long before the
	// sleeper was created — exactly what a recycled PID looks like.
	writeEngineLock(t, a.StateDir, pid, time.Unix(1, 0))

	release, err := a.acquireEngineLock()
	if err != nil {
		t.Fatalf("reused-pid lock was not taken over: %v", err)
	}
	release()
}
