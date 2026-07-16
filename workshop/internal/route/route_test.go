package route

import (
	"testing"

	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/domain"
)

var (
	pipelineBundle = domain.Bundle{Agent: "claude", Model: "claude-sonnet-5", Effort: "medium"}
	typesTable     = map[string]domain.Bundle{
		"code": {Agent: "claude", Model: "claude-opus-4-8", Effort: "high"},
		"art":  {Agent: "agy", Model: "gemini-3-flash"},
		"docs": {Effort: "low"}, // partial: same-agent fallthrough
	}
)

func TestResolvePrecedence(t *testing.T) {
	cases := []struct {
		name string
		task *domain.Task
		want domain.Bundle
	}{
		{"nil task -> pipeline", nil, pipelineBundle},
		{"untyped -> pipeline", &domain.Task{}, pipelineBundle},
		{"typed -> table", &domain.Task{Type: "code"},
			domain.Bundle{Agent: "claude", Model: "claude-opus-4-8", Effort: "high"}},
		{"unknown type -> pipeline", &domain.Task{Type: "mystery"}, pipelineBundle},
		{"pin beats table", &domain.Task{Type: "code", Pin: domain.Bundle{Model: "claude-fable-5", Effort: "max"}},
			domain.Bundle{Agent: "claude", Model: "claude-fable-5", Effort: "max"}},
		{"partial table inherits same-agent fields", &domain.Task{Type: "docs"},
			domain.Bundle{Agent: "claude", Model: "claude-sonnet-5", Effort: "low"}},
	}
	for _, c := range cases {
		if got := Resolve(c.task, domain.Bundle{}, typesTable, pipelineBundle); got != c.want {
			t.Errorf("%s: got %+v want %+v", c.name, got, c.want)
		}
	}
}

// The cross-agent guard: switching agents must never inherit the other
// agent's model or effort (bad agy ids fail silently).
func TestResolveCrossAgentDropsModel(t *testing.T) {
	got := Resolve(&domain.Task{Type: "art"}, domain.Bundle{}, typesTable, pipelineBundle)
	want := domain.Bundle{Agent: "agy", Model: "gemini-3-flash"}
	if got != want {
		t.Fatalf("got %+v want %+v", got, want)
	}

	// Pin to a different agent without a model: pipeline model must drop.
	got = Resolve(&domain.Task{Pin: domain.Bundle{Agent: "agy"}}, domain.Bundle{}, typesTable, pipelineBundle)
	want = domain.Bundle{Agent: "agy"}
	if got != want {
		t.Fatalf("pin cross-agent: got %+v want %+v", got, want)
	}
}

func TestClassify(t *testing.T) {
	vocab := Vocabulary(typesTable, []domain.Pipeline{
		{TaskTypes: []string{"tests", "audio"}},
	})
	for _, want := range []string{"code", "art", "docs", "tests", "audio"} {
		if !vocab[want] {
			t.Fatalf("vocabulary missing %q: %v", want, vocab)
		}
	}

	cases := []struct{ title, want string }{
		{"resolve the merge conflict in the save system", ""}, // merge-conflict not in vocab
		{"add hit sound effects and music stings", "audio"},
		{"repaint the player sprite palette", "art"},
		{"write unit tests for the store", "tests"},
		{"update the README with install docs", "docs"},
		{"fix the crash when saving", "code"},
		{"ponder the meaning of life", ""},
	}
	for _, c := range cases {
		if got := Classify(c.title, "", vocab); got != c.want {
			t.Errorf("%q: got %q want %q", c.title, got, c.want)
		}
	}

	// merge-conflict classifies once it's in the vocabulary, and beats
	// other matching rules ("art" appears in the branch name).
	vocab["merge-conflict"] = true
	if got := Classify("resolve the merge conflict on workshop/art", "", vocab); got != "merge-conflict" {
		t.Fatalf("merge-conflict: got %q", got)
	}
	// Empty vocabulary: never classify.
	if got := Classify("fix the crash", "", nil); got != "" {
		t.Fatalf("empty vocab: got %q", got)
	}
}

// Art GENERATION (an image file is the deliverable) routes to the agy
// art-gen types; art-flavored code work stays plain "art".
func TestClassifyArtGeneration(t *testing.T) {
	vocab := map[string]bool{
		"art": true, "code": true,
		domain.ArtGenType: true, domain.ArtGenTransType: true,
	}
	cases := []struct{ title, detail, want string }{
		{"Generate a pixel art sprite of the space explorer", "", "art-gen"},
		{"Create a title-screen banner image", "1920x480, nebula theme", "art-gen"},
		{"draw concept art for the boss", "", "art-gen"},
		{"Generate a sprite of the merchant", "must have a transparent background", "art-gen-trans"},
		{"Create the hero icon with no background", "", "art-gen-trans"},
		{"transparent cursor needed — please generate the image", "", "art-gen-trans"},
		{"fix the sprite flicker on jump", "", "art"},           // no generation verb
		{"repaint the player sprite palette", "", "art"},        // code-side art work
		{"make the loading screen faster", "", ""},              // generation verb but no image-asset noun
		{"add transparency support to the renderer", "", "code"}, // transparency alone isn't art-gen
	}
	for _, c := range cases {
		if got := Classify(c.title, c.detail, vocab); got != c.want {
			t.Errorf("%q / %q: got %q want %q", c.title, c.detail, got, c.want)
		}
	}
}
