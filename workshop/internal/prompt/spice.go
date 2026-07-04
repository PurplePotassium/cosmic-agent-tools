package prompt

import (
	"fmt"
	"math/rand"
	"strings"
	"unicode"
)

// Spice is one pass's anti-circling injection. Long-running loops stop
// exploring and repeat earlier ideas; injecting semantic tension each pass —
// a persona lens or a recoding-decoding word prime — breaks the rut
// (technique from turso.tech/blog/edgar-allan-poe).
type Spice struct {
	Mode   string // "persona:<p>" | "recode:<noun>/<Stem>" | "" (plain)
	Prefix string // goes before the task block
	Suffix string // trails the task block
}

// NewSpice draws one spice from the pools using rng (seedable for tests).
// Empty pools degrade gracefully: persona needs personas, recode needs nouns;
// with neither, the zero Spice (plain) is returned.
func NewSpice(rng *rand.Rand, personas, nouns []string) Spice {
	pickPersona := len(personas) > 0 && (len(nouns) == 0 || rng.Intn(2) == 0)
	switch {
	case pickPersona:
		p := personas[rng.Intn(len(personas))]
		return Spice{
			Mode: "persona:" + p,
			Prefix: fmt.Sprintf("For this iteration only, channel the mindset of %s. "+
				"Bring that distinctive way of seeing to the task below - it is a lens to break you "+
				"out of repeating earlier ideas, not a change to the goal.\n\n", p),
		}
	case len(nouns) > 0:
		noun := nouns[rng.Intn(len(nouns))]
		stemSource := nouns[rng.Intn(len(nouns))]
		stem := wordStem(rng, stemSource)
		return Spice{
			Mode:   fmt.Sprintf("recode:%s/%s", noun, stem),
			Prefix: fmt.Sprintf("Related to %s: ", strings.ToUpper(noun)),
			Suffix: " " + stem,
		}
	default:
		return Spice{}
	}
}

// wordStem takes a random 2-4 character stem of the source's first word,
// capitalized.
func wordStem(rng *rand.Rand, source string) string {
	word := source
	if i := strings.IndexFunc(source, unicode.IsSpace); i > 0 {
		word = source[:i]
	}
	runes := []rune(word)
	max := 4
	if len(runes) < max {
		max = len(runes)
	}
	if max < 2 {
		return strings.ToUpper(word)
	}
	n := 2 + rng.Intn(max-1) // 2..max
	// Slice by rune, not byte: byte-slicing the first rune of a non-ASCII
	// pool entry ("Éclair") splits a multibyte sequence and yields invalid
	// UTF-8.
	stem := runes[:n]
	return strings.ToUpper(string(stem[:1])) + strings.ToLower(string(stem[1:]))
}
