// Package config defines the Workshop configuration schema and its layered
// resolution: built-ins → user-global file → repo file → runtime overrides →
// environment → CLI flags. Every resolved key carries provenance so the
// operator can always answer "why is it using that value?".
package config

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/domain"
)

// Config is the fully resolved configuration for one project.
type Config struct {
	Project    ProjectConfig            `toml:"project"`
	Git        GitConfig                `toml:"git"`
	Safety     SafetyConfig             `toml:"safety"`
	Spice      SpiceConfig              `toml:"spice"`
	Classifier ClassifierConfig         `toml:"classifier"`
	Types      map[string]domain.Bundle `toml:"types"` // task-type routing table
	Pipelines  []PipelineConfig         `toml:"pipelines"`
	Server     ServerConfig             `toml:"server"`
	Agents     map[string]AgentConfig   `toml:"agents"`
}

type ProjectConfig struct {
	Name      string `toml:"name"`       // default: repo folder name
	Trunk     string `toml:"trunk"`      // branch pipelines fork from / merge into; "" = current
	Verify    string `toml:"verify"`     // gate command, exit 0 = pass; "" = prompt-level verify only
	VerifyDir string `toml:"verify_dir"` // cwd for verify, repo-relative
}

type GitConfig struct {
	// Worktrees: "auto" (on iff >1 enabled pipeline), "on", "off".
	// "true"/"false" are accepted spellings for on/off.
	Worktrees    string `toml:"worktrees"`
	BranchPrefix string `toml:"branch_prefix"` // pipeline branches: <prefix><pipeline>
}

type SafetyConfig struct {
	MaxIterations   int  `toml:"max_iterations"` // 0 = unbounded (supervised)
	SkipPermissions bool `toml:"skip_permissions"`
	BreakerFailures int  `toml:"breaker_failures"` // consecutive failed passes -> halt
	WedgeMinutes    int  `toml:"wedge_minutes"`    // in-flight pass older than this -> killed; must exceed agy's 30m --print-timeout
	MaxConcurrent   int  `toml:"max_concurrent"`   // simultaneous agent passes across pipelines
	SleepSeconds    int  `toml:"sleep_seconds"`    // pause between passes of one pipeline
}

type SpiceConfig struct {
	Enabled  bool   `toml:"enabled"`
	Personas string `toml:"personas"` // "general" | "gamedev" | repo-relative path
	Nouns    string `toml:"nouns"`
}

type ClassifierConfig struct {
	Mode  string `toml:"mode"` // "off" | "heuristic" | "agent"
	Agent string `toml:"agent"`
	Model string `toml:"model"`
}

type PipelineConfig struct {
	Name      string   `toml:"name"`
	Types     []string `toml:"types"`      // main-backlog claim filter; empty = all
	DrainMain *bool    `toml:"drain_main"` // default true
	Agent     string   `toml:"agent"`
	Model     string   `toml:"model"`
	Effort    string   `toml:"effort"`
	Invent    *bool    `toml:"invent"`   // default true; ignored when Mode is set
	// Mode names the pipeline's goal-pursuit/backlog-growth stance:
	//   "goal" (default) - invents work when idle (per Invent) AND accepts
	//     agent-proposed follow-ups. Today's behavior.
	//   "discover" - never invents idle work, but still accepts follow-ups an
	//     agent notices immediately after finishing its assigned task.
	//   "drain" - never invents idle work and never accepts follow-ups: the
	//     pipeline can only shrink the backlog.
	// Set, it overrides Invent.
	Mode      string   `toml:"mode"`
	Enabled   *bool    `toml:"enabled"`  // default true
	Worktree  *bool    `toml:"worktree"` // per-pipeline override of [git].worktrees
	ScopeHint string   `toml:"scope_hint"`
	// WedgeMinutes overrides [safety].wedge_minutes for this pipeline (0 = inherit).
	WedgeMinutes int `toml:"wedge_minutes"`
	// ExtraArgs are appended verbatim to every agent invocation of this pipeline.
	ExtraArgs []string `toml:"extra_args"`
}

type ServerConfig struct {
	Port        int  `toml:"port"` // binds 127.0.0.1 only — by design, not configurable
	OpenBrowser bool `toml:"open_browser"`
	// StartStopped launches `up` with every pipeline parked (operator-halted)
	// so nothing runs until you resume it from the dashboard. Only affects
	// `up`; `run` (headless CI) ignores it.
	StartStopped bool `toml:"start_stopped"`
}

type AgentConfig struct {
	// ExtraModels extends the curated known-good model list for this agent
	// (silences "unknown model" validation warnings).
	ExtraModels []string `toml:"extra_models"`
}

