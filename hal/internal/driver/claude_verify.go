package driver

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// ClaudeModelRejectedMarker is the sentence Claude Code prints when --model
// names an id it will not run — either the id doesn't exist or the account
// has no access to it. Verified against v2.1.225:
//
//	$ claude -p --model claude-opus-4-6-fast
//	There's an issue with the selected model (claude-opus-4-6-fast). It may
//	not exist or you may not have access to it. Run --model to pick a
//	different model.
//
// It is printed BEFORE the prompt reaches the API and no response follows, so
// probing a BAD id is quota-free; a GOOD id costs one minimal turn. That
// asymmetry is why verdicts are cached (internal/modelcheck).
//
// Like agy's "Available models:" dump (AGENTS.md), this is an English UI
// string a Claude Code release can reword. The failure direction is
// deliberate: a reworded marker makes every id read as VALID, so verification
// degrades to today's behaviour instead of inventing failures for models that
// actually work.
const ClaudeModelRejectedMarker = "There's an issue with the selected model"

// claudeModelProbePrompt keeps a passing probe's turn as small as possible —
// the probe only cares whether the marker appears, never what was said back.
const claudeModelProbePrompt = "Reply with exactly: ok"

// ClaudeModelRejected reports whether combined claude output carries the
// rejected-model marker. Split out so the parse is testable without spawning.
func ClaudeModelRejected(output string) bool {
	return strings.Contains(output, ClaudeModelRejectedMarker)
}

// binary returns the probed executable path under the lock. Probe takes the
// same lock, so callers must Probe first and read the path separately rather
// than holding it across both.
func (c *Claude) binary() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.exe
}

// VerifyModel reports whether the installed Claude Code can actually run
// model, by running the smallest possible print-mode turn and looking for
// ClaudeModelRejectedMarker.
//
// A false return is definitive ("this id will not run here"). An error means
// the probe itself could not be carried out — claude missing, timeout, dead
// auth — which is NOT evidence against the model and must never be treated as
// one; callers surface it as unknown.
//
// The probe runs in a temp dir, not the repo: a repo cwd would load CLAUDE.md
// and the project's hooks into a throwaway turn, spending tokens on context
// the probe ignores.
func (c *Claude) VerifyModel(ctx context.Context, model string) (bool, error) {
	if model == "" {
		return true, nil // "" means "engine default" — nothing to verify
	}
	if _, err := c.Probe(ctx); err != nil {
		return false, err
	}
	exe := c.binary()
	dir, err := os.MkdirTemp("", "hal-model-probe-")
	if err != nil {
		return false, err
	}
	defer os.RemoveAll(dir)

	pctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(pctx, exe, "-p", "--model", model, "--max-turns", "1")
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(claudeModelProbePrompt)
	// Same WaitDelay reasoning as Probe: an npm shim's node child can hold the
	// pipe open past the timeout and wedge CombinedOutput forever.
	cmd.WaitDelay = 2 * time.Second
	out, runErr := cmd.CombinedOutput()

	// Check the marker before the exit code: claude reports a rejected model
	// in its output, and the exit status for that case is not contractual.
	if ClaudeModelRejected(string(out)) {
		return false, nil
	}
	if runErr != nil {
		return false, fmt.Errorf("driver: claude model probe for %q did not complete: %w", model, runErr)
	}
	return true, nil
}

// Version returns the installed Claude Code version string. modelcheck keys
// its verdict cache on it — which models exist and which an account may reach
// both move with releases, so a verdict from another version is not evidence
// about this one.
func (c *Claude) Version(ctx context.Context) (string, error) {
	if _, err := c.Probe(ctx); err != nil {
		return "", err
	}
	vctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(vctx, c.binary(), "--version")
	cmd.WaitDelay = 2 * time.Second
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("driver: claude --version: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}
