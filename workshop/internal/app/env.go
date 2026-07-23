package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/chroma"
	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/config"
	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/driver"
	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/engine"
)

// ToolInfo is one external dependency's status line for the settings panel:
// where it is, what version answered, and — where knowable headless — whether
// it is logged in.
type ToolInfo struct {
	Name    string `json:"name"`
	Present bool   `json:"present"`
	Path    string `json:"path,omitempty"`
	Version string `json:"version,omitempty"`
	Detail  string `json:"detail,omitempty"`
	// Auth is the best headless login signal available for the tool ("" =
	// not applicable). agy's comes from the model-list probe; claude's from
	// whether any pipeline is currently auth-halted.
	Auth string `json:"auth,omitempty"`
	// Fix is the suggested remedy when the tool is absent or degraded.
	Fix string `json:"fix,omitempty"`
}

// EnvPaths is every directory/file an operator may need to find.
type EnvPaths struct {
	Repo       string `json:"repo"`
	StateDir   string `json:"stateDir"`
	Logs       string `json:"logs"`
	RepoConfig string `json:"repoConfig"`
	UserConfig string `json:"userConfig"`
	Overrides  string `json:"overrides"`
	Goal       string `json:"goal"`
	Prompts    string `json:"prompts"`
}

// EnvExport is the transcript-export view: where pass evidence is mirrored.
type EnvExport struct {
	Enabled       bool   `json:"enabled"`
	Configured    string `json:"configured"`         // raw [export].dir value
	Resolved      string `json:"resolved,omitempty"` // absolute destination
	HumanReadable bool   `json:"humanReadable"`
	Source        string `json:"source"` // config layer that set export.dir
	Error         string `json:"error,omitempty"`
}

// EnvServerInfo describes the serving process; filled by the HTTP layer (the
// app can't import the server package — it imports us).
type EnvServerInfo struct {
	PID     int       `json:"pid"`
	Port    int       `json:"port"`
	Started time.Time `json:"started"`
	Version string    `json:"version"`
}

// EnvInfo is the settings panel's environment snapshot.
type EnvInfo struct {
	OS        string         `json:"os"`
	Arch      string         `json:"arch"`
	CheckedAt time.Time      `json:"checkedAt"`
	Tools     []ToolInfo     `json:"tools"`
	Paths     EnvPaths       `json:"paths"`
	Export    EnvExport      `json:"export"`
	Server    *EnvServerInfo `json:"server,omitempty"`
	Warnings  []string       `json:"warnings,omitempty"`
}

// envCacheTTL bounds how often the tool probes re-spawn version commands
// (claude's npm shim takes a second or two).
const envCacheTTL = 30 * time.Second

// EnvStatus assembles the environment snapshot. Tool probes are cached for
// envCacheTTL unless fresh is set (the panel's "re-run checks" button).
// The returned value is a copy — callers may fill Server freely.
func (a *App) EnvStatus(ctx context.Context, fresh bool) EnvInfo {
	a.envMu.Lock()
	if !fresh && a.env != nil && time.Since(a.envAt) < envCacheTTL {
		cached := *a.env
		a.envMu.Unlock()
		return a.refreshEnvLive(ctx, cached)
	}
	a.envMu.Unlock()

	info := EnvInfo{
		OS:        runtime.GOOS,
		Arch:      runtime.GOARCH,
		CheckedAt: time.Now().UTC(),
		Tools:     a.probeTools(ctx),
	}

	a.envMu.Lock()
	cp := info
	a.env = &cp
	a.envAt = time.Now()
	a.envMu.Unlock()
	return a.refreshEnvLive(ctx, info)
}

// refreshEnvLive fills the parts of an EnvInfo that must never be stale —
// paths, export resolution, config warnings, and the login signals that come
// from the store rather than a spawn — over a (possibly cached) probe result.
func (a *App) refreshEnvLive(ctx context.Context, info EnvInfo) EnvInfo {
	res := a.Res()
	info.Paths = EnvPaths{
		Repo:       a.RepoDir,
		StateDir:   a.StateDir,
		Logs:       filepath.Join(a.StateDir, "logs"),
		RepoConfig: config.RepoConfigFile(a.RepoDir),
		UserConfig: config.UserConfigFile(),
		Overrides:  config.OverridesFile(a.RepoDir),
		Goal:       config.GoalFile(a.RepoDir),
		Prompts:    config.PromptsDir(a.RepoDir),
	}
	info.Export = EnvExport{
		Configured:    res.Config.Export.Dir,
		HumanReadable: res.Config.Export.HumanReadable,
		Source:        res.Source("export.dir"),
	}
	if resolved, err := a.ExportBase(); err != nil {
		info.Export.Error = err.Error()
	} else {
		info.Export.Resolved = resolved
		info.Export.Enabled = resolved != ""
	}
	info.Warnings = res.Warnings

	// Tools are cached, but the login signals ride the kv store / halt
	// state and are cheap — recompute them onto the cached rows.
	tools := make([]ToolInfo, len(info.Tools))
	copy(tools, info.Tools)
	for i := range tools {
		switch tools[i].Name {
		case "claude":
			if tools[i].Present {
				tools[i].Auth = a.capturableAuthSignal(ctx, "claude")
			}
		case "codex":
			if tools[i].Present {
				tools[i].Auth = a.capturableAuthSignal(ctx, "codex")
			}
		case "agy":
			if tools[i].Present {
				tools[i].Auth = a.agyAuthSignal(ctx)
			}
		}
	}
	info.Tools = tools
	return info
}

