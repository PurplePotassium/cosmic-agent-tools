package prompt

import (
	"fmt"
	"path/filepath"
	"strings"
)

// tailSeparator splits a composed prompt's stable prefix from its per-turn
// tail (the prompt-cache boundary — see stages.go ComposeStage).
const tailSeparator = "\n\n---\n\n"

// InquiryInputs are the pieces of one self-evaluator prompt.
type InquiryInputs struct {
	Mechanics string // evidence paths for this project
	Goal      string // GOAL.md content, as context
	Question  string // the operator's question
}

// ComposeInquiry assembles the self-evaluator prompt: the read-only forensics
// contract, this project's evidence map, the goal for context, and the
// operator's question.
func ComposeInquiry(in InquiryInputs) string {
	return joinBlocks(
		InquiryContract(),
		in.Mechanics,
		section("GOAL (the project's north star — context, not a work order)", in.Goal),
		section("THE OPERATOR'S QUESTION", in.Question),
	)
}

// InquiryMechanicsInputs carry the per-project evidence paths.
type InquiryMechanicsInputs struct {
	RepoDir      string
	EvidencePath string // the materialized evidence.json
	LogsDir      string // root of per-pipeline pass logs
}

// InquiryMechanics renders the evidence-path block of an inquiry prompt.
func InquiryMechanics(m InquiryMechanicsInputs) string {
	var b strings.Builder
	b.WriteString("## MECHANICS FOR THIS INQUIRY\n\n")
	fmt.Fprintf(&b, "- Repository working directory (read-only): %s\n", m.RepoDir)
	fmt.Fprintf(&b, "- evidence.json (recent-pass digest): %s\n", m.EvidencePath)
	fmt.Fprintf(&b, "- Pass logs and archived transcripts: %s / .transcript.jsonl\n",
		filepath.Join(m.LogsDir, "<pipeline>", "iter-NNNNNN.log"))
	b.WriteString("  (both live OUTSIDE the repository — reference them by these absolute paths)\n")
	return strings.TrimRight(b.String(), "\n")
}

func joinBlocks(blocks ...string) string {
	var kept []string
	for _, b := range blocks {
		b = strings.TrimSpace(b)
		if b != "" {
			kept = append(kept, b)
		}
	}
	return strings.Join(kept, "\n\n")
}

func section(title, body string) string {
	body = strings.TrimSpace(body)
	if body == "" {
		return ""
	}
	return "## " + title + "\n\n" + body
}
