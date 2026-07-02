package prompt

import (
	"math/rand"
	"strings"
	"testing"

	"github.com/gw1108/cosmic-agent-tools/workshop/internal/domain"
)

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
