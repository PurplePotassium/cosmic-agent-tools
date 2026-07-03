// Package prompt owns per-pass prompt assembly: the embedded base contract,
// operator fragments, the pipeline scope block, the task block, and the
// anti-circling "spice". Its central invariant: everything BEFORE the
// variable tail is byte-identical across passes (given unchanged operator
// files), so the long preamble stays a cacheable prompt prefix.
package prompt

import (
	"embed"
	"fmt"
	"os"
	"strings"
)

//go:embed assets/*
var assets embed.FS

// BaseContract returns the embedded pass contract.
func BaseContract() string {
	b, err := assets.ReadFile("assets/contract.md")
	if err != nil {
		panic("prompt: embedded contract missing: " + err.Error()) // build-time invariant
	}
	return strings.TrimSpace(string(b))
}

// InquiryContract returns the embedded self-evaluator (read-only forensics)
// contract.
func InquiryContract() string {
	b, err := assets.ReadFile("assets/inquiry.md")
	if err != nil {
		panic("prompt: embedded inquiry contract missing: " + err.Error()) // build-time invariant
	}
	return strings.TrimSpace(string(b))
}

// LoadPool resolves a persona/noun pool: the built-in names "general" and
// "gamedev", or a path to a file (one entry per line, '#' comments and blank
// lines ignored).
func LoadPool(kind, nameOrPath string) ([]string, error) {
	switch strings.ToLower(strings.TrimSpace(nameOrPath)) {
	case "", "general":
		return parsePool(mustAsset(kind + "-general.txt")), nil
	case "gamedev":
		return parsePool(mustAsset(kind + "-gamedev.txt")), nil
	}
	raw, err := os.ReadFile(nameOrPath)
	if err != nil {
		return nil, fmt.Errorf("prompt: %s pool %q: %w", kind, nameOrPath, err)
	}
	pool := parsePool(string(raw))
	if len(pool) == 0 {
		return nil, fmt.Errorf("prompt: %s pool %q is empty", kind, nameOrPath)
	}
	return pool, nil
}

func mustAsset(name string) string {
	b, err := assets.ReadFile("assets/" + name)
	if err != nil {
		panic("prompt: embedded pool missing: " + name)
	}
	return string(b)
}

func parsePool(raw string) []string {
	var out []string
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, line)
	}
	return out
}
