package agent

import (
	"regexp"
	"strings"
)

// Auto model routing. When the operator picks "Auto", each pass classifies the TOP
// backlog item (title+detail keywords) into one of three concrete combos:
//
//   - light presentation work (art/audio/juice/tuning/copy)       -> agy / gemini-3.5-flash (fastest)
//   - heavy/structural work (refactor/reorg/architecture/save/AI)  -> claude / opus-4-8       (deepest)
//   - everything else (content/systems/features/mechanics)         -> claude / sonnet-4-6     (gated default)
//
// Empty/unclassifiable text falls through to the safe Sonnet default.
var (
	lightRe = regexp.MustCompile(`juice|particle|screenshake|\bshake\b|telegraph|\bfeel\b|polish|\bart\b|sprite|palette|\bcolou?r\b|\baudio\b|\bsound\b|\bsfx\b|music|\btween|easing|\bcopy\b|tooltip|label|\btext\b|wording|cosmetic|\bvfx\b`)
	heavyRe = regexp.MustCompile(`refactor|re-?organ|reorganize|restructure|re-?architect|architecture|\bsplit(ting)?\b|\bmodulari|save/load|serializ|persist|netcode|multiplayer|pathfind|state machine|overhaul|migrat|\bredesign\b|\brework\b|boss ai|complex`)
)

// AutoSelection is the concrete pick for a classified backlog item.
type AutoSelection struct {
	Agent  string
	Model  string
	Reason string
}

// Classify maps a backlog item's title+detail to a concrete agent/model.
func Classify(title, detail string) AutoSelection {
	txt := strings.ToLower(strings.TrimSpace(title + " " + detail))
	if txt == "" {
		return AutoSelection{Agent: "claude", Model: "claude-sonnet-4-6", Reason: "default/systems"}
	}
	if heavyRe.MatchString(txt) {
		return AutoSelection{Agent: "claude", Model: "claude-opus-4-8", Reason: "heavy/structural"}
	}
	if lightRe.MatchString(txt) {
		return AutoSelection{Agent: "agy", Model: "gemini-3.5-flash", Reason: "light/presentation"}
	}
	return AutoSelection{Agent: "claude", Model: "claude-sonnet-4-6", Reason: "default/systems"}
}
