//go:build !windows

package proc

import (
	"fmt"
	"os/exec"
	"syscall"
)

// configure puts the child in its own process group. Consoled needs nothing
// extra on Unix: there is no console concept, and a nil Stdin already means
// the null device — opening os.DevNull here leaked one fd per spawn
// (exec.Cmd never closes caller-supplied files).
func configure(cmd *exec.Cmd, _ SpawnMode) {
	attr := cmd.SysProcAttr
	if attr == nil {
		attr = &syscall.SysProcAttr{}
		cmd.SysProcAttr = attr
	}
	// New process group so a negative-pid signal reaches the whole tree.
	attr.Setpgid = true
}

func killTree(pid int) error {
	// The child was started with Setpgid, so its pgid == its pid; signal
	// the group.
	if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil && err != syscall.ESRCH {
		return fmt.Errorf("proc: kill pgid %d: %w", pid, err)
	}
	return nil
}

// adopt/finished: Job Objects are Windows-only; process groups already give
// Unix whole-tree kills.
func adopt(pid int)    {}
func finished(pid int) {}

func alive(pid int) bool {
	// Signal 0 probes existence without touching the process. EPERM means
	// it exists but belongs to someone else — still alive.
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}
