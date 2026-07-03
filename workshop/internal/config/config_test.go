package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func noEnv(string) string { return "" }

func TestDefaultsAloneWork(t *testing.T) {
	res, err := Load("", "", "", noEnv)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", res.Warnings)
	}
	if res.Config.Server.Port != 4455 {
		t.Fatalf("port = %d", res.Config.Server.Port)
	}
	if !res.Config.Server.StartStopped {
		t.Fatal("start_stopped should default to true")
	}
	pl := res.Config.ResolvedPipelines()
	if len(pl) != 1 || pl[0].Name != DefaultPipelineName || !pl[0].DrainMain || !pl[0].Enabled || !pl[0].Invent {
		t.Fatalf("implicit pipeline wrong: %+v", pl)
	}
	if pl[0].Bundle.Agent != "claude" {
		t.Fatalf("implicit agent = %q", pl[0].Bundle.Agent)
	}
	if res.Config.WorktreesEnabled() {
		t.Fatal("single implicit pipeline should not enable worktrees under auto")
	}
	if res.Source("server.port") != LayerBuiltin {
		t.Fatalf("provenance = %q", res.Source("server.port"))
	}
}

func TestLayerPrecedenceAndProvenance(t *testing.T) {
	dir := t.TempDir()
	user := writeFile(t, dir, "user.toml", `
[server]
port = 5000
open_browser = false
[spice]
personas = "gamedev"
`)
	repo := writeFile(t, dir, "repo.toml", `
[project]
name = "space-game"
trunk = "main"
[server]
port = 6000
`)
	over := writeFile(t, dir, "overrides.toml", `
[project]
verify = "npm test"
`)
	res, err := Load(user, repo, over, noEnv)
	if err != nil {
		t.Fatal(err)
	}
	c := res.Config
	if c.Server.Port != 6000 {
		t.Fatalf("repo should beat user: port = %d", c.Server.Port)
	}
	if c.Server.OpenBrowser {
		t.Fatal("user layer open_browser=false lost")
	}
	if c.Spice.Personas != "gamedev" {
		t.Fatalf("personas = %q", c.Spice.Personas)
	}
	if c.Project.Verify != "npm test" {
		t.Fatalf("override layer lost: verify = %q", c.Project.Verify)
	}
	for key, want := range map[string]string{
		"server.port":         LayerRepo,
		"server.open_browser": LayerUser,
		"project.verify":      LayerOverride,
		"project.name":        LayerRepo,
		"safety.max_iterations": LayerBuiltin,
	} {
		if got := res.Source(key); got != want {
			t.Errorf("provenance[%s] = %q, want %q", key, got, want)
		}
	}
}

func TestEnvOverridesFiles(t *testing.T) {
	dir := t.TempDir()
	repo := writeFile(t, dir, "repo.toml", "[server]\nport = 6000\n")
	env := func(k string) string {
		if k == "WORKSHOP_PORT" {
			return "7777"
		}
		return ""
	}
	res, err := Load("", repo, "", env)
	if err != nil {
		t.Fatal(err)
	}
	if res.Config.Server.Port != 7777 {
		t.Fatalf("port = %d", res.Config.Server.Port)
	}
	if res.Source("server.port") != LayerEnv {
		t.Fatalf("provenance = %q", res.Source("server.port"))
	}
}

func TestUnknownKeysWarnButLoad(t *testing.T) {
	dir := t.TempDir()
	repo := writeFile(t, dir, "repo.toml", `
[project]
name = "x"
tpyo_key = "oops"
`)
	res, err := Load("", repo, "", noEnv)
	if err != nil {
		t.Fatal(err)
	}
	if res.Config.Project.Name != "x" {
		t.Fatalf("known key lost: %q", res.Config.Project.Name)
	}
	found := false
	for _, w := range res.Warnings {
		if strings.Contains(w, "tpyo_key") {
			found = true
		}
	}
	if !found {
		t.Fatalf("no unknown-key warning in %v", res.Warnings)
	}
}

