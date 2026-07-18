package config

import (
	"strings"
	"testing"

	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/domain"
)

func TestArtDefaults(t *testing.T) {
	c := Default()
	if c.Art.Remover != "ffmpeg" {
		t.Fatalf("default remover = %q; want ffmpeg", c.Art.Remover)
	}
	for _, ty := range []string{domain.ArtGenType, domain.ArtGenTransType} {
		b, ok := c.Types[ty]
		if !ok || b.Agent != "claude" {
			t.Fatalf("built-in type %s = %+v (present=%v); want agent claude (the art orchestrator)", ty, b, ok)
		}
		if b.Model != "" {
			t.Fatalf("built-in type %s pins model %q; the engine forces a frontier family at pass time", ty, b.Model)
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

func TestArtRemoversListValidation(t *testing.T) {
	dir := t.TempDir()
	repo := writeFile(t, dir, "bad.toml", "[art]\nremovers = [\"ffmpeg\", \"photoshop\"]\n")
	if _, err := Load("", repo, "", noEnv); err == nil || !strings.Contains(err.Error(), "art.removers") {
		t.Fatalf("bad removers entry should block load; got %v", err)
	}
	repo = writeFile(t, dir, "dup.toml", "[art]\nremovers = [\"ffmpeg\", \"ffmpeg\"]\n")
	if _, err := Load("", repo, "", noEnv); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate removers entry should block load; got %v", err)
	}
	repo = writeFile(t, dir, "good.toml", "[art]\nremovers = [\"corridorkey\", \"ffmpeg\"]\n")
	res, err := Load("", repo, "", noEnv)
	if err != nil {
		t.Fatal(err)
	}
	if got := res.Config.ArtKeyers(); len(got) != 2 || got[0] != "corridorkey" || got[1] != "ffmpeg" {
		t.Fatalf("ArtKeyers = %v; want [corridorkey ffmpeg] (order preserved, primary first)", got)
	}
}

func TestArtKeyersResolution(t *testing.T) {
	c := Default()
	if got := c.ArtKeyers(); len(got) != 1 || got[0] != "ffmpeg" {
		t.Fatalf("default ArtKeyers = %v; want [ffmpeg]", got)
	}
	c.Art.Remover = "corridorkey"
	if got := c.ArtKeyers(); len(got) != 1 || got[0] != "corridorkey" {
		t.Fatalf("single-remover ArtKeyers = %v; want [corridorkey]", got)
	}
	// removers beats remover, and the resolved copy is deduped.
	c.Art.Removers = []string{"corridorkey", "ffmpeg", "corridorkey"}
	if got := c.ArtKeyers(); len(got) != 2 || got[0] != "corridorkey" || got[1] != "ffmpeg" {
		t.Fatalf("list ArtKeyers = %v; want [corridorkey ffmpeg]", got)
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