// probeTools spawns the version probes. Slow path — cached by EnvStatus.
func (a *App) probeTools(ctx context.Context) []ToolInfo {
	res := a.Res()
	tools := make([]ToolInfo, 0, 6)

	// claude — capturable CLI: version and capabilities are probeable.
	claude := ToolInfo{Name: "claude"}
	if path, err := driver.ClaudePath(); err != nil {
		claude.Fix = "install Claude Code, or set WORKSHOP_CLAUDE_BIN"
	} else {
		claude.Present = true
		claude.Path = path
		claude.Version = probeVersion(ctx, path, "--version")
		if caps, err := driver.NewClaude().Probe(ctx); err == nil {
			if caps.Effort {
				claude.Detail = "streams output; supports effort levels"
			} else {
				claude.Detail = "streams output; no effort flag (configured efforts are ignored)"
			}
		}
	}
	tools = append(tools, claude)

	// codex — capturable non-interactive CLI, with the same per-pass auth
	// observation model as Claude.
	codex := ToolInfo{Name: "codex"}
	if path, err := driver.CodexPath(); err != nil {
		codex.Fix = "install Codex CLI, or set WORKSHOP_CODEX_BIN"
	} else {
		codex.Present = true
		codex.Path = path
		codex.Version = probeVersion(ctx, path, "--version")
		if caps, err := driver.NewCodex().Probe(ctx); err == nil {
			if caps.Effort {
				codex.Detail = "streams output; supports effort levels"
			} else {
				codex.Detail = "streams output; no config effort override (configured efforts are ignored)"
			}
		}
	}
	tools = append(tools, codex)

	// agy — blind CLI: NEVER spawned with piped stdio (it drops output and
	// can hang — AGENTS.md), so there is no headless version probe. Presence
	// comes from binary discovery, login state from the model-list probe's
	// kv record (see agyAuthSignal).
	agy := ToolInfo{Name: "agy", Detail: "blind headless CLI — no version probe (piped output is dropped upstream)"}
	if path, err := driver.AgyPath(); err != nil {
		agy.Detail = ""
		agy.Fix = "install the Antigravity CLI (agy), or set WORKSHOP_AGY_BIN"
	} else {
		agy.Present = true
		agy.Path = path
	}
	tools = append(tools, agy)

	// ffmpeg — the [art] ffmpeg keyer needs it on PATH.
	ffmpeg := ToolInfo{Name: "ffmpeg"}
	if path, err := exec.LookPath("ffmpeg"); err != nil {
		ffmpeg.Detail = "the ffmpeg keyer will refuse to run"
		ffmpeg.Fix = "winget install ffmpeg (or pick another keyer)"
	} else {
		ffmpeg.Present = true
		ffmpeg.Path = path
		ffmpeg.Version = probeVersion(ctx, path, "-version")
	}
	tools = append(tools, ffmpeg)

	// corridorkey — the neural keyer checkout (not a PATH tool).
	ck := ToolInfo{Name: "corridorkey", Detail: "neural keyer; always runs --device cuda (CPU inference measured 2+ hours per image, so it refuses CPU)"}
	dir := chroma.DiscoverCorridorKey(res.Config.Art.CorridorkeyDir)
	if exe := chroma.CorridorKeyExe(dir); exe != "" {
		ck.Present = true
		ck.Path = exe
	} else {
		ck.Fix = "clone the CorridorKey fork, run its install script, and set [art].corridorkey_dir (or WORKSHOP_CORRIDORKEY)"
	}
	tools = append(tools, ck)

	// git — the engine's substrate.
	git := ToolInfo{Name: "git"}
	if path, err := exec.LookPath("git"); err != nil {
		git.Fix = "install git and re-open the terminal"
	} else {
		git.Present = true
		git.Path = path
		git.Version = probeVersion(ctx, path, "--version")
	}
	tools = append(tools, git)

	return tools
}

// capturableAuthSignal derives a streaming driver's login signal from the engine's own auth
// detection: the driver's output is scanned during passes and an expired
// login halts the pipeline with the auth reason. No pass-independent
// headless check exists, so absence of a halt is the best available signal.
func (a *App) capturableAuthSignal(ctx context.Context, agent string) string {
	for _, p := range a.Res().Config.ResolvedPipelines() {
		if p.Bundle.Agent == agent {
			if halted, _ := a.Store.HaltedReason(ctx, p.Name); halted == engine.HaltAuth {
				return fmt.Sprintf("AUTH FAILURE — pipeline %q halted; log in (`%s`) and resume it", p.Name, agent)
			}
		}
	}
	return "no auth failure observed (verified per pass from agent output)"
}

// agyAuthSignal derives agy's login signal from the last model-list probe
// (launch verification / re-verify button): the probe only yields labels when
// agy is logged in.
func (a *App) agyAuthSignal(ctx context.Context) string {
	if a.artVerifying.Load() {
		return "verifying…"
	}
	if raw, err := a.Store.GetKV(ctx, engine.KVArtAgyModels); err == nil && raw != "" {
		var models []string
		if json.Unmarshal([]byte(raw), &models) == nil && len(models) > 0 {
			return fmt.Sprintf("logged in — %d models enumerated at the last probe", len(models))
		}
	}
	return "not verified — run `agy` interactively once to log in, then re-verify models from the art tab"
}

// probeVersion runs one --version-style command and returns the first
// non-empty output line ("" on any failure). WaitDelay guards against npm
// shims whose orphaned child keeps the pipe open past the timeout.
func probeVersion(ctx context.Context, exe string, args ...string) string {
	pctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(pctx, exe, args...)
	cmd.WaitDelay = 2 * time.Second
	out, err := cmd.CombinedOutput()
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(out), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return line
		}
	}
	return ""
}
