package driver

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

// Agy drives the Antigravity CLI (Gemini). Hard-won external facts this
// driver encodes (do not "fix" them — they are upstream behavior):
//
//   - agy silently DROPS stdout when it is a pipe/redirect (non-TTY) and can
//     hang under redirected spawn. It must own a real (hidden) console and
//     is therefore a BLIND driver: Capture=None, the agent's progress.json
//     self-report is the only window into a pass.
//   - The prompt goes as a `-p` argument, never stdin.
//   - `--log-file` captures agy's OPERATIONAL log, not the model response.
//   - A wrong `--model` id fails silently (blind + no output). Never guess
//     ids; auth failures are equally invisible (generic nonzero exit).
//   - `--print-timeout` defaults to 5m upstream, which cuts real increments
//     short; 30m is the proven setting. Pass wedge timeouts must exceed it.
type Agy struct {
	mu     sync.Mutex
	exe    string
	caps   Capabilities
	probed bool
}

// NewAgy returns the agy driver (unprobed).
func NewAgy() *Agy { return &Agy{} }

func (a *Agy) Name() string { return "agy" }

// Probe locates the binary.
func (a *Agy) Probe(ctx context.Context) (Capabilities, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.probed {
		return a.caps, nil
	}
	exe, err := findAgy()
	if err != nil {
		return Capabilities{}, err
	}
	a.exe = exe
	a.caps = Capabilities{
		PromptVia:    PromptArg,
		Capture:      CaptureNone,
		NeedsConsole: true,
		ModelSelect:  true,
		// Effort: no reasoning-effort flag exists.
		// AuthProbe: false — failures are invisible headless.
		// The full transcript lands under agy's own brain dir, keyed by the
		// runtime-minted conversation id (see AgyTranscriptPath).
		ConversationTranscript: true,
	}
	a.probed = true
	return a.caps, nil
}

// Plan builds the invocation: prompt as -p arg, own hidden console.
func (a *Agy) Plan(spec InvokeSpec) (ExecPlan, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if spec.Interactive {
		return ExecPlan{}, fmt.Errorf("driver: agy cannot run interactive turns")
	}
	if !a.probed {
		return ExecPlan{}, fmt.Errorf("driver: agy not probed")
	}
	args := []string{}
	if spec.SkipPermissions {
		args = append(args, "--dangerously-skip-permissions")
	}
	args = append(args, "--print-timeout", "30m")
	if spec.Model != "" {
		args = append(args, "--model", spec.Model)
	}
	if spec.ConversationID != "" {
		args = append(args, "--conversation", spec.ConversationID)
	}
	if spec.OpLogPath != "" {
		args = append(args, "--log-file", spec.OpLogPath)
	}
	args = append(args, spec.ExtraArgs...)
	args = append(args, "-p", spec.Prompt)
	// The whole prompt rides in argv. Windows caps a command line at 32,767
	// chars, and blowing it fails the spawn opaquely (agy is blind) — refuse
	// up front with an actionable error instead.
	if runtime.GOOS == "windows" {
		total := encodedArgLen(a.exe)
		for _, s := range args {
			total += 1 + encodedArgLen(s) // separator + encoded arg
		}
		if total > maxWindowsCommandLine {
			return ExecPlan{}, fmt.Errorf(
				"driver: agy invocation is %d chars — over the ~%d Windows command-line limit (agy takes the prompt as an argument); trim GOAL.md / prompt fragments, or route the task to claude, which reads the prompt from stdin",
				total, maxWindowsCommandLine)
		}
	}
	return ExecPlan{Exe: a.exe, Args: args, StdinPrompt: false, Mode: a.caps.SpawnMode()}, nil
}

// maxWindowsCommandLine leaves headroom under the hard 32,767-char cap.
const maxWindowsCommandLine = 30000

// encodedArgLen is the length of s as Go encodes it on the Windows command
// line (os/exec's appendEscapeArg: surrounding quotes, `\"` escapes, doubled
// backslash runs). A flat "+3 per arg" undercounts quote/backslash-dense
// prompts by almost 2x — exactly the ones that then blow the 32,767 cap and
// fail the blind spawn this check exists to prevent.
func encodedArgLen(s string) int {
	if s == "" {
		return 2 // ""
	}
	if !strings.ContainsAny(s, " \t\"") {
		return len(s)
	}
	n := len(s) + 2 // surrounding quotes
	slashes := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\\':
			slashes++
		case '"':
			n += slashes + 1 // backslash run doubles, quote gains an escape
			slashes = 0
		default:
			slashes = 0
		}
	}
	return n + slashes // trailing backslashes double before the closing quote
}

func findAgy() (string, error) {
	if v := os.Getenv("HAL_AGY_BIN"); v != "" {
		// Absolutize: a relative override would resolve against cmd.Dir —
		// the agent's WORKTREE — so a previous pass committing a file at
		// that relative path would get executed as the agent binary.
		abs, err := filepath.Abs(v)
		if err != nil {
			return "", fmt.Errorf("driver: HAL_AGY_BIN %q: %w", v, err)
		}
		if info, err := os.Stat(abs); err != nil || info.IsDir() {
			return "", fmt.Errorf("driver: HAL_AGY_BIN %q is not an executable file", v)
		}
		return abs, nil
	}
	if exe, err := exec.LookPath("agy"); err == nil {
		return exe, nil
	}
	// Known install locations (the installer updates the registry PATH,
	// which shells started earlier don't see).
	var candidates []string
	if v := os.Getenv("LOCALAPPDATA"); v != "" {
		candidates = append(candidates, filepath.Join(v, "agy", "bin", "agy.exe"))
	}
	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates,
			filepath.Join(home, ".local", "bin", "agy"),
			filepath.Join(home, "AppData", "Local", "agy", "bin", "agy.exe"),
		)
	}
	candidates = append(candidates, "/usr/local/bin/agy")
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c, nil
		}
	}
	return "", fmt.Errorf("driver: agy not found on PATH or known install locations (set HAL_AGY_BIN)")
}
