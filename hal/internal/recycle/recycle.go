// Package recycle sends files or directories to the OS trash instead of
// deleting them outright — the validation run's archive step uses it so a
// validated workflow's artifact folder stays recoverable (once in the
// recycle bin, once in git history) rather than vanishing.
package recycle

import (
	"fmt"
	"os"
	"path/filepath"
)

// Dispose moves path (file or directory) to the OS recycle bin / trash.
// path must exist; it is resolved to an absolute path first.
func Dispose(path string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("recycle: %w", err)
	}
	if _, err := os.Stat(abs); err != nil {
		return fmt.Errorf("recycle: %w", err)
	}
	return dispose(abs)
}
