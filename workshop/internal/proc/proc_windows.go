//go:build windows

package proc

import (
	"fmt"
	"os/exec"
	"strconv"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
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

// jobs maps adopted pids to their kill-on-close Job Objects.
var (
	jobsMu sync.Mutex
	jobs   = map[int]windows.Handle{}
)

// adopt wraps the child in a Job Object with KILL_ON_JOB_CLOSE. This closes
// the two gaps taskkill /T leaves open: a grandchild whose intermediate
// parent already exited (npm→node chains break the parent-PID walk), and
// orphans surviving a Workshop crash (the OS closes our handles → the job
// dies with us). Children the process spawns AFTER assignment join the job
// automatically; the assignment races only the child's first few
// milliseconds.
func adopt(pid int) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return // taskkill fallback still works
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
		BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{
			LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE,
		},
	}
	if _, err := windows.SetInformationJobObject(job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		windows.CloseHandle(job)
		return
	}
	h, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		windows.CloseHandle(job)
		return
	}
	defer windows.CloseHandle(h)
	if err := windows.AssignProcessToJobObject(job, h); err != nil {
		windows.CloseHandle(job)
		return
	}
	jobsMu.Lock()
	jobs[pid] = job
	jobsMu.Unlock()
}

// finished releases pid's Job Object after a normal exit. Kill-on-close
// reaps any straggler grandchildren the agent left behind — nothing an agent
// spawns is meant to outlive its pass.
func finished(pid int) {
	jobsMu.Lock()
	job, ok := jobs[pid]
	delete(jobs, pid)
	jobsMu.Unlock()
	if ok {
		_ = windows.CloseHandle(job)
	}
}

func takeJob(pid int) (windows.Handle, bool) {
	jobsMu.Lock()
	defer jobsMu.Unlock()
	job, ok := jobs[pid]
	delete(jobs, pid)
	return job, ok
}

func killTree(pid int) error {
	// Preferred path: terminate the Job Object — reaches every descendant
	// regardless of broken parent-PID chains.
	if job, ok := takeJob(pid); ok {
		terr := windows.TerminateJobObject(job, 137)
		_ = windows.CloseHandle(job)
		if terr == nil {
			return nil
		}
		// Fall through to taskkill on a terminate failure.
	}
	// taskkill /T walks the child tree; /F is unconditional. This is the
	// documented tool for the job and works without extra privileges for
	// processes we spawned.
	out, err := exec.Command("taskkill", "/PID", strconv.Itoa(pid), "/T", "/F").CombinedOutput()
	if err != nil {
		return fmt.Errorf("proc: taskkill %d: %v: %s", pid, err, out)
	}
	return nil
}

const (
	processQueryLimitedInformation = 0x1000
	stillActive                    = 259 // STILL_ACTIVE exit-code sentinel
)

func alive(pid int) bool {
	h, err := syscall.OpenProcess(processQueryLimitedInformation, false, uint32(pid))
	if err != nil {
		return false
	}
	defer syscall.CloseHandle(h)
	var code uint32
	if err := syscall.GetExitCodeProcess(h, &code); err != nil {
		return false
	}
	return code == stillActive
}
