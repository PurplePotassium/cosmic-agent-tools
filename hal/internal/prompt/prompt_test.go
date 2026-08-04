package prompt

import (
	"strings"
	"testing"
)

func TestComposeInquiry(t *testing.T) {
	got := ComposeInquiry(InquiryInputs{
		Mechanics: InquiryMechanics(InquiryMechanicsInputs{
			RepoDir:      "/repo",
			EvidencePath: "/state/inquiry/evidence.json",
			LogsDir:      "/state/logs",
		}),
		Goal:     "ship the space game",
		Question: "why are the coin pickups so big?",
	})
	for _, want := range []string{
		"SELF-EVALUATOR",
		"evidence.json",
		"Hal-Pass",
		"ship the space game",
		"why are the coin pickups so big?",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("inquiry prompt missing %q", want)
		}
	}
	// The evaluator must never inherit the pass contract's write duties.
	if strings.Contains(got, "progress.json") {
		t.Fatal("inquiry prompt must not reference pass state files")
	}
}
