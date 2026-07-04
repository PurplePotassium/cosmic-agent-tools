package main

import (
	"bytes"
	"flag"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/domain"
)

func TestWarnUnknownType(t *testing.T) {
	vocab := map[string]domain.Bundle{"code": {}, "tests": {}, "docs": {}}

	// A type outside the vocabulary warns and lists the known types (sorted).
	var buf bytes.Buffer
	warnUnknownTypeTo(&buf, vocab, "tets")
	out := buf.String()
	if !strings.Contains(out, `"tets"`) {
		t.Errorf("warning should name the bad type; got %q", out)
	}
	if !strings.Contains(out, "code, docs, tests") {
		t.Errorf("warning should list the known vocab sorted; got %q", out)
	}

	// A known type, and the empty (auto-classify) type, are silent.
	for _, ok := range []string{"code", ""} {
		buf.Reset()
		warnUnknownTypeTo(&buf, vocab, ok)
		if buf.Len() != 0 {
			t.Errorf("type %q should not warn; got %q", ok, buf.String())
		}
	}
}

// A lone "-" is a positional arg the flag package refuses to consume; the
// interleave loop must swallow it too or it spins forever at 100% CPU.
func TestParseMixedLoneDash(t *testing.T) {
	fs := flag.NewFlagSet("add", flag.ContinueOnError)
	typ := fs.String("type", "", "")

	done := make(chan []string, 1)
	go func() { done <- parseMixed(fs, []string{"fix", "-", "thing", "--type", "art"}) }()

	select {
	case got := <-done:
		if want := []string{"fix", "-", "thing"}; !slices.Equal(got, want) {
			t.Fatalf("positional = %v, want %v", got, want)
		}
		if *typ != "art" {
			t.Fatalf("interleaved flag lost: type=%q", *typ)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("parseMixed spun forever on a lone dash")
	}
}

// The original motivation for parseMixed: flags AFTER positionals must not
// be silently eaten as positionals.
func TestParseMixedInterleaves(t *testing.T) {
	fs := flag.NewFlagSet("add", flag.ContinueOnError)
	typ := fs.String("type", "", "")
	first := fs.Bool("first", false, "")

	got := parseMixed(fs, []string{"paint the sky", "--type", "art", "--first"})
	if want := []string{"paint the sky"}; !slices.Equal(got, want) {
		t.Fatalf("positional = %v, want %v", got, want)
	}
	if *typ != "art" || !*first {
		t.Fatalf("flags lost: type=%q first=%v", *typ, *first)
	}
}