func TestPipelinesResolveAndValidate(t *testing.T) {
	dir := t.TempDir()
	repo := writeFile(t, dir, "repo.toml", `
[types.code]
agent = "claude"
model = "claude-opus-4-6"
effort = "high"

[[pipelines]]
name = "code"
types = ["code", "tests"]
effort = "high"

[[pipelines]]
name = "art"
types = ["art", "audio"]
agent = "agy"
model = "gemini-3-flash"
invent = false
drain_main = false
`)
	res, err := Load("", repo, "", noEnv)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Warnings) != 0 {
		t.Fatalf("warnings: %v", res.Warnings)
	}
	pl := res.Config.ResolvedPipelines()
	if len(pl) != 2 {
		t.Fatalf("pipelines = %d", len(pl))
	}
	code, art := pl[0], pl[1]
	if !code.DrainMain || code.Bundle.Agent != "claude" || code.Bundle.Effort != "high" || !code.Invent {
		t.Fatalf("code pipeline: %+v", code)
	}
	if art.DrainMain || art.Invent || art.Bundle.Agent != "agy" {
		t.Fatalf("art pipeline: %+v", art)
	}
	if !code.HandlesType("tests") || code.HandlesType("art") {
		t.Fatal("type filter wrong")
	}
	if !res.Config.WorktreesEnabled() {
		t.Fatal("auto worktrees should be on with 2 enabled pipelines")
	}
}

func TestPipelineModeResolution(t *testing.T) {
	dir := t.TempDir()
	repo := writeFile(t, dir, "repo.toml", `
[[pipelines]]
name = "goal-default"

[[pipelines]]
name = "legacy-invent-off"
invent = false

[[pipelines]]
name = "discover"
mode = "discover"

[[pipelines]]
name = "drain"
mode = "drain"
`)
	res, err := Load("", repo, "", noEnv)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Warnings) != 0 {
		t.Fatalf("warnings: %v", res.Warnings)
	}
	byName := map[string]struct{ invent, accept bool }{}
	for _, p := range res.Config.ResolvedPipelines() {
		byName[p.Name] = struct{ invent, accept bool }{p.Invent, p.AcceptProposals}
	}
	cases := map[string]struct{ invent, accept bool }{
		"goal-default":       {true, true},
		"legacy-invent-off":  {false, true}, // pre-existing invent=false behavior: proposals still accepted
		"discover":           {false, true},
		"drain":              {false, false},
	}
	for name, want := range cases {
		if got := byName[name]; got != want {
			t.Errorf("%s: invent=%v accept=%v, want %+v", name, got.invent, got.accept, want)
		}
	}
}

func TestValidateCatchesProblems(t *testing.T) {
	dir := t.TempDir()
	repo := writeFile(t, dir, "repo.toml", `
[[pipelines]]
name = "shared"

[[pipelines]]
name = "Bad Name"

[[pipelines]]
name = "dup"

[[pipelines]]
name = "dup"
effort = "gigantic"
`)
	res, err := Load("", repo, "", noEnv)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(res.Warnings, "\n")
	for _, want := range []string{"reserved", "must match", "duplicate", "effort"} {
		if !strings.Contains(joined, want) {
			t.Errorf("warnings missing %q:\n%s", want, joined)
		}
	}
}

func TestStartStoppedLayering(t *testing.T) {
	dir := t.TempDir()
	// A layer that sets other [server] keys but omits start_stopped must not
	// flip the default-true bool to false (the classic default-true footgun).
	repo := writeFile(t, dir, "repo.toml", "[server]\nport = 6000\n")
	res, err := Load("", repo, "", noEnv)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Config.Server.StartStopped {
		t.Fatal("start_stopped lost when [server] set only other keys")
	}
	// An explicit false is honored.
	off := writeFile(t, dir, "off.toml", "[server]\nstart_stopped = false\n")
	res, err = Load("", off, "", noEnv)
	if err != nil {
		t.Fatal(err)
	}
	if res.Config.Server.StartStopped {
		t.Fatal("start_stopped = false not honored")
	}
}

func TestProjectSlugStable(t *testing.T) {
	a := ProjectSlug(`C:\GameDev\My Game`)
	b := ProjectSlug(`C:\GameDev\My Game`)
	if a != b {
		t.Fatalf("slug not stable: %s vs %s", a, b)
	}
	c := ProjectSlug(`C:\Other\My Game`)
	if a == c {
		t.Fatal("same-basename repos must not collide")
	}
	if strings.ContainsAny(a, ` \/:*?"<>|`) {
		t.Fatalf("slug not filesystem-safe: %q", a)
	}
}
