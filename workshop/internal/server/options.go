package server

// AgentOption is one selectable agent/model combo the Workshop offers, keyed by a
// stable UI id. The loop stores {agent,model}; the UI shows Label.
//
// AGY MODEL ID: `gemini-3.5-flash` is an ACCEPTED --model input that agy resolves and
// serves as canonical `gemini-3-flash` (Gemini 3 Flash; there is no distinct served
// "3.5"). Keep this exact string — it is proven to run. See workshop/AGENTS.md before
// changing it: an unverified agy id fails silently and blind headless.
type AgentOption struct {
	ID    string `json:"id"`
	Agent string `json:"agent"`
	Model string `json:"model"`
	Label string `json:"label"`
}

// AgentOptions is the ordered menu the UI renders.
var AgentOptions = []AgentOption{
	{ID: "auto", Agent: "auto", Model: "auto", Label: "Auto — pick per task"},
	{ID: "agy-flash", Agent: "agy", Model: "gemini-3.5-flash", Label: "Agy — Gemini 3 Flash (headless: blind)"},
	{ID: "claude-opus", Agent: "claude", Model: "claude-opus-4-8", Label: "Claude Code — Opus 4.8"},
	{ID: "claude-sonnet", Agent: "claude", Model: "claude-sonnet-4-6", Label: "Claude Code — Sonnet 4.6"},
}

func agentOptionByID(id string) (AgentOption, bool) {
	for _, o := range AgentOptions {
		if o.ID == id {
			return o, true
		}
	}
	return AgentOption{}, false
}

func agentIDFor(agent, model string) string {
	for _, o := range AgentOptions {
		if o.Agent == agent && o.Model == model {
			return o.ID
		}
	}
	return ""
}
