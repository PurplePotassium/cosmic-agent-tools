package prompt

import (
	"math/rand"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/domain"
)

// A non-ASCII pool entry must not be byte-sliced mid-rune: the recode stem has
// to stay valid UTF-8 (byte-slicing "Éclair" split the leading two-byte rune).
func TestSpiceRecodeNonASCII(t *testing.T) {
	nouns := []string{"Éclair", "Ångström", "Über"}
	for seed := int64(0); seed < 200; seed++ {
		rng := rand.New(rand.NewSource(seed))
		s := NewSpice(rng, nil, nouns)
		if !strings.HasPrefix(s.Mode, "recode:") {
			t.Fatalf("nouns-only should recode, got %q", s.Mode)
		}
		if !utf8.ValidString(s.Mode) || !utf8.ValidString(s.Suffix) {
			t.Fatalf("recode produced invalid UTF-8: mode=%q suffix=%q", s.Mode, s.Suffix)
		}
	}
}

func stableInputs() Inputs {
	return Inputs{
		BaseContract: BaseContract(),
		Mechanics: Mechanics(MechanicsInputs{
			StateDir:  `C:\state\projects\demo`,
			RepoDir:   `C:\repos\demo`,
			Branch:    "workshop/code",
			VerifyCmd: "npm test",
		}),
		Goal:            "Ship a delightful demo.",
		ProjectFragment: "Read ARCHITECTURE.md before touching the engine.",
		ScopeBlock:      ScopeBlock(domain.Pipeline{Name: "code", TaskTypes: []string{"code", "tests"}}, true),
	}
}

// The load-bearing invariant: the prefix (and the full prompt up to the tail
// separator) is byte-identical across passes with different tasks and spice.
func TestStablePrefixInvariant(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	personas, _ := LoadPool("personas", "general")
	nouns, _ := LoadPool("nouns", "general")

	in1 := stableInputs()
	in1.TaskBlock = TaskBlock(&domain.Task{ID: "ws-1", Title: "add feature A", Type: "code"})
	in1.Spice = NewSpice(rng, personas, nouns)

	in2 := stableInputs()
	in2.TaskBlock = TaskBlock(&domain.Task{ID: "ws-2", Title: "totally different task", Detail: "long detail", Files: []string{"a.go"}})
	in2.TypeFragment = "Prefer table-driven tests."
	in2.Spice = NewSpice(rng, personas, nouns)

	p1, f1 := Compose(in1)
	p2, f2 := Compose(in2)

	if p1 != p2 {
		t.Fatal("stable prefix differs across passes")
	}
	if !strings.HasPrefix(f1, p1+tailSeparator) || !strings.HasPrefix(f2, p2+tailSeparator) {
		t.Fatal("full prompt does not start with prefix + separator")
	}
	if f1 == f2 {
		t.Fatal("tails should differ")
	}
	// Everything per-pass must be strictly after the separator.
	if strings.Contains(p2, "totally different task") || strings.Contains(p2, "table-driven") {
		t.Fatal("per-pass content leaked into the stable prefix")
	}
}

func TestComposeContainsAllPieces(t *testing.T) {
	in := stableInputs()
	task := &domain.Task{ID: "ws-9", Title: "wire the thing", Type: "code", Files: []string{"x.go", "y.go"}}
	in.TaskBlock = TaskBlock(task)
	in.TypeFragment = "Type guidance here."
	in.Spice = Spice{Mode: "persona:x", Prefix: "PERSONA-PREFIX ", Suffix: " SPICE-SUFFIX"}

	_, full := Compose(in)
	for _, want := range []string{
		"Workshop pass contract",
		"MECHANICS FOR THIS PASS",
		"npm test",
		"GOAL (the north star)",
		"Ship a delightful demo.",
		"YOUR PIPELINE",
		"task types: code, tests",
		"wire the thing",
		"x.go, y.go",
		"Type guidance here.",
		"PERSONA-PREFIX",
	} {
		if !strings.Contains(full, want) {
			t.Errorf("full prompt missing %q", want)
		}
	}
	if !strings.HasSuffix(full, " SPICE-SUFFIX") {
		t.Error("spice suffix must trail the prompt")
	}
}

// ExpandBlock keeps the fenced task data and appends the expansion directive
// that overrides the contract's one-increment framing and proposal cap.
func TestExpandBlock(t *testing.T) {
	got := ExpandBlock(&domain.Task{ID: "ws-7", Title: "for each weapon, add an update task", Detail: "see the wiki"})
	for _, want := range []string{
		"<task-data>",
		"for each weapon, add an update task",
		"THIS IS AN EXPANSION TASK",
		"ONE proposal per item",
		"does NOT apply",
		"empty proposals.json",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("expand block missing %q", want)
		}
	}
}

