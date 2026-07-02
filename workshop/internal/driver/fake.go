package driver

import (
	"context"
	"fmt"
	"os"
)

// Fake is the test/smoke driver. It execs whatever WORKSHOP_FAKE_BIN points
// at (usually the test binary re-entering as a scripted agent) with the
// spec's ExtraArgs verbatim, prompt on stdin, fully capturable — so the whole
// engine state machine can be driven deterministically without a real agent.
type Fake struct{}

// NewFake returns the fake driver.
func NewFake() *Fake { return &Fake{} }

func (f *Fake) Name() string { return "fake" }

func (f *Fake) Probe(context.Context) (Capabilities, error) {
	if os.Getenv("WORKSHOP_FAKE_BIN") == "" {
		return Capabilities{}, fmt.Errorf("driver: fake agent needs WORKSHOP_FAKE_BIN")
	}
	return Capabilities{
		PromptVia:   PromptStdin,
		Capture:     CaptureStreaming,
		ModelSelect: true,
		Effort:      true,
		AuthProbe:   true,
	}, nil
}

func (f *Fake) Plan(spec InvokeSpec) (ExecPlan, error) {
	exe := os.Getenv("WORKSHOP_FAKE_BIN")
	if exe == "" {
		return ExecPlan{}, fmt.Errorf("driver: fake agent needs WORKSHOP_FAKE_BIN")
	}
	caps, _ := f.Probe(context.Background())
	return ExecPlan{Exe: exe, Args: spec.ExtraArgs, StdinPrompt: true, Mode: caps.SpawnMode()}, nil
}
