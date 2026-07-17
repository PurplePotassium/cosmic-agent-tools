package main

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/config"
)

// First launch in a repo with no .workshop/config.toml creates it with the
// init template; an existing file (even an empty one) is never touched, and a
// non-repo dir is left alone for openApp to reject.
func TestEnsureRepoConfig(t *testing.T) {
	ctx := context.Background()

	t.Run("creates the default config on first launch", func(t *testing.T) {
		repo := initTestRepo(t)
		ensureRepoConfig(ctx, repo)
		b, err := os.ReadFile(config.RepoConfigFile(repo))
		if err != nil {
			t.Fatalf("config.toml should exist: %v", err)
		}
		if !strings.Contains(string(b), "[project]") {
			t.Fatalf("config.toml should hold the scaffold template; got:\n%s", b)
		}
		// The created file must load cleanly with zero errors and warnings.
		res, err := config.Load("", config.RepoConfigFile(repo), "", func(string) string { return "" })
		if err != nil {
			t.Fatalf("scaffolded config must load: %v", err)
		}
		if len(res.Warnings) != 0 {
			t.Fatalf("scaffolded config must load warning-free; got %v", res.Warnings)
		}
	})

	t.Run("keeps an existing config", func(t *testing.T) {
		repo := initTestRepo(t)
		path := config.RepoConfigFile(repo)
		if err := os.MkdirAll(repo+"/.workshop", 0o755); err != nil {
			t.Fatal(err)
		}
		want := "# operator's own file\n"
		if err := os.WriteFile(path, []byte(want), 0o644); err != nil {
			t.Fatal(err)
		}
		ensureRepoConfig(ctx, repo)
		b, err := os.ReadFile(path)
		if err != nil || string(b) != want {
			t.Fatalf("existing config was modified: %q, %v", b, err)
		}
	})

	t.Run("non-repo dir is left alone", func(t *testing.T) {
		dir := t.TempDir()
		ensureRepoConfig(ctx, dir)
		if _, err := os.Stat(config.RepoConfigFile(dir)); !os.IsNotExist(err) {
			t.Fatalf(".workshop must not be created outside a git repo; stat err = %v", err)
		}
	})
}
