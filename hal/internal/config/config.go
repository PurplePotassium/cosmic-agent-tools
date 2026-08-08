// Package config defines the Hal configuration schema and its layered
// resolution: built-ins → user-global file → repo file → runtime overrides →
// environment → CLI flags. Every resolved key carries provenance so the
// operator can always answer "why is it using that value?".
package config

import (
	"fmt"
	"strings"

	"github.com/PurplePotassium/cosmic-agent-tools/hal/internal/chroma"
	"github.com/PurplePotassium/cosmic-agent-tools/hal/internal/domain"
)

// Config is the fully resolved configuration for one project.
type Config struct {
	Project  ProjectConfig            `toml:"project"`
	Safety   SafetyConfig             `toml:"safety"`
	Workflow WorkflowConfig           `toml:"workflow"`
	Art      ArtConfig                `toml:"art"`
	Export   ExportConfig             `toml:"export"`
	Types    map[string]domain.Bundle `toml:"types"` // per-surface routing (only "inquiry" is read today)
	Server   ServerConfig             `toml:"server"`
	Agents   map[string]AgentConfig   `toml:"agents"`

	// --- deprecated pass-loop sections, parsed tolerantly and NEVER used.
	// They swallow old config files without tripping the unknown-key check;
	// load.go emits a deprecation warning per section instead (see
	// applyDeprecations). Excluded from the JSON config view.
	Pipelines   []map[string]any `toml:"pipelines" json:"-"`
	Spice       map[string]any   `toml:"spice" json:"-"`
	Personality map[string]any   `toml:"personality" json:"-"`
	Classifier  map[string]any   `toml:"classifier" json:"-"`
	Git         map[string]any   `toml:"git" json:"-"`
}

// WorkflowConfig governs the interactive workflow engine (`hal up`).
type WorkflowConfig struct {
	// ArtifactDir is the repo-relative root the per-workflow stage artifacts
	// land under (default ".hal/workflows").
	ArtifactDir string `toml:"artifact_dir"`
	// TurnMinutes bounds one agent turn — one process, never the wait on a
	// human (default 20).
	TurnMinutes int `toml:"turn_minutes"`
	// Stages optionally overrides model/effort per stage, keyed by the fixed
	// stage names (refine/research/design/plan/implement/validate). Config-file
	// defaults are Claude-backed; dashboard workflow overrides can select
	// another interactive driver such as Codex.
	Stages map[string]StageBundle `toml:"stages"`
}

// StageBundle is one stage's model/effort override ("" = engine default).
type StageBundle struct {
	Model  string `toml:"model"`
	Effort string `toml:"effort"`
}

// ArtConfig governs art jobs (the claude-orchestrated agy image flow plus,
// for transparent jobs, green/blue-screen removal).
type ArtConfig struct {
	// Remover names the green/blue-screen removal backend for transparent art
	// jobs: "ffmpeg" (colorkey+despill, needs ffmpeg on PATH, the default) or
	// "corridorkey" (the neural keyer at CorridorkeyDir).
	// The dashboard can override it live (kv "art.remover" / "art.removers")
	// without a restart. Ignored when Removers is set.
	Remover string `toml:"remover"`
	// Removers is the ordered multi-keyer form of Remover: every listed
	// backend keys each transparent job's screen; the FIRST entry's output
	// becomes the committed asset, the rest are archived beside the job log
	// (iter-NNNNNN.keyed-<keyer>.png, mirrored by [export]) so a human can
	// compare backends side by side and settle on the most effective one.
	// Non-empty, it takes precedence over Remover.
	Removers []string `toml:"removers"`
	// CorridorkeyDir is the CorridorKey checkout used by the corridorkey
	// remover (HAL_CORRIDORKEY env overrides).
	CorridorkeyDir string `toml:"corridorkey_dir"`
}

// ArtKeyers resolves the configured keyer list: [art].removers when set,
// else [art].remover as a one-element list, else the chroma default. The
// returned slice is always non-empty, deduped, primary first — a fresh copy
// the caller may own.
func (c *Config) ArtKeyers() []string {
	src := c.Art.Removers
	if len(src) == 0 {
		if c.Art.Remover != "" {
			return []string{c.Art.Remover}
		}
		return []string{chroma.Removers[0]}
	}
	out := make([]string, 0, len(src))
	seen := map[string]bool{}
	for _, r := range src {
		if r != "" && !seen[r] {
			seen[r] = true
			out = append(out, r)
		}
	}
	if len(out) == 0 {
		return []string{chroma.Removers[0]}
	}
	return out
}

