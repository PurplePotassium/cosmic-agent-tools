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

func TestPipelineMode(t *testing.T) {
	cases := []struct {
		name            string
		invent          bool
		acceptProposals bool
		want            string
	}{
		{"goal", true, true, "goal"},
		{"discover", false, true, "discover"},
		{"drain", false, false, "drain"},
	}
	for _, c := range cases {
		p := Pipeline{Invent: c.invent, AcceptProposals: c.acceptProposals}
		if got := p.Mode(); got != c.want {
			t.Errorf("%s: Mode() = %q, want %q", c.name, got, c.want)
		}
	}
}

// TestModeFlags pins ModeFlags as Mode's exact inverse, since the live
// dashboard override round-trips a mode name back to flags.
func TestModeFlags(t *testing.T) {
	cases := []struct {
		name            string
		invent          bool
		acceptProposals bool
	}{
		{"goal", true, true},
		{"discover", false, true},
		{"drain", false, false},
	}
	for _, c := range cases {
		invent, acceptProposals := ModeFlags(c.name)
		if invent != c.invent || acceptProposals != c.acceptProposals {
			t.Errorf("ModeFlags(%q) = (%v, %v), want (%v, %v)", c.name, invent, acceptProposals, c.invent, c.acceptProposals)
		}
	}
}

func TestValidMode(t *testing.T) {
	for _, m := range []string{"", "goal", "discover", "drain"} {
		if !ValidMode(m) {
			t.Errorf("ValidMode(%q) = false, want true", m)
		}
	}
	if ValidMode("bogus") {
		t.Error("ValidMode(\"bogus\") = true, want false")
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

func TestCodexModelFamilies(t *testing.T) {
	for _, model := range CodexModels {
		if !KnownModel("codex", model) {
			t.Errorf("KnownModel(codex, %q) = false, want true", model)
		}
	}
	if KnownModel("codex", "gpt-5.6-unknown") {
		t.Fatal("an uncurated Codex model must not be treated as known")
	}
}
