package config

import (
	"testing"

	"github.com/PurplePotassium/cosmic-agent-tools/hal/internal/domain"
)

func TestClaudeModelRefs(t *testing.T) {
	c := Default()
	c.Types = map[string]domain.Bundle{
		"inquiry": {Agent: "claude", Model: "claude-opus-5"},
		"code":    {Agent: "claude", Model: "claude-sonnet-5"},
		// Another agent's id must never be handed to the claude probe.
		"art-gen": {Agent: "agy", Model: "Gemini 3.1 Pro (High)"},
		// An agent with no model override contributes nothing.
		"audio": {Agent: "claude"},
	}
	c.Workflow.Stages = map[string]StageBundle{
		// Same id as types.inquiry — one probe, not two.
		"design":    {Model: "claude-opus-5"},
		"research":  {Model: "claude-fable-5"},
		"implement": {Effort: "max"},
	}

	got := c.ClaudeModelRefs()
	want := []ModelRef{
		{Where: "types.code", Agent: "claude", Model: "claude-sonnet-5"},
		{Where: "types.inquiry", Agent: "claude", Model: "claude-opus-5"},
		{Where: "workflow.stages.research", Agent: "claude", Model: "claude-fable-5"},
	}
	if len(got) != len(want) {
		t.Fatalf("ClaudeModelRefs() = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("ref %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// Ordering must be stable across runs — the refs drive startup error text and
// doctor output, and Go map iteration is randomized.
func TestClaudeModelRefsStableOrder(t *testing.T) {
	c := Default()
	c.Workflow.Stages = map[string]StageBundle{
		"validate": {Model: "claude-sonnet-5"},
		"design":   {Model: "claude-opus-5"},
		"plan":     {Model: "claude-fable-5"},
	}
	first := c.ClaudeModelRefs()
	for range 20 {
		if got := c.ClaudeModelRefs(); got != nil && len(got) == len(first) {
			for i := range first {
				if got[i] != first[i] {
					t.Fatalf("order drifted: %+v then %+v", first, got)
				}
			}
		} else {
			t.Fatalf("length drifted: %+v then %+v", first, got)
		}
	}
}

// An empty config asks for no probes at all — the common case must cost
// nothing at startup.
func TestClaudeModelRefsEmpty(t *testing.T) {
	c := Default()
	if refs := c.ClaudeModelRefs(); len(refs) != 0 {
		t.Fatalf("default config wants %d probes, expected 0: %+v", len(refs), refs)
	}
}
