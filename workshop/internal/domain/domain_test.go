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
