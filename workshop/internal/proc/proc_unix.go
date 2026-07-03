//go:build !windows

package proc

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

func configure(cmd *exec.Cmd, mode SpawnMode) {
	attr := cmd.SysProcAttr
	if attr == nil {
		attr = &syscall.SysProcAttr{}
		cmd.SysProcAttr = attr
	}
	// New process group so a negative-pid signal reaches the whole tree.
	attr.Setpgid = true
	if mode == Consoled {
		// No console concept to allocate; just make sure nothing is
		// attached so a blind driver can't wedge on a full pipe.
		if cmd.Stdin == nil {
			cmd.Stdin, _ = os.Open(os.DevNull)
		}
	}
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
