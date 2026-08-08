package domain

import "testing"

func TestNormalizeType(t *testing.T) {
	cases := map[string]string{
		"code":           "code",
		"Code":           "code",
		"  AUDIO  ":      "audio",
		"merge-conflict": "merge-conflict",
		"level_design":   "level_design",
		"":               "",
		"c o d e":        "",
		"../../etc":      "",
		"types\\..\\x":   "",
		"art.md":         "",
	}
	for in, want := range cases {
		if got := NormalizeType(in); got != want {
			t.Errorf("NormalizeType(%q) = %q, want %q", in, got, want)
		}
	}
}

// Art orchestrators are frontier-only: fable/opus of ANY version qualify
// (prefix match, case-insensitive), weaker families and other agents' ids
// never do.
func TestAllowedArtClaudeModel(t *testing.T) {
	for _, m := range []string{"claude-fable-5", "claude-fable-6", "Claude-Opus-4-8", ArtClaudeDefault} {
		if !AllowedArtClaudeModel(m) {
			t.Errorf("AllowedArtClaudeModel(%q) = false, want true", m)
		}
	}
	for _, m := range []string{"", "claude-sonnet-5", "claude-haiku-4-5-20251001", "Gemini 3.1 Pro (High)", "fable"} {
		if AllowedArtClaudeModel(m) {
			t.Errorf("AllowedArtClaudeModel(%q) = true, want false", m)
		}
	}
	if !AllowedArtClaudeModel(ArtClaudeDefault) {
		t.Errorf("the default %q must itself be allowed", ArtClaudeDefault)
	}
}

func TestCodexModelFamily(t *testing.T) {
	for _, model := range CodexModels {
		if !KnownModel("codex", model) {
			t.Errorf("KnownModel(codex, %q) = false", model)
		}
	}
	for _, model := range []string{"gpt-5.6", "gpt-5.6-solar", "claude-sonnet-5"} {
		if KnownModel("codex", model) {
			t.Errorf("KnownModel(codex, %q) = true, want false", model)
		}
	}
}
