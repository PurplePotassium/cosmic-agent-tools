//go:build windows

package proc

import (
	"fmt"
	"os/exec"
	"strconv"
	"syscall"
)

const createNewConsole = 0x00000010

func configure(cmd *exec.Cmd, mode SpawnMode) {
	attr := cmd.SysProcAttr
	if attr == nil {
		attr = &syscall.SysProcAttr{}
		cmd.SysProcAttr = attr
	}
	attr.CreationFlags |= syscall.CREATE_NEW_PROCESS_GROUP
	if mode == Consoled {
		// A real console (so blind drivers don't drop output on a
		// non-TTY handle), but hidden from the operator's desktop.
		attr.CreationFlags |= createNewConsole
		attr.HideWindow = true
	}
}

func killTree(pid int) error {
	// taskkill /T walks the child tree; /F is unconditional. This is the
	// documented tool for the job and works without extra privileges for
	// processes we spawned.
	out, err := exec.Command("taskkill", "/PID", strconv.Itoa(pid), "/T", "/F").CombinedOutput()
	if err != nil {
		return fmt.Errorf("proc: taskkill %d: %v: %s", pid, err, out)
	}
	return nil
}
