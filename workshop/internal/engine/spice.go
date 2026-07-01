package engine

import (
	"math/rand"
	"strings"
)

// spice is the per-iteration anti-circling wrapper (turso.tech/blog/edgar-allan-poe):
// long-running agents stop exploring and loop on the same ideas, so we inject semantic
// tension each pass — either a PERSONA lens or a RECODING-DECODING priming pair.
type spice struct {
	mode   string // human-readable tag recorded in the log/iteration ("persona:X" / "recode:n/S")
	prefix string
	suffix string
}

// getSpice picks one anti-circling mode at random from the given pools. It mirrors
// the PowerShell Get-Spice exactly so behavior/logs stay recognizable across the port.
func getSpice(personas, nouns []string, rng *rand.Rand) spice {
	if rng.Intn(2) == 0 && len(personas) > 0 {
		p := personas[rng.Intn(len(personas))]
		return spice{
			mode: "persona:" + p,
			prefix: "For this iteration only, channel the mindset of " + p +
				". Bring that distinctive way of seeing to the task below - it is a lens to break you out of repeating earlier ideas, not a change to the goal.\n\n",
		}
	}
	// recoding-decoding: a priming noun up front + a diverting word-stem at the end.
	noun := "food"
	if len(nouns) > 0 {
		noun = nouns[rng.Intn(len(nouns))]
	}
	stemSource := "pasta"
	if len(nouns) > 0 {
		stemSource = nouns[rng.Intn(len(nouns))]
	}
	maxLen := 4
	if len(stemSource) < maxLen {
		maxLen = len(stemSource)
	}
	stemLen := 2
	if maxLen > 2 {
		stemLen = 2 + rng.Intn(maxLen-2+1) // 2..maxLen inclusive
	} else if maxLen >= 1 {
		stemLen = maxLen
	}
	if stemLen > len(stemSource) {
		stemLen = len(stemSource)
	}
	stem := stemSource[:stemLen]
	if stem != "" {
		stem = strings.ToUpper(stem[:1]) + stem[1:]
	}
	return spice{
		mode:   "recode:" + noun + "/" + stem,
		prefix: "Related to " + strings.ToUpper(noun) + ": ",
		suffix: " " + stem,
	}
}
