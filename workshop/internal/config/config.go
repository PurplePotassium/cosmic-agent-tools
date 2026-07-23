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

	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/chroma"
	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/domain"
)

// Config is the fully resolved configuration for one project.
type Config struct {
	Project     ProjectConfig            `toml:"project"`
	Git         GitConfig                `toml:"git"`
	Safety      SafetyConfig             `toml:"safety"`
	Spice       SpiceConfig              `toml:"spice"`
	Personality PersonalityConfig        `toml:"personality"`
	Classifier  ClassifierConfig         `toml:"classifier"`
	Art         ArtConfig                `toml:"art"`
	Export      ExportConfig             `toml:"export"`
	Types       map[string]domain.Bundle `toml:"types"` // task-type routing table
	Pipelines   []PipelineConfig         `toml:"pipelines"`
	Server      ServerConfig             `toml:"server"`
	Agents      map[string]AgentConfig   `toml:"agents"`
}

// ArtConfig governs the art-gen / art-gen-trans task types (agy image-model
// passes plus, for -trans, green/blue-screen removal).
type ArtConfig struct {
	// Remover names the green/blue-screen removal backend for art-gen-trans:
	// "ffmpeg" (colorkey+despill, needs ffmpeg on PATH, the default) or
	// "corridorkey" (the neural keyer at CorridorkeyDir).
	// The dashboard can override it live (kv
	// "art.remover" / "art.removers") without a restart, mirroring the
	// pipeline overrides. Ignored when Removers is set.
	Remover string `toml:"remover"`
	// Removers is the ordered multi-keyer form of Remover: every listed
	// backend keys each art-gen-trans screen; the FIRST entry's output
	// becomes the committed asset, the rest are archived beside the pass log
	// (iter-NNNNNN.keyed-<keyer>.png, mirrored by [export]) so a human can
	// compare backends side by side and settle on the most effective one.
	// Non-empty, it takes precedence over Remover.
	Removers []string `toml:"removers"`
	// CorridorkeyDir is the CorridorKey checkout used by the corridorkey
	// remover (WORKSHOP_CORRIDORKEY env overrides).
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

// ExportConfig mirrors each finished pass's evidence files (pass log, driver
// operational log, archived agent transcript) into an operator-chosen folder
// for auditing.
type ExportConfig struct {
	// Dir is the export destination ("" — the default — disables export).
	// It receives one subfolder per pipeline (plus "inquiry" for the
	// self-evaluator). A relative path resolves against the repository
	// root, but the folder itself must lie OUTSIDE the repository and its
	// worktrees: passes commit anything dirty in the working tree, so
	// exported evidence inside it would be swept into project history
	// (app.ExportBase enforces this at engine start).
	Dir string `toml:"dir"`
	// HumanReadable additionally renders each exported transcript as
	// markdown (<pass>.transcript.md) beside the raw JSONL. Default off.
	HumanReadable bool `toml:"human_readable"`
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

// PersonalityConfig is the operator-editable roster a pipeline's Personality
// selector draws from when set to "random". Unlike Spice (an anti-circling
// technique that always varies), a personality is a fixed roleplay flavor a
// pipeline opts into deliberately — most pipelines leave it at "" (none).
type PersonalityConfig struct {
	Enabled bool     `toml:"enabled"` // master switch; false makes every pipeline's selector a no-op
	List    []string `toml:"list"`    // roster "random" draws from; also the valid named choices
}

// DefaultPersonalities seeds the roster with the built-in cast — an
// intentionally varied set of lenses (literary, paranoid, meticulous,
// destructive, famous) for pipelines that want a persistent flavor rather
// than spice's per-pass anti-circling variety.
var DefaultPersonalities = []string{
	"Edgar Allan Poe",
	"H.P. Lovecraft",
	"a conspiracy theorist who believes everything is connected and distrusts everything",
	"an obsessive detective",
	"a rules lawyer",
	"a video game speedrunner",
	"a chaos toddler who pokes and prods the app in unexpected ways",
	"a min-maxer who tries to optimize everything",
	"Rick, a mad scientist",
	"Paul, a principal architect engineer",
	"Bill Gates",
	"Steve Jobs",
	"Elon Musk",
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
	Invent    *bool    `toml:"invent"` // default true; ignored when Mode is set
	// Mode names the pipeline's goal-pursuit/backlog-growth stance:
	//   "goal" (default) - invents work when idle (per Invent) AND accepts
	//     agent-proposed follow-ups. Today's behavior.
	//   "discover" - never invents idle work, but still accepts follow-ups an
	//     agent notices immediately after finishing its assigned task.
	//   "drain" - never invents idle work and never accepts follow-ups: the
	//     pipeline can only shrink the backlog.
	// Set, it overrides Invent.
	Mode      string `toml:"mode"`
	Enabled   *bool  `toml:"enabled"`  // default true
	Worktree  *bool  `toml:"worktree"` // per-pipeline override of [git].worktrees
	ScopeHint string `toml:"scope_hint"`
	// Personality: "" (none, default) | "random" (draw from [personality].list
	// each pass) | a roster entry name, pinning this pipeline to one flavor.
	Personality string `toml:"personality"`
	// WedgeMinutes overrides [safety].wedge_minutes for this pipeline (0 = inherit).
	WedgeMinutes int `toml:"wedge_minutes"`
	// ExtraArgs are appended verbatim to every agent invocation of this pipeline.
	ExtraArgs []string `toml:"extra_args"`
}

type ServerConfig struct {
	Port        int  `toml:"port"` // binds 127.0.0.1 only — by design, not configurable
	OpenBrowser bool `toml:"open_browser"`
	// PlanningPercent is the chance that an idle, goal-mode pass creates
	// goal-moving proposals rather than reviewing recent committed work.
	PlanningPercent int `toml:"planning_percent"`
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
		Project: ProjectConfig{VerifyDir: "."},
		Git:     GitConfig{Worktrees: "auto", BranchPrefix: "workshop/"},
		// WedgeMinutes must stay above agy's 30m --print-timeout or default
		// config kills healthy agy passes (and can trip the breaker).
		Safety:      SafetyConfig{MaxIterations: 0, SkipPermissions: true, BreakerFailures: 5, WedgeMinutes: 35, MaxConcurrent: 2},
		Spice:       SpiceConfig{Enabled: true, Personas: "general", Nouns: "general"},
		Personality: PersonalityConfig{Enabled: true, List: append([]string(nil), DefaultPersonalities...)},
		Classifier:  ClassifierConfig{Mode: "heuristic"},
		// Built-in task types ship with EMPTY bundles: routing falls through
		// to the pipeline's own bundle, so nothing changes about which agent
		// runs. Their presence seeds the classifier vocabulary (tasks get
		// typed out of the box) and provides the merge-conflict route the
		// integrator's conflict-task machinery keys on. Operators override
		// per-type bundles ([types.art] agent = "agy" ...) to route work.
		//
		// The two art-generation types are the exception: they are
		// claude-orchestrated by definition — a frontier claude pass invokes
		// the agy (Gemini) image model via `workshop agy-run` and verifies
		// its output — so they ship pre-routed to claude. The MODEL is
		// deliberately not pinned here: the engine forces a frontier family
		// (fable, else opus) at pass time and hands agy the launch-verified
		// Gemini label.
		Types: map[string]domain.Bundle{
			"code": {}, "tests": {}, "docs": {}, "art": {}, "audio": {}, "merge-conflict": {},
			domain.ArtGenType:      {Agent: "claude"},
			domain.ArtGenTransType: {Agent: "claude"},
		},
		Art:    ArtConfig{Remover: "ffmpeg"},
		Server: ServerConfig{Port: 4455, OpenBrowser: true, PlanningPercent: 75, StartStopped: true},
		Agents: map[string]AgentConfig{},
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
		// Normalize type filters: the SQL claim filter compares types
		// case-sensitively (BINARY collation), so `types = ["Code"]` would
		// silently claim nothing from the shared backlog.
		types := make([]string, 0, len(pc.Types))
		for _, ty := range pc.Types {
			if n := domain.NormalizeType(ty); n != "" {
				types = append(types, n)
			}
		}
		out = append(out, domain.Pipeline{
			Name:            pc.Name,
			Bundle:          domain.Bundle{Agent: agent, Model: pc.Model, Effort: pc.Effort},
			TaskTypes:       types,
			DrainMain:       boolOr(pc.DrainMain, true),
			ScopeHint:       pc.ScopeHint,
			Invent:          invent,
			AcceptProposals: acceptProposals,
			Enabled:         boolOr(pc.Enabled, true),
			Worktree:        pc.Worktree,
			Personality:     pc.Personality,
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

// KnownOrExtraModel reports whether model is acceptable for agent: empty, a
// curated family match (domain.KnownModel), or an operator-declared
// [agents.<agent>] extra_models entry. The live-override / pin surfaces reuse
// it to warn (never block) on an off-family id — the same warn-not-block policy
// config load applies, since a wrong id fails silently for blind drivers (see
// AGENTS.md).
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

// checkPersonality validates a pipeline's personality selector: "" and "none"
// (both mean no personality — resolvePersonality treats them as synonyms) and
// "random" are always accepted; anything else must name an entry in
// [personality].list (case-insensitive) so the dropdown and the resolved
// value never disagree. "random" with an empty list is also rejected — it
// would silently resolve to none every pass, hiding a typo'd or emptied
// roster.
func (c *Config) checkPersonality(where, personality string) error {
	switch strings.ToLower(strings.TrimSpace(personality)) {
	case "", "none", "random":
		if strings.EqualFold(personality, "random") && len(c.Personality.List) == 0 {
			return fmt.Errorf("%s: personality \"random\" needs a non-empty [personality] list", where)
		}
		return nil
	}
	for _, p := range c.Personality.List {
		if strings.EqualFold(p, personality) {
			return nil
		}
	}
	return fmt.Errorf("%s: personality %q is not \"\", \"none\", \"random\", or an entry in [personality] list (%v)",
		where, personality, c.Personality.List)
}

// CheckPersonality is checkPersonality exported for the live dashboard
// override (SetPipelinePersonality): it needs the same validation config load
// applies, so a typo'd or stale roster entry from the browser is rejected the
// same way a bad TOML value would be.
func (c *Config) CheckPersonality(where, personality string) error {
	return c.checkPersonality(where, personality)
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
		if err := c.checkPersonality("pipelines."+p.Name, p.Personality); err != nil {
			errs = append(errs, err)
		}
		for _, ty := range p.Types {
			if domain.NormalizeType(ty) == "" {
				errs = append(errs, fmt.Errorf("pipelines.%s: type %q is not a valid task type (lowercase letters, digits, - and _)", p.Name, ty))
			}
		}
		// The safety-level knob is checked below; a NEGATIVE per-pipeline
		// override slips past the ==0 inherit check and instant-kills every
		// pass of that lane (then trips the breaker).
		if p.WedgeMinutes < 0 {
			errs = append(errs, fmt.Errorf("pipelines.%s: wedge_minutes %d is negative — every pass would be killed at start", p.Name, p.WedgeMinutes))
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
	// Validate the RAW string: normalizeWorktrees maps anything it doesn't
	// recognize to "auto", so checking its output can never fail — a typo
	// like "of" would silently turn worktrees ON for a multi-pipeline repo
	// whose operator was disabling them.
	switch strings.ToLower(strings.TrimSpace(c.Git.Worktrees)) {
	case "", "auto", "on", "off", "true", "false", "yes", "no", "0", "1":
	default:
		errs = append(errs, fmt.Errorf("git.worktrees: %q is not auto/on/off", c.Git.Worktrees))
	}
	switch c.Classifier.Mode {
	case "", "off", "heuristic", "agent":
	default:
		errs = append(errs, fmt.Errorf("classifier.mode: %q is not off/heuristic/agent", c.Classifier.Mode))
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
	if c.Server.PlanningPercent < 0 || c.Server.PlanningPercent > 100 {
		errs = append(errs, fmt.Errorf("server.planning_percent: %d out of range (want 0-100)", c.Server.PlanningPercent))
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