// Default returns the built-in configuration — the "empty file works" layer.
func Default() Config {
	return Config{
		Project:    ProjectConfig{VerifyDir: "."},
		Git:        GitConfig{Worktrees: "auto", BranchPrefix: "workshop/"},
		// WedgeMinutes must stay above agy's 30m --print-timeout or default
		// config kills healthy agy passes (and can trip the breaker).
		Safety:     SafetyConfig{MaxIterations: 0, SkipPermissions: true, BreakerFailures: 5, WedgeMinutes: 35, MaxConcurrent: 2},
		Spice:      SpiceConfig{Enabled: true, Personas: "general", Nouns: "general"},
		Classifier: ClassifierConfig{Mode: "heuristic"},
		// Built-in task types ship with EMPTY bundles: routing falls through
		// to the pipeline's own bundle, so nothing changes about which agent
		// runs. Their presence seeds the classifier vocabulary (tasks get
		// typed out of the box) and provides the merge-conflict route the
		// integrator's conflict-task machinery keys on. Operators override
		// per-type bundles ([types.art] agent = "agy" ...) to route work.
		Types: map[string]domain.Bundle{
			"code": {}, "tests": {}, "docs": {}, "art": {}, "audio": {}, "merge-conflict": {},
		},
		Server:     ServerConfig{Port: 4455, OpenBrowser: true, StartStopped: true},
		Agents:     map[string]AgentConfig{},
	}
}

// SharedBacklogName is the CLI/API/UI sentinel addressing the shared main
// backlog (domain.MainBacklog internally). A pipeline may not take this name.
const SharedBacklogName = "shared"

// DefaultPipelineName names the implicit pipeline used when the config
// defines none.
const DefaultPipelineName = "main"

var pipelineNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

// ValidPipelineName reports whether name (case-insensitive) is a legal
// pipeline name: matches pipelineNameRe and isn't the reserved shared-backlog
// sentinel. It does not check for collisions with existing pipelines — that's
// the caller's job (it needs the current list to say which name collided).
func ValidPipelineName(name string) bool {
	lower := strings.ToLower(strings.TrimSpace(name))
	return pipelineNameRe.MatchString(lower) && lower != SharedBacklogName
}

// ResolvedPipelines expands the configured pipelines into domain.Pipeline
// values with defaults applied. With no [[pipelines]] configured it returns
// the single implicit pipeline: all types, drains main, claude, invent on.
func (c *Config) ResolvedPipelines() []domain.Pipeline {
	boolOr := func(p *bool, def bool) bool {
		if p == nil {
			return def
		}
		return *p
	}
	if len(c.Pipelines) == 0 {
		return []domain.Pipeline{{
			Name:            DefaultPipelineName,
			Bundle:          domain.Bundle{Agent: "claude"},
			DrainMain:       true,
			Invent:          true,
			AcceptProposals: true,
			Enabled:         true,
			PassTimeout:     time.Duration(c.Safety.WedgeMinutes) * time.Minute,
		}}
	}
	out := make([]domain.Pipeline, 0, len(c.Pipelines))
	for _, pc := range c.Pipelines {
		wedge := pc.WedgeMinutes
		if wedge == 0 {
			wedge = c.Safety.WedgeMinutes
		}
		agent := pc.Agent
		if agent == "" {
			agent = "claude"
		}
		invent, acceptProposals := resolveMode(pc)
		out = append(out, domain.Pipeline{
			Name:            pc.Name,
			Bundle:          domain.Bundle{Agent: agent, Model: pc.Model, Effort: pc.Effort},
			TaskTypes:       pc.Types,
			DrainMain:       boolOr(pc.DrainMain, true),
			ScopeHint:       pc.ScopeHint,
			Invent:          invent,
			AcceptProposals: acceptProposals,
			Enabled:         boolOr(pc.Enabled, true),
			Worktree:        pc.Worktree,
			PassTimeout:     time.Duration(wedge) * time.Minute,
			ExtraArgs:       pc.ExtraArgs,
		})
	}
	return out
}

// resolveMode derives (Invent, AcceptProposals) from a pipeline's Mode, or —
// with no Mode set — from the legacy Invent bool (proposals always accepted,
// today's behavior).
func resolveMode(pc PipelineConfig) (invent, acceptProposals bool) {
	switch pc.Mode {
	case "discover":
		return false, true
	case "drain":
		return false, false
	default: // "", "goal"
		invent = true
		if pc.Invent != nil {
			invent = *pc.Invent
		}
		return invent, true
	}
}

// WorktreesEnabled resolves the [git].worktrees tri-state against the number
// of enabled pipelines.
func (c *Config) WorktreesEnabled() bool {
	enabled := 0
	for _, p := range c.ResolvedPipelines() {
		if p.Enabled {
			enabled++
		}
	}
	switch normalizeWorktrees(c.Git.Worktrees) {
	case "on":
		return true
	case "off":
		return false
	default: // auto
		return enabled > 1
	}
}

