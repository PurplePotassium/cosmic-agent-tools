package driver

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
	}
	a.probed = true
	return a.caps, nil
}

// Plan builds the invocation: prompt as -p arg, own hidden console.
func (a *Agy) Plan(spec InvokeSpec) (ExecPlan, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
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
	if spec.OpLogPath != "" {
		args = append(args, "--log-file", spec.OpLogPath)
	}
	args = append(args, spec.ExtraArgs...)
	args = append(args, "-p", spec.Prompt)
	return ExecPlan{Exe: a.exe, Args: args, StdinPrompt: false, Mode: a.caps.SpawnMode()}, nil
}

func findAgy() (string, error) {
	if v := os.Getenv("WORKSHOP_AGY_BIN"); v != "" {
		return v, nil
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
	return "", fmt.Errorf("driver: agy not found on PATH or known install locations (set WORKSHOP_AGY_BIN)")
}