func TestPersonalityDirective(t *testing.T) {
	if got := PersonalityDirective(""); got != "" {
		t.Fatalf("empty personality should render nothing, got %q", got)
	}
	if got := PersonalityDirective("  "); got != "" {
		t.Fatalf("whitespace-only personality should render nothing, got %q", got)
	}
	got := PersonalityDirective("Edgar Allan Poe")
	want := "From now on, while staying focused on your mission I want you to think and act like Edgar Allan Poe."
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// The personality directive must trail the very end of the composed prompt —
// after spice — and stay absent when no personality is resolved, since "none"
// is the default and must do nothing.
func TestComposePersonality(t *testing.T) {
	in := stableInputs()
	in.TaskBlock = TaskBlock(&domain.Task{ID: "ws-1", Title: "wire the thing"})
	in.Spice = Spice{Mode: "persona:x", Suffix: " SPICE-SUFFIX"}

	_, withoutPersonality := Compose(in)
	if strings.Contains(withoutPersonality, "think and act like") {
		t.Fatal("no personality configured should not inject a directive")
	}

	in.Personality = "Bill Gates"
	_, withPersonality := Compose(in)
	want := "From now on, while staying focused on your mission I want you to think and act like Bill Gates."
	if !strings.HasSuffix(withPersonality, want) {
		t.Fatalf("personality directive must trail the prompt, got tail: %q", withPersonality[len(withPersonality)-200:])
	}
	if !strings.Contains(withPersonality, "SPICE-SUFFIX") {
		t.Fatal("personality directive must not replace spice, both should be present")
	}
}

func TestScopeBlockSoloIsEmpty(t *testing.T) {
	if got := ScopeBlock(domain.Pipeline{Name: "main"}, false); got != "" {
		t.Fatalf("solo scope block should be empty, got %q", got)
	}
}

func TestSpiceModes(t *testing.T) {
	rng := rand.New(rand.NewSource(42))
	personas := []string{"a poet"}
	nouns := []string{"volcanoes", "libraries"}

	sawPersona, sawRecode := false, false
	for i := 0; i < 100; i++ {
		s := NewSpice(rng, personas, nouns)
		switch {
		case strings.HasPrefix(s.Mode, "persona:"):
			sawPersona = true
			if !strings.Contains(s.Prefix, "a poet") || s.Suffix != "" {
				t.Fatalf("persona spice malformed: %+v", s)
			}
		case strings.HasPrefix(s.Mode, "recode:"):
			sawRecode = true
			if s.Prefix == "" || s.Suffix == "" {
				t.Fatalf("recode spice malformed: %+v", s)
			}
			stem := strings.TrimSpace(s.Suffix)
			if len(stem) < 2 || len(stem) > 4 {
				t.Fatalf("stem length: %q", stem)
			}
		default:
			t.Fatalf("unexpected mode %q", s.Mode)
		}
	}
	if !sawPersona || !sawRecode {
		t.Fatalf("both modes should occur: persona=%v recode=%v", sawPersona, sawRecode)
	}

	// Only nouns: always recode. Only personas: always persona. Neither: plain.
	for i := 0; i < 10; i++ {
		if s := NewSpice(rng, nil, nouns); !strings.HasPrefix(s.Mode, "recode:") {
			t.Fatalf("nouns-only gave %q", s.Mode)
		}
		if s := NewSpice(rng, personas, nil); !strings.HasPrefix(s.Mode, "persona:") {
			t.Fatalf("personas-only gave %q", s.Mode)
		}
	}
	if s := NewSpice(rng, nil, nil); s.Mode != "" || s.Prefix != "" || s.Suffix != "" {
		t.Fatalf("empty pools should be plain: %+v", s)
	}
}

func TestPoolsLoad(t *testing.T) {
	for _, kind := range []string{"personas", "nouns"} {
		for _, flavor := range []string{"general", "gamedev"} {
			pool, err := LoadPool(kind, flavor)
			if err != nil || len(pool) < 10 {
				t.Fatalf("%s/%s: %d entries, %v", kind, flavor, len(pool), err)
			}
		}
	}
	if _, err := LoadPool("personas", "no-such-file.txt"); err == nil {
		t.Fatal("missing custom pool should error")
	}
}

func TestComposeInquiry(t *testing.T) {
	got := ComposeInquiry(InquiryInputs{
		Mechanics: InquiryMechanics(InquiryMechanicsInputs{
			RepoDir:      "/repo",
			EvidencePath: "/state/inquiry/evidence.json",
			LogsDir:      "/state/logs",
		}),
		Goal:     "ship the space game",
		Question: "why are the coin pickups so big?",
	})
	for _, want := range []string{
		"SELF-EVALUATOR",
		"evidence.json",
		"Workshop-Pass",
		"ship the space game",
		"why are the coin pickups so big?",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("inquiry prompt missing %q", want)
		}
	}
	// The evaluator must never inherit the pass contract's write duties.
	if strings.Contains(got, "progress.json") {
		t.Fatal("inquiry prompt must not reference pass state files")
	}
}