func normalizeWorktrees(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "on", "true", "yes", "1":
		return "on"
	case "off", "false", "no", "0":
		return "off"
	default:
		return "auto"
	}
}

// checkModel warns when model doesn't match agent's curated family list —
// e.g. claude models must be sonnet/fable/opus/haiku, agy models gemini.
// [agents.<agent>] extra_models silences a deliberate off-list choice.
func (c *Config) checkModel(where, agent, model string) error {
	if domain.KnownModel(agent, model) {
		return nil
	}
	for _, extra := range c.Agents[agent].ExtraModels {
		if model == extra {
			return nil
		}
	}
	return fmt.Errorf("%s: model %q is not a known %s model (%v) — add it to [agents.%s] extra_models to silence this",
		where, model, agent, domain.ModelFamilies(agent), agent)
}

// Validate returns blocking problems (errs — Load aborts startup on any: for
// an unattended tool, a bad safety knob or malformed pipeline must never
// silently run) and advisories (warns — model-family mismatches, which are
// documented to warn-not-block so proxy aliases and brand-new ids work).
func (c *Config) Validate() (errs, warns []error) {
	seen := map[string]bool{}
	for _, p := range c.Pipelines {
		name := strings.ToLower(p.Name)
		switch {
		case p.Name == "":
			errs = append(errs, fmt.Errorf("pipelines: every [[pipelines]] entry needs a name"))
		case !pipelineNameRe.MatchString(p.Name):
			// The RAW name is checked: it is what lands in branch and
			// directory names and exact-match SQL — lowercasing it first
			// would bless "MyPipe" here while everything else uses the
			// mixed-case original.
			errs = append(errs, fmt.Errorf("pipelines: name %q must match %s (used in branch and directory names)", p.Name, pipelineNameRe))
		case name == SharedBacklogName:
			errs = append(errs, fmt.Errorf("pipelines: name %q is reserved for the shared backlog", p.Name))
		case seen[name]:
			errs = append(errs, fmt.Errorf("pipelines: duplicate name %q", p.Name))
		}
		seen[name] = true
		if !domain.ValidEffort(p.Effort) {
			errs = append(errs, fmt.Errorf("pipelines.%s: effort %q is not one of %v", p.Name, p.Effort, domain.Efforts))
		}
		agent := p.Agent
		if agent == "" {
			agent = "claude"
		}
		if err := c.checkModel("pipelines."+p.Name, agent, p.Model); err != nil {
			warns = append(warns, err)
		}
		switch p.Mode {
		case "", "goal", "discover", "drain":
		default:
			errs = append(errs, fmt.Errorf("pipelines.%s: mode %q is not goal/discover/drain", p.Name, p.Mode))
		}
	}
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
			// An agent-less model overlays whatever agent the PIPELINE
			// runs, and a wrong id for that agent can fail silently (agy).
			errs = append(errs, fmt.Errorf("types.%s: model %q needs an explicit agent — a model id is only valid for its own agent", t, b.Model))
		}
	}
	if c.Classifier.Agent != "" {
		if err := c.checkModel("classifier", c.Classifier.Agent, c.Classifier.Model); err != nil {
			warns = append(warns, err)
		}
	}
	switch normalizeWorktrees(c.Git.Worktrees) {
	case "on", "off", "auto":
	default:
		errs = append(errs, fmt.Errorf("git.worktrees: %q is not auto/on/off", c.Git.Worktrees))
	}
	switch c.Classifier.Mode {
	case "", "off", "heuristic", "agent":
	default:
		errs = append(errs, fmt.Errorf("classifier.mode: %q is not off/heuristic/agent", c.Classifier.Mode))
	}
	if c.Server.Port < 1 || c.Server.Port > 65535 {
		errs = append(errs, fmt.Errorf("server.port: %d out of range", c.Server.Port))
	}
	// Safety knobs guard unattended execution — nonsense values must block.
	if c.Safety.WedgeMinutes <= 0 {
		errs = append(errs, fmt.Errorf("safety.wedge_minutes: %d — a pass with no wedge timeout can hang the pipeline forever (or be killed instantly); use a large value instead of 0", c.Safety.WedgeMinutes))
	}
	if c.Safety.MaxConcurrent < 0 {
		errs = append(errs, fmt.Errorf("safety.max_concurrent: %d is negative", c.Safety.MaxConcurrent))
	}
	if c.Safety.BreakerFailures < 0 {
		errs = append(errs, fmt.Errorf("safety.breaker_failures: %d is negative", c.Safety.BreakerFailures))
	}
	if c.Safety.SleepSeconds < 0 {
		errs = append(errs, fmt.Errorf("safety.sleep_seconds: %d is negative", c.Safety.SleepSeconds))
	}
	return errs, warns
}