// ExportConfig mirrors each finished art job's / inquiry's evidence files
// (log, driver operational log, archived agent transcript) into an
// operator-chosen folder for auditing.
type ExportConfig struct {
	// Dir is the export destination ("" — the default — disables export).
	// It receives one subfolder per surface ("art", "inquiry"). A relative
	// path resolves against the repository root, but the folder itself must
	// lie OUTSIDE the repository (app.ExportBase enforces this).
	Dir string `toml:"dir"`
	// HumanReadable additionally renders each exported transcript as
	// markdown (<pass>.transcript.md) beside the raw JSONL. Default off.
	HumanReadable bool `toml:"human_readable"`
}

type ProjectConfig struct {
	Name      string `toml:"name"`       // default: repo folder name
	Trunk     string `toml:"trunk"`      // the branch workflows work on; "" = current
	Verify    string `toml:"verify"`     // gate command, exit 0 = pass; "" = prompt-level verify only
	VerifyDir string `toml:"verify_dir"` // cwd for verify, repo-relative
}

// SafetyConfig holds the knobs guarding unattended agent execution. The
// wedge timeout applies to art jobs only now (workflow turns have their own
// [workflow].turn_minutes).
type SafetyConfig struct {
	SkipPermissions bool `toml:"skip_permissions"`
	// WedgeMinutes bounds one art-job orchestrator invocation; must exceed
	// agy's 30m --print-timeout or default config kills healthy agy runs.
	WedgeMinutes  int `toml:"wedge_minutes"`
	MaxConcurrent int `toml:"max_concurrent"` // simultaneous agent turn processes

	// Deprecated pass-loop knobs — parsed tolerantly, ignored (see load.go).
	MaxIterations   int `toml:"max_iterations" json:"-"`
	BreakerFailures int `toml:"breaker_failures" json:"-"`
	SleepSeconds    int `toml:"sleep_seconds" json:"-"`
}

type ServerConfig struct {
	Port        int  `toml:"port"` // binds 127.0.0.1 only — by design, not configurable
	OpenBrowser bool `toml:"open_browser"`

	// Deprecated pass-loop knobs — parsed tolerantly, ignored (see load.go).
	PlanningPercent     int  `toml:"planning_percent" json:"-"`
	PlanningProposalMax int  `toml:"planning_proposal_max" json:"-"`
	FollowUpProposalMax int  `toml:"follow_up_proposal_max" json:"-"`
	StartStopped        bool `toml:"start_stopped" json:"-"`
}

type AgentConfig struct {
	// ExtraModels extends the curated known-good model list for this agent
	// (silences "unknown model" validation warnings).
	ExtraModels []string `toml:"extra_models"`
}

// Default returns the built-in configuration — the "empty file works" layer.
func Default() Config {
	return Config{
		Project: ProjectConfig{VerifyDir: "."},
		// WedgeMinutes must stay above agy's 30m --print-timeout or default
		// config kills healthy agy art jobs.
		Safety:   SafetyConfig{SkipPermissions: true, WedgeMinutes: 35, MaxConcurrent: 2},
		Workflow: WorkflowConfig{ArtifactDir: ".hal/workflows", TurnMinutes: 20, Stages: map[string]StageBundle{}},
		Types:    map[string]domain.Bundle{},
		Art:      ArtConfig{Remover: "ffmpeg"},
		Server:   ServerConfig{Port: 4455, OpenBrowser: true},
		Agents:   map[string]AgentConfig{},
	}
}

// KnownOrExtraModel reports whether model is acceptable for agent: empty, a
// curated family match (domain.KnownModel), or an operator-declared
// [agents.<agent>] extra_models entry. The pin surfaces reuse it to warn
// (never block) on an off-family id — a wrong id fails silently for blind
// drivers (see AGENTS.md).
func (c *Config) KnownOrExtraModel(agent, model string) bool {
	if domain.KnownModel(agent, model) {
		return true
	}
	for _, extra := range c.Agents[agent].ExtraModels {
		if model == extra {
			return true
		}
	}
	return false
}

