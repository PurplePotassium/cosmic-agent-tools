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
	if res.Config.Workflow.ArtifactDir != ".hal/workflows" {
		t.Fatalf("artifact_dir = %q", res.Config.Workflow.ArtifactDir)
	}
	if res.Config.Workflow.TurnMinutes != 20 {
		t.Fatalf("turn_minutes = %d", res.Config.Workflow.TurnMinutes)
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
	if c.Project.Verify != "npm test" {
		t.Fatalf("override layer lost: verify = %q", c.Project.Verify)
	}
	for key, want := range map[string]string{
		"server.port":         LayerRepo,
		"server.open_browser": LayerUser,
		"project.verify":      LayerOverride,
		"project.name":        LayerRepo,
		"workflow.turn_minutes": LayerBuiltin,
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
		if k == "HAL_PORT" {
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

// A config file from the pass-loop era must LOAD (never fail startup) but
// warn, per deprecated section — and none of the deprecated values may leak
// into the effective configuration.
func TestDeprecatedPassLoopKeysWarn(t *testing.T) {
	dir := t.TempDir()
	repo := writeFile(t, dir, "repo.toml", `
[project]
name = "legacy"

[git]
worktrees = "on"
branch_prefix = "hal/"

[safety]
max_iterations = 5
breaker_failures = 3
sleep_seconds = 2
wedge_minutes = 40

[server]
port = 6001
planning_percent = 75
planning_proposal_max = 5
follow_up_proposal_max = 2
start_stopped = true

[spice]
enabled = true
personas = "gamedev"

[personality]
enabled = true

[classifier]
mode = "heuristic"

[[pipelines]]
name  = "code"
types = ["code", "tests"]
agent = "claude"
effort = "high"

[[pipelines]]
name       = "art"
agent      = "agy"
invent     = false
drain_main = false
`)
	res, err := Load("", repo, "", noEnv)
	if err != nil {
		t.Fatalf("a pass-loop era config must still load: %v", err)
	}
	// The still-live keys apply.
	if res.Config.Server.Port != 6001 || res.Config.Safety.WedgeMinutes != 40 {
		t.Fatalf("live keys lost: %+v %+v", res.Config.Server, res.Config.Safety)
	}
	joined := strings.Join(res.Warnings, "\n")
	for _, want := range []string{
		"[[pipelines]] is ignored",
		"[spice] is ignored",
		"[personality] is ignored",
		"[classifier] is ignored",
		"[git] worktrees/branch_prefix are ignored",
		"safety.max_iterations is ignored",
		"safety.breaker_failures is ignored",
		"safety.sleep_seconds is ignored",
		"server.planning_percent is ignored",
		"server.planning_proposal_max is ignored",
		"server.follow_up_proposal_max is ignored",
		"server.start_stopped is ignored",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing deprecation warning %q in:\n%s", want, joined)
		}
	}
	// No "unknown config key" noise for the deprecated sections.
	if strings.Contains(joined, "unknown config key") {
		t.Errorf("deprecated keys must not double-report as unknown:\n%s", joined)
	}
}

// A clean new-world config emits no deprecation warnings.
func TestNewWorldConfigNoDeprecationWarnings(t *testing.T) {
	dir := t.TempDir()
	repo := writeFile(t, dir, "repo.toml", `
[project]
name   = "clean"
verify = "go test ./..."

[workflow]
artifact_dir = ".hal/wf"
turn_minutes = 30

[workflow.stages.design]
model  = "claude-opus-4-8"
effort = "high"

[safety]
skip_permissions = true
max_concurrent   = 2
`)
	res, err := Load("", repo, "", noEnv)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", res.Warnings)
	}
	if res.Config.Workflow.ArtifactDir != ".hal/wf" || res.Config.Workflow.TurnMinutes != 30 {
		t.Fatalf("workflow section lost: %+v", res.Config.Workflow)
	}
	st, ok := res.Config.Workflow.Stages["design"]
	if !ok || st.Model != "claude-opus-4-8" || st.Effort != "high" {
		t.Fatalf("stage override lost: %+v", res.Config.Workflow.Stages)
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

// Safety/workflow knobs and agent-less type models are blocking: an
// unattended tool with a zero timeout or a model overlaid on the wrong agent
// must refuse to start, not WARN into the void.
func TestValidateBlocksUnsafeConfig(t *testing.T) {
	for name, body := range map[string]string{
		"zero wedge":            "[safety]\nwedge_minutes = 0",
		"negative concurrency":  "[safety]\nmax_concurrent = -1",
		"agent-less type model": "[types.art]\nmodel = \"gemini-3-flash\"",
		"port out of range":     "[server]\nport = 999999",
		"zero turn minutes":     "[workflow]\nturn_minutes = 0",
		"empty artifact dir":    "[workflow]\nartifact_dir = \"  \"",
		"unknown stage":         "[workflow.stages.polish]\nmodel = \"claude-opus-4-8\"",
		"bad stage effort":      "[workflow.stages.design]\neffort = \"gigantic\"",
		"bad type effort":       "[types.inquiry]\nagent = \"claude\"\neffort = \"gigantic\"",
		"bad art remover":       "[art]\nremover = \"photoshop\"",
	} {
		dir := t.TempDir()
		repo := writeFile(t, dir, "repo.toml", body)
		if _, err := Load("", repo, "", noEnv); err == nil {
			t.Errorf("%s: must block startup", name)
		}
	}

	// Model-family mismatch stays a WARNING (documented: proxy aliases and
	// brand-new ids must not block) — and [agents] extra_models silences it.
	dir := t.TempDir()
	repo := writeFile(t, dir, "repo.toml", "[workflow.stages.design]\nmodel = \"weird-model\"")
	res, err := Load("", repo, "", noEnv)
	if err != nil {
		t.Fatalf("model mismatch must warn, not block: %v", err)
	}
	if joined := strings.Join(res.Warnings, "\n"); !strings.Contains(joined, "weird-model") {
		t.Fatalf("model mismatch warning missing: %v", res.Warnings)
	}
	repo = writeFile(t, dir, "repo2.toml", `
[workflow.stages.design]
model = "my-internal-proxy-model"

[agents.claude]
extra_models = ["my-internal-proxy-model"]
`)
	res, err = Load("", repo, "", noEnv)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Warnings) != 0 {
		t.Fatalf("extra_models must silence the warning: %v", res.Warnings)
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
