// Package project constructs the first-class Workshop unit: a project bound to one
// repo working directory, with its state (GOAL.md/PROMPT.md/logs + materialized
// backlog/completions/progress) in a per-project state dir OUTSIDE the repo tree.
// The store IS the registry; this package just derives stable ids/paths and defaults.
package project

import (
	"crypto/sha1"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"

	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/agent"
	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/assets"
	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/store"
)

// Slug is a stable, filesystem-safe id for a repo path: <basename>-<hash10>. The hash
// is over the normalized absolute path so the SAME repo always maps to the SAME
// project (re-running `workshop` in a repo reuses its state), while distinct repos
// with the same basename never collide.
func Slug(repoPath string) string {
	abs, err := filepath.Abs(repoPath)
	if err != nil {
		abs = repoPath
	}
	norm := strings.ToLower(filepath.Clean(abs))
	sum := sha1.Sum([]byte(norm))
	short := hex.EncodeToString(sum[:])[:10]
	name := sanitize(filepath.Base(abs))
	if name == "" {
		name = "repo"
	}
	return name + "-" + short
}

// StateDir is where a project's out-of-tree state lives: <baseDir>/projects/<slug>.
func StateDir(baseDir, repoPath string) string {
	return filepath.Join(baseDir, "projects", Slug(repoPath))
}

// New builds a Project with derived id/state-dir and sensible defaults. Agent/model
// default to claude/Sonnet when unset.
func New(baseDir, repoPath, branch, ag, model, preview string) *store.Project {
	abs, err := filepath.Abs(repoPath)
	if err != nil {
		abs = repoPath
	}
	if ag == "" {
		ag = "claude"
	}
	if model == "" && ag == "claude" {
		model = agent.DefaultClaudeModel
	}
	return &store.Project{
		ID:       Slug(abs),
		Name:     filepath.Base(abs),
		RepoPath: abs,
		StateDir: StateDir(baseDir, abs),
		Branch:   branch,
		Agent:    ag,
		Model:    model,
		Status:   store.StatusStopped,
		Preview:  preview,
	}
}

// Scaffold makes a project's state dir (+ logs) and seeds GOAL.md / PROMPT.md from the
// embedded templates if absent — the load-bearing repo-hygiene move: Workshop state
// lives OUTSIDE the repo tree, so the per-pass `git add -A` never sweeps it in. Never
// overwrites an existing GOAL.md/PROMPT.md (they're user-edited).
func Scaffold(p *store.Project) error {
	if err := os.MkdirAll(filepath.Join(p.StateDir, "logs"), 0o755); err != nil {
		return err
	}
	seeds := map[string]string{
		"GOAL.md":   assets.GoalTemplate,
		"PROMPT.md": assets.PromptTemplate,
	}
	for name, content := range seeds {
		path := filepath.Join(p.StateDir, name)
		if _, err := os.Stat(path); os.IsNotExist(err) {
			if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
				return err
			}
		}
	}
	return nil
}

func sanitize(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	return strings.Trim(b.String(), "-")
}
