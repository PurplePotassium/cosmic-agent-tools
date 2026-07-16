package config

import (
	"strings"
	"testing"

	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/domain"
)

func TestArtDefaults(t *testing.T) {
	c := Default()
	if c.Art.Remover != "builtin" {
		t.Fatalf("default remover = %q; want builtin", c.Art.Remover)
	}
	for _, ty := range []string{domain.ArtGenType, domain.ArtGenTransType} {
		b, ok := c.Types[ty]
		if !ok || b.Agent != "agy" {
			t.Fatalf("built-in type %s = %+v (present=%v); want agent agy", ty, b, ok)
		}
		if b.Model != "" {
			t.Fatalf("built-in type %s pins model %q; the engine picks the verified label", ty, b.Model)
		}
	}
}

func TestArtRemoverValidation(t *testing.T) {
	dir := t.TempDir()
	repo := writeFile(t, dir, "config.toml", "[art]\nremover = \"photoshop\"\n")
	if _, err := Load("", repo, "", noEnv); err == nil || !strings.Contains(err.Error(), "art.remover") {
		t.Fatalf("bad remover should block load; got %v", err)
	}
	repo = writeFile(t, dir, "config2.toml", "[art]\nremover = \"corridorkey\"\ncorridorkey_dir = 'C:\\tools\\ck'\n")
	res, err := Load("", repo, "", noEnv)
	if err != nil {
		t.Fatal(err)
	}
	if res.Config.Art.Remover != "corridorkey" || res.Config.Art.CorridorkeyDir != `C:\tools\ck` {
		t.Fatalf("art config = %+v", res.Config.Art)
	}
}

// The agy family match is case-insensitive: agy labels are display strings
// ("Gemini 3.1 Pro (High)"), the curated family prefix is lowercase.
func TestAgyModelFamilyCaseInsensitive(t *testing.T) {
	c := Default()
	if err := c.checkModel("types.art-gen", "agy", "Gemini 3.1 Pro (High)"); err != nil {
		t.Fatalf("display-cased gemini label should be known: %v", err)
	}
	if err := c.checkModel("types.art-gen", "agy", "claude-opus-4-8"); err == nil {
		t.Fatal("a claude id on agy should warn")
	}
	if !domain.AllowedArtModel("gemini 3.1 pro (high)") || domain.AllowedArtModel("gemini 3 pro") {
		t.Fatal("AllowedArtModel: want case-insensitive exact-label matching only")
	}
}
