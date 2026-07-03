// Package statedir handles the agent-facing state files: atomic, BOM-free
// UTF-8 JSON that both machines and humans (and agents mid-pass) can read.
//
// Writes are atomic (temp file + rename in the same directory) so a reader —
// including an agent polling mid-pass — never observes a partial file.
package statedir

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// ErrEmpty is returned by ReadJSON when the file exists but is empty or
// whitespace-only. Callers usually treat it like "not written yet".
var ErrEmpty = errors.New("statedir: file is empty")

var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

// Windows can transiently refuse to open or replace a file while another
// process/goroutine holds it (ERROR_SHARING_VIOLATION=32,
// ERROR_LOCK_VIOLATION=33). Agents poll these files while we write and vice
// versa, so reads and renames retry briefly on exactly those errors. Other
// errors (like fs.ErrNotExist) surface immediately.
func retrySharing[T any](fn func() (T, error)) (T, error) {
	var out T
	var err error
	for attempt := 0; attempt < 50; attempt++ {
		out, err = fn()
		if err == nil || !isSharingViolation(err) {
			return out, err
		}
		time.Sleep(4 * time.Millisecond)
	}
	return out, err
}

func isSharingViolation(err error) bool {
	var errno syscall.Errno
	if !errors.As(err, &errno) {
		return false
	}
	// Only meaningful on Windows; these errno values don't occur for file
	// open/rename elsewhere, so the check is harmless cross-platform.
	return errno == 32 || errno == 33
}

// WriteJSON marshals v with two-space indentation and atomically replaces
// path. Output is UTF-8 without a BOM, LF line endings, trailing newline.
func WriteJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("statedir: marshal %s: %w", filepath.Base(path), err)
	}
	data = append(data, '\n')
	return WriteFileAtomic(path, data)
}

// WriteFileAtomic writes data to path via a temp file + rename in the same
// directory, creating parent directories as needed.
func WriteFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("statedir: mkdir %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return fmt.Errorf("statedir: temp file in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	// Best-effort cleanup if anything below fails before the rename.
	defer os.Remove(tmpName)

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("statedir: write %s: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("statedir: close %s: %w", tmpName, err)
	}
	if _, err := retrySharing(func() (struct{}, error) {
		return struct{}{}, os.Rename(tmpName, path)
	}); err != nil {
		return fmt.Errorf("statedir: rename to %s: %w", path, err)
	}
	return nil
}

// CopyFile copies src to dst atomically (temp file + rename), creating dst's
// parent directories as needed. Reads retry on Windows sharing violations
// like everything else in this package.
func CopyFile(src, dst string) error {
	data, err := retrySharing(func() ([]byte, error) { return os.ReadFile(src) })
	if err != nil {
		return err
	}
	return WriteFileAtomic(dst, data)
}

// ReadJSON reads path into v, tolerating a UTF-8 BOM (some editors and
// PowerShell versions write one). Returns fs.ErrNotExist if the file is
// missing and ErrEmpty if it holds no content.
func ReadJSON(path string, v any) error {
	data, err := retrySharing(func() ([]byte, error) { return os.ReadFile(path) })
	if err != nil {
		return err
	}
	data = bytes.TrimPrefix(data, utf8BOM)
	if len(bytes.TrimSpace(data)) == 0 {
		return ErrEmpty
	}
	if err := json.Unmarshal(data, v); err != nil {
		return fmt.Errorf("statedir: parse %s: %w", filepath.Base(path), err)
	}
	return nil
}
