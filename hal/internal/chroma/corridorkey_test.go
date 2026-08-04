package chroma

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/PurplePotassium/cosmic-agent-tools/hal/internal/proc"
)

// The test binary doubles as a fake corridorkey CLI. "launcher" mimics the
// venv console-script .exe: it spawns the actual work (python, here the
// "worker" role) as a CHILD process and waits on it — the exact shape that
// makes a launcher-only kill orphan the inference process.
func TestMain(m *testing.M) {
	switch os.Getenv("CHROMA_TEST_ROLE") {
	case "launcher":
		child := exec.Command(os.Args[0])
		child.Env = append(os.Environ(), "CHROMA_TEST_ROLE=worker")
		if err := child.Start(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		_ = os.WriteFile(os.Getenv("CHROMA_TEST_PIDFILE"),
			[]byte(strconv.Itoa(child.Process.Pid)), 0o644)
		_, _ = child.Process.Wait()
		os.Exit(0)
	case "worker":
		time.Sleep(60 * time.Second) // bounded: leaks die on their own if a kill is missed
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// installFakeCorridorKey copies the test binary into the checkout layout
// CorridorKeyExe expects, so RemoveCorridorKey runs it as the keyer CLI.
func installFakeCorridorKey(t *testing.T, dir string) {
	t.Helper()
	sub, name := filepath.Join(dir, ".venv", "bin"), "corridorkey"
	if runtime.GOOS == "windows" {
		sub, name = filepath.Join(dir, ".venv", "Scripts"), "corridorkey.exe"
	}
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	src, err := os.Open(self)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	dst, err := os.OpenFile(filepath.Join(sub, name), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(dst, src); err != nil {
		dst.Close()
		t.Fatal(err)
	}
	if err := dst.Close(); err != nil {
		t.Fatal(err)
	}
}

// Cancelling the keying context (chromaTimeout, engine shutdown) must take
// down the keyer's WHOLE process tree. The corridorkey CLI is a console-
// script launcher whose child python process does the actual work: killing
// only the launcher leaves the inference process grinding the GPU for
// minutes (hours on a CPU install) and free to write the output file into a
// tree the engine has already moved on from.
func TestRemoveCorridorKeyCancelKillsProcessTree(t *testing.T) {
	dir := t.TempDir()
	installFakeCorridorKey(t, dir)
	pidFile := filepath.Join(dir, "worker.pid")
	t.Setenv("CHROMA_TEST_ROLE", "launcher")
	t.Setenv("CHROMA_TEST_PIDFILE", pidFile)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- RemoveCorridorKey(ctx, dir,
			filepath.Join(dir, "in.png"), filepath.Join(dir, "out.png"), KeyGreen)
	}()

	worker := 0
	for deadline := time.Now().Add(15 * time.Second); worker == 0; {
		if time.Now().After(deadline) {
			t.Fatal("fake keyer worker never started")
		}
		if raw, err := os.ReadFile(pidFile); err == nil {
			if pid, perr := strconv.Atoi(strings.TrimSpace(string(raw))); perr == nil && pid > 0 {
				worker = pid
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !proc.Alive(worker) {
		t.Fatalf("worker %d not alive before cancel", worker)
	}

	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("RemoveCorridorKey succeeded despite cancellation")
		}
	case <-time.After(30 * time.Second):
		t.Fatal("RemoveCorridorKey did not return after cancel (blocked on the orphaned worker)")
	}
	for deadline := time.Now().Add(5 * time.Second); ; {
		if !proc.Alive(worker) {
			return
		}
		if time.Now().After(deadline) {
			_ = proc.KillTree(worker) // don't leak the sleeper past the test
			t.Fatalf("keyer worker %d survived cancellation — orphaned process tree", worker)
		}
		time.Sleep(100 * time.Millisecond)
	}
}
