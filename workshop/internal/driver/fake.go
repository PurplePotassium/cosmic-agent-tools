package driver

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

// Fake is the test/smoke driver. It execs whatever WORKSHOP_FAKE_BIN points
// at (usually the test binary re-entering as a scripted agent) with the
// spec's ExtraArgs verbatim, prompt on stdin, fully capturable — so the whole
// engine state machine can be driven deterministically without a real agent.
type Fake struct{}

// NewFake returns the fake driver.
func NewFake() *Fake { return &Fake{} }

func (f *Fake) Name() string { return "fake" }

// fakeBin resolves WORKSHOP_FAKE_BIN with the same absolutize+stat guard
// findClaude applies to WORKSHOP_CLAUDE_BIN: a relative override would resolve
// against the agent's worktree cwd, so a file a previous pass committed at that
// path could be executed as the driver binary.
func fakeBin() (string, error) {
	v := os.Getenv("WORKSHOP_FAKE_BIN")
	if v == "" {
		return "", fmt.Errorf("driver: fake agent needs WORKSHOP_FAKE_BIN")
	}
	abs, err := filepath.Abs(v)
	if err != nil {
		return "", fmt.Errorf("driver: WORKSHOP_FAKE_BIN %q: %w", v, err)
	}
	if info, err := os.Stat(abs); err != nil || info.IsDir() {
		return "", fmt.Errorf("driver: WORKSHOP_FAKE_BIN %q is not an executable file", v)
	}
	return abs, nil
}

func (f *Fake) Probe(context.Context) (Capabilities, error) {
	if _, err := fakeBin(); err != nil {
		return Capabilities{}, err
	}
	if os.Getenv("WORKSHOP_FAKE_BLIND") == "1" {
		// Simulate a blind driver (agy-shaped) for engine tests.
		return Capabilities{
			PromptVia:              PromptArg,
			Capture:                CaptureNone,
			ModelSelect:            true,
			ConversationTranscript: true,
		}, nil
	}
	// ConversationTranscript in the capturable shape too: the art tests seed
	// the fake into the "agy" driver slot non-blind (so the fake agent can
	// read the prompt), and the fake agent mimics agy's conversation
	// bookkeeping + brain transcript whenever WORKSHOP_AGY_STATE_DIR is set.
	return Capabilities{
		PromptVia:              PromptStdin,
		Capture:                CaptureStreaming,
		ModelSelect:            true,
		Effort:                 true,
		AuthProbe:              true,
		ConversationTranscript: true,
	}, nil
}

func (f *Fake) Plan(spec InvokeSpec) (ExecPlan, error) {
	exe, err := fakeBin()
	if err != nil {
		return ExecPlan{}, err
	}
	caps, _ := f.Probe(context.Background())
	args := append([]string{"_fake-agent"}, spec.ExtraArgs...)
	return ExecPlan{
		Exe:         exe,
		Args:        args,
		StdinPrompt: caps.PromptVia == PromptStdin,
		Mode:        caps.SpawnMode(),
	}, nil
}
