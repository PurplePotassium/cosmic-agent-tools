package proc

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"testing"
	"time"
)

// The test binary doubles as the spawner/sleeper helper processes.
func TestMain(m *testing.M) {
	switch os.Getenv("PROC_TEST_ROLE") {
	case "sleeper":
		time.Sleep(60 * time.Second)
		os.Exit(0)
	case "exit259":
		// Exit with the STILL_ACTIVE sentinel a healthy GetExitCodeProcess
		// would return for a running process — crashed .NET/test hosts do.
		os.Exit(259)
	case "spawner":
		child := exec.Command(os.Args[0])
		child.Env = append(os.Environ(), "PROC_TEST_ROLE=sleeper")
		child.Stdout = nil
		child.Stderr = nil
		if err := child.Start(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Println(child.Process.Pid)
		child.Wait()
		os.Exit(0)
	case "gated-spawner":
		// Waits for the test's go-ahead byte so Adopt() deterministically
		// precedes the spawn (job membership is inherited only by children
		// created after assignment), then spawns a sleeper and EXITS without
		// waiting — orphaning it and breaking the parent-PID chain.
		if _, err := bufio.NewReader(os.Stdin).ReadByte(); err != nil {
			os.Exit(1)
		}
		child := exec.Command(os.Args[0])
		child.Env = append(os.Environ(), "PROC_TEST_ROLE=sleeper")
		child.Stdout = nil
		child.Stderr = nil
		if err := child.Start(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Println(child.Process.Pid)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// TestKillTreeKillsGrandchild spawns self->spawner->sleeper and asserts
// KillTree on the spawner also takes down the sleeper grandchild.
func TestKillTreeKillsGrandchild(t *testing.T) {
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), "PROC_TEST_ROLE=spawner")
	Configure(cmd, Piped)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = KillTree(cmd.Process.Pid) // belt and braces on failure paths
		cmd.Wait()
	}()

	sc := bufio.NewScanner(stdout)
	if !sc.Scan() {
		t.Fatalf("no pid line from spawner: %v", sc.Err())
	}
	grandchild, err := strconv.Atoi(sc.Text())
	if err != nil {
		t.Fatalf("bad pid line %q", sc.Text())
	}
	if !isAlive(grandchild) {
		t.Fatal("grandchild not alive before kill")
	}

	if err := KillTree(cmd.Process.Pid); err != nil {
		t.Fatal(err)
	}
	cmd.Wait()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !isAlive(grandchild) {
			return // success
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("grandchild %d survived KillTree", grandchild)
}
