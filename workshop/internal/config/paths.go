package config

import (
	"crypto/sha1"
	"encoding/hex"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

// RepoConfigDir is the versioned per-repo directory holding intent:
// config.toml, GOAL.md, prompts/.
const RepoConfigDir = ".workshop"

// RepoConfigFile returns the path of the checked-in config for a repo.
func RepoConfigFile(repo string) string {
	return filepath.Join(repo, RepoConfigDir, "config.toml")
}

// GoalFile returns the path of the versioned north-star file for a repo.
func GoalFile(repo string) string {
	return filepath.Join(repo, RepoConfigDir, "GOAL.md")
}

// PromptsDir returns the versioned prompt-fragments directory for a repo.
func PromptsDir(repo string) string {
	return filepath.Join(repo, RepoConfigDir, "prompts")
}

// UserConfigFile returns the user-global config path
// (%APPDATA%\workshop\config.toml on Windows; XDG config dir elsewhere).
func UserConfigFile() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "workshop", "config.toml")
}

// StateRoot returns the root of all mutable Workshop state
// (%LOCALAPPDATA%\workshop on Windows). WORKSHOP_STATE_DIR overrides it.
func StateRoot() string {
	if v := os.Getenv("WORKSHOP_STATE_DIR"); v != "" {
		return v
	}
	switch runtime.GOOS {
	case "windows":
		if v := os.Getenv("LOCALAPPDATA"); v != "" {
			return filepath.Join(v, "workshop")
		}
	case "darwin":
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, "Library", "Application Support", "workshop")
		}
	default:
		if v := os.Getenv("XDG_STATE_HOME"); v != "" {
			return filepath.Join(v, "workshop")
		}
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, ".local", "state", "workshop")
		}
	}
	// Last resort: a dot-dir in the working directory.
	return filepath.Join(".", ".workshop-state")
}

var slugStrip = regexp.MustCompile(`[^a-z0-9-]+`)

// ProjectSlug derives a stable, filesystem-safe identifier for a repo path:
// sanitized basename + 8-hex-char hash of the normalized absolute path. The
// same repo always maps to the same slug; same-named repos never collide.
func ProjectSlug(repoPath string) string {
	abs, err := filepath.Abs(repoPath)
	if err != nil {
		abs = repoPath
	}
	norm := strings.ToLower(filepath.ToSlash(filepath.Clean(abs)))
	sum := sha1.Sum([]byte(norm))
	base := slugStrip.ReplaceAllString(strings.ToLower(filepath.Base(abs)), "-")
	base = strings.Trim(base, "-")
	if base == "" {
		base = "repo"
	}
	return base + "-" + hex.EncodeToString(sum[:])[:8]
}

// ProjectStateDir returns the mutable state directory for a repo:
// <StateRoot>/projects/<slug>.
func ProjectStateDir(repoPath string) string {
	return filepath.Join(StateRoot(), "projects", ProjectSlug(repoPath))
}

// OverridesFile returns the runtime-overrides layer path for a repo.
func OverridesFile(repoPath string) string {
	return filepath.Join(ProjectStateDir(repoPath), "overrides.toml")
}
