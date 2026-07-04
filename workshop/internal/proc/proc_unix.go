//go:build !windows

package proc

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
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

func aliveSince(pid int, recorded time.Time) bool {
	if !alive(pid) {
		return false
	}
	if recorded.IsZero() {
		return true // lock written by an older binary: no instant to compare
	}
	created, ok := startTime(pid)
	if !ok {
		return true // creation unknowable (darwin) — liveness only
	}
	return startConsistent(created, recorded)
}

// startTime returns pid's OS creation time where the platform exposes it
// (/proc on Linux). ok=false means unknowable — no /proc (darwin) or an
// unexpected format.
func startTime(pid int) (time.Time, bool) {
	stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return time.Time{}, false
	}
	// Fields follow the LAST ')' — comm may itself contain spaces or parens.
	// starttime is field 22 (1-indexed); state after the paren is field 3,
	// so starttime is the 20th field from there.
	i := bytes.LastIndexByte(stat, ')')
	if i < 0 {
		return time.Time{}, false
	}
	fields := strings.Fields(string(stat[i+1:]))
	if len(fields) < 20 {
		return time.Time{}, false
	}
	ticks, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil {
		return time.Time{}, false
	}
	boot, ok := bootTime()
	if !ok {
		return time.Time{}, false
	}
	// starttime counts USER_HZ ticks since boot; the /proc ABI pins USER_HZ
	// at 100 on every mainstream architecture.
	return boot.Add(time.Duration(ticks) * time.Second / 100), true
}

func bootTime() (time.Time, bool) {
	data, err := os.ReadFile("/proc/stat")
	if err != nil {
		return time.Time{}, false
	}
	for _, line := range strings.Split(string(data), "\n") {
		if rest, ok := strings.CutPrefix(line, "btime "); ok {
			secs, err := strconv.ParseInt(strings.TrimSpace(rest), 10, 64)
			if err != nil {
				return time.Time{}, false
			}
			return time.Unix(secs, 0), true
		}
	}
	return time.Time{}, false
}
