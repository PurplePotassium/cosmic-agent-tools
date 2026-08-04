// Package prompt owns prompt assembly for the surfaces that still compose
// one-shot prompts — the interactive workflow stage prompts (stages.go) and
// the read-only self-evaluator (ComposeInquiry) — plus the embedded assets
// they draw from.
package prompt

import (
	"embed"
	"strings"
)

//go:embed assets/*
var assets embed.FS

// InquiryContract returns the embedded self-evaluator (read-only forensics)
// contract.
func InquiryContract() string {
	return strings.TrimSpace(mustAsset("inquiry.md"))
}

func mustAsset(name string) string {
	b, err := assets.ReadFile("assets/" + name)
	if err != nil {
		panic("prompt: embedded asset missing: " + name) // build-time invariant
	}
	return string(b)
}
