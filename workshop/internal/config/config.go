// Package config loads Workshop settings, layered defaults → JSON file → env → flags,
// into a typed struct so zero-config works: in a fresh repo with only the agent's own
// credentials present, first launch scaffolds and runs without hand-editing a file.
package config

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
)

// Config holds every knob. Fields are set from (lowest→highest precedence): built-in
// defaults, the JSON config file, WORKSHOP_* env vars, then command-line flags.
type Config struct {
	Addr            string `json:"addr"`            // bind address (localhost-only by default)
	Port            int    `json:"port"`            // UI/API port
	BaseDir         string `json:"baseDir"`         // OS state base dir (DB + per-project state)
	Repo            string `json:"repo"`            // repo to auto-detect as a project (default: cwd)
	Open            bool   `json:"open"`            // open a browser on start
	Iterations      int    `json:"iterations"`      // >0 = bounded smoke run then exit
	Agent           string `json:"agent"`           // default agent for the detected project
	Model           string `json:"model"`           // default model
	Branch          string `json:"branch"`          // branch the loop commits onto
	Preview         string `json:"preview"`         // optional preview URL shown in the UI
	PersonaFlavor   string `json:"personaFlavor"`   // "gamedev" | "plain"
	Random          bool   `json:"random"`          // anti-circling on
	SkipPermissions bool   `json:"skipPermissions"` // run the agent unattended
	SleepSeconds    int    `json:"sleepSeconds"`    // pause between passes
	MaxConcurrent   int    `json:"maxConcurrent"`   // cap on concurrent project loops
}

// Defaults returns the built-in configuration.
func Defaults() Config {
	return Config{
		Addr:            "127.0.0.1",
		Port:            4455,
		BaseDir:         DefaultBaseDir(),
		Open:            true,
		Iterations:      0,
		Agent:           "claude",
		Model:           "claude-sonnet-4-6",
		Branch:          "",
		PersonaFlavor:   "gamedev",
		Random:          true,
		SkipPermissions: true,
		SleepSeconds:    0,
		MaxConcurrent:   4,
	}
}

// DefaultBaseDir is the OS-appropriate state directory (kept OUT of any repo tree):
//
//	Windows: %LOCALAPPDATA%\workshop
//	macOS:   ~/Library/Application Support/workshop
//	Linux:   $XDG_STATE_HOME/workshop  (or ~/.local/state/workshop)
func DefaultBaseDir() string {
	switch runtime.GOOS {
	case "windows":
		if la := os.Getenv("LOCALAPPDATA"); la != "" {
			return filepath.Join(la, "workshop")
		}
	case "darwin":
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, "Library", "Application Support", "workshop")
		}
	default:
		if xdg := os.Getenv("XDG_STATE_HOME"); xdg != "" {
			return filepath.Join(xdg, "workshop")
		}
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, ".local", "state", "workshop")
		}
	}
	// last-ditch fallback
	dir, _ := os.UserConfigDir()
	return filepath.Join(dir, "workshop")
}

// Load resolves the configuration from all layers. args are the CLI args (os.Args[1:]).
func Load(args []string) (Config, error) {
	cfg := Defaults()

	// Layer 2: JSON config file (path via WORKSHOP_CONFIG, else <baseDir>/workshop.json).
	cfgPath := os.Getenv("WORKSHOP_CONFIG")
	if cfgPath == "" {
		cfgPath = filepath.Join(cfg.BaseDir, "workshop.json")
	}
	if b, err := os.ReadFile(cfgPath); err == nil {
		_ = json.Unmarshal(b, &cfg) // best-effort; unknown keys ignored
	}

	// Layer 3: env vars.
	applyEnv(&cfg)

	// Layer 4: flags (highest precedence).
	fs := flag.NewFlagSet("workshop", flag.ContinueOnError)
	fs.StringVar(&cfg.Addr, "addr", cfg.Addr, "bind address (keep localhost — the server spawns agents)")
	fs.IntVar(&cfg.Port, "port", cfg.Port, "UI/API port")
	fs.StringVar(&cfg.BaseDir, "base-dir", cfg.BaseDir, "state base directory (DB + per-project state)")
	fs.StringVar(&cfg.Repo, "repo", cfg.Repo, "repo to open as a project (default: current directory)")
	fs.BoolVar(&cfg.Open, "open", cfg.Open, "open a browser on start")
	fs.IntVar(&cfg.Iterations, "iterations", cfg.Iterations, "bounded smoke run: N passes then exit (0 = run the server)")
	fs.StringVar(&cfg.Agent, "agent", cfg.Agent, "default agent for the detected project (claude|agy|auto)")
	fs.StringVar(&cfg.Model, "model", cfg.Model, "default model id")
	fs.StringVar(&cfg.Branch, "branch", cfg.Branch, "branch the loop commits onto ('' = current)")
	fs.StringVar(&cfg.Preview, "preview", cfg.Preview, "optional preview URL shown in the UI")
	fs.StringVar(&cfg.PersonaFlavor, "personas", cfg.PersonaFlavor, "anti-circling pool flavor (gamedev|plain)")
	fs.BoolVar(&cfg.Random, "random", cfg.Random, "anti-circling randomness each pass")
	fs.BoolVar(&cfg.SkipPermissions, "skip-permissions", cfg.SkipPermissions, "run the agent unattended (--dangerously-skip-permissions)")
	fs.IntVar(&cfg.SleepSeconds, "sleep", cfg.SleepSeconds, "seconds to pause between passes")
	fs.IntVar(&cfg.MaxConcurrent, "max-concurrent", cfg.MaxConcurrent, "cap on concurrently running project loops")
	if err := fs.Parse(args); err != nil {
		return cfg, err
	}
	return cfg, nil
}

func applyEnv(cfg *Config) {
	if v := os.Getenv("WORKSHOP_ADDR"); v != "" {
		cfg.Addr = v
	}
	if v := os.Getenv("WORKSHOP_PORT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.Port = n
		}
	}
	if v := os.Getenv("WORKSHOP_BASE_DIR"); v != "" {
		cfg.BaseDir = v
	}
	if v := os.Getenv("WORKSHOP_PERSONAS"); v != "" {
		cfg.PersonaFlavor = v
	}
	if v := os.Getenv("WORKSHOP_AGENT"); v != "" {
		cfg.Agent = v
	}
	if v := os.Getenv("WORKSHOP_MODEL"); v != "" {
		cfg.Model = v
	}
}