// checkModel warns when model doesn't match agent's curated family list —
// e.g. claude models must be sonnet/fable/opus/haiku, agy models gemini.
// [agents.<agent>] extra_models silences a deliberate off-list choice.
func (c *Config) checkModel(where, agent, model string) error {
	if c.KnownOrExtraModel(agent, model) {
		return nil
	}
	return fmt.Errorf("%s: model %q is not a known %s model (%v) — add it to [agents.%s] extra_models to silence this",
		where, model, agent, domain.ModelFamilies(agent), agent)
}

// Validate returns blocking problems (errs — Load aborts startup on any: for
// an unattended tool, a bad safety knob must never silently run) and
// advisories (warns — model-family mismatches, which are documented to
// warn-not-block so proxy aliases and brand-new ids work).
func (c *Config) Validate() (errs, warns []error) {
	for t, b := range c.Types {
		if !domain.ValidEffort(b.Effort) {
			errs = append(errs, fmt.Errorf("types.%s: effort %q is not one of %v", t, b.Effort, domain.Efforts))
		}
		switch {
		case b.Agent != "":
			if err := c.checkModel("types."+t, b.Agent, b.Model); err != nil {
				warns = append(warns, err)
			}
		case b.Model != "":
			// An agent-less model overlays whatever agent the surface runs,
			// and a wrong id for that agent can fail silently (agy).
			errs = append(errs, fmt.Errorf("types.%s: model %q needs an explicit agent — a model id is only valid for its own agent", t, b.Model))
		}
	}
	// [workflow]: stage names are fixed; a typo'd table would silently apply
	// to nothing.
	for stage, b := range c.Workflow.Stages {
		if !domain.ValidWorkflowStage(stage) {
			errs = append(errs, fmt.Errorf("workflow.stages.%s: unknown stage (stages: refine, research, design, plan, implement, validate)", stage))
			continue
		}
		if !domain.ValidEffort(b.Effort) {
			errs = append(errs, fmt.Errorf("workflow.stages.%s: effort %q is not one of %v", stage, b.Effort, domain.Efforts))
		}
		if err := c.checkModel("workflow.stages."+stage, "claude", b.Model); err != nil {
			warns = append(warns, err)
		}
	}
	if c.Workflow.TurnMinutes <= 0 {
		errs = append(errs, fmt.Errorf("workflow.turn_minutes: %d — a turn with no timeout can hang the workflow forever (or be killed instantly); use a large value instead of 0", c.Workflow.TurnMinutes))
	}
	if strings.TrimSpace(c.Workflow.ArtifactDir) == "" {
		errs = append(errs, fmt.Errorf("workflow.artifact_dir: must not be empty"))
	}
	if !chroma.ValidRemover(c.Art.Remover) {
		errs = append(errs, fmt.Errorf("art.remover: %q is not one of %v", c.Art.Remover, chroma.Removers))
	}
	seenRemovers := map[string]bool{}
	for _, r := range c.Art.Removers {
		switch {
		case r == "" || !chroma.ValidRemover(r):
			errs = append(errs, fmt.Errorf("art.removers: %q is not one of %v", r, chroma.Removers))
		case seenRemovers[r]:
			errs = append(errs, fmt.Errorf("art.removers: duplicate entry %q", r))
		}
		seenRemovers[r] = true
	}
	if c.Export.HumanReadable && c.Export.Dir == "" {
		warns = append(warns, fmt.Errorf("export.human_readable is set but export.dir is empty — nothing is exported until a destination folder is configured"))
	}
	if c.Server.Port < 1 || c.Server.Port > 65535 {
		errs = append(errs, fmt.Errorf("server.port: %d out of range", c.Server.Port))
	}
	// Safety knobs guard unattended execution — nonsense values must block.
	if c.Safety.WedgeMinutes <= 0 {
		errs = append(errs, fmt.Errorf("safety.wedge_minutes: %d — an art job with no wedge timeout can hang forever (or be killed instantly); use a large value instead of 0", c.Safety.WedgeMinutes))
	}
	if c.Safety.MaxConcurrent < 0 {
		errs = append(errs, fmt.Errorf("safety.max_concurrent: %d is negative", c.Safety.MaxConcurrent))
	}
	return errs, warns
}
