// Package agent is the coding-agent-CLI driver abstraction. It knows how each
// backend wants to be invoked and how its output behaves — the two differ enough
// that the engine must NOT pipe them the same way:
//
//   - claude (Claude Code): prompt over STDIN, streams output → capture it live.
//   - agy (Antigravity/Gemini): prompt as a -p ARG; its stdout is UNCAPTURABLE under
//     any pipe/redirect/subprocess (non-TTY upstream bug) and it HANGS if you redirect
//     its streams. So the engine lets agy own the console and reads progress.json
//     instead. See AGENTS.md.
package agent

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

// Default models per backend when the operator leaves the model unset.
const (
	DefaultClaudeModel = "claude-sonnet-4-6"
)

// Mode is how the prompt reaches the agent.
const (
	ModeStdin = "stdin" // claude: prompt written to the child's stdin
	ModeArg   = "arg"   // agy: prompt passed as the -p argument
)

// Driver is a resolved invocation plan for one pass.
type Driver struct {
	Agent      string   // "claude" | "agy"
	Model      string   // resolved model id ("" = the agent's own default)
	Exe        string   // resolved executable (absolute path or bare name for PATH lookup)
	Mode       string   // ModeStdin | ModeArg
	Args       []string // args that precede the prompt/log flags
	Capturable bool     // true for claude (stream+capture), false for agy (blind headless)
}

// Resolve builds the driver for an (agent, model) selection, mirroring the
// PowerShell Resolve-AgentDriver. Unknown agents fall back to a gated claude/Sonnet
// so an invocation can never fire with an empty exe.
func Resolve(agentName, model string, skipPermissions bool, extraArgs []string) Driver {
	switch agentName {
	case "agy":
		args := []string{"--dangerously-skip-permissions", "--print-timeout", "30m"}
		if model != "" {
			args = append(args, "--model", model)
		}
		args = append(args, extraArgs...)
		return Driver{
			Agent:      "agy",
			Model:      model,
			Exe:        findAgy(),
			Mode:       ModeArg,
			Args:       args,
			Capturable: false,
		}
	default: // "claude" and any unresolved value (e.g. "auto" before classification)
		m := model
		if m == "" {
			m = DefaultClaudeModel
		}
		args := []string{"-p", "--model", m}
		if skipPermissions {
			args = append(args, "--dangerously-skip-permissions")
		}
		args = append(args, extraArgs...)
		return Driver{
			Agent:      "claude",
			Model:      m,
			Exe:        findClaude(),
			Mode:       ModeStdin,
			Args:       args,
			Capturable: true,
		}
	}
}

func findClaude() string {
	if bin := os.Getenv("WORKSHOP_CLAUDE_BIN"); bin != "" {
		return bin
	}
	if p, err := exec.LookPath("claude"); err == nil {
		return p
	}
	return "claude"
}

// findAgy locates the agy binary. A shell/UI process launched before agy's installer
// ran can inherit a PATH without agy's bin dir, so fall back to known install spots.
func findAgy() string {
	if bin := os.Getenv("WORKSHOP_AGY_BIN"); bin != "" {
		return bin
	}
	if p, err := exec.LookPath("agy"); err == nil {
		return p
	}
	for _, c := range agyCandidates() {
		if fi, err := os.Stat(c); err == nil && !fi.IsDir() {
			return c
		}
	}
	return "agy"
}

func agyCandidates() []string {
	var out []string
	switch runtime.GOOS {
	case "windows":
		if la := os.Getenv("LOCALAPPDATA"); la != "" {
			out = append(out, filepath.Join(la, "agy", "bin", "agy.exe"))
		}
		if up := os.Getenv("USERPROFILE"); up != "" {
			out = append(out, filepath.Join(up, "AppData", "Local", "agy", "bin", "agy.exe"))
		}
	default:
		if home, err := os.UserHomeDir(); err == nil {
			out = append(out,
				filepath.Join(home, ".local", "bin", "agy"),
				filepath.Join(home, "agy", "bin", "agy"))
		}
		out = append(out, "/usr/local/bin/agy")
	}
	return out
}
