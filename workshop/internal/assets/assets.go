// Package assets embeds the default per-project scaffold (PROMPT.md / GOAL.md
// templates) and the anti-circling persona/noun pools, so a freshly-installed
// single binary can scaffold a project with zero repo checkout.
package assets

import (
	_ "embed"
	"strings"
)

//go:embed files/PROMPT.default.md
var PromptTemplate string

//go:embed files/GOAL.default.md
var GoalTemplate string

//go:embed files/personas.txt
var personasPlain string

//go:embed files/nouns.txt
var nounsPlain string

//go:embed files/personas-gamedev.txt
var personasGamedev string

//go:embed files/nouns-gamedev.txt
var nounsGamedev string

// pool splits a text file into non-blank, non-comment lines.
func pool(raw string) []string {
	var out []string
	for _, line := range strings.Split(raw, "\n") {
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		out = append(out, t)
	}
	return out
}

// Personas returns the persona pool for the given flavor ("gamedev" or "plain").
func Personas(flavor string) []string {
	if flavor == "gamedev" {
		return pool(personasGamedev)
	}
	return pool(personasPlain)
}

// Nouns returns the priming-noun pool for the given flavor.
func Nouns(flavor string) []string {
	if flavor == "gamedev" {
		return pool(nounsGamedev)
	}
	return pool(nounsPlain)
}
