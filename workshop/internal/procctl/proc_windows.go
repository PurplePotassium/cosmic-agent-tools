//go:build windows

package procctl

import (
	"os/exec"
	"strconv"
	"syscall"
)

// CREATE_NEW_PROCESS_GROUP detaches the child from the parent's Ctrl-C group so a
// Ctrl-C in the Workshop console doesn't also kill an in-flight agent pass.
const createNewProcessGroup = 0x00000200

// Prepare sets platform process attributes on cmd before Start.
func Prepare(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNewProcessGroup}
}

// KillTree terminates the process AND its descendants. On Windows the reliable
// tree-kill is `taskkill /T /F` (walks the child-PID tree) — context-cancel alone
// would kill only the direct child (claude/agy), orphaning any git/build it spawned.
func KillTree(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	pid := strconv.Itoa(cmd.Process.Pid)
	kill := exec.Command("taskkill", "/PID", pid, "/T", "/F")
	kill.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNewProcessGroup}
	return kill.Run()
}
