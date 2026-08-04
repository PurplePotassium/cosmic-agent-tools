package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PurplePotassium/cosmic-agent-tools/hal/internal/config"
)

// First launch in a repo with no .hal/config.toml creates it with the
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
		if err := os.MkdirAll(repo+"/.hal", 0o755); err != nil {
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
			t.Fatalf(".hal must not be created outside a git repo; stat err = %v", err)
		}
	})
}

// cmdInit seeds the agent_play smoke convention: a repo carrying the toolkit
// but no toolkit config gets one holding an example `smoke` entry; an
// existing config may hold real toolkit settings and is never rewritten,
// even with --force.
func TestCmdInitAgentPlaySmoke(t *testing.T) {
	t.Run("seeds an example smoke entry when the toolkit has no config", func(t *testing.T) {
		repo := initTestRepo(t)
		if err := os.MkdirAll(filepath.Join(repo, "agent_play"), 0o755); err != nil {
			t.Fatal(err)
		}
		if code := cmdInit([]string{"--repo", repo}); code != 0 {
			t.Fatalf("cmdInit = %d, want 0", code)
		}
		b, err := os.ReadFile(filepath.Join(repo, "agent_play", "agent_play.config.json"))
		if err != nil {
			t.Fatalf("agent_play.config.json should exist: %v", err)
		}
		var cfg map[string]any
		if err := json.Unmarshal(b, &cfg); err != nil {
			t.Fatalf("scaffolded config must be valid JSON: %v\n%s", err, b)
		}
		if _, ok := cfg["smoke"]; !ok {
			t.Fatalf("scaffolded config must carry a smoke entry; got:\n%s", b)
		}
	})

	t.Run("never rewrites an existing toolkit config, even with --force", func(t *testing.T) {
		repo := initTestRepo(t)
		if err := os.MkdirAll(filepath.Join(repo, "agent_play"), 0o755); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(repo, "agent_play", "agent_play.config.json")
		want := `{"godotProject": "game"}`
		if err := os.WriteFile(path, []byte(want), 0o644); err != nil {
			t.Fatal(err)
		}
		if code := cmdInit([]string{"--repo", repo, "--force"}); code != 0 {
			t.Fatalf("cmdInit = %d, want 0", code)
		}
		if b, err := os.ReadFile(path); err != nil || string(b) != want {
			t.Fatalf("toolkit config was modified: %q, %v", b, err)
		}
	})

	t.Run("repo without the toolkit gets no agent_play scaffold", func(t *testing.T) {
		repo := initTestRepo(t)
		if code := cmdInit([]string{"--repo", repo}); code != 0 {
			t.Fatalf("cmdInit = %d, want 0", code)
		}
		if _, err := os.Stat(filepath.Join(repo, "agent_play")); !os.IsNotExist(err) {
			t.Fatalf("agent_play/ must not be created; stat err = %v", err)
		}
	})
}
