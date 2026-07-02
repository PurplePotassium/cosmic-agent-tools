// Package route resolves which {agent, model, effort} bundle runs a task:
// per-task pin > [types.*] routing table > pipeline bundle. It also houses
// the keyword classifier that assigns types to untyped tasks.
package route

import (
	"regexp"
	"strings"

	"github.com/gw1108/cosmic-agent-tools/workshop/internal/domain"
)

// Resolve applies the precedence chain for one task. A nil task (invent
// pass) resolves to the pipeline bundle.
func Resolve(task *domain.Task, types map[string]domain.Bundle, pipeline domain.Bundle) domain.Bundle {
	b := pipeline
	if task == nil {
		return b
	}
	if task.Type != "" {
		if tb, ok := types[task.Type]; ok {
			b = merge(tb, b)
		}
	}
	return merge(task.Pin, b)
}

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

// classRule maps a built-in type to its keyword signature. Ordered: the
// first matching rule whose type is in the vocabulary wins; specific
// categories come before the generic "code" fallback.
var classRules = []struct {
	Type string
	Re   *regexp.Regexp
}{
	{"merge-conflict", regexp.MustCompile(`(?i)merge[- ]conflict|resolve.*\bconflict\b|\bconflicted\b`)},
	{"audio", regexp.MustCompile(`(?i)\baudio\b|\bsound(s|track)?\b|\bsfx\b|\bmusic\b|\bvolume\b|\bmute\b|\bfoley\b`)},
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
