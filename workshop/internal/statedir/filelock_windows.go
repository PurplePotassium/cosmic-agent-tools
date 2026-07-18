//go:build windows

package statedir

import (
	"os"

	"golang.org/x/sys/windows"
)

func lockFile(f *os.File) error {
	// Blocking exclusive lock over one byte; byte-range identity is all that
	// matters for mutual exclusion, not file size.
	var ov windows.Overlapped
	return windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK, 0, 1, 0, &ov)
}

func unlockFile(f *os.File) error {
	var ov windows.Overlapped
	return windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, &ov)
}
