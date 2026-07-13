//go:build windows

package statedir

import (
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// TestAtomicWriteRetriesAccessDeniedReader reproduces the real failure the
// rename retry exists for: a reader holding the destination open WITHOUT
// FILE_SHARE_DELETE (how CRT, .NET, and PowerShell open files) makes
// MoveFileEx fail with ERROR_ACCESS_DENIED — not a sharing violation — until
// the handle closes. The atomic replace must ride that out.
func TestAtomicWriteRetriesAccessDeniedReader(t *testing.T) {
	path := filepath.Join(t.TempDir(), "held.json")
	if err := WriteFileAtomic(path, []byte("{\"n\":1}\n")); err != nil {
		t.Fatal(err)
	}

	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	// GENERIC_READ, share READ|WRITE but NOT DELETE: replacing the file while
	// this handle is open is refused with ACCESS_DENIED.
	h, err := syscall.CreateFile(p, syscall.GENERIC_READ,
		syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE, nil,
		syscall.OPEN_EXISTING, syscall.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	closed := make(chan struct{})
	go func() {
		defer close(closed)
		time.Sleep(40 * time.Millisecond) // well inside the ~200ms retry budget
		_ = syscall.CloseHandle(h)
	}()

	err = WriteFileAtomic(path, []byte("{\"n\":2}\n"))
	<-closed // handle is closed by the goroutine either way
	if err != nil {
		t.Fatalf("atomic replace over a no-share-delete reader: %v", err)
	}
}
