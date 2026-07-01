//go:build !windows

package procctl

import (
	"os/exec"
	"syscall"
)

// Prepare puts the child in its own process group (Setpgid) so the whole subtree
// can be signalled at once.
func Prepare(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// KillTree sends SIGKILL to the child's entire process group (negative pid), so any
// git/build the agent spawned dies with it rather than being orphaned.
func KillTree(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	// The child is the group leader (Setpgid), so its pgid == its pid.
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
		// Fall back to killing just the child if the group send fails.
		return cmd.Process.Kill()
	}
	return nil
}
