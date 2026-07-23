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
	if res.Config.Server.PlanningPercent != 75 {
		t.Fatalf("planning_percent = %d", res.Config.Server.PlanningPercent)
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
planning_percent = 60
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
	if c.Server.PlanningPercent != 60 {
		t.Fatalf("planning_percent = %d", c.Server.PlanningPercent)
	}
	if c.Spice.Personas != "gamedev" {
		t.Fatalf("personas = %q", c.Spice.Personas)
	}
	if c.Project.Verify != "npm test" {
		t.Fatalf("override layer lost: verify = %q", c.Project.Verify)
	}
	for key, want := range map[string]string{
		"server.port":             LayerRepo,
		"server.open_browser":     LayerUser,
		"server.planning_percent": LayerUser,
		"project.verify":          LayerOverride,
		"project.name":            LayerRepo,
		"safety.max_iterations":   LayerBuiltin,
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
		"goal-default":      {true, true},
		"legacy-invent-off": {false, true}, // pre-existing invent=false behavior: proposals still accepted
		"discover":          {false, true},
		"drain":             {false, false},
	}
	for name, want := range cases {
		if got := byName[name]; got != want {
			t.Errorf("%s: invent=%v accept=%v, want %+v", name, got.invent, got.accept, want)
		}
	}
}

func TestExportConfig(t *testing.T) {
	dir := t.TempDir()
	repo := writeFile(t, dir, "repo.toml", `
[export]
dir = 'C:\audits\space-game'
human_readable = true
`)
	res, err := Load("", repo, "", noEnv)
	if err != nil {
		t.Fatal(err)
	}
	if res.Config.Export.Dir != `C:\audits\space-game` || !res.Config.Export.HumanReadable {
		t.Fatalf("export config lost: %+v", res.Config.Export)
	}
	if len(res.Warnings) != 0 {
		t.Fatalf("warnings: %v", res.Warnings)
	}
	if def := Default().Export; def.Dir != "" || def.HumanReadable {
		t.Fatalf("export must default OFF: %+v", def)
	}

	// human_readable without a destination exports nothing — warn, don't block.
	repo = writeFile(t, dir, "repo2.toml", "[export]\nhuman_readable = true\n")
	res, err = Load("", repo, "", noEnv)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, w := range res.Warnings {
		if strings.Contains(w, "export.human_readable") {
			found = true
		}
	}
	if !found {
		t.Fatalf("no export.human_readable warning in %v", res.Warnings)
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
	if err == nil {
		t.Fatalf("malformed pipelines must BLOCK startup, got warnings only: %v", res.Warnings)
	}
	for _, want := range []string{"reserved", "must match", "duplicate", "effort"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("blocking error missing %q:\n%s", want, err)
		}
	}
}

// Safety knobs and agent-less type models are blocking too: an unattended
// loop with a zero wedge timeout or a model overlaid on the wrong agent must
// refuse to start, not WARN into the void.
func TestValidateBlocksUnsafeConfig(t *testing.T) {
	for name, body := range map[string]string{
		"zero wedge":                    "[safety]\nwedge_minutes = 0",
		"negative breaker":              "[safety]\nbreaker_failures = -1",
		"agent-less model":              "[types.art]\nmodel = \"gemini-3-flash\"",
		"port out of range":             "[server]\nport = 999999",
		"planning percent out of range": "[server]\nplanning_percent = 101",
	} {
		dir := t.TempDir()
		repo := writeFile(t, dir, "repo.toml", body)
		if _, err := Load("", repo, "", noEnv); err == nil {
			t.Errorf("%s: must block startup", name)
		}
	}

	// Model-family mismatch stays a WARNING (documented: proxy aliases and
	// brand-new ids must not block).
	dir := t.TempDir()
	repo := writeFile(t, dir, "repo.toml", "[[pipelines]]\nname = \"code\"\nagent = \"claude\"\nmodel = \"weird-model\"")
	res, err := Load("", repo, "", noEnv)
	if err != nil {
		t.Fatalf("model mismatch must warn, not block: %v", err)
	}
	if joined := strings.Join(res.Warnings, "\n"); !strings.Contains(joined, "weird-model") {
		t.Fatalf("model mismatch warning missing: %v", res.Warnings)
	}
}

// normalizeWorktrees maps every unrecognized string to "auto", so validating
// its OUTPUT could never fail: a typo like "of" silently turned worktrees ON
// for a multi-pipeline repo whose operator was disabling them. The raw string
// must be validated instead.
func TestValidateWorktreesTypoBlocks(t *testing.T) {
	for _, bad := range []string{"of", "none", "disabled"} {
		dir := t.TempDir()
		repo := writeFile(t, dir, "repo.toml", "[git]\nworktrees = \""+bad+"\"")
		if _, err := Load("", repo, "", noEnv); err == nil {
			t.Errorf("worktrees = %q must block startup", bad)
		}
	}
	for _, ok := range []string{"auto", "ON", "off", "true", "0", ""} {
		dir := t.TempDir()
		repo := writeFile(t, dir, "repo.toml", "[git]\nworktrees = \""+ok+"\"")
		if _, err := Load("", repo, "", noEnv); err != nil {
			t.Errorf("worktrees = %q must be accepted: %v", ok, err)
		}
	}
}

