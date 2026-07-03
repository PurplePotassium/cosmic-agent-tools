package domain

import "testing"

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
