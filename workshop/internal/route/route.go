// Package route resolves which {agent, model, effort} bundle runs a task:
// per-task pin > live pipeline override > [types.*] routing table > pipeline
// bundle. It also houses the keyword classifier that assigns types to
// untyped tasks.
package route

import (
	"regexp"
	"strings"

	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/domain"
)

// Resolve applies the precedence chain for one task: per-task pin > the
// operator's LIVE pipeline override (the UI's "switch model for the next
// pass" dial) > [types.*] routing table > pipeline bundle. A nil task
// (invent pass) resolves through the same chain minus the pin.
func Resolve(task *domain.Task, override domain.Bundle, types map[string]domain.Bundle, pipeline domain.Bundle) domain.Bundle {
	b := pipeline
	if task != nil && task.Type != "" {
		if tb, ok := types[task.Type]; ok {
			b = merge(tb, b)
		}
	}
	b = merge(override, b)
	if task == nil {
		return b
	}
	return merge(task.Pin, b)
}

// Effective overlays the live override on a configured bundle with the same
// agent-switch guard Resolve uses (for status displays).
func Effective(override, base domain.Bundle) domain.Bundle { return merge(override, base) }

// merge overlays `over` on `base`, with one guard: when `over` switches to a
// different agent, base's model and effort must NOT leak through — a model id
// is only valid for its own agent (and some agents fail silently on a bad
// id).
func merge(over, base domain.Bundle) domain.Bundle {
	if over.Agent != "" && base.Agent != "" && !strings.EqualFold(over.Agent, base.Agent) {
		base.Model, base.Effort = "", ""
	}
	return over.Overlay(base)
}

// Vocabulary is the set of task types this project knows: routing-table keys
// plus every type any pipeline handles.
func Vocabulary(types map[string]domain.Bundle, pipelines []domain.Pipeline) map[string]bool {
	vocab := map[string]bool{}
	for t := range types {
		vocab[strings.ToLower(t)] = true
	}
	for _, p := range pipelines {
		for _, t := range p.TaskTypes {
			vocab[strings.ToLower(t)] = true
		}
	}
	return vocab
}

// artGenSignature spots "produce an image asset" phrasing: a generation verb
// within reach of an image-asset noun. Plain art-flavored CODE work ("fix the
// sprite flicker", "tweak the palette") must NOT match — no generation verb.
const artGenSignature = `\b(generate|create|draw|make|produce|design|render)\b[^.!?]{0,80}\b(sprite|pixel[- ]art|image|artwork|illustration|icon|texture|portrait|logo|banner|splash|concept art|art asset|sticker|emoji|tileset|spritesheet)`

// classRule maps a built-in type to its keyword signature. Ordered: the
// first matching rule whose type is in the vocabulary wins; specific
// categories come before the generic "code" fallback.
var classRules = []struct {
	Type string
	Re   *regexp.Regexp
}{
	{"merge-conflict", regexp.MustCompile(`(?i)merge[- ]conflict|resolve.*\bconflict\b|\bconflicted\b`)},
	{"audio", regexp.MustCompile(`(?i)\baudio\b|\bsound(s|track)?\b|\bsfx\b|\bmusic\b|\bvolume\b|\bmute\b|\bfoley\b`)},
	// Asset GENERATION (an image file is the deliverable) routes to the agy
	// image-model flow, not the code-flavored "art" type below. The -trans
	// variant is checked first: same generation signature plus any wording
	// that implies the asset needs a transparent background.
	{domain.ArtGenTransType, regexp.MustCompile(`(?i)` + artGenSignature + `(?s).*(transparen|no +background|without +(a +)?background|remove +the +background|background-?less|alpha +channel|cut-?out)`)},
	{domain.ArtGenTransType, regexp.MustCompile(`(?i)(transparen|no +background|without +(a +)?background|remove +the +background|background-?less|alpha +channel|cut-?out)(?s).*` + artGenSignature)},
	{domain.ArtGenType, regexp.MustCompile(`(?i)` + artGenSignature)},
	{"art", regexp.MustCompile(`(?i)\bart\b|sprite|palette|colou?r|\bvisual\b|\bicon\b|texture|\banimation\b|\bvfx\b|particle|\bjuice\b|screenshake|cosmetic|\bskin\b`)},
	{"tests", regexp.MustCompile(`(?i)\btests?\b|\btesting\b|coverage|\bflaky\b|regression test`)},
	{"docs", regexp.MustCompile(`(?i)\bdocs?\b|\breadme\b|documentation|changelog|\btutorial\b|\bguide\b`)},
	{"code", regexp.MustCompile(`(?i)refactor|\bbug\b|\bfix\b|implement|\bfeature\b|\bcrash\b|optimi[sz]e|\bapi\b|\berror\b|\badd\b|\bwire\b|\bbuild\b`)},
}

// Classify assigns a type to free text, restricted to the project's
// vocabulary. Returns "" when nothing matches confidently — untyped tasks
// are still claimable by pipelines without a type filter.
func Classify(title, detail string, vocab map[string]bool) string {
	if len(vocab) == 0 {
		return ""
	}
	text := strings.ToLower(title + " " + detail)
	for _, rule := range classRules {
		if vocab[rule.Type] && rule.Re.MatchString(text) {
			return rule.Type
		}
	}
	return ""
}
