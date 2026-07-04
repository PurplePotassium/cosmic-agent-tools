// Package proc owns cross-platform process-group spawning and tree-kill.
// Exactly two spawn modes exist:
//
//   - Piped: stdout/stderr are pipes (streaming drivers like claude).
//   - Consoled: the child gets its own hidden console and NO redirected
//     stdio — required by blind drivers (agy) whose output silently
//     disappears when attached to a pipe.
//
// KillTree kills the whole process subtree: wedged passes, hard stops, and
// gate timeouts all funnel through it so no agent-spawned grandchild
// (builds, test servers) outlives the Workshop.
package proc

import (
	"os/exec"
	"time"
)

// SpawnMode selects how a child process is wired up.
type SpawnMode int

const (
	// Piped: caller sets Stdout/Stderr/Stdin pipes; child joins a new
	// process group so KillTree can reach its descendants.
	Piped SpawnMode = iota
	// Consoled: child owns a fresh hidden console (Windows) / detached
	// stdio (Unix). Caller must NOT set Stdout/Stderr/Stdin.
	Consoled
)

// Configure applies the platform-specific process-group (and console)
// attributes for the given mode. Call before cmd.Start.
func Configure(cmd *exec.Cmd, mode SpawnMode) { configure(cmd, mode) }

// KillTree forcefully terminates pid and every descendant.
func KillTree(pid int) error { return killTree(pid) }

// Alive reports whether a process with the given pid currently exists.
func Alive(pid int) bool { return alive(pid) }

// AliveSince reports whether pid is alive AND is still the process that was
// alive at `recorded` — its OS creation time must not be later. This defeats
// PID reuse when probing a pid persisted in a lock file (engine.lock,
// server.json): the recorder wrote the file *after* it started, so its
// creation time is ≤ the recorded instant; a later creation time means the
// pid has been recycled by a stranger, and treating that stranger as the
// lock holder would defer to it — or `stop --force` would KillTree it.
// A zero `recorded` (a file written by an older binary) and platforms where
// creation time is unavailable (darwin) fall back to plain liveness.
func AliveSince(pid int, recorded time.Time) bool { return aliveSince(pid, recorded) }

// startSlack absorbs rounding between the recorded wall-clock instant and the
// OS-reported creation time. Both come from the same machine's clock; the
// margin only guards coarse timestamp granularity, so it stays far below any
// realistic lock-write-to-reuse window.
const startSlack = 2 * time.Second

// startConsistent: a process created after the recorded instant cannot be the
// process that recorded it.
func startConsistent(created, recorded time.Time) bool {
	return !created.After(recorded.Add(startSlack))
}

// Adopt places a just-started child in a kill-on-close Job Object (Windows)
// so its whole subtree dies with the pass — even grandchildren whose
// intermediate parent exited, and even if the Workshop itself crashes. No-op
// on other platforms (process groups cover them). Call right after Start.
func Adopt(pid int) { adopt(pid) }

// Finished releases the pid's Job Object after the process exited, reaping
// any stragglers it left behind. No-op if the pid was never adopted.
func Finished(pid int) { finished(pid) }