// A NEGATIVE per-pipeline wedge override slipped past the ==0 inherit check
// and instant-killed every pass of that lane; invalid pipeline types silently
// claimed nothing (case-sensitive SQL filter).
func TestValidatePipelineWedgeAndTypes(t *testing.T) {
	for name, body := range map[string]string{
		"negative pipeline wedge": "[[pipelines]]\nname = \"code\"\nwedge_minutes = -1",
		"invalid pipeline type":   "[[pipelines]]\nname = \"code\"\ntypes = [\"c o d e\"]",
	} {
		dir := t.TempDir()
		repo := writeFile(t, dir, "repo.toml", body)
		if _, err := Load("", repo, "", noEnv); err == nil {
			t.Errorf("%s: must block startup", name)
		}
	}
}

// Pipeline type filters feed a case-sensitive SQL IN(...) — resolve them
// lowercased so `types = ["Code"]` claims "code" tasks.
func TestResolvedPipelinesNormalizeTypes(t *testing.T) {
	c := Default()
	c.Pipelines = []PipelineConfig{{Name: "code", Types: []string{"Code", "  TESTS "}}}
	got := c.ResolvedPipelines()[0].TaskTypes
	if len(got) != 2 || got[0] != "code" || got[1] != "tests" {
		t.Fatalf("TaskTypes = %v, want [code tests]", got)
	}
}

func TestValidateModelFamilies(t *testing.T) {
	dir := t.TempDir()
	repo := writeFile(t, dir, "repo.toml", `
[[pipelines]]
name  = "code"
agent = "claude"
model = "claude-opus-4-8"

[[pipelines]]
name  = "art"
agent = "agy"
model = "gemini-3-flash"

[[pipelines]]
name  = "bad"
agent = "claude"
model = "gpt-4o"

[[pipelines]]
name        = "whitelisted"
agent       = "claude"
model       = "my-internal-proxy-model"

[agents.claude]
extra_models = ["my-internal-proxy-model"]
`)
	res, err := Load("", repo, "", noEnv)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(res.Warnings, "\n")
	if !strings.Contains(joined, `pipelines.bad: model "gpt-4o" is not a known claude model`) {
		t.Fatalf("expected unknown-model warning, got:\n%s", joined)
	}
	for _, unwanted := range []string{"pipelines.code", "pipelines.art", "pipelines.whitelisted"} {
		if strings.Contains(joined, unwanted) {
			t.Errorf("unexpected warning for %s:\n%s", unwanted, joined)
		}
	}
}

func TestValidatePersonality(t *testing.T) {
	// "" and "random" (any case) are always fine, and a name matching the
	// roster case-insensitively resolves too.
	dir := t.TempDir()
	repo := writeFile(t, dir, "repo.toml", `
[[pipelines]]
name = "code"
personality = "RANDOM"

[[pipelines]]
name = "flavor"
personality = "edgar allan poe"
`)
	res, err := Load("", repo, "", noEnv)
	if err != nil {
		t.Fatalf("valid personality selectors must not block: %v", err)
	}
	if len(res.Warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", res.Warnings)
	}
	pl := res.Config.ResolvedPipelines()
	if pl[1].Personality != "edgar allan poe" {
		t.Fatalf("resolved personality = %q, want the configured (unlowered) value", pl[1].Personality)
	}

	// A name that isn't "", "random", or a roster entry must block startup —
	// same "typo'd config never silently runs" policy as pipeline mode/effort.
	dir2 := t.TempDir()
	repoBad := writeFile(t, dir2, "repo.toml", `
[[pipelines]]
name = "code"
personality = "Not A Real Persona"
`)
	if _, err := Load("", repoBad, "", noEnv); err == nil || !strings.Contains(err.Error(), "personality") {
		t.Fatalf("unknown personality must block startup, got: %v", err)
	}

	// "random" with an emptied-out roster would silently resolve to none
	// every pass — that's a config mistake, not a valid no-op.
	dir3 := t.TempDir()
	repoEmpty := writeFile(t, dir3, "repo.toml", `
[personality]
list = []

[[pipelines]]
name = "code"
personality = "random"
`)
	if _, err := Load("", repoEmpty, "", noEnv); err == nil || !strings.Contains(err.Error(), "random") {
		t.Fatalf("\"random\" with an empty list must block startup, got: %v", err)
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
