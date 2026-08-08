package driver

import "testing"

// Captured from Claude Code v2.1.225. `claude -p --model claude-opus-4-6-fast`
// (a model id the binary still recognizes but no longer serves) prints exactly
// this and no response — the signal VerifyModel keys on.
const sampleRejectedOutput = `There's an issue with the selected model (claude-opus-4-6-fast). It may not exist or you may not have access to it. Run --model to pick a different model.`

// An id the CLI does not recognize at all additionally warns about the context
// window it has to assume. Both lines appear; the rejection is still the one
// that decides the verdict.
const sampleUnknownOutput = `"definitely-not-a-real-model" is not a model this version of Claude Code recognizes, so auto-compact will keep this session within 200k tokens (the context window it assumes).
There's an issue with the selected model (definitely-not-a-real-model). It may not exist or you may not have access to it. Run --model to pick a different model.`

func TestClaudeModelRejected(t *testing.T) {
	for name, out := range map[string]string{
		"recognized but unavailable": sampleRejectedOutput,
		"unrecognized id":            sampleUnknownOutput,
	} {
		if !ClaudeModelRejected(out) {
			t.Errorf("%s: ClaudeModelRejected = false, want true", name)
		}
	}

	// A normal turn must never read as a rejection: a false positive here
	// blocks startup for a model that works.
	for name, out := range map[string]string{
		"plain reply": "ok",
		"empty":       "",
		// The context-window warning fires for ids the binary doesn't know but
		// the API may still serve (a model newer than the installed CLI). On
		// its own it is NOT a rejection.
		"unrecognized but served": `"claude-opus-9" is not a model this version of Claude Code recognizes, so auto-compact will keep this session within 200k tokens.
ok`,
	} {
		if ClaudeModelRejected(out) {
			t.Errorf("%s: ClaudeModelRejected = true, want false", name)
		}
	}
}

// An empty model means "engine default" — there is nothing to ask the CLI
// about, and probing must not spawn anything (or charge a turn) for it.
func TestVerifyModelEmptyIsTriviallyValid(t *testing.T) {
	c := NewClaude() // deliberately unprobed: a spawn would panic on the empty exe
	ok, err := c.VerifyModel(t.Context(), "")
	if err != nil || !ok {
		t.Fatalf("VerifyModel(\"\") = %v, %v; want true, nil", ok, err)
	}
}
