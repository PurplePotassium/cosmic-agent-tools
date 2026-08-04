package app

import (
	"path/filepath"
	"strings"
	"testing"
)

// ExportBase: off by default, relative dirs resolve against the repo root,
// and destinations inside the repository (or a pipeline worktree) are refused
// — passes commit anything dirty in the working tree, so exported evidence
// there would be swept into project history.
func TestExportBase(t *testing.T) {
	a := newTestApp(t, initRepo(t))

	if base, err := a.ExportBase(); err != nil || base != "" {
		t.Fatalf("default: base=%q err=%v; want off, nil", base, err)
	}

	outside := t.TempDir()
	a.Res().Config.Export.Dir = outside
	if base, err := a.ExportBase(); err != nil || base != filepath.Clean(outside) {
		t.Fatalf("absolute outside dir: base=%q err=%v", base, err)
	}

	// Relative: resolves against the repo root — which is inside the repo,
	// so it must be refused.
	a.Res().Config.Export.Dir = "audits"
	if _, err := a.ExportBase(); err == nil || !strings.Contains(err.Error(), "inside the repository") {
		t.Fatalf("repo-relative dir must be refused: %v", err)
	}

	// A relative path escaping the repo is fine.
	a.Res().Config.Export.Dir = filepath.Join("..", filepath.Base(a.RepoDir)+"-audits")
	if base, err := a.ExportBase(); err != nil || base == "" {
		t.Fatalf("sibling dir refused: base=%q err=%v", base, err)
	}

	// A worktree sibling (<repo>-wt-<pipeline>) is the same sweep hazard.
	a.Res().Config.Export.Dir = a.RepoDir + "-wt-main"
	if _, err := a.ExportBase(); err == nil {
		t.Fatal("worktree sibling must be refused")
	}
}
