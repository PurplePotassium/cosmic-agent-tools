package engine

import (
	"math/rand"
	"strings"
	"testing"
)

func TestGetSpicePersona(t *testing.T) {
	personas := []string{"a hard-boiled noir detective"}
	nouns := []string{"lighthouse"}
	// rng seeded so the first Intn(2) is 0 → persona branch (deterministic per seed).
	rng := rand.New(rand.NewSource(1))
	var sawPersona, sawRecode bool
	for i := 0; i < 50; i++ {
		s := getSpice(personas, nouns, rng)
		switch {
		case strings.HasPrefix(s.mode, "persona:"):
			sawPersona = true
			if !strings.Contains(s.prefix, "channel the mindset of") {
				t.Fatalf("persona prefix malformed: %q", s.prefix)
			}
		case strings.HasPrefix(s.mode, "recode:"):
			sawRecode = true
			if !strings.HasPrefix(s.prefix, "Related to ") {
				t.Fatalf("recode prefix malformed: %q", s.prefix)
			}
			if !strings.HasPrefix(s.suffix, " ") {
				t.Fatalf("recode suffix should be a leading-space stem: %q", s.suffix)
			}
		default:
			t.Fatalf("unexpected spice mode: %q", s.mode)
		}
	}
	if !sawPersona || !sawRecode {
		t.Fatalf("expected both modes over 50 draws (persona=%v recode=%v)", sawPersona, sawRecode)
	}
}

func TestGetSpiceEmptyPoolsFallBackToRecode(t *testing.T) {
	rng := rand.New(rand.NewSource(2))
	// No personas → must never produce a persona; recode uses the food/pasta fallback.
	for i := 0; i < 20; i++ {
		s := getSpice(nil, nil, rng)
		if strings.HasPrefix(s.mode, "persona:") {
			t.Fatalf("no personas available but got persona spice: %q", s.mode)
		}
		if !strings.HasPrefix(s.mode, "recode:") {
			t.Fatalf("expected recode spice with empty pools, got %q", s.mode)
		}
	}
}

func TestIsAuthFailure(t *testing.T) {
	if !isAuthFailure([]string{"Error: unauthorized (401)"}) {
		t.Fatal("should detect 401/unauthorized as auth failure")
	}
	if !isAuthFailure([]string{"please re-authenticate your session"}) {
		t.Fatal("should detect re-authenticate as auth failure")
	}
	if isAuthFailure([]string{"wrote a login screen", "added a token bucket"}) {
		t.Fatal("normal work mentioning login/token must NOT read as auth failure")
	}
	if isAuthFailure(nil) {
		t.Fatal("empty output is not an auth failure (agy blind case)")
	}
}
