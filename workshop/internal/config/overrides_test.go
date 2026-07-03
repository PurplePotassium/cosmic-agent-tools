package config

import (
	"path/filepath"
	"testing"
)

func TestValidPipelineName(t *testing.T) {
	for _, tc := range []struct {
		name string
		want bool
	}{
		{"art", true},
		{"art-2", true},
		{"Art_Team", true}, // case-insensitive
		{"", false},
		{"-art", false},       // must start with letter/digit
		{"art team", false},   // no spaces
		{SharedBacklogName, false}, // reserved
	} {
		if got := ValidPipelineName(tc.name); got != tc.want {
			t.Errorf("ValidPipelineName(%q) = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestWriteOverridePipelinesPreservesOtherKeysAndRoundTrips: the override
// layer replaces arrays wholesale (mergeMaps), so WriteOverridePipelines must
// still leave any other already-written override keys (e.g. a live
// server.port override) untouched, and the written pipelines must decode
// back correctly through the normal Load path, on top of a repo config.
func TestWriteOverridePipelinesPreservesOtherKeysAndRoundTrips(t *testing.T) {
	dir := t.TempDir()
	repo := writeFile(t, dir, "repo.toml", `
[[pipelines]]
name = "code"
agent = "claude"
`)
	overrides := filepath.Join(dir, "overrides.toml")
	writeFile(t, dir, "overrides.toml", "[server]\nport = 6000\n")

	newSet := []PipelineConfig{
		{Name: "code", Agent: "claude"},
		{Name: "art", Agent: "agy", Model: "gemini-3-flash"},
	}
	if err := WriteOverridePipelines(overrides, newSet); err != nil {
		t.Fatal(err)
	}

	res, err := Load("", repo, overrides, noEnv)
	if err != nil {
		t.Fatal(err)
	}
	if res.Config.Server.Port != 6000 {
		t.Fatalf("server.port = %d, want the untouched override 6000", res.Config.Server.Port)
	}
	pl := res.Config.ResolvedPipelines()
	if len(pl) != 2 || pl[0].Name != "code" || pl[1].Name != "art" || pl[1].Bundle.Agent != "agy" {
		t.Fatalf("pipelines after override write: %+v", pl)
	}

	// Writing again must replace, not append, the pipelines array.
	if err := WriteOverridePipelines(overrides, []PipelineConfig{{Name: "solo", Agent: "claude"}}); err != nil {
		t.Fatal(err)
	}
	res, err = Load("", repo, overrides, noEnv)
	if err != nil {
		t.Fatal(err)
	}
	pl = res.Config.ResolvedPipelines()
	if len(pl) != 1 || pl[0].Name != "solo" {
		t.Fatalf("pipelines after second override write: %+v", pl)
	}
}
