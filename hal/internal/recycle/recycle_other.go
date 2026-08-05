//go:build !windows

package recycle

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// dispose approximates the recycle bin off Windows: `gio trash` (the
// freedesktop trash, present on mainstream Linux) or macOS's ~/.Trash;
// otherwise a hal trash folder under the user cache dir. Never a hard
// delete — recoverability is the point.
func dispose(abs string) error {
	if _, err := exec.LookPath("gio"); err == nil {
		if out, err := exec.Command("gio", "trash", abs).CombinedOutput(); err == nil {
			return nil
		} else {
			_ = out // fall through to the move-based fallback
		}
	}
	dest := ""
	if home, err := os.UserHomeDir(); err == nil {
		if info, err := os.Stat(filepath.Join(home, ".Trash")); err == nil && info.IsDir() {
			dest = filepath.Join(home, ".Trash")
		}
	}
	if dest == "" {
		cache, err := os.UserCacheDir()
		if err != nil {
			return fmt.Errorf("recycle: no trash destination: %w", err)
		}
		dest = filepath.Join(cache, "hal", "trash")
		if err := os.MkdirAll(dest, 0o755); err != nil {
			return fmt.Errorf("recycle: %w", err)
		}
	}
	target := filepath.Join(dest, fmt.Sprintf("%s-%d", filepath.Base(abs), time.Now().UnixMilli()))
	if err := os.Rename(abs, target); err != nil {
		return fmt.Errorf("recycle: %w", err)
	}
	return nil
}
