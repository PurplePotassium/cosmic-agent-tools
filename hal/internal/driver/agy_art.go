package driver

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/PurplePotassium/cosmic-agent-tools/hal/internal/proc"
)

// agy conversation/model plumbing for the art-generation flow. Upstream facts
// this file encodes (verified empirically 2026-07 — see AGENTS.md):
//
//   - agy keys "the most recent conversation per workspace directory" in
//     <state>/cache/last_conversations.json ({"<abs workdir>": "<uuid>"}).
//     That file is the ONLY headless way to learn the conversation id a
//     `-p` run just used; `agy --conversation <id>` resumes it. The file is
//     rewritten whole per run — concurrent agy processes race it, which is
//     WHY art passes hold the exclusive agy lock.
//   - An invalid `--model` label makes print mode exit 1 BEFORE sending the
//     prompt, and the operational log (--log-file) then contains the full
//     "Available models:" list. Probing with a deliberately-bogus label is
//     therefore a free, quota-less way to enumerate valid labels headless
//     (`agy models` needs a real TTY and hangs on pipes).

// agyStateDir returns agy's mutable state directory. HAL_AGY_STATE_DIR
// overrides it (tests, relocated installs).
func agyStateDir() (string, error) {
	if v := os.Getenv("HAL_AGY_STATE_DIR"); v != "" {
		return v, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("driver: agy state dir: %w", err)
	}
	return filepath.Join(home, ".gemini", "antigravity-cli"), nil
}

// AgyLastConversation returns the id of the most recent agy conversation
// started in workDir ("" when none is recorded). Windows paths compare
// case-insensitively — agy records the cwd string it was spawned with, which
// may differ in casing from the caller's.
func AgyLastConversation(workDir string) (string, error) {
	stateDir, err := agyStateDir()
	if err != nil {
		return "", err
	}
	raw, err := os.ReadFile(filepath.Join(stateDir, "cache", "last_conversations.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("driver: agy last_conversations.json: %w", err)
	}
	var m map[string]string
	if err := json.Unmarshal(raw, &m); err != nil {
		return "", fmt.Errorf("driver: agy last_conversations.json: %w", err)
	}
	abs, err := filepath.Abs(workDir)
	if err != nil {
		abs = workDir
	}
	want := strings.ToLower(filepath.Clean(abs))
	for k, v := range m {
		if strings.ToLower(filepath.Clean(k)) == want {
			return v, nil
		}
	}
	return "", nil
}

// AgyTranscriptPath returns where agy leaves the full transcript of one
// conversation (verified 2026-07-16): JSONL steps carrying the USER_INPUT
// content exactly as the model saw it, the MODEL response `content` AND its
// `thinking` reasoning summary, plus SYSTEM checkpoint/reminder steps.
// Written unconditionally per conversation — no flag involved (`--log-file`
// is only the operational log). Callers stat before trusting it: agy owns
// the directory and may prune old conversations.
func AgyTranscriptPath(conversationID string) (string, error) {
	stateDir, err := agyStateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(stateDir, "brain", conversationID, ".system_generated", "logs", "transcript.jsonl"), nil
}

// AgyExec runs agy with args verbatim in workDir and returns its exit code.
// The spawn is consoled — agy silently drops output (and can hang) on piped
// stdio, so it must own a real hidden console; nothing is captured, the
// caller reads the op log (--log-file) and agy's own artifacts instead. This
// is the machinery behind `hal agy-run`, the passthrough the art
// orchestrator (a claude pass) uses to invoke agy safely from its shell.
func AgyExec(ctx context.Context, workDir string, args []string) (int, error) {
	exe, err := findAgy()
	if err != nil {
		return -1, err
	}
	cmd := exec.CommandContext(ctx, exe, args...)
	cmd.Dir = workDir
	cmd.Cancel = func() error {
		if cmd.Process != nil {
			return proc.KillTree(cmd.Process.Pid)
		}
		return nil
	}
	cmd.WaitDelay = 15 * time.Second
	proc.Configure(cmd, proc.Consoled)
	if err := cmd.Start(); err != nil {
		return -1, fmt.Errorf("driver: agy-run: %w", err)
	}
	proc.Adopt(cmd.Process.Pid)
	defer proc.Finished(cmd.Process.Pid)
	waitErr := cmd.Wait()
	code := cmd.ProcessState.ExitCode()
	if waitErr != nil && code == 0 {
		code = -1
	}
	return code, nil
}

// modelProbeLabel is deliberately never a real model: the probe RELIES on agy
// rejecting it (exit 1 + the "Available models:" dump in the op log).
const modelProbeLabel = "hal-model-probe"

// AgyListModels enumerates the model labels agy currently offers, headless.
// It spawns one consoled agy print run with an invalid --model (rejected
// before any prompt is sent — no tokens spent) and parses the operational
// log. Requires agy to be logged in; an empty list with nil error means the
// probe ran but agy printed no list (usually dead auth).
func (a *Agy) ListModels(ctx context.Context) ([]string, error) {
	if _, err := a.Probe(ctx); err != nil {
		return nil, err
	}
	logDir, err := os.MkdirTemp("", "hal-agy-probe-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(logDir)
	logPath := filepath.Join(logDir, "probe.op.log")

	pctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(pctx, a.exe,
		"--print-timeout", "1m",
		"--model", modelProbeLabel,
		"--log-file", logPath,
		"-p", "noop")
	cmd.Dir = logDir
	cmd.Cancel = func() error {
		if cmd.Process != nil {
			return proc.KillTree(cmd.Process.Pid)
		}
		return nil
	}
	cmd.WaitDelay = 5 * time.Second
	// Consoled, no redirection: agy drops/hangs on piped stdio (AGENTS.md).
	proc.Configure(cmd, proc.Consoled)
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("driver: agy model probe: %w", err)
	}
	proc.Adopt(cmd.Process.Pid)
	defer proc.Finished(cmd.Process.Pid)
	_ = cmd.Wait() // exit 1 is the EXPECTED outcome (invalid model)
	if pctx.Err() != nil {
		return nil, fmt.Errorf("driver: agy model probe timed out (agy wedged or not logged in — run `agy` interactively once)")
	}

	raw, err := os.ReadFile(logPath)
	if err != nil {
		return nil, fmt.Errorf("driver: agy model probe wrote no op log: %w", err)
	}
	return ParseAgyAvailableModels(string(raw)), nil
}

// ParseAgyAvailableModels extracts the "Available models:" block agy prints
// into its operational log when a --model label is rejected. Model lines are
// indented; the block ends at the first non-indented line.
func ParseAgyAvailableModels(opLog string) []string {
	var models []string
	inBlock := false
	for _, line := range strings.Split(opLog, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "Available models:") {
			inBlock = true
			continue
		}
		if !inBlock {
			continue
		}
		if !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") {
			break
		}
		if m := strings.TrimSpace(strings.TrimRight(line, "\r")); m != "" {
			models = append(models, m)
		}
	}
	return models
}

// AgyHasModel reports whether label appears in models (case-insensitive —
// labels are display strings).
func AgyHasModel(models []string, label string) bool {
	for _, m := range models {
		if strings.EqualFold(m, label) {
			return true
		}
	}
	return false
}
